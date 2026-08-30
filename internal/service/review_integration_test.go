//go:build integration

package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/domain/signals"
	"recruiting/internal/service"
)

type reviewFixture struct {
	*executionFixture
	reviews *service.ReviewService
	blob    *fakeBlob
	otherID uuid.UUID
}

// newReviewFixture parks the application in the assessment stage with the
// fixture's user as its vetter, and adds a second vetter to test authorship.
func newReviewFixture(t *testing.T) *reviewFixture {
	t.Helper()
	ef := newExecutionFixture(t, pastedImport(t, "Reviewed Adder"))
	f := &reviewFixture{executionFixture: ef, blob: newFakeBlob(), otherID: uuid.New()}
	f.reviews = service.NewReviewService(ef.st, service.NewPoolService(ef.st), f.blob)
	ctx := context.Background()
	if _, err := ef.sys.Exec(ctx,
		`insert into org_user (id, org_id, email, name) values ($1, $2, $3, 'Vic Vetter')`,
		f.otherID, ef.orgID, "rev2-"+ef.orgID.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := ef.sys.Exec(ctx, `update application set stage_id = $1, vetter_id = $2 where id = $3`,
		ef.stages[domain.StageAssessment], ef.userID, ef.appID); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *reviewFixture) vetter() service.Principal { return f.principal(service.RoleVetter) }

func (f *reviewFixture) other() service.Principal {
	return service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, UserID: f.otherID, Roles: []string{service.RoleVetter}}
}

