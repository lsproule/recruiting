package main

import (
	"context"
	"fmt"
	"log/slog"

	"recruiting/internal/config"
	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/store"
)

// worker runs the queue consumer until ctx is cancelled. Like serve it
// connects as app_rw: the queue's own tables carry no tenant data, and every
// handler that touches domain tables opens a transaction scoped to the org
// its payload names.
func worker(ctx context.Context, logger *slog.Logger, cfg *config.Config) error {
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	defer st.Close()

	renderer, err := mail.NewRenderer()
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	sender, err := mail.NewSMTPSender(cfg.SMTPURL, mail.FromAddress(cfg.BaseURL))
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}

	q, err := queue.NewWorker(st.Pool(), queue.Config{
		Logger:   logger,
		Handlers: handlers(logger, st, renderer, sender),
	})
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	logger.Info("working queue", "kinds", queue.Kinds(), "smtp_from", sender.From())
	return q.Run(ctx)
}

// handlers is the one table binding every job kind to the code that works it.
// A kind with no entry here stops the worker at startup, so a new kind cannot
// be enqueued into a worker that would silently ignore it.
func handlers(logger *slog.Logger, st queue.TenantStore, r *mail.Renderer, sender mail.Sender) map[string]queue.Handler {
	return map[string]queue.Handler{
		queue.KindEmailSend: queue.EmailHandler(st, r, sender, logger),
		// The subsystems behind these kinds are not built yet. They are
		// registered so the worker starts, but they fail rather than report
		// work that never happened: the job is retried and then discarded,
		// where it can still be found and replayed once its subsystem lands.
		queue.KindInterviewRemind:  unimplemented(logger, queue.KindInterviewRemind),
		queue.KindAssessmentInvite: unimplemented(logger, queue.KindAssessmentInvite),
		queue.KindAssessmentRemind: unimplemented(logger, queue.KindAssessmentRemind),
		queue.KindRunnerExecute:    unimplemented(logger, queue.KindRunnerExecute),
		queue.KindAttemptFinalize:  unimplemented(logger, queue.KindAttemptFinalize),
		queue.KindSignalsCompute:   unimplemented(logger, queue.KindSignalsCompute),
	}
}

func unimplemented(logger *slog.Logger, kind string) queue.Handler {
	return func(_ context.Context, job queue.Job) error {
		logger.Warn("job kind has no implementation yet", "kind", kind, "job_id", job.ID)
		return fmt.Errorf("%s has no implementation yet", kind)
	}
}
