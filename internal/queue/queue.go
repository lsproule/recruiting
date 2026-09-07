package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

// The job kinds. A kind names both the payload shape and the handler that
// works it; every one of them must have a handler registered in a worker.
//
//	email.send        {template, to, org_id, data}
//	interview.remind  {slot_id, offset}
//	assessment.invite {attempt_id}
//	assessment.remind {attempt_id}
//	runner.execute    {submission_id}
//	attempt.finalize  {attempt_id}
//	signals.compute   {attempt_id}
//	attempt.purge_preview {}
//	snapshot.purge    {}
//
// Handlers must be idempotent on their payload: a job may be worked more than
// once when a worker dies mid-flight.
const (
	KindEmailSend        = "email.send"
	KindInterviewRemind  = "interview.remind"
	KindAssessmentInvite = "assessment.invite"
	KindAssessmentRemind = "assessment.remind"
	KindRunnerExecute    = "runner.execute"
	KindAttemptFinalize  = "attempt.finalize"
	KindSignalsCompute   = "signals.compute"
	// attempt.purge_preview sweeps every org; its payload is empty.
	KindAttemptPurgePreview = "attempt.purge_preview"
	// snapshot.purge sweeps every org under its own retention; its payload
	// is empty.
	KindSnapshotPurge = "snapshot.purge"
)

// MaxAttempts is how many times a job is tried before it is discarded.
const MaxAttempts = 5

// DefaultBackoff is the first retry delay; each further attempt doubles it.
const DefaultBackoff = 15 * time.Second

// MaxBackoff caps the retry delay so a late attempt still lands in a shift.
const MaxBackoff = time.Hour

// A worked job's row keeps its payload, and a payload may carry a password
// reset link or a candidate's details. Retention is therefore short: long
// enough to inspect what a worker just did, too short to be a store of
// personal data. A discarded job is kept longer because someone has to be
// able to see why it failed.
const (
	CompletedRetention = 15 * time.Minute
	DiscardedRetention = time.Hour
)

// shutdownGrace bounds how long a running job has once the worker is asked to
// stop; longer than that and the job is left for another worker to pick up.
const shutdownGrace = 30 * time.Second

// queueName is the single queue every kind runs on. Splitting kinds across
// queues is a throughput decision no deployment has needed yet.
const queueName = river.QueueDefault

// Job is one unit of work as a handler sees it.
type Job struct {
	ID      int64
	Kind    string
	Attempt int
	Payload json.RawMessage
}

// Handler works one kind. Returning an error retries the job with backoff
// until MaxAttempts, after which it is discarded.
type Handler func(ctx context.Context, job Job) error

// Config configures a queue client. Handlers is set only by a worker; an
// insert-only client (the HTTP server) leaves it nil.
type Config struct {
	Logger   *slog.Logger
	Handlers map[string]Handler
	// Workers is how many jobs run at once; defaults to 10.
	Workers int
	// Backoff is the first retry delay; defaults to DefaultBackoff. Tests set
	// it low so a retry is observable.
	Backoff time.Duration
}

// Client inserts jobs and, in a worker, runs them.
type Client struct {
	river  *river.Client[pgx.Tx]
	logger *slog.Logger
}

var (
	// ErrNoHandler reports kinds a worker was started without.
	ErrNoHandler = errors.New("queue: no handler registered for kind")
	// ErrUnknownKind reports a handler registered under a kind that is not
	// one of the agreed set.
	ErrUnknownKind = errors.New("queue: unknown job kind")
)

// Kinds lists every job kind, in declaration order.
func Kinds() []string {
	kinds := make([]string, 0, len(registry))
	for _, def := range registry {
		kinds = append(kinds, def.kind)
	}
	return kinds
}

// New builds an insert-only client: it can enqueue but works nothing.
func New(pool *pgxpool.Pool, cfg Config) (*Client, error) {
	rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:      cfg.Logger,
		MaxAttempts: MaxAttempts,
	})
	if err != nil {
		return nil, fmt.Errorf("queue: client: %w", err)
	}
	return &Client{river: rc, logger: cfg.Logger}, nil
}

