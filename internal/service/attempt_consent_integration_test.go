//go:build integration

package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/service"
)

// integrity turns the assessment's measures on, which is what makes the
// session ask for consent before it starts.
func (f *attemptFixture) integrity(t *testing.T, in service.IntegritySettings) {
	t.Helper()
	rec := f.principal(service.RoleRecruiter)
	_, err := service.NewAssessmentService(f.st).Update(context.Background(), rec, f.assessment.ID, service.AssessmentInput{
		Name: f.assessment.Name, DurationMinutes: f.assessment.DurationMinutes, InviteWindowDays: f.assessment.InviteWindowDays,
		ProblemIDs: []uuid.UUID{f.problem.ID}, Integrity: in,
	})
	if err != nil {
		t.Fatalf("set integrity: %v", err)
	}
}

// recruiter gives the org's user the recruiter role, which is who a declined
// invite has to reach.
func (f *attemptFixture) recruiter(t *testing.T) {
	t.Helper()
	if _, err := f.sys.Exec(context.Background(),
		`insert into org_user_role (org_user_id, org_id, role) values ($1, $2, 'recruiter')`, f.userID, f.orgID); err != nil {
		t.Fatal(err)
	}
}

// A recorded session cannot begin behind the candidate's back: with any
// measure on, Start refuses until consent is on the attempt, and the
// recording then opens with what was agreed to.
func TestStartNeedsConsentWhenIntegrityIsOn(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	f.integrity(t, service.IntegritySettings{Webcam: true, WebcamEvery: 60, PhotoID: true, Fullscreen: true, BlockPaste: true})
	att := f.invite(t)
	cand := f.candidate(att.ID)

	if _, err := f.attempts.Start(ctx, cand); !errors.Is(err, service.ErrConsentRequired) {
		t.Fatalf("start without consent: err = %v, want ErrConsentRequired", err)
	}
	s, err := f.attempts.Session(ctx, cand)
	if err != nil || s.Attempt.Status != service.AttemptInvited {
		t.Fatalf("attempt = %q %v, want it still invited", s.Attempt.Status, err)
	}

	if err := f.attempts.Consent(ctx, cand); err != nil {
		t.Fatalf("consent: %v", err)
	}
	started, err := f.attempts.Start(ctx, cand)
	if err != nil {
		t.Fatalf("start after consent: %v", err)
	}
	if started.Attempt.Status != service.AttemptStarted {
		t.Fatalf("attempt = %q, want started", started.Attempt.Status)
	}
	var consented bool
	if err := f.sys.QueryRow(ctx, `select consent_at is not null from attempt where id = $1`, att.ID).Scan(&consented); err != nil {
		t.Fatal(err)
	}
	if !consented {
		t.Error("consent_at is unset on a consented attempt")
	}
	var payload []byte
	if err := f.sys.QueryRow(ctx,
		`select payload from attempt_event where attempt_id = $1 and kind = 'consent'`, att.ID).Scan(&payload); err != nil {
		t.Fatalf("consent event: %v", err)
	}
	var got struct {
		Webcam  bool `json:"webcam"`
		PhotoID bool `json:"photo_id"`
	}
	if err := json.Unmarshal(payload, &got); err != nil || !got.Webcam || !got.PhotoID {
		t.Errorf("consent event = %s (%v), want what the candidate agreed to", payload, err)
	}
}

// With every measure off nothing changed: there is nothing to consent to,
// so Start is the one step it has always been.
func TestStartNeedsNoConsentWhenIntegrityIsOff(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	att := f.invite(t)
	if _, err := f.attempts.Start(ctx, f.candidate(att.ID)); err != nil {
		t.Fatalf("start: %v", err)
	}
	var events int
	if err := f.sys.QueryRow(ctx, `select count(*) from attempt_event where attempt_id = $1`, att.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Errorf("%d events on a session with no measures, want none", events)
	}
}

// Declining records nothing about the candidate and leaves the invite where
// it was, but the recruiter has to learn that it stalled.
func TestDeclineLeavesTheInviteAndTellsTheRecruiter(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	f.integrity(t, service.IntegritySettings{Webcam: true, WebcamEvery: 60})
	f.recruiter(t)
	att := f.invite(t)

	if err := f.attempts.Decline(ctx, f.candidate(att.ID)); err != nil {
		t.Fatalf("decline: %v", err)
	}
	var status string
	var consented bool
	if err := f.sys.QueryRow(ctx, `select status, consent_at is not null from attempt where id = $1`, att.ID).Scan(&status, &consented); err != nil {
		t.Fatal(err)
	}
	if status != service.AttemptInvited || consented {
		t.Errorf("attempt = %q consented=%v, want it left invited with no consent", status, consented)
	}
	var events int
	if err := f.sys.QueryRow(ctx, `select count(*) from attempt_event where attempt_id = $1`, att.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Errorf("%d events recorded for a declined session, want none", events)
	}

	var raw string
	if err := f.sys.QueryRow(ctx,
		`select args->>'payload' from river_job
		 where kind = $1 and args->>'payload' like '%' || $2 || '%' and args->>'payload' like '%' || $3 || '%'`,
		queue.KindEmailSend, mail.TemplateAssessmentDeclined, f.orgID.String()).Scan(&raw); err != nil {
		t.Fatalf("declined email job: %v", err)
	}
	var email queue.EmailPayload
	if err := json.Unmarshal([]byte(raw), &email); err != nil {
		t.Fatal(err)
	}
	if email.To != "rec-"+f.orgID.String()+"@example.com" {
		t.Errorf("declined email to %q, want the org's recruiter", email.To)
	}
}
