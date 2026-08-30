package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"recruiting/internal/config"
	"recruiting/internal/mail"
	"recruiting/internal/observe"
	"recruiting/internal/queue"
	runnerclient "recruiting/internal/runner/client"
	"recruiting/internal/service"
	"recruiting/internal/store"
)

// expireDuePeriod is how often the worker sweeps for assessment attempts
// past their deadline: frequent enough that an abandoned attempt does not
// block a vetter's queue for long, infrequent enough to be a non-event on
// load.
const expireDuePeriod = time.Minute

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
	blobs, err := objectStore(ctx, logger, cfg)
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	var blobStore service.BlobStore
	var blobReader service.BlobReader
	if blobs != nil {
		blobStore = blobs
		blobReader = blobs
	}
	runnerExec := runnerclient.New(cfg.RunnerURL, cfg.RunnerSecret)

	// q is insert-only: handlers that enqueue further work (a reminder,
	// a follow-up invite) use it rather than the worker client below, since
	// that client's Handlers must be supplied to queue.NewWorker before it
	// exists to hand back a *queue.Client of its own.
	q, err := queue.New(st.Pool(), queue.Config{Logger: logger})
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	workerClient, err := queue.NewWorker(st.Pool(), queue.Config{
		Logger:   logger,
		Handlers: handlers(logger, st, q, renderer, sender, runnerExec, blobStore, blobReader, cfg.BaseURL),
	})
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}

	go observe.PollQueueDepth(ctx, st.Pool(), queueDepthPollInterval, logger)
	go expireDueAttempts(ctx, service.NewAttemptService(st, q, cfg.BaseURL), logger)

	metricsErrc := make(chan error, 1)
	go func() { metricsErrc <- serveMetrics(ctx, logger, metricsAddr()) }()

	logger.Info("working queue", "kinds", queue.Kinds(), "smtp_from", sender.From(), "metrics_addr", metricsAddr())
	runErr := workerClient.Run(ctx)
	if err := <-metricsErrc; err != nil && runErr == nil {
		return fmt.Errorf("worker: %w", err)
	}
	return runErr
}

// expireDueAttempts sweeps for overdue attempts every expireDuePeriod until
// ctx is cancelled. A sweep that fails is logged and retried at the next
// tick rather than stopping the worker: a transient database error should
// not take assessment auto-submission down with it.
func expireDueAttempts(ctx context.Context, attempts *service.AttemptService, logger *slog.Logger) {
	ticker := time.NewTicker(expireDuePeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := attempts.ExpireDue(ctx)
			if err != nil {
				logger.Warn("expire due attempts failed", "error", err)
				continue
			}
			if n > 0 {
				logger.Info("expired due attempts", "count", n)
			}
		}
	}
}

// handlers is the one table binding every job kind to the code that works
// it. A kind with no entry here stops the worker at startup, so a new kind
// cannot be enqueued into a worker that would silently ignore it.
//
// email.send and runner.execute are wrapped so their outcome and, for
// runner.execute, its duration reach the metrics registry — instrumentation
// lives here rather than in internal/service, which stays free of metrics
// concerns.
func handlers(logger *slog.Logger, st *store.Store, q *queue.Client, r *mail.Renderer, sender mail.Sender, exec *runnerclient.Client, blobStore service.BlobStore, blobReader service.BlobReader, baseURL string) map[string]queue.Handler {
	return map[string]queue.Handler{
		queue.KindEmailSend:        instrumentEmail(queue.EmailHandler(st, r, sender, logger)),
		queue.KindInterviewRemind:  service.RemindHandler(st, q, baseURL),
		queue.KindAssessmentInvite: service.AssessmentInviteHandler(st, q, baseURL),
		queue.KindAssessmentRemind: service.AssessmentRemindHandler(st, q, baseURL),
		queue.KindRunnerExecute:    instrumentRunnerExecute(service.RunnerExecuteHandler(st, exec, logger)),
		queue.KindAttemptFinalize:  service.AttemptFinalizeHandler(st, q, blobStore, logger),
		queue.KindSignalsCompute:   service.SignalsComputeHandler(st, blobReader, logger),
	}
}

// instrumentEmail counts email.send jobs whose handler returned an error,
// which is the "email failures" metric the observability contract asks for.
func instrumentEmail(next queue.Handler) queue.Handler {
	return func(ctx context.Context, job queue.Job) error {
		err := next(ctx, job)
		if err != nil {
			observe.EmailFailuresTotal.Inc()
		}
		return err
	}
}

// instrumentRunnerExecute times a runner.execute handler call and records
// its outcome — the "runner latency and failure rate" the observability
// contract asks for.
func instrumentRunnerExecute(next queue.Handler) queue.Handler {
	return func(ctx context.Context, job queue.Job) error {
		start := time.Now()
		err := next(ctx, job)
		observe.ObserveRunnerExecute(time.Since(start), err)
		return err
	}
}
