//go:build integration

package queue_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/store"
)

// testBackoff keeps a retry observable inside a test's lifetime.
const testBackoff = 50 * time.Millisecond

type fixture struct {
	st    *store.Store
	pool  *pgxpool.Pool
	sys   *pgxpool.Pool
	orgID uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ownerURL := os.Getenv("DATABASE_URL")
	if ownerURL == "" {
		t.Fatal("DATABASE_URL is not set; run `make dev-up` and use `make test-integration`")
	}
	lockSchema(t, ownerURL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	if err := store.MigrateUp(ctx, ownerURL); err != nil {
		t.Fatal(err)
	}
	sys, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sys.Close)

	u, _ := url.Parse(ownerURL)
	u.User = url.UserPassword("app_rw", "app_rw")
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	f := &fixture{st: st, pool: st.Pool(), sys: sys, orgID: uuid.New()}
	if _, err := sys.Exec(ctx, `insert into org (id, name, slug) values ($1, $2, $2)`, f.orgID, "Queue Test "+f.orgID.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sys.Exec(context.Background(), `delete from org where id = $1`, f.orgID) })
	return f
}

func lockSchema(t *testing.T, ownerURL string) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), ownerURL)
	if err != nil {
		t.Fatalf("schema lock connect: %v", err)
	}
	if _, err := conn.Exec(context.Background(), "select pg_advisory_lock_shared($1)", 7371); err != nil {
		t.Fatalf("schema lock: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type principal struct{ orgID uuid.UUID }

func (p principal) Scope() store.Scope { return store.Scope{OrgID: p.orgID} }

// stubHandlers fills in every kind the test is not interested in, so the
// worker starts.
func stubHandlers(under map[string]queue.Handler) map[string]queue.Handler {
	all := make(map[string]queue.Handler, len(queue.Kinds()))
	for _, kind := range queue.Kinds() {
		all[kind] = func(context.Context, queue.Job) error { return nil }
	}
	for kind, h := range under {
		all[kind] = h
	}
	return all
}

// runWorker starts a worker for the test's lifetime.
func runWorker(t *testing.T, pool *pgxpool.Pool, handlers map[string]queue.Handler) {
	t.Helper()
	q, err := queue.NewWorker(pool, queue.Config{
		Logger:   discardLogger(),
		Handlers: handlers,
		Backoff:  testBackoff,
		Workers:  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- q.Run(ctx) }()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("worker: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("worker did not stop")
		}
	})
}

// waitFor polls until cond holds, so a test never sleeps longer than it must.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// enqueue adds a job in a tenant transaction, the way a stage move would.
func (f *fixture) enqueue(t *testing.T, q *queue.Client, kind string, payload any) {
	t.Helper()
	err := f.st.WithTx(context.Background(), principal{orgID: f.orgID}, func(ctx context.Context, tx *store.Tx) error {
		_, err := q.Enqueue(ctx, tx, kind, payload)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A job enqueued in a transaction that rolls back must never be worked, and
// one enqueued in a transaction that commits must be.
func TestEnqueueIsTransactional(t *testing.T) {
	f := newFixture(t)
	q, err := queue.New(f.pool, queue.Config{Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	var worked sync.Map
	runWorker(t, f.pool, stubHandlers(map[string]queue.Handler{
		queue.KindAttemptFinalize: func(_ context.Context, job queue.Job) error {
			var p struct {
				AttemptID string `json:"attempt_id"`
			}
			if err := json.Unmarshal(job.Payload, &p); err != nil {
				return err
			}
			worked.Store(p.AttemptID, true)
			return nil
		},
	}))

	rollback := uuid.NewString()
	wantRollback := errors.New("changed my mind")
	err = f.st.WithTx(context.Background(), principal{orgID: f.orgID}, func(ctx context.Context, tx *store.Tx) error {
		if _, err := q.Enqueue(ctx, tx, queue.KindAttemptFinalize, map[string]string{"attempt_id": rollback}); err != nil {
			return err
		}
		return wantRollback
	})
	if !errors.Is(err, wantRollback) {
		t.Fatalf("WithTx = %v, want the caller's error", err)
	}

	committed := uuid.NewString()
	f.enqueue(t, q, queue.KindAttemptFinalize, map[string]string{"attempt_id": committed})
	waitFor(t, "the committed job to be worked", 20*time.Second, func() bool {
		_, ok := worked.Load(committed)
		return ok
	})
	if _, ok := worked.Load(rollback); ok {
		t.Error("a job enqueued in a rolled-back transaction was worked")
	}
}

// A handler that fails once is retried and then succeeds; the job is not lost
// and is not worked forever.
func TestFailingJobIsRetriedThenSucceeds(t *testing.T) {
	f := newFixture(t)
	q, err := queue.New(f.pool, queue.Config{Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int64
	done := make(chan int, 8)
	runWorker(t, f.pool, stubHandlers(map[string]queue.Handler{
		queue.KindSignalsCompute: func(_ context.Context, job queue.Job) error {
			attempts.Add(1)
			if job.Attempt < 2 {
				return errors.New("transient")
			}
			done <- job.Attempt
			return nil
		},
	}))
	f.enqueue(t, q, queue.KindSignalsCompute, map[string]string{"attempt_id": uuid.NewString()})

	select {
	case attempt := <-done:
		if attempt != 2 {
			t.Errorf("succeeded on attempt %d, want the second", attempt)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("job never succeeded; %d attempts made", attempts.Load())
	}
}

// A handler that always fails stops after MaxAttempts rather than retrying
// forever, and the job ends up discarded.
func TestPermanentFailureStopsAtMaxAttempts(t *testing.T) {
	f := newFixture(t)
	q, err := queue.New(f.pool, queue.Config{Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	var attempts atomic.Int64
	runWorker(t, f.pool, stubHandlers(map[string]queue.Handler{
		queue.KindRunnerExecute: func(_ context.Context, job queue.Job) error {
			attempts.Add(1)
			return fmt.Errorf("always fails on attempt %d", job.Attempt)
		},
	}))
	f.enqueue(t, q, queue.KindRunnerExecute, map[string]string{"submission_id": id})

	waitFor(t, "the job to be discarded", 60*time.Second, func() bool {
		var state string
		err := f.sys.QueryRow(context.Background(),
			`select state from river_job where kind = $1 and encode(args::text::bytea, 'escape') like '%' || $2 || '%'`,
			queue.KindRunnerExecute, id).Scan(&state)
		return err == nil && state == "discarded"
	})
	if got := attempts.Load(); got != queue.MaxAttempts {
		t.Errorf("handler ran %d times, want %d", got, queue.MaxAttempts)
	}
}

// A scheduled job waits for its run time and is not worked early.
func TestScheduledJobRunsAtItsTime(t *testing.T) {
	f := newFixture(t)
	q, err := queue.New(f.pool, queue.Config{Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	ran := make(chan time.Time, 1)
	runWorker(t, f.pool, stubHandlers(map[string]queue.Handler{
		queue.KindInterviewRemind: func(_ context.Context, _ queue.Job) error {
			select {
			case ran <- time.Now():
			default:
			}
			return nil
		},
	}))

	runAt := time.Now().Add(3 * time.Second)
	err = f.st.WithTx(context.Background(), principal{orgID: f.orgID}, func(ctx context.Context, tx *store.Tx) error {
		_, err := q.EnqueueAt(ctx, tx, queue.KindInterviewRemind,
			map[string]string{"slot_id": uuid.NewString(), "offset": "24h"}, runAt)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case at := <-ran:
		if at.Before(runAt) {
			t.Errorf("scheduled job ran at %s, before its run time %s", at, runAt)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("scheduled job never ran")
	}
}

// recorder stands in for SMTP so the log's state can be checked without a
// mail server.
type recorder struct {
	mu   sync.Mutex
	sent []mail.Message
	fail func(attempt int) error
	n    int
	// only limits recording to one recipient; other packages' tests enqueue
	// email.send jobs into the shared queue and this worker drains them too.
	only string
}

func (r *recorder) Send(_ context.Context, m mail.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.only != "" && m.To != r.only {
		return nil
	}
	r.n++
	if r.fail != nil {
		if err := r.fail(r.n); err != nil {
			return err
		}
	}
	r.sent = append(r.sent, m)
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

func (f *fixture) emailLog(t *testing.T, template string) (status string, attempts int32, lastErr *string) {
	t.Helper()
	err := f.sys.QueryRow(context.Background(),
		`select status, attempts, last_error from email_log where org_id = $1 and template = $2`,
		f.orgID, template).Scan(&status, &attempts, &lastErr)
	if err != nil {
		t.Fatalf("read email_log: %v", err)
	}
	return status, attempts, lastErr
}

// The happy path: one row in email_log, moved to sent, and one message out.
func TestEmailJobSendsAndLogs(t *testing.T) {
	f := newFixture(t)
	q, err := queue.New(f.pool, queue.Config{Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := mail.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{only: "ada@example.test"}
	runWorker(t, f.pool, stubHandlers(map[string]queue.Handler{
		queue.KindEmailSend: queue.EmailHandler(f.st, renderer, rec, discardLogger()),
	}))
	f.enqueue(t, q, queue.KindEmailSend, queue.EmailPayload{
		Template: mail.TemplateApplyReceived,
		To:       "ada@example.test",
		OrgID:    f.orgID,
		Data:     map[string]any{"CandidateName": "Ada", "JobTitle": "Backend Engineer"},
	})

	waitFor(t, "the email to be sent", 30*time.Second, func() bool { return rec.count() == 1 })
	waitFor(t, "email_log to reach sent", 30*time.Second, func() bool {
		var status string
		err := f.sys.QueryRow(context.Background(),
			`select status from email_log where org_id = $1 and template = $2`,
			f.orgID, mail.TemplateApplyReceived).Scan(&status)
		return err == nil && status == queue.EmailSent
	})
	msg := rec.sent[0]
	if msg.To != "ada@example.test" || msg.Subject == "" {
		t.Errorf("sent %+v", msg)
	}
}

// An SMTP failure is logged and retried on the same row, and a later success
// leaves the row sent rather than adding a second one.
func TestEmailJobRetriesOnTheSameLogRow(t *testing.T) {
	f := newFixture(t)
	q, err := queue.New(f.pool, queue.Config{Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := mail.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{only: "ada@example.test", fail: func(attempt int) error {
		if attempt == 1 {
			return errors.New("connection refused")
		}
		return nil
	}}
	runWorker(t, f.pool, stubHandlers(map[string]queue.Handler{
		queue.KindEmailSend: queue.EmailHandler(f.st, renderer, rec, discardLogger()),
	}))
	f.enqueue(t, q, queue.KindEmailSend, queue.EmailPayload{
		Template: mail.TemplateReminder,
		To:       "ada@example.test",
		OrgID:    f.orgID,
		Data: map[string]any{
			"CandidateName": "Ada", "JobTitle": "Backend Engineer",
			"StartsAt": "2026-09-02 14:00", "Timezone": "UTC",
			"BookingURL": "https://example.test/book/abc",
		},
	})

	waitFor(t, "email_log to reach sent after a retry", 60*time.Second, func() bool {
		var status string
		err := f.sys.QueryRow(context.Background(),
			`select status from email_log where org_id = $1 and template = $2`,
			f.orgID, mail.TemplateReminder).Scan(&status)
		return err == nil && status == queue.EmailSent
	})
	var rows int
	if err := f.sys.QueryRow(context.Background(),
		`select count(*) from email_log where org_id = $1 and template = $2`,
		f.orgID, mail.TemplateReminder).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("email_log has %d rows for the job, want 1", rows)
	}
	if _, attempts, _ := f.emailLog(t, mail.TemplateReminder); attempts < 2 {
		t.Errorf("attempts = %d, want the failed try counted too", attempts)
	}
}

// A payload that does not carry every variable the template needs must fail
// while rendering, before anything is sent.
func TestEmailJobFailsRenderBeforeSending(t *testing.T) {
	f := newFixture(t)
	q, err := queue.New(f.pool, queue.Config{Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := mail.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{only: "ada@example.test"}
	runWorker(t, f.pool, stubHandlers(map[string]queue.Handler{
		queue.KindEmailSend: queue.EmailHandler(f.st, renderer, rec, discardLogger()),
	}))
	f.enqueue(t, q, queue.KindEmailSend, queue.EmailPayload{
		Template: mail.TemplateBookingInvite,
		To:       "ada@example.test",
		OrgID:    f.orgID,
		Data:     map[string]any{"CandidateName": "Ada"}, // no JobTitle, no BookingURL
	})

	waitFor(t, "email_log to record the failure", 30*time.Second, func() bool {
		var status string
		err := f.sys.QueryRow(context.Background(),
			`select status from email_log where org_id = $1 and template = $2`,
			f.orgID, mail.TemplateBookingInvite).Scan(&status)
		return err == nil && status == queue.EmailFailed
	})
	_, _, lastErr := f.emailLog(t, mail.TemplateBookingInvite)
	if lastErr == nil || *lastErr == "" {
		t.Error("email_log.last_error should say why the render failed")
	}
	if rec.count() != 0 {
		t.Errorf("%d messages were sent despite the render failing", rec.count())
	}
}

// The end-to-end path the Compose stack exists for: a queued email arrives in
// Mailpit over real SMTP.
func TestEmailJobReachesMailpit(t *testing.T) {
	smtpURL := envOr("SMTP_URL", "smtp://localhost:1025")
	apiURL := envOr("MAILPIT_URL", "http://localhost:8025")
	u, err := url.Parse(smtpURL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", u.Host, 2*time.Second)
	if err != nil {
		t.Skipf("no SMTP server on %s; run `make dev-up`: %v", u.Host, err)
	}
	_ = conn.Close()

	f := newFixture(t)
	q, err := queue.New(f.pool, queue.Config{Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := mail.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	sender, err := mail.NewSMTPSender(smtpURL, mail.FromAddress("http://localhost:8080"))
	if err != nil {
		t.Fatal(err)
	}
	runWorker(t, f.pool, stubHandlers(map[string]queue.Handler{
		queue.KindEmailSend: queue.EmailHandler(f.st, renderer, sender, discardLogger()),
	}))

	to := "candidate-" + uuid.NewString() + "@example.test"
	f.enqueue(t, q, queue.KindEmailSend, queue.EmailPayload{
		Template: mail.TemplateApplyReceived,
		To:       to,
		OrgID:    f.orgID,
		Data:     map[string]any{"CandidateName": "Ada", "JobTitle": "Backend Engineer"},
	})

	waitFor(t, "the message to reach Mailpit", 30*time.Second, func() bool {
		return mailpitCount(t, apiURL, to) == 1
	})
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func mailpitCount(t *testing.T, apiURL, to string) int {
	t.Helper()
	res, err := http.Get(apiURL + "/api/v1/search?query=" + url.QueryEscape("to:"+to))
	if err != nil {
		return 0
	}
	defer res.Body.Close()
	var body struct {
		MessagesCount int `json:"messages_count"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return 0
	}
	return body.MessagesCount
}

// failingStore fails the nth WithTx call, standing in for a database that
// goes away between the send and the bookkeeping that records it.
type failingStore struct {
	inner  queue.TenantStore
	failOn int
	calls  atomic.Int64
}

func (s *failingStore) WithTx(ctx context.Context, p store.Principal, fn func(context.Context, *store.Tx) error) error {
	if int(s.calls.Add(1)) == s.failOn {
		return errors.New("database went away")
	}
	return s.inner.WithTx(ctx, p, fn)
}

// Once SMTP has accepted a message the send cannot be taken back. A failure
// to record it must not retry the job, or the recipient gets a second copy.
func TestEmailJobDoesNotResendWhenBookkeepingFails(t *testing.T) {
	f := newFixture(t)
	q, err := queue.New(f.pool, queue.Config{Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := mail.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{only: "ada@example.test"}
	// The handler opens three transactions: org lookup, log row, and the
	// status write that follows the send.
	st := &failingStore{inner: f.st, failOn: 3}
	runWorker(t, f.pool, stubHandlers(map[string]queue.Handler{
		queue.KindEmailSend: queue.EmailHandler(st, renderer, rec, discardLogger()),
	}))
	f.enqueue(t, q, queue.KindEmailSend, queue.EmailPayload{
		Template: mail.TemplateApplyReceived,
		To:       "ada@example.test",
		OrgID:    f.orgID,
		Data:     map[string]any{"CandidateName": "Ada", "JobTitle": "Backend Engineer"},
	})

	waitFor(t, "the job to complete", 30*time.Second, func() bool {
		var state string
		err := f.sys.QueryRow(context.Background(),
			`select state from river_job where kind = $1 and args->>'payload' like '%' || $2 || '%'`,
			queue.KindEmailSend, "ada@example.test").Scan(&state)
		return err == nil && state == "completed"
	})
	// Give a retry, were one scheduled, time to send a second copy.
	time.Sleep(2 * time.Second)
	if got := rec.count(); got != 1 {
		t.Errorf("sent %d messages, want exactly 1", got)
	}
}

// A worker that dies after sending but before completing the job leaves the
// job to be worked again. The log row says the message is already out, so the
// second run must not send it twice.
func TestEmailHandlerSkipsAJobAlreadyLoggedAsSent(t *testing.T) {
	f := newFixture(t)
	renderer, err := mail.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{only: "grace@example.test"}
	handler := queue.EmailHandler(f.st, renderer, rec, discardLogger())

	payload, err := json.Marshal(queue.EmailPayload{
		Template: mail.TemplateApplyReceived,
		To:       "grace@example.test",
		OrgID:    f.orgID,
		Data:     map[string]any{"CandidateName": "Grace", "JobTitle": "Backend Engineer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The same job id twice is exactly what a redelivery looks like.
	job := queue.Job{ID: time.Now().UnixNano(), Kind: queue.KindEmailSend, Attempt: 1, Payload: payload}
	if err := handler(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	job.Attempt = 2
	if err := handler(context.Background(), job); err != nil {
		t.Fatalf("the redelivery should succeed without sending: %v", err)
	}
	if got := rec.count(); got != 1 {
		t.Errorf("sent %d messages, want exactly 1", got)
	}
	status, _, _ := f.emailLog(t, mail.TemplateApplyReceived)
	if status != queue.EmailSent {
		t.Errorf("email_log status = %q, want %q", status, queue.EmailSent)
	}
}
