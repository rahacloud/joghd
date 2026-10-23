// Command joghd is a URL health check and uptime monitoring service.
//
// It probes HTTP and HTTPS endpoints on a schedule, retries with exponential
// backoff before believing a failure, and pushes alerts to Telegram and
// Mattermost when a target goes down, is still down, or recovers. It runs
// either as a one-shot check that exits with a non-zero status (suitable for
// cron or a CI job) or as a long-lived monitor.
//
// Usage:
//
//	joghd [-config path] [-mode oneshot|continuous] [-quiet] [-version]
//
// See https://github.com/rahacloud/joghd for the configuration reference.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/pterm/pterm"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/rahacloud/joghd/internal/alerter"
	"github.com/rahacloud/joghd/internal/checker"
	"github.com/rahacloud/joghd/internal/config"
	"github.com/rahacloud/joghd/internal/domain"
	"github.com/rahacloud/joghd/internal/metrics"
	"github.com/rahacloud/joghd/internal/proxy"
	"github.com/rahacloud/joghd/internal/scheduler"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func printBanner() {
	owl := ` ,___,
 (o,o)
 /)_)
  """`

	pterm.DefaultCenter.Println(
		pterm.LightYellow(owl),
	)

	pterm.DefaultCenter.Println(
		pterm.Sprintf(
			"%s %s\n%s %s   %s %s",
			pterm.LightCyan("version:"), pterm.White(version),
			pterm.LightCyan("commit:"), pterm.White(commit),
			pterm.LightCyan("built:"), pterm.White(date),
		),
	)

	pterm.Println()
}

func main() {
	configPath := flag.String("config", "config.toml", "Path to configuration file")
	mode := flag.String("mode", "", "Run mode: oneshot or continuous (overrides config)")
	quiet := flag.Bool("quiet", false, "Suppress the startup banner")
	showVersion := flag.Bool("version", false, "Show version information and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("joghd %s (commit: %s, built: %s)\n", version, commit, date)

		return
	}

	if !*quiet {
		printBanner()
	}

	app := fx.New(
		fx.Supply(config.CLIParams{
			ConfigPath: *configPath,
			Mode:       *mode,
		}),

		fx.WithLogger(func(log *slog.Logger) fxevent.Logger {
			l := &fxevent.SlogLogger{Logger: log}
			l.UseLogLevel(slog.LevelDebug)

			return l
		}),

		fx.Module("config",
			fx.Provide(config.ProvideConfig),
		),

		fx.Module("checker",
			fx.Provide(
				provideHTTPClient,
				provideChecker,
			),
		),

		fx.Module("metrics",
			fx.Provide(provideRecorder),
		),

		fx.Module("alerter",
			fx.Provide(provideAlerter),
		),

		fx.Module("scheduler",
			fx.Provide(scheduler.New),
		),

		fx.Provide(provideLogger),
		fx.Invoke(registerRunner),
	)

	app.Run()
}

func provideLogger(appCfg config.AppConfig) *slog.Logger {
	// Configuration validation guarantees the level parses.
	logLevel, _ := config.ParseLogLevel(appCfg.LogLevel)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	return logger
}

// provideHTTPClient builds the shared HTTP client and ties its connection pool
// to the application lifecycle.
func provideHTTPClient(lc fx.Lifecycle, httpCfg config.HTTPConfig) checker.HTTPClient {
	client := checker.NewRestyClient(httpCfg)

	slog.Info("Default proxy for checks and alerts", "proxy", proxy.Label(httpCfg.Proxy))

	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			return client.Close()
		},
	})

	return client
}

func provideChecker(client checker.HTTPClient, retryCfg config.RetryConfig, appCfg config.AppConfig) checker.Checker {
	return checker.New(
		checker.WithHTTPClient(client),
		checker.WithRetryConfig(retryCfg),
		checker.WithConcurrency(appCfg.Concurrency),
	)
}

// metricsReadHeaderTimeout bounds how long a /metrics client may take to send
// its request headers, so a stalled scraper cannot hold a connection open.
const metricsReadHeaderTimeout = 10 * time.Second

// provideRecorder serves Prometheus metrics on metrics.listen for the life of
// the application, or returns a no-op recorder when it is not set.
func provideRecorder(lc fx.Lifecycle, metricsCfg config.MetricsConfig) metrics.Recorder {
	if metricsCfg.Listen == "" {
		return metrics.Nop{}
	}

	m := metrics.New(version, commit)

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler())

	srv := &http.Server{
		Addr:              metricsCfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: metricsReadHeaderTimeout,
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			// Listen here rather than in the goroutine, so a taken port
			// fails startup instead of being logged and ignored.
			ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", metricsCfg.Listen)
			if err != nil {
				return fmt.Errorf("metrics.listen: %w", err)
			}

			slog.Info("Serving metrics", "address", ln.Addr().String())

			go func() {
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					slog.Error("Metrics server stopped", "error", err)
				}
			}()

			return nil
		},
		OnStop: srv.Shutdown,
	})

	return m
}

