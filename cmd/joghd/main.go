package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/pterm/pterm"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/rahacloud/joghd/internal/alerter"
	"github.com/rahacloud/joghd/internal/checker"
	"github.com/rahacloud/joghd/internal/config"
	"github.com/rahacloud/joghd/internal/domain"
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
	showVersion := flag.Bool("version", false, "Show version information")
	flag.Parse()

	printBanner()

	if *showVersion {
		os.Exit(0)
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
				fx.Annotate(checker.NewRestyClient, fx.As(new(checker.HTTPClient))),
				provideChecker,
			),
		),

		fx.Module("alerter",
			fx.Provide(provideAlerter),
		),

		fx.Module("scheduler",
			fx.Provide(scheduler.New),
		),

		fx.Provide(provideLogger),
		fx.Invoke(validateTargets),
		fx.Invoke(registerRunner),
	)

	app.Run()
}

func provideLogger(appCfg config.AppConfig) *slog.Logger {
	var logLevel slog.Level

	switch strings.ToLower(appCfg.LogLevel) {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	return logger
}

func provideChecker(client checker.HTTPClient, retryCfg config.RetryConfig, appCfg config.AppConfig) checker.Checker {
	return checker.New(
		checker.WithHTTPClient(client),
		checker.WithRetryConfig(retryCfg),
		checker.WithConcurrency(appCfg.Concurrency),
	)
}

func provideAlerter(alerters map[string]config.AlerterConfig) (alerter.Alerter, error) {
	composite := alerter.NewCompositeAlerter()

	for name, a := range alerters {
		if !a.Enabled {
			continue
		}

		var inner alerter.Alerter
		switch a.Type {
		case config.AlerterTypeTelegram:
			inner = alerter.NewTelegramAlerter(name, a.BotToken, a.ChatID, a.Timeout)
		case config.AlerterTypeMattermost:
			inner = alerter.NewMattermostAlerter(name, a.WebhookURL, a.Channel, a.Username, a.IconURL, a.Timeout)
		default:
			return nil, fmt.Errorf("unsupported alerter type %q for %q", a.Type, name)
		}

		composite.Add(alerter.NewCompanyFilter(inner, a.Companies))
		slog.Info("Alerter enabled", "name", name, "type", a.Type, "companies", a.Companies)
	}

	return composite, nil
}

func validateTargets(targets []domain.Target) error {
	if len(targets) == 0 {
		return fmt.Errorf("no targets configured")
	}

	return nil
}

func registerRunner(
	lc fx.Lifecycle,
	shutdowner fx.Shutdowner,
	appCfg config.AppConfig,
	chk checker.Checker,
	alt alerter.Alerter,
	targets []domain.Target,
	sched *scheduler.Scheduler,
) {
	slog.Info("Joghd starting", "mode", appCfg.Mode, "targets", len(targets))

	switch appCfg.Mode {
	case "oneshot":
		lc.Append(fx.Hook{
			OnStart: func(ctx context.Context) error {
				go func() {
					exitCode := runOneshot(context.Background(), chk, alt, targets)

					if err := shutdowner.Shutdown(fx.ExitCode(exitCode)); err != nil {
						slog.Error("Failed to trigger shutdown", "error", err)
					}
				}()

				return nil
			},
		})
	case "continuous":
		var cancel context.CancelFunc

		lc.Append(fx.Hook{
			OnStart: func(_ context.Context) error {
				ctx, c := context.WithCancel(context.Background())
				cancel = c

				go func() {
					slog.Info("Starting continuous monitoring...")

					if err := sched.Start(ctx); err != nil {
						slog.Error("Scheduler error", "error", err)
					}
				}()

				return nil
			},
			OnStop: func(_ context.Context) error {
				slog.Info("Stopping continuous monitoring...")
				cancel()

				return nil
			},
		})
	}
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
		} else {
			hasFailures = true
			slog.Error("Target unhealthy",
				"target", result.Target.Name,
				"status", result.ActualStatus,
				"expected", result.Target.ExpectedStatus,
				"error", result.Error,
			)

			alert := domain.NewFailureAlert(result)
			if err := alt.Send(ctx, alert); err != nil {
				slog.Error("Failed to send alert", "error", err)
			}
		}
	}

	if hasFailures {
		slog.Warn("Health check completed with failures")
		return 1
	}

	slog.Info("Health check completed successfully")
	return 0
}
