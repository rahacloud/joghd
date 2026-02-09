package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/raha-io/joghd/internal/alerter"
	"github.com/raha-io/joghd/internal/checker"
	"github.com/raha-io/joghd/internal/config"
	"github.com/raha-io/joghd/internal/domain"
	"github.com/raha-io/joghd/internal/scheduler"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	configPath := flag.String("config", "config.toml", "Path to configuration file")
	mode := flag.String("mode", "", "Run mode: oneshot or continuous (overrides config)")
	showVersion := flag.Bool("version", false, "Show version information")
	flag.Parse()

	if *showVersion {
		fmt.Printf("joghd %s (commit: %s, built: %s)\n", version, commit, date)
		os.Exit(0)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	setupLogger(cfg.App.LogLevel)

	// Command-line mode flag overrides config
	if *mode != "" {
		cfg.App.Mode = *mode
	}

	if len(cfg.Targets) == 0 {
		slog.Error("No targets configured")
		os.Exit(1)
	}

	slog.Info("Joghd starting", "mode", cfg.App.Mode, "targets", len(cfg.Targets))

	// Create HTTP client
	httpClient := checker.NewRestyClient(cfg.HTTP)

	// Create checker
	chk := checker.New(
		checker.WithHTTPClient(httpClient),
		checker.WithRetryConfig(cfg.Retry),
		checker.WithConcurrency(cfg.App.Concurrency),
	)

	// Create alerter
	alt := buildAlerter(cfg)

	// Create context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("Received signal, shutting down...", "signal", sig)
		cancel()
	}()

	// Run based on mode
	switch cfg.App.Mode {
	case "oneshot":
		exitCode := runOneshot(ctx, chk, alt, cfg.Targets)
		os.Exit(exitCode)
	case "continuous":
		runContinuous(ctx, chk, alt, cfg.Targets)
	default:
		slog.Error("Unknown mode", "mode", cfg.App.Mode)
		os.Exit(1)
	}
}

func setupLogger(level string) {
	var logLevel slog.Level

	switch strings.ToLower(level) {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	})))
}

func buildAlerter(cfg *config.Config) alerter.Alerter {
	composite := alerter.NewCompositeAlerter()

	if cfg.Alerters.Telegram.Enabled {
		telegram := alerter.NewTelegramAlerter(cfg.Alerters.Telegram)
		composite.Add(telegram)
		slog.Info("Telegram alerter enabled")
	}

	return composite
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

			// Send failure alert
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

func runContinuous(ctx context.Context, chk checker.Checker, alt alerter.Alerter, targets []domain.Target) {
	slog.Info("Starting continuous monitoring...")

	sched := scheduler.New(chk, alt, targets)
	if err := sched.Start(ctx); err != nil {
		slog.Error("Scheduler error", "error", err)
	}
}