func provideAlerter(alerters map[string]config.AlerterConfig, rec metrics.Recorder) (alerter.Alerter, error) {
	composite := alerter.NewCompositeAlerter()

	enabled := 0

	for name, a := range alerters {
		if !a.Enabled {
			continue
		}

		var inner alerter.Alerter
		switch a.Type {
		case config.AlerterTypeTelegram:
			inner = alerter.NewTelegramAlerter(name, a.BotToken, a.ChatID, a.Timeout, alerter.WithProxy(a.Proxy))
		case config.AlerterTypeMattermost:
			inner = alerter.NewMattermostAlerter(name, a.WebhookURL, a.Channel, a.Username, a.IconURL, a.Timeout,
				alerter.WithProxy(a.Proxy))
		default:
			return nil, fmt.Errorf("unsupported alerter type %q for %q", a.Type, name)
		}

		rec.RegisterAlerter(name)
		composite.Add(alerter.NewCompanyFilter(alerter.NewInstrumented(inner, rec), a.Companies))
		enabled++

		slog.Info("Alerter enabled", "name", name, "type", a.Type, "companies", a.Companies, "proxy", proxy.Label(a.Proxy))
	}

	if enabled == 0 {
		slog.Warn("No alerters are enabled; health check failures will only be logged")
	}

	return composite, nil
}

func registerRunner(
	lc fx.Lifecycle,
	shutdowner fx.Shutdowner,
	appCfg config.AppConfig,
	chk checker.Checker,
	alt alerter.Alerter,
	targets []domain.Target,
	sched *scheduler.Scheduler,
) error {
	slog.Info("Joghd starting", "mode", appCfg.Mode, "targets", len(targets))

	switch appCfg.Mode {
	case config.ModeOneshot:
		appendCancellableHook(lc, func(ctx context.Context) {
			exitCode := runOneshot(ctx, chk, alt, targets)

			if err := shutdowner.Shutdown(fx.ExitCode(exitCode)); err != nil {
				slog.Error("Failed to trigger shutdown", "error", err)
			}
		})
	case config.ModeContinuous:
		appendCancellableHook(lc, func(ctx context.Context) {
			slog.Info("Starting continuous monitoring...")

			if err := sched.Start(ctx); err != nil {
				slog.Error("Scheduler error", "error", err)
			}
		})
	default:
		return fmt.Errorf("unsupported app.mode %q", appCfg.Mode)
	}

	return nil
}

// appendCancellableHook runs fn in the background on start and cancels its
// context on stop, so an interrupt aborts in-flight work instead of being
// ignored.
func appendCancellableHook(lc fx.Lifecycle, fn func(ctx context.Context)) {
	var cancel context.CancelFunc

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			//nolint:gosec // cancel is retained and invoked by the OnStop hook
			ctx, c := context.WithCancel(context.Background())
			cancel = c

			go fn(ctx)

			return nil
		},
		OnStop: func(_ context.Context) error {
			if cancel != nil {
				cancel()
			}

			return nil
		},
	})
}

func runOneshot(ctx context.Context, chk checker.Checker, alt alerter.Alerter, targets []domain.Target) int {
	slog.Info("Running oneshot health check...")

	results := chk.CheckAll(ctx, targets)

	hasFailures := false

	for _, result := range results {
		if result.Success {
			slog.Info("Target healthy",
				"target", result.Target.Name,
				"status", result.ActualStatus,
				"latency", result.Latency,
			)

			continue
		}

		hasFailures = true

		slog.Error("Target unhealthy",
			"target", result.Target.Name,
			"status", result.ActualStatus,
			"expected", result.Target.ExpectedStatus,
			"error", result.Error,
		)

		sendAlert(ctx, alt, domain.NewFailureAlert(result))
	}

	if hasFailures {
		slog.Warn("Health check completed with failures")

		return 1
	}

	slog.Info("Health check completed successfully")

	return 0
}

// sendAlert fans an alert out under a bounded context so a slow alerter cannot
// keep the process alive indefinitely.
func sendAlert(ctx context.Context, alt alerter.Alerter, alert domain.Alert) {
	ctx, cancel := context.WithTimeout(ctx, config.AlertFanoutTimeout)
	defer cancel()

	if err := alt.Send(ctx, alert); err != nil {
		slog.Error("Failed to send alert", "target", alert.Target.Name, "alert_id", alert.ID, "error", err)
	}
}
