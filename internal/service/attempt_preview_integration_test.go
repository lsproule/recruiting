//go:build integration

package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/queue"
	"recruiting/internal/service"
)

// preview opens the fixture's assessment as its recruiter.
func (f *attemptFixture) preview(t *testing.T) service.AttemptPreview {
	t.Helper()
	p, err := f.attempts.Preview(context.Background(), f.principal(service.RoleRecruiter), f.assessment.ID)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	return p
}

// A preview is a real sitting: it opens, starts, and records like a
// candidate's, but it hangs off the recruiter rather than an application.
func TestPreviewOpensARealSessionOutsideThePipeline(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	pv := f.preview(t)

	if !pv.Attempt.Preview || pv.Attempt.ApplicationID != uuid.Nil || pv.Attempt.StageID != uuid.Nil {
		t.Errorf("attempt = %+v, want a preview with no application or stage", pv.Attempt)
	}
	if pv.URL == "" {
		t.Fatal("preview returned no link")
	}
	var links int
	if err := f.sys.QueryRow(ctx, `select count(*) from magic_link where purpose = 'assessment' and subject_id = $1`, pv.Attempt.ID).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Errorf("%d links for the preview, want 1", links)
	}

	cand := f.candidate(pv.Attempt.ID)
	s, err := f.attempts.Start(ctx, cand)
	if err != nil {
		t.Fatalf("start preview: %v", err)
	}
	if s.CandidateName != service.PreviewCandidateName || s.JobTitle != f.assessment.Name {
		t.Errorf("session names %q / %q, want the preview standing in for a candidate", s.CandidateName, s.JobTitle)
	}
	if len(s.Problems) != 1 {
		t.Fatalf("%d problems in the preview session, want the assessment's", len(s.Problems))
	}
	if err := f.attempts.SaveSource(ctx, cand, pv.Attempt.ID, f.problem.ID, s.Problems[0].Languages[0], "print(3)"); err != nil {
		t.Fatalf("save source: %v", err)
	}

	// Nothing downstream may see it: not the vetter's queue, not the pool's
	// best scores, and not the count that guards deleting the assessment.
	if _, err := f.sys.Exec(ctx, `update attempt set status = 'scored', score = 100 where id = $1`, pv.Attempt.ID); err != nil {
		t.Fatal(err)
	}
	reviews := service.NewReviewService(f.st, service.NewPoolService(f.st), nil)
	queueRows, err := reviews.Assignments(ctx, f.principal(service.RoleVetter))
	if err != nil {
		t.Fatalf("review queue: %v", err)
	}
	for _, r := range queueRows {
		if r.AttemptID == pv.Attempt.ID {
			t.Errorf("the preview is in the vetter's queue")
		}
	}
	if _, err := reviews.Attempt(ctx, f.principal(service.RoleVetter), pv.Attempt.ID); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("reviewing the preview = %v, want ErrNotFound", err)
	}
	var best int
	if err := f.sys.QueryRow(ctx, `select count(*) from attempt a
		join application app on app.id = a.application_id where a.id = $1`, pv.Attempt.ID).Scan(&best); err != nil {
		t.Fatal(err)
	}
	if best != 0 {
		t.Errorf("the preview joins an application, so pool aggregates would count it")
	}
}

// Signals are a reviewer's reading of a candidate; a preview has no
// candidate, so the computation writes nothing for one.
func TestSignalsSkipPreviewAttempts(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	pv := f.preview(t)
	if _, err := f.attempts.Start(ctx, f.candidate(pv.Attempt.ID)); err != nil {
		t.Fatalf("start: %v", err)
	}
	h := service.SignalsComputeHandler(f.st, nil, nil)
	payload, _ := json.Marshal(service.SignalsComputePayload{AttemptID: pv.Attempt.ID, OrgID: f.orgID})
	if err := h(ctx, queue.Job{Kind: queue.KindSignalsCompute, Payload: payload}); err != nil {
		t.Fatalf("signals on a preview: %v", err)
	}
	var n int
	if err := f.sys.QueryRow(ctx, `select count(*) from integrity_signal where attempt_id = $1`, pv.Attempt.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d signals stored for a preview, want none", n)
	}
}