// session records a full sitting on the first problem — a blur and return, a
// paste carrying the solution, a run, and the submit — and closes the
// attempt, so the replay has one of every marker.
func (f *reviewFixture) session(t *testing.T, att service.Attempt, exec service.Executor) {
	t.Helper()
	ctx := context.Background()
	cand := f.candidateOf(att.ID)
	p := f.problems[0]
	base := f.now.UnixMilli()
	lines := mustJSON(strings.Split(pastedSolution, "\n"))
	change := `{"changes":[[0,` + string(lines[1:len(lines)-1]) + `]]}`
	record := func(events ...service.AttemptEvent) {
		t.Helper()
		if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, events); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	record(
		service.AttemptEvent{Seq: 1, T: base, ProblemID: p.ID, Type: "focus", Data: json.RawMessage(`{}`)},
		service.AttemptEvent{Seq: 2, T: base + 1000, ProblemID: p.ID, Type: "blur", Data: json.RawMessage(`{}`)},
		service.AttemptEvent{Seq: 3, T: base + 60000, ProblemID: p.ID, Type: "focus", Data: json.RawMessage(`{}`)},
		service.AttemptEvent{Seq: 4, T: base + 61000, ProblemID: p.ID, Type: "paste", Data: json.RawMessage(
			`{"len":` + fmt.Sprint(len(pastedSolution)) + `,"sha256":"` + strings.Repeat("a", 64) + `","internal":false}`)},
		service.AttemptEvent{Seq: 5, T: base + 61100, ProblemID: p.ID, Type: "edit", Data: json.RawMessage(change)},
	)
	f.now = f.now.Add(70 * time.Second)
	run, err := f.attempts.Run(ctx, cand, att.ID, p.ID, "python", pastedSolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.execute(t, exec, run.ID, 1); err != nil {
		t.Fatal(err)
	}
	record(service.AttemptEvent{Seq: 6, T: f.now.UnixMilli(), ProblemID: p.ID, Type: "run",
		Data: json.RawMessage(`{"submission_id":"` + run.ID.String() + `"}`)})

	f.now = f.now.Add(10 * time.Second)
	sub, err := f.attempts.Submit(ctx, cand, att.ID, p.ID, "python", pastedSolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.execute(t, exec, sub.ID, 1); err != nil {
		t.Fatal(err)
	}
	record(service.AttemptEvent{Seq: 7, T: f.now.UnixMilli(), ProblemID: p.ID, Type: "submit",
		Data: json.RawMessage(`{"submission_id":"` + sub.ID.String() + `"}`)})
	if _, err := f.attempts.Finish(ctx, cand, att.ID); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// scoredAttempt runs a sitting judged by exec and finalizes it, so the
// attempt is scored and its recording compacted into object storage.
func (f *reviewFixture) scoredAttempt(t *testing.T, exec service.Executor) service.Attempt {
	t.Helper()
	att := f.start(t)
	f.session(t, att, exec)
	if err := f.finalize(t, f.blob, att.ID, 0); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := f.computeSignals(t, f.blob, att.ID); err != nil {
		t.Fatalf("signals: %v", err)
	}
	return att
}

func (f *reviewFixture) livePoolEntries(t *testing.T) []string {
	t.Helper()
	rows, err := f.sys.Query(context.Background(), `select source from talent_pool_entry where org_id = $1 and removed_at is null`, f.orgID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (f *reviewFixture) poolEntries(t *testing.T) []string {
	t.Helper()
	rows, err := f.sys.Query(context.Background(), `select source from talent_pool_entry where org_id = $1`, f.orgID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// The replay carries the whole stream and the text each editor started from,
// so applying the changesets in seq order reproduces the final source
// character for character.
func TestReplayReconstructsTheFinalSource(t *testing.T) {
	f := newReviewFixture(t)
	att := f.scoredAttempt(t, passExecutor{})

	rep, err := f.reviews.Replay(context.Background(), f.vetter(), att.ID, service.ReplayQuery{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if rep.SnapshotEvery != service.ReplaySnapshotEvery {
		t.Errorf("snapshot every %d, want %d", rep.SnapshotEvery, service.ReplaySnapshotEvery)
	}
	if rep.RecordingStatus != service.RecordingComplete {
		t.Errorf("recording status %q, want complete", rep.RecordingStatus)
	}
	if len(rep.Events) != 7 {
		t.Fatalf("%d events, want the 7 recorded", len(rep.Events))
	}
	for i, ev := range rep.Events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d; the stream is not in seq order", i, ev.Seq)
		}
	}
	kinds := map[string]int{}
	for _, m := range rep.Markers {
		kinds[m.Kind]++
	}
	for _, want := range []string{"paste", "blur", "run", "submit"} {
		if kinds[want] == 0 {
			t.Errorf("no %s marker; markers = %+v", want, rep.Markers)
		}
	}

	initial := map[uuid.UUID]string{}
	var final string
	for _, p := range rep.Problems {
		initial[p.ID] = p.InitialSource
		if p.ID == f.problems[0].ID {
			final = p.FinalSource
		}
	}
	if final != pastedSolution {
		t.Fatalf("final source = %q, want the submitted text", final)
	}
	events := make([]signals.Event, 0, len(rep.Events))
	for _, ev := range rep.Events {
		events = append(events, signals.Event(ev))
	}
	tl := signals.Build(events, initial)[f.problems[0].ID]
	if tl == nil || !tl.Reconstructed {
		t.Fatalf("timeline = %+v, want a reconstructed one", tl)
	}
	if tl.Source != final {
		t.Errorf("replayed source = %q, want %q", tl.Source, final)
	}
}

// The vetter's queue holds the attempts assigned to them, and the detail
// carries everything the verdict is formed on.
func TestTheReviewQueueAndDetailCarryTheAttempt(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	att := f.scoredAttempt(t, passExecutor{})

	queue, err := f.reviews.Assignments(ctx, f.vetter())
	if err != nil {
		t.Fatalf("assignments: %v", err)
	}
	if len(queue) != 1 || queue[0].AttemptID != att.ID {
		t.Fatalf("queue = %+v, want the scored attempt", queue)
	}
	if queue[0].Reviewed {
		t.Error("the attempt is already marked reviewed")
	}
	rows, err := f.reviews.Assignments(ctx, f.other())
	if err != nil {
		t.Fatalf("another vetter's queue: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("an unassigned vetter sees %d attempts, want none", len(rows))
	}

	detail, err := f.reviews.Attempt(ctx, f.vetter(), att.ID)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Score == nil || *detail.Score != 100 {
		t.Errorf("score = %v, want 100", detail.Score)
	}
	if len(detail.ProblemScores) != 1 || detail.ProblemScores[0].Score != 1 {
		t.Errorf("problem scores = %+v, want the one problem fully passed", detail.ProblemScores)
	}
	if detail.RecordingStatus != service.RecordingComplete || detail.ErrorCount != 0 {
		t.Errorf("recording %q, %d errors; want complete and none", detail.RecordingStatus, detail.ErrorCount)
	}
	if detail.RiskScore == nil {
		t.Error("no risk score on the detail")
	}
	if len(detail.Signals) == 0 {
		t.Fatal("no integrity signals on the detail")
	}
	var evidence int
	for _, s := range detail.Signals {
		evidence += len(s.Evidence)
	}
	if evidence == 0 {
		t.Error("no evidence on any signal")
	}
	if len(detail.Submissions) != 2 {
		t.Errorf("%d submissions, want the run and the submit", len(detail.Submissions))
	}
	if !detail.CanReview {
		t.Error("the assigned vetter may not file the verdict")
	}
	overseer, err := f.reviews.Attempt(ctx, f.principal(service.RoleRecruiter), att.ID)
	if err != nil {
		t.Fatalf("recruiter detail: %v", err)
	}
	if overseer.CanReview {
		t.Error("a recruiter is offered the verdict form")
	}
	if _, err := f.reviews.Attempt(ctx, f.other(), att.ID); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("detail for an unassigned vetter = %v, want ErrForbidden", err)
	}
}

// A pass below the org's pool threshold earns no entry, though the verdict
// still stands and closes the attempt.
func TestAPassBelowTheThresholdFilesNoPoolEntry(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	att := f.scoredAttempt(t, failingExecutor{})

	if _, err := f.reviews.Save(ctx, f.vetter(), service.ReviewInput{
		AttemptID: att.ID, Verdict: service.VerdictPass, Notes: "worth another look",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := f.poolEntries(t); len(got) != 0 {
		t.Errorf("pool entries %v after a pass scoring zero, want none", got)
	}
	var status string
	if err := f.sys.QueryRow(ctx, `select status from attempt where id = $1`, att.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != service.AttemptReviewed {
		t.Errorf("attempt status %q, want reviewed", status)
	}
}

// A pass at or above the threshold files the candidate in the pool.
func TestAPassAtTheThresholdFilesThePoolEntry(t *testing.T) {
	f := newReviewFixture(t)
	att := f.scoredAttempt(t, passExecutor{})

	if _, err := f.reviews.Save(context.Background(), f.vetter(), service.ReviewInput{
		AttemptID: att.ID, Verdict: service.VerdictPass, Notes: "strong",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := f.poolEntries(t)
	if len(got) != 1 || got[0] != service.PoolSourceReview {
		t.Errorf("pool entries %v, want one from the assessment review", got)
	}

	// Amending the pass down withdraws the entry that pass earned.
	if _, err := f.reviews.Save(context.Background(), f.vetter(), service.ReviewInput{
		AttemptID: att.ID, Verdict: service.VerdictBorderline,
	}); err != nil {
		t.Fatalf("re-review: %v", err)
	}
	if got := f.livePoolEntries(t); len(got) != 0 {
		t.Errorf("live pool entries %v after amending the pass down, want none", got)
	}
}

// An entry the recruiter flagged is theirs to keep: amending the pass down
// does not withdraw it.
func TestAmendingAPassKeepsARecruiterFlaggedEntry(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	att := f.scoredAttempt(t, passExecutor{})
	if _, err := f.reviews.Save(ctx, f.vetter(), service.ReviewInput{AttemptID: att.ID, Verdict: service.VerdictPass}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := service.NewPoolService(f.st).Flag(ctx, f.principal(service.RoleRecruiter), f.appID); err != nil {
		t.Fatalf("flag: %v", err)
	}
	if _, err := f.reviews.Save(ctx, f.vetter(), service.ReviewInput{AttemptID: att.ID, Verdict: service.VerdictFail}); err != nil {
		t.Fatalf("re-review: %v", err)
	}
	if got := f.livePoolEntries(t); len(got) != 1 {
		t.Errorf("live pool entries %v after amending a flagged pass, want the flagged one kept", got)
	}
}

// One review per attempt: the author may amend theirs, nobody else may
// overwrite it.
func TestOnlyTheAuthorMayChangeTheVerdict(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	att := f.scoredAttempt(t, passExecutor{})

	if _, err := f.reviews.Save(ctx, f.vetter(), service.ReviewInput{AttemptID: att.ID, Verdict: service.VerdictPass}); err != nil {
		t.Fatalf("save: %v", err)
	}
	amended, err := f.reviews.Save(ctx, f.vetter(), service.ReviewInput{
		AttemptID: att.ID, Verdict: service.VerdictBorderline, Notes: "on reflection",
	})
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	if amended.Verdict != service.VerdictBorderline {
		t.Errorf("amended verdict = %q, want borderline", amended.Verdict)
	}

	if _, err := f.sys.Exec(ctx, `update application set vetter_id = $1 where id = $2`, f.otherID, f.appID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reviews.Save(ctx, f.other(), service.ReviewInput{AttemptID: att.ID, Verdict: service.VerdictFail}); !errors.Is(err, service.ErrReviewFiled) {
		t.Fatalf("second reviewer = %v, want ErrReviewFiled", err)
	}
	var n int
	if err := f.sys.QueryRow(ctx, `select count(*) from review where attempt_id = $1`, att.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d reviews on the attempt, want 1", n)
	}
	if _, err := f.reviews.Save(ctx, f.vetter(), service.ReviewInput{AttemptID: att.ID, Verdict: "maybe"}); !errors.Is(err, service.ErrBadVerdict) {
		t.Errorf("unknown verdict = %v, want ErrBadVerdict", err)
	}
}

// The verdict is what lets the application leave the assessment stage.
func TestTheVerdictSatisfiesTheAssessmentPrerequisite(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	att := f.scoredAttempt(t, passExecutor{})
	v := f.vetter()

	if _, err := f.move(t, v, f.stages[domain.StageClientReview], service.MoveRequest{}); !errors.Is(err, domain.ErrPrereqMissing) {
		t.Fatalf("move before the verdict = %v, want ErrPrereqMissing", err)
	}
	if _, err := f.reviews.Save(ctx, v, service.ReviewInput{AttemptID: att.ID, Verdict: service.VerdictPass}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := f.move(t, v, f.stages[domain.StageClientReview], service.MoveRequest{}); err != nil {
		t.Fatalf("move after the verdict: %v", err)
	}
}

// The recruiter's summary of an application reports the score and verdict of
// every attempt on it.
func TestSummariesReportTheScoreAndVerdict(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	att := f.scoredAttempt(t, passExecutor{})
	if _, err := f.reviews.Save(ctx, f.vetter(), service.ReviewInput{AttemptID: att.ID, Verdict: service.VerdictPass, Notes: "hire"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	rows, err := f.reviews.Summaries(ctx, f.principal(service.RoleRecruiter), f.appID)
	if err != nil {
		t.Fatalf("summaries: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d summaries, want 1", len(rows))
	}
	if rows[0].Verdict != service.VerdictPass || rows[0].Score == nil || *rows[0].Score != 100 {
		t.Errorf("summary = %+v, want a pass at 100", rows[0])
	}
	if _, err := f.reviews.Summaries(ctx, f.other(), f.appID); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("summaries for an unassigned vetter = %v, want ErrForbidden", err)
	}
}

// The stream is served a page at a time, so no single response carries a
// whole sitting. The pages joined are the stream.
func TestTheReplayPagesThroughTheStream(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	att := f.scoredAttempt(t, passExecutor{})

	var paged []service.RecordedEvent
	var pages int
	q := service.ReplayQuery{Limit: 3}
	for {
		page, err := f.reviews.Replay(ctx, f.vetter(), att.ID, q)
		if err != nil {
			t.Fatalf("replay page after %d: %v", q.AfterSeq, err)
		}
		if len(page.Events) > 3 {
			t.Fatalf("page carries %d events, want at most the 3 asked for", len(page.Events))
		}
		if (q.AfterSeq == 0) != (len(page.Problems) > 0) {
			t.Errorf("page after %d carries %d problems; only the first page carries the editors", q.AfterSeq, len(page.Problems))
		}
		paged = append(paged, page.Events...)
		pages++
		if page.NextAfterSeq == 0 {
			break
		}
		if page.NextAfterSeq != page.Events[len(page.Events)-1].Seq {
			t.Fatalf("next after seq %d, want the last seq of the page", page.NextAfterSeq)
		}
		q.AfterSeq = page.NextAfterSeq
	}
	if pages < 3 {
		t.Errorf("%d pages of 3 for 7 events, want at least 3", pages)
	}

	// An unbounded ask is capped, not honoured, and reads the same stream.
	whole, err := f.reviews.Replay(ctx, f.vetter(), att.ID, service.ReplayQuery{Limit: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if whole.NextAfterSeq != 0 || len(whole.Events) != len(paged) {
		t.Fatalf("one page = %d events, next %d; the pages held %d", len(whole.Events), whole.NextAfterSeq, len(paged))
	}
	for i, ev := range whole.Events {
		if paged[i].Seq != ev.Seq {
			t.Fatalf("paged event %d has seq %d, want %d", i, paged[i].Seq, ev.Seq)
		}
	}
}

// A verdict is formed on a score, so an attempt that has not been finalized
// takes none.
func TestAVerdictNeedsAScoredAttempt(t *testing.T) {
	f := newReviewFixture(t)
	att := f.start(t)
	_, err := f.reviews.Save(context.Background(), f.vetter(), service.ReviewInput{
		AttemptID: att.ID, Verdict: service.VerdictPass,
	})
	if !errors.Is(err, service.ErrNotScored) {
		t.Fatalf("verdict on an unscored attempt = %v, want ErrNotScored", err)
	}
}

// Another org's attempt does not exist as far as this org is concerned: RLS
// hides the row, and every screen reads that as not found rather than
// admitting the id names something.
func TestAnotherOrgsAttemptIsNotFound(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	att := f.scoredAttempt(t, passExecutor{})
	stranger := service.Principal{
		Kind: service.PrincipalOrgUser, OrgID: uuid.New(), UserID: uuid.New(),
		Roles: []string{service.RoleVetter, service.RoleRecruiter, service.RoleAdmin},
	}
	if _, err := f.reviews.Attempt(ctx, stranger, att.ID); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("detail across orgs = %v, want ErrNotFound", err)
	}
	if _, err := f.reviews.Replay(ctx, stranger, att.ID, service.ReplayQuery{}); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("replay across orgs = %v, want ErrNotFound", err)
	}
	if _, err := f.reviews.Summaries(ctx, stranger, f.appID); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("summaries across orgs = %v, want ErrNotFound", err)
	}
	if _, err := f.reviews.Save(ctx, stranger, service.ReviewInput{AttemptID: att.ID, Verdict: service.VerdictPass}); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("verdict across orgs = %v, want ErrNotFound", err)
	}
}