// NewWorker builds a client that works jobs. It fails when any kind lacks a
// handler, so a deploy that forgot one dies at startup rather than leaving
// those jobs to pile up unworked.
func NewWorker(pool *pgxpool.Pool, cfg Config) (*Client, error) {
	workers := river.NewWorkers()
	var missing []string
	known := make(map[string]bool, len(registry))
	for _, def := range registry {
		known[def.kind] = true
		h, ok := cfg.Handlers[def.kind]
		if !ok {
			missing = append(missing, def.kind)
			continue
		}
		if err := def.addWorker(workers, h); err != nil {
			return nil, fmt.Errorf("queue: register %s: %w", def.kind, err)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("%w: %v", ErrNoHandler, missing)
	}
	var unknown []string
	for kind := range cfg.Handlers {
		if !known[kind] {
			unknown = append(unknown, kind)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("%w: %v", ErrUnknownKind, unknown)
	}

	workerCount := cfg.Workers
	if workerCount <= 0 {
		workerCount = 10
	}
	backoff := cfg.Backoff
	if backoff <= 0 {
		backoff = DefaultBackoff
	}
	rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:                      cfg.Logger,
		MaxAttempts:                 MaxAttempts,
		RetryPolicy:                 &backoffPolicy{base: backoff},
		CompletedJobRetentionPeriod: CompletedRetention,
		DiscardedJobRetentionPeriod: DiscardedRetention,
		Queues:                      map[string]river.QueueConfig{queueName: {MaxWorkers: workerCount}},
		Workers:                     workers,
	})
	if err != nil {
		return nil, fmt.Errorf("queue: worker client: %w", err)
	}
	return &Client{river: rc, logger: cfg.Logger}, nil
}

// EncodePayload marshals a payload to the JSON a job carries.
func EncodePayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return json.RawMessage("{}"), nil
	}
	if raw, ok := payload.(json.RawMessage); ok {
		return raw, nil
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("queue: encode payload: %w", err)
	}
	return b, nil
}

// Enqueue adds a job inside tx, so the work is queued exactly when the
// transaction that decided on it commits — a rolled-back stage move enqueues
// nothing. *store.Tx satisfies pgx.Tx.
func (c *Client) Enqueue(ctx context.Context, tx pgx.Tx, kind string, payload any) (int64, error) {
	return c.enqueue(ctx, tx, kind, payload, time.Time{})
}

// EnqueueAt is Enqueue with a run time in the future: the job is guaranteed
// not to run before runAt. Both return the job id, which a caller may keep
// to cancel the job later with CancelTx.
func (c *Client) EnqueueAt(ctx context.Context, tx pgx.Tx, kind string, payload any, runAt time.Time) (int64, error) {
	return c.enqueue(ctx, tx, kind, payload, runAt)
}

// CancelTx cancels a job inside tx. A job that has already finished or does
// not exist is not an error: the point is that it will not run, and it will
// not.
func (c *Client) CancelTx(ctx context.Context, tx pgx.Tx, id int64) error {
	if _, err := c.river.JobCancelTx(ctx, tx, id); err != nil && !errors.Is(err, rivertype.ErrNotFound) {
		return fmt.Errorf("queue: cancel job %d: %w", id, err)
	}
	return nil
}

func (c *Client) enqueue(ctx context.Context, tx pgx.Tx, kind string, payload any, runAt time.Time) (int64, error) {
	def, ok := defByKind[kind]
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrUnknownKind, kind)
	}
	raw, err := EncodePayload(payload)
	if err != nil {
		return 0, err
	}
	opts := &river.InsertOpts{Queue: queueName, MaxAttempts: MaxAttempts}
	if !runAt.IsZero() {
		opts.ScheduledAt = runAt.UTC()
	}
	// A periodic sweep has no transaction to commit with; everything else
	// queues inside the one that decided on the work.
	if tx == nil {
		res, err := c.river.Insert(ctx, def.args(raw), opts)
		if err != nil {
			return 0, fmt.Errorf("queue: enqueue %s: %w", kind, err)
		}
		return res.Job.ID, nil
	}
	res, err := c.river.InsertTx(ctx, tx, def.args(raw), opts)
	if err != nil {
		return 0, fmt.Errorf("queue: enqueue %s: %w", kind, err)
	}
	return res.Job.ID, nil
}

// Run works jobs until ctx is cancelled, then stops gracefully: running jobs
// get shutdownGrace to finish before the process exits.
func (c *Client) Run(ctx context.Context) error {
	// River's own context must outlive ctx, or cancelling it would abort
	// running jobs instead of draining them.
	if err := c.river.Start(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("queue: start: %w", err)
	}
	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	if err := c.river.Stop(stopCtx); err != nil {
		return fmt.Errorf("queue: stop: %w", err)
	}
	return nil
}

// Backoff is the delay before the given attempt is retried: the base doubled
// per prior attempt, capped at MaxBackoff.
func Backoff(base time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for i := 1; i < attempt && d < MaxBackoff; i++ {
		d *= 2
	}
	if d > MaxBackoff {
		return MaxBackoff
	}
	return d
}

// backoffPolicy applies Backoff to river's retry scheduling, spread by up to
// a quarter of the delay so a dependency that failed for many jobs at once
// does not get all of their retries in the same instant.
type backoffPolicy struct{ base time.Duration }

func (p *backoffPolicy) NextRetry(job *rivertype.JobRow) time.Time {
	d := Backoff(p.base, job.Attempt)
	spread := int64(d / 4)
	if spread > 0 {
		d += time.Duration(rand.Int64N(spread))
	}
	return time.Now().UTC().Add(d)
}
