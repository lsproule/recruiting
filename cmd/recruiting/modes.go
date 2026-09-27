package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"

	"recruiting/internal/config"
	"recruiting/internal/runner/server"
	"recruiting/internal/store"
)

func runServe(logger *slog.Logger, cfg *config.Config, _ []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return serve(ctx, logger, cfg, listenAddr(), nil)
}

func runWorker(logger *slog.Logger, cfg *config.Config, _ []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return worker(ctx, logger, cfg)
}

// runRunner starts the sandboxed execution service and, alongside it, its
// own /metrics listener on METRICS_ADDR, kept apart from the runner's
// secret-protected listener so a scraper needs no secret. It serves the
// runner's own registry (runner_executions_in_flight, runner_queue_depth
// and the rest; see docs/runner-scaling.md), not internal/observe's, whose
// series are the app's.
func runRunner(logger *slog.Logger, cfg *config.Config, _ []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	metricsErrc := make(chan error, 1)
	go func() { metricsErrc <- serveMetrics(ctx, logger, metricsAddr(), server.MetricsHandler()) }()

	runErr := server.Run(ctx, logger, server.ConfigFromEnv(cfg.RunnerSecret))
	// server.Run can return before ctx is ever cancelled — a startup
	// failure such as the configured OCI runtime being unavailable returns
	// at once, and RUNNER_IDLE_EXIT returns nil once the runner has sat
	// idle for that long. stop() here (safe to call more than once) is what
	// tells the metrics listener to shut down in those cases, so waiting on
	// it below cannot deadlock against a signal that will never arrive.
	stop()
	if err := <-metricsErrc; err != nil && runErr == nil {
		return fmt.Errorf("runner: %w", err)
	}
	return runErr
}

// runMigrate applies or rolls back migrations. It connects as the schema
// owner, so DATABASE_URL must name the owning role here, not app_rw.
func runMigrate(logger *slog.Logger, cfg *config.Config, args []string) error {
	direction := "up"
	if len(args) > 0 {
		direction = args[0]
	}
	ctx := context.Background()
	switch direction {
	case "up":
		if err := store.MigrateUp(ctx, cfg.DatabaseURL); err != nil {
			return err
		}
		logger.Info("migrations applied")
	case "down":
		if err := store.MigrateDownAll(ctx, cfg.DatabaseURL); err != nil {
			return err
		}
		logger.Info("migrations rolled back")
	default:
		return fmt.Errorf("unknown migrate direction %q\n\nusage: recruiting migrate [up|down]", direction)
	}
	return nil
}

func runAdmin(logger *slog.Logger, cfg *config.Config, args []string) error {
	return RunAdmin(logger, cfg, args)
}
