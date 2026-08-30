//go:build integration

package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/queue"
	"recruiting/internal/service"
)

type attemptFixture struct {
	*pipelineFixture
	q          *queue.Client
	attempts   *service.AttemptService
	assessment service.Assessment
	problem    service.Problem
	now        time.Time
}

func newAttemptFixture(t *testing.T) *attemptFixture {
	t.Helper()
	pf := newPipelineFixture(t)
	ctx := context.Background()
	rec := pf.principal(service.RoleRecruiter)

	parsed, err := domain.ParseProblemImport([]byte("[" + codeProblemJSON("Attempt Adder", "print(3)") + "]"))
	if err != nil {
		t.Fatal(err)
	}
	problem, err := service.NewProblemService(pf.st, passExecutor{}).Create(ctx, rec, parsed[0])
	if err != nil {
		t.Fatalf("create problem: %v", err)
	}
	assessments := service.NewAssessmentService(pf.st)
	a, err := assessments.Create(ctx, rec, service.AssessmentInput{
		Name: "Backend screen", DurationMinutes: 30, InviteWindowDays: 5, ProblemIDs: []uuid.UUID{problem.ID},
	})
	if err != nil {
		t.Fatalf("create assessment: %v", err)
	}
	if err := assessments.AttachToStage(ctx, rec, pf.jobID, pf.stages[domain.StageAssessment], a.ID); err != nil {
		t.Fatalf("attach: %v", err)
	}
	q, err := queue.New(pf.st.Pool(), queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f := &attemptFixture{pipelineFixture: pf, q: q, assessment: a, problem: problem, now: time.Now().UTC().Truncate(time.Second)}
	f.attempts = service.NewAttemptService(pf.st, q, "https://example.test/")
	f.attempts.Now = func() time.Time { return f.now }
	return f
}

func (f *attemptFixture) invite(t *testing.T) service.Attempt {
	t.Helper()
	h := service.AssessmentInviteHandler(f.st, f.q, "https://example.test/")
	payload, _ := json.Marshal(service.AssessmentInvitePayload{ApplicationID: f.appID, StageID: f.stages[domain.StageAssessment], OrgID: f.orgID})
	if err := h(context.Background(), queue.Job{Kind: queue.KindAssessmentInvite, Payload: payload}); err != nil {
		t.Fatalf("invite handler: %v", err)
	}
	var id uuid.UUID
	if err := f.sys.QueryRow(context.Background(), `select id from attempt where application_id = $1 and stage_id = $2`, f.appID, f.stages[domain.StageAssessment]).Scan(&id); err != nil {
		t.Fatalf("attempt row: %v", err)
	}
	att, err := f.attempts.Session(context.Background(), f.candidate(id))
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	return att.Attempt
}

func (f *attemptFixture) candidate(attemptID uuid.UUID) service.Principal {
	return service.Principal{Kind: service.PrincipalMagicLink, OrgID: f.orgID, MagicPurpose: service.LinkAssessment, SubjectID: attemptID}
}

func TestInviteHandlerCreatesOneAttemptAndQueuesTheEmail(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	att := f.invite(t)
	if att.Status != service.AttemptInvited || att.AssessmentID != f.assessment.ID {
		t.Fatalf("attempt = %+v, want invited on the assessment", att)
	}
	wantExpiry := f.now.Add(5 * 24 * time.Hour)
	if d := att.InviteExpiresAt.Sub(wantExpiry); d < -time.Minute || d > time.Minute {
		t.Errorf("invite expires %v, want the assessment's 5 invite days (%v)", att.InviteExpiresAt, wantExpiry)
	}
	var links int
	if err := f.sys.QueryRow(ctx, `select count(*) from magic_link where purpose = 'assessment' and subject_id = $1`, att.ID).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Errorf("%d assessment links, want 1", links)
	}
	if n := f.jobs(t, queue.KindEmailSend); n != 1 {
		t.Errorf("%d email jobs, want 1", n)
	}
	var payload string
	if err := f.sys.QueryRow(ctx, `select args->>'payload' from river_job where kind = $1 and args->>'payload' like '%' || $2 || '%'`, queue.KindEmailSend, f.orgID.String()).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var email queue.EmailPayload
	_ = json.Unmarshal([]byte(payload), &email)
	if email.Template != "assessment_invite" || email.Data["AssessmentURL"] == "" {
		t.Errorf("email payload = %+v, want an assessment_invite with a URL", email)
	}

	// Redelivery of the job must not invite twice.
	f.invite(t)
	var attempts int
	if err := f.sys.QueryRow(ctx, `select count(*) from attempt where application_id = $1`, f.appID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || f.jobs(t, queue.KindEmailSend) != 1 {
		t.Errorf("after redelivery: %d attempts, %d emails; want 1 and 1", attempts, f.jobs(t, queue.KindEmailSend))
	}
}

func TestInviteQueuesAReminderThatOnlyFiresForAnUnstartedAttempt(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	att := f.invite(t)

	var scheduled time.Time
	var payload string
	if err := f.sys.QueryRow(ctx, `select scheduled_at, args->>'payload' from river_job where kind = $1 and args->>'payload' like '%' || $2 || '%'`,
		queue.KindAssessmentRemind, att.ID.String()).Scan(&scheduled, &payload); err != nil {
		t.Fatalf("reminder job: %v", err)
	}
	wantAt := att.InviteExpiresAt.Add(-service.AssessmentReminderLead)
	if d := scheduled.Sub(wantAt); d < -time.Minute || d > time.Minute {
		t.Errorf("reminder scheduled at %v, want %v (24h before the window closes)", scheduled, wantAt)
	}
	var p service.AssessmentRemindPayload
	_ = json.Unmarshal([]byte(payload), &p)
	if p.AttemptID != att.ID || p.OrgID != f.orgID || !strings.Contains(p.AssessmentURL, "https://example.test/") {
		t.Fatalf("reminder payload = %+v", p)
	}

	h := service.AssessmentRemindHandler(f.st, f.q, "https://example.test/")
	if err := h(ctx, queue.Job{Kind: queue.KindAssessmentRemind, Payload: []byte(payload)}); err != nil {
		t.Fatalf("remind handler: %v", err)
	}
	if n := f.jobs(t, queue.KindEmailSend); n != 2 {
		t.Fatalf("%d email jobs after the reminder, want the invite and the reminder", n)
	}
	var reminders int
	if err := f.sys.QueryRow(ctx, `select count(*) from river_job where kind = $1 and args->'payload'->>'template' = $2 and args->>'payload' like '%' || $3 || '%'`,
		queue.KindEmailSend, "assessment_reminder", f.orgID.String()).Scan(&reminders); err != nil {
		t.Fatal(err)
	}
	if reminders != 1 {
		t.Fatalf("%d assessment_reminder emails, want 1", reminders)
	}

	// Once the candidate has started, the reminder has nothing to say.
	if _, err := f.attempts.Start(ctx, f.candidate(att.ID)); err != nil {
		t.Fatal(err)
	}
	if err := h(ctx, queue.Job{Kind: queue.KindAssessmentRemind, Payload: []byte(payload)}); err != nil {
		t.Fatalf("remind handler after start: %v", err)
	}
	if n := f.jobs(t, queue.KindEmailSend); n != 2 {
		t.Errorf("%d email jobs after reminding a started attempt, want still 2", n)
	}
}

func TestStartRefusesAnExpiredInvite(t *testing.T) {
	f := newAttemptFixture(t)
	att := f.invite(t)
	f.now = f.now.Add(6 * 24 * time.Hour)
	if _, err := f.attempts.Start(context.Background(), f.candidate(att.ID)); !errors.Is(err, service.ErrInviteExpired) {
		t.Fatalf("start after the window = %v, want ErrInviteExpired", err)
	}
}

func TestExpiryAutoSubmitsTheLastSyncedSource(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	att := f.invite(t)
	cand := f.candidate(att.ID)

	started, err := f.attempts.Start(ctx, cand)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if started.Attempt.Status != service.AttemptStarted || !started.Attempt.ExpiresAt.Equal(f.now.Add(30*time.Minute)) {
		t.Fatalf("started = %+v, want started with a 30 minute deadline", started.Attempt)
	}
	if err := f.attempts.SaveSource(ctx, cand, att.ID, f.problem.ID, "python", "print(1+2)"); err != nil {
		t.Fatalf("save source: %v", err)
	}
	f.now = f.now.Add(31 * time.Minute)
	if _, err := f.attempts.Session(ctx, cand); !errors.Is(err, service.ErrAttemptExpired) {
		t.Fatalf("session after the deadline = %v, want ErrAttemptExpired", err)
	}
	var status, kind, source string
	if err := f.sys.QueryRow(ctx, `select a.status, s.kind, s.source from attempt a join submission s on s.attempt_id = a.id where a.id = $1`, att.ID).Scan(&status, &kind, &source); err != nil {
		t.Fatalf("auto submission: %v", err)
	}
	if status != service.AttemptExpired || kind != service.SubmissionSubmit || source != "print(1+2)" {
		t.Errorf("got %s/%s/%q, want expired with the synced source submitted", status, kind, source)
	}
	if n := f.jobs(t, queue.KindAttemptFinalize); n != 1 {
		t.Errorf("%d finalize jobs, want 1", n)
	}
	if n := f.jobs(t, queue.KindRunnerExecute); n != 1 {
		t.Errorf("%d runner jobs, want 1", n)
	}
}

func TestRunAndSubmitQueueExecution(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	att := f.invite(t)
	cand := f.candidate(att.ID)
	if _, err := f.attempts.Start(ctx, cand); err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.Run(ctx, cand, att.ID, f.problem.ID, "go", "x"); !errors.Is(err, service.ErrLanguageNotAllowed) {
		t.Fatalf("run in a language the problem forbids = %v, want ErrLanguageNotAllowed", err)
	}
	sub, err := f.attempts.Run(ctx, cand, att.ID, f.problem.ID, "python", "print(3)")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got, err := f.attempts.Submission(ctx, cand, att.ID, sub.ID)
	if err != nil || got.Status != service.SubmissionQueued || got.Kind != service.SubmissionRun {
		t.Fatalf("submission = %+v, %v; want a queued run", got, err)
	}
	// The runner (another task) marks the run done; only then may the
	// candidate submit the same problem.
	if _, err := f.sys.Exec(ctx, `update submission set status = 'done' where id = $1`, sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.Submit(ctx, cand, att.ID, f.problem.ID, "python", "print(3)"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if n := f.jobs(t, queue.KindRunnerExecute); n != 2 {
		t.Errorf("%d runner jobs, want 2", n)
	}
	done, err := f.attempts.Finish(ctx, cand, att.ID)
	if err != nil || done.Status != service.AttemptSubmitted {
		t.Fatalf("finish = %+v, %v", done, err)
	}
	if n := f.jobs(t, queue.KindAttemptFinalize); n != 1 {
		t.Errorf("%d finalize jobs, want 1", n)
	}
	if _, err := f.attempts.Run(ctx, cand, att.ID, f.problem.ID, "python", "print(3)"); !errors.Is(err, service.ErrAttemptClosed) {
		t.Errorf("run after finish = %v, want ErrAttemptClosed", err)
	}
}

func TestEventIngestRejectsStaleSeq(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	att := f.invite(t)
	cand := f.candidate(att.ID)
	if _, err := f.attempts.Start(ctx, cand); err != nil {
		t.Fatal(err)
	}
	ev := func(seq int64, kind string) service.AttemptEvent {
		return service.AttemptEvent{Seq: seq, T: f.now.UnixMilli(), ProblemID: f.problem.ID, Type: kind, Data: json.RawMessage(`{"len":3,"sha256":"` + strings.Repeat("ab", 32) + `","internal":true}`)}
	}
	last, err := f.attempts.RecordEvents(ctx, cand, att.ID, []service.AttemptEvent{ev(1, "focus"), ev(2, "paste")})
	if err != nil || last != 2 {
		t.Fatalf("record = %d, %v", last, err)
	}
	if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, []service.AttemptEvent{ev(2, "blur")}); !errors.Is(err, service.ErrEventSeq) {
		t.Fatalf("duplicate seq = %v, want ErrEventSeq", err)
	}
	if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, []service.AttemptEvent{ev(5, "blur"), ev(4, "focus")}); !errors.Is(err, service.ErrEventSeq) {
		t.Fatalf("out-of-order batch = %v, want ErrEventSeq", err)
	}
	if last, err := f.attempts.RecordEvents(ctx, cand, att.ID, []service.AttemptEvent{ev(3, "blur")}); err != nil || last != 3 {
		t.Fatalf("next seq = %d, %v", last, err)
	}
	var n int
	var stamped bool
	if err := f.sys.QueryRow(ctx, `select count(*), bool_and(server_ts is not null) from attempt_event where attempt_id = $1`, att.ID).Scan(&n, &stamped); err != nil {
		t.Fatal(err)
	}
	if n != 3 || !stamped {
		t.Errorf("%d events stored (stamped %v), want 3", n, stamped)
	}
	var lastSeq int64
	if err := f.sys.QueryRow(ctx, `select last_event_seq from attempt where id = $1`, att.ID).Scan(&lastSeq); err != nil {
		t.Fatal(err)
	}
	if lastSeq != 3 {
		t.Errorf("attempt.last_event_seq = %d, want 3", lastSeq)
	}
}

func TestStartExtendsTheLinkPastTheDeadline(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	att := f.invite(t)
	// A window that ends before a 30 minute session would.
	if _, err := f.sys.Exec(ctx, `update magic_link set expires_at = $2 where subject_id = $1`, att.ID, f.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.Start(ctx, f.candidate(att.ID)); err != nil {
		t.Fatal(err)
	}
	var expires time.Time
	if err := f.sys.QueryRow(ctx, `select expires_at from magic_link where subject_id = $1`, att.ID).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if want := f.now.Add(30*time.Minute + service.LinkGrace); !expires.Equal(want) {
		t.Errorf("link expires %v, want deadline plus grace %v", expires, want)
	}
}

func TestExpireDueClosesOverdueAttemptsAcrossOrgs(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	att := f.invite(t)
	cand := f.candidate(att.ID)
	if _, err := f.attempts.Start(ctx, cand); err != nil {
		t.Fatal(err)
	}
	if err := f.attempts.SaveSource(ctx, cand, att.ID, f.problem.ID, "python", "print(3)"); err != nil {
		t.Fatal(err)
	}
	if n, err := f.attempts.ExpireDue(ctx); err != nil || n != 0 {
		t.Fatalf("before the deadline: %d, %v; want 0", n, err)
	}
	f.now = f.now.Add(31 * time.Minute)
	n, err := f.attempts.ExpireDue(ctx)
	if err != nil || n != 1 {
		t.Fatalf("expire due = %d, %v; want 1", n, err)
	}
	var status string
	var subs int
	if err := f.sys.QueryRow(ctx, `select status, (select count(*) from submission where attempt_id = a.id and kind = 'submit') from attempt a where id = $1`, att.ID).Scan(&status, &subs); err != nil {
		t.Fatal(err)
	}
	if status != service.AttemptExpired || subs != 1 {
		t.Errorf("status %s with %d submits, want expired with 1", status, subs)
	}
	if n := f.jobs(t, queue.KindAttemptFinalize); n != 1 {
		t.Errorf("%d finalize jobs, want 1", n)
	}
	if n, err := f.attempts.ExpireDue(ctx); err != nil || n != 0 {
		t.Errorf("second sweep = %d, %v; want nothing left", n, err)
	}
}

func TestRunRefusesWhileAnotherRunIsPendingAndOversizedSource(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	att := f.invite(t)
	cand := f.candidate(att.ID)
	if _, err := f.attempts.Start(ctx, cand); err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.Run(ctx, cand, att.ID, f.problem.ID, "python", "print(3)"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.Submit(ctx, cand, att.ID, f.problem.ID, "python", "print(3)"); !errors.Is(err, service.ErrSubmissionPending) {
		t.Errorf("submit while a run is queued = %v, want ErrSubmissionPending", err)
	}
	if _, err := f.sys.Exec(ctx, `update submission set status = 'done' where attempt_id = $1`, att.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.Submit(ctx, cand, att.ID, f.problem.ID, "python", "print(3)"); err != nil {
		t.Errorf("submit after the run finished = %v", err)
	}
	big := strings.Repeat("x", service.MaxSourceBytes+1)
	if err := f.attempts.SaveSource(ctx, cand, att.ID, f.problem.ID, "python", big); !errors.Is(err, service.ErrSourceTooLarge) {
		t.Errorf("oversized save = %v, want ErrSourceTooLarge", err)
	}
	other := f.candidate(uuid.New())
	if _, err := f.attempts.Run(ctx, other, att.ID, f.problem.ID, "python", "x"); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("another link on this attempt = %v, want ErrNotFound", err)
	}
}

func TestEventValidation(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	att := f.invite(t)
	cand := f.candidate(att.ID)
	if _, err := f.attempts.Start(ctx, cand); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("ab", 32)
	ev := func(kind string, data string) service.AttemptEvent {
		return service.AttemptEvent{Seq: 1, T: f.now.UnixMilli(), ProblemID: f.problem.ID, Type: kind, Data: json.RawMessage(data)}
	}
	bad := map[string]service.AttemptEvent{
		"unknown problem": {Seq: 1, T: f.now.UnixMilli(), ProblemID: uuid.New(), Type: "focus", Data: json.RawMessage("{}")},
		"nil problem":     {Seq: 1, T: f.now.UnixMilli(), Type: "focus", Data: json.RawMessage("{}")},
		"paste hash":      ev("paste", `{"len":3,"sha256":"abc","internal":true}`),
		"paste len":       ev("paste", `{"len":-1,"sha256":"`+hash+`","internal":true}`),
		"paste internal":  ev("paste", `{"len":1,"sha256":"`+hash+`"}`),
		"run id":          ev("run", `{"submission_id":"nope"}`),
		"lang":            ev("lang_change", `{"language":"cobol"}`),
		"skew":            {Seq: 1, T: f.now.Add(-25 * time.Hour).UnixMilli(), ProblemID: f.problem.ID, Type: "focus", Data: json.RawMessage("{}")},
		"size":            ev("edit", `{"x":"`+strings.Repeat("y", service.MaxEventDataBytes)+`"}`),
		"type":            ev("scroll", `{}`),
	}
	for name, e := range bad {
		if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, []service.AttemptEvent{e}); !errors.Is(err, service.ErrEventInvalid) {
			t.Errorf("%s: err = %v, want ErrEventInvalid", name, err)
		}
	}
	good := []service.AttemptEvent{
		ev("paste", `{"len":3,"sha256":"`+hash+`","internal":false}`),
		ev("run", `{"submission_id":"`+uuid.New().String()+`"}`),
		ev("lang_change", `{"language":"python"}`),
		ev("edit", `[0,[0,"x"]]`),
	}
	for i := range good {
		good[i].Seq = int64(i + 1)
	}
	if last, err := f.attempts.RecordEvents(ctx, cand, att.ID, good); err != nil || last != 4 {
		t.Fatalf("valid batch = %d, %v", last, err)
	}

	// The last batch may land shortly after the close; not long after.
	done, err := f.attempts.Finish(ctx, cand, att.ID)
	if err != nil {
		t.Fatal(err)
	}
	late := ev("submit", `{"submission_id":"`+uuid.New().String()+`"}`)
	late.Seq = 5
	if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, []service.AttemptEvent{late}); err != nil {
		t.Errorf("batch right after finish = %v, want accepted", err)
	}
	if again, err := f.attempts.Finish(ctx, cand, att.ID); err != nil || again.Status != done.Status {
		t.Errorf("second finish = %+v, %v; want the same closed attempt", again, err)
	}
	f.now = f.now.Add(2 * time.Minute)
	late.Seq = 6
	if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, []service.AttemptEvent{late}); !errors.Is(err, service.ErrAttemptClosed) {
		t.Errorf("batch two minutes after finish = %v, want ErrAttemptClosed", err)
	}
}