// The purge clears a preview a day after it was opened, with everything it
// produced; a preview opened since is left alone.
func TestPurgePreviewLeavesNothingBehind(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	stale := f.preview(t)
	fresh := f.preview(t)
	invited := f.invite(t)

	cand := f.candidate(stale.Attempt.ID)
	if _, err := f.attempts.Start(ctx, cand); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := f.attempts.SaveSource(ctx, cand, stale.Attempt.ID, f.problem.ID, "python", "print(3)"); err != nil {
		t.Fatalf("save source: %v", err)
	}
	if _, err := f.attempts.Run(ctx, cand, stale.Attempt.ID, f.problem.ID, "python", "print(3)"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := f.attempts.RecordEvents(ctx, cand, stale.Attempt.ID, []service.AttemptEvent{
		{Seq: 1, T: f.now.UnixMilli(), ProblemID: f.problem.ID, Type: "edit", Data: []byte(`{"len":3}`)},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	blob := newFakeBlob()
	key := "attempts/" + stale.Attempt.ID.String() + "/events.jsonl.gz"
	if err := blob.Put(ctx, key, strings.NewReader("x"), 1, "application/gzip"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sys.Exec(ctx, `update attempt set recording_blob_key = $2, created_at = created_at - interval '25 hours' where id = $1`,
		stale.Attempt.ID, key); err != nil {
		t.Fatal(err)
	}

	h := service.AttemptPurgePreviewHandler(f.st, blob, nil)
	if err := h(ctx, queue.Job{Kind: queue.KindAttemptPurgePreview, Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("purge: %v", err)
	}

	for _, q := range []struct {
		what, sql string
	}{
		{"attempt", `select count(*) from attempt where id = $1`},
		{"events", `select count(*) from attempt_event where attempt_id = $1`},
		{"sources", `select count(*) from attempt_source where attempt_id = $1`},
		{"submissions", `select count(*) from submission where attempt_id = $1`},
		{"links", `select count(*) from magic_link where subject_id = $1`},
	} {
		var n int
		if err := f.sys.QueryRow(ctx, q.sql, stale.Attempt.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%d %s left after the purge, want none", n, q.what)
		}
	}
	if blob.has(key) {
		t.Error("the preview's recording is still in object storage")
	}
	for _, id := range []uuid.UUID{fresh.Attempt.ID, invited.ID} {
		var n int
		if err := f.sys.QueryRow(ctx, `select count(*) from attempt where id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("attempt %s was purged, want only the stale preview gone", id)
		}
	}
}

// The assessment's languages narrow every problem it carries; a set that
// leaves a problem with nothing to answer in is refused when it is attached.
func TestAssessmentAllowedLanguagesNarrowTheSession(t *testing.T) {
	f := newAttemptFixture(t)
	ctx := context.Background()
	rec := f.principal(service.RoleRecruiter)
	assessments := service.NewAssessmentService(f.st)

	if _, err := assessments.Update(ctx, rec, f.assessment.ID, service.AssessmentInput{
		Name: "Backend screen", DurationMinutes: 30, InviteWindowDays: 5,
		AllowedLanguages: []string{"haskell"}, ProblemIDs: []uuid.UUID{f.problem.ID},
	}); !errors.Is(err, service.ErrNoLanguage) {
		t.Fatalf("update with an impossible language set = %v, want ErrNoLanguage", err)
	}

	want := f.problem.AllowedLanguages[0]
	a, err := assessments.Update(ctx, rec, f.assessment.ID, service.AssessmentInput{
		Name: "Backend screen", DurationMinutes: 30, InviteWindowDays: 5,
		AllowedLanguages: []string{want, "haskell"},
		Integrity:        service.IntegritySettings{Fullscreen: true, BlockPaste: true, Webcam: true},
		ProblemIDs:       []uuid.UUID{f.problem.ID},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if a.Integrity.WebcamEvery != service.DefaultWebcamInterval || !a.Integrity.Fullscreen {
		t.Errorf("integrity = %+v, want it stored with the default interval", a.Integrity)
	}

	pv := f.preview(t)
	// The measures are on, so the sitting answers for them first — a
	// preview is the candidate's session, consent screen included.
	if err := f.attempts.Consent(ctx, f.candidate(pv.Attempt.ID)); err != nil {
		t.Fatalf("consent: %v", err)
	}
	s, err := f.attempts.Start(ctx, f.candidate(pv.Attempt.ID))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(s.Problems[0].Languages) != 1 || s.Problems[0].Languages[0] != want {
		t.Errorf("session languages = %v, want only %q", s.Problems[0].Languages, want)
	}
	if err := f.attempts.SaveSource(ctx, f.candidate(pv.Attempt.ID), pv.Attempt.ID, f.problem.ID, "haskell", "main = return ()"); !errors.Is(err, service.ErrLanguageNotAllowed) {
		t.Errorf("saving in a language outside the set = %v, want ErrLanguageNotAllowed", err)
	}
}
