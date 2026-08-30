package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"

	"recruiting/internal/config"
	"recruiting/internal/store"
)

// The runner mode is still a stub: it proves configuration loads and reports
// itself until its subsystem lands.

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

func runRunner(logger *slog.Logger, cfg *config.Config, _ []string) error {
	logger.Info("configuration loaded", "runner_url", cfg.RunnerURL)
	return nil
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