// The island's last batch may land up to EventGrace after the close, so
// finalization must not run before that window has passed.
func TestFinishSchedulesFinalizeAfterTheEventGrace(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	att := f.invite(t)
	cand := f.candidate(att.ID)
	if _, err := f.attempts.Start(ctx, cand); err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.Finish(ctx, cand, att.ID); err != nil {
		t.Fatal(err)
	}
	var scheduled time.Time
	if err := f.sys.QueryRow(ctx, `select scheduled_at from river_job where kind = $1 and args->>'payload' like '%' || $2 || '%'`,
		queue.KindAttemptFinalize, att.ID.String()).Scan(&scheduled); err != nil {
		t.Fatalf("finalize job: %v", err)
	}
	if want := f.now.Add(service.EventGrace); !scheduled.Equal(want) {
		t.Errorf("finalize scheduled at %v, want finished_at + grace = %v", scheduled, want)
	}
}

// Once finalization has compacted the recording into object storage the
// stream in Postgres is no longer the record; a late batch is refused even
// inside the grace window rather than silently missing from the replay.
func TestEventIngestRefusesOnceTheRecordingIsSealed(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	att := f.invite(t)
	cand := f.candidate(att.ID)
	if _, err := f.attempts.Start(ctx, cand); err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.Finish(ctx, cand, att.ID); err != nil {
		t.Fatal(err)
	}
	ev := func(seq int64) service.AttemptEvent {
		return service.AttemptEvent{Seq: seq, T: f.now.UnixMilli(), ProblemID: f.problem.ID, Type: "blur", Data: json.RawMessage(`{}`)}
	}
	if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, []service.AttemptEvent{ev(1)}); err != nil {
		t.Fatalf("event inside the grace window = %v, want accepted", err)
	}
	if _, err := f.sys.Exec(ctx, `update attempt set recording_blob_key = 'attempts/x/events.jsonl.gz' where id = $1`, att.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, []service.AttemptEvent{ev(2)}); !errors.Is(err, service.ErrAttemptClosed) {
		t.Fatalf("event after sealing = %v, want ErrAttemptClosed", err)
	}
}
