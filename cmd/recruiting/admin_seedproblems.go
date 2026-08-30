package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"recruiting/internal/config"
	runnerclient "recruiting/internal/runner/client"
	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/store/system"
)

// seedProblemsTimeout bounds validating and writing the whole seed bank,
// which runs every seed problem's tests against the runner.
const seedProblemsTimeout = 5 * time.Minute

// seedProblems imports the platform's built-in problem bank under the
// platform org. It connects as the schema owner, via system.WithSystemTx:
// the platform org is not one any tenant may write, so this is the only way
// in. Re-running it refreshes the bank rather than duplicating it.
func seedProblems(out io.Writer, logger *slog.Logger, cfg *config.Config, _ []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), seedProblemsTimeout)
	defer cancel()

	sys, err := system.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer sys.Close()

	problems := service.NewProblemService(nil, runnerclient.New(cfg.RunnerURL, cfg.RunnerSecret))
	var imported []service.Problem
	err = sys.WithSystemTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		var err error
		imported, err = problems.ImportPlatformSeed(ctx, tx)
		return err
	})
	if err != nil {
		return fmt.Errorf("admin seed-problems: %w", err)
	}

	logger.Info("platform problem bank seeded", "count", len(imported))
	fmt.Fprintf(out, "Imported %d platform problems.\n", len(imported))
	return nil
}
