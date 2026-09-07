//go:build integration

package service_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/queue"
	"recruiting/internal/runner/server"
	"recruiting/internal/service"
)

// finalize works one attempt.finalize job that has already stood aside for
// running submissions wait times.
func (f *executionFixture) finalize(t *testing.T, b service.BlobStore, attemptID uuid.UUID, wait int) error {
	t.Helper()
	payload, _ := json.Marshal(service.AttemptFinalizeJob{
		AttemptFinalizePayload: service.AttemptFinalizePayload{AttemptID: attemptID, OrgID: f.orgID}, Wait: wait,
	})
	h := service.AttemptFinalizeHandler(f.st, f.q, b, nil)
	return h(context.Background(), queue.Job{Kind: queue.KindAttemptFinalize, Payload: payload})
}

// scored reads what finalization wrote onto the attempt.
func (f *executionFixture) scored(t *testing.T, attemptID uuid.UUID) (status string, score *float64, breakdown []service.ProblemScore, errCount int, recording string, key *string) {
	t.Helper()
	var raw []byte
	if err := f.sys.QueryRow(context.Background(),
		`select status, score, problem_scores, error_count, recording_status, recording_blob_key from attempt where id = $1`,
		attemptID).Scan(&status, &score, &raw, &errCount, &recording, &key); err != nil {
		t.Fatalf("attempt row: %v", err)
	}
	if err := json.Unmarshal(raw, &breakdown); err != nil {
		t.Fatalf("problem_scores %s: %v", raw, err)
	}
	return status, score, breakdown, errCount, recording, key
}

func blobBody(t *testing.T, b *fakeBlob, key string) []byte {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	body, ok := b.objects[key]
	if !ok {
		t.Fatalf("no object at %s", key)
	}
	return body
}

// The attempt score is the mean of the problems' weighted shares: the adder
// carries a public case of weight 1 and a hidden case of weight 2, so a
// submission that passes only the public case scores a third.
func TestFinalizeScoresTheFinalSubmitOfEveryProblem(t *testing.T) {
	f := newExecutionFixture(t, adderImport(t, "Scored Adder"), adderImport(t, "Unsolved Adder"))
	ctx := context.Background()
	att := f.start(t)
	cand := f.candidateOf(att.ID)
	solved, unsolved := f.problems[0], f.problems[1]

	// A first submit that passes nothing, then the final one that passes the
	// public case only: the final submit is the one that counts.
	first, err := f.attempts.Submit(ctx, cand, att.ID, solved.ID, "python", "print(0)")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.execute(t, failingExecutor{}, first.ID, 1); err != nil {
		t.Fatal(err)
	}
	last, err := f.attempts.Submit(ctx, cand, att.ID, solved.ID, "python", "print(3)")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.execute(t, publicOnlyExecutor{cases: solved.TestCases}, last.ID, 1); err != nil {
		t.Fatal(err)
	}

	if _, err := f.attempts.Finish(ctx, cand, att.ID); err != nil {
		t.Fatalf("finish: %v", err)
	}
	blob := newFakeBlob()
	if err := f.finalize(t, blob, att.ID, 0); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	status, score, breakdown, errCount, _, _ := f.scored(t, att.ID)
	if status != service.AttemptScored {
		t.Fatalf("status = %s, want scored", status)
	}
	// One problem scored on its public case alone, one never submitted at 0.
	var public, all float64
	for _, c := range solved.TestCases {
		all += c.Weight
		if c.Visibility == "public" {
			public += c.Weight
		}
	}
	want := 100 * public / all / 2
	if score == nil || math.Abs(*score-want) > 0.01 {
		t.Fatalf("attempt score = %v, want the mean of %.1f and 0", score, 100*public/all)
	}
	if errCount != 0 {
		t.Errorf("error count = %d, want 0", errCount)
	}
	if len(breakdown) != 2 {
		t.Fatalf("breakdown = %+v, want one entry per problem", breakdown)
	}
	byProblem := map[uuid.UUID]service.ProblemScore{}
	for _, ps := range breakdown {
		byProblem[ps.ProblemID] = ps
	}
	got := byProblem[solved.ID]
	if math.Abs(got.Score-public/all) > 1e-9 || got.PassedWeight != public || got.TotalWeight != all || got.SubmissionID != last.ID {
		t.Errorf("solved problem = %+v, want %v of %v weight from the final submit %s", got, public, all, last.ID)
	}
	if got := byProblem[unsolved.ID]; got.Score != 0 || got.TotalWeight != all || got.SubmissionID != uuid.Nil {
		t.Errorf("unsubmitted problem = %+v, want a zero with no submission", got)
	}

	// Re-delivery of the job leaves the scored attempt as it is.
	if err := f.finalize(t, blob, att.ID, 0); err != nil {
		t.Fatalf("second finalize: %v", err)
	}
	if _, again, _, _, _, _ := f.scored(t, att.ID); *again != *score {
		t.Errorf("score changed on redelivery: %v then %v", *score, *again)
	}
}

func TestFinalizeCompactsTheRecordingAndQueuesTheSignals(t *testing.T) {
	f := newExecutionFixture(t, adderImport(t, "Recorded Adder"))
	ctx := context.Background()
	att := f.start(t)
	cand := f.candidateOf(att.ID)
	events := []service.AttemptEvent{
		{Seq: 1, T: f.now.UnixMilli(), ProblemID: f.problems[0].ID, Type: "focus", Data: json.RawMessage(`{}`)},
		{Seq: 2, T: f.now.UnixMilli(), ProblemID: f.problems[0].ID, Type: "edit", Data: json.RawMessage(`{"c":1}`)},
	}
	if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, events); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := f.attempts.Finish(ctx, cand, att.ID); err != nil {
		t.Fatal(err)
	}
	blob := newFakeBlob()
	if err := f.finalize(t, blob, att.ID, 0); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	_, _, _, _, recording, key := f.scored(t, att.ID)
	if recording != service.RecordingComplete {
		t.Errorf("recording = %s, want complete", recording)
	}
	want := "attempts/" + att.ID.String() + "/events.jsonl.gz"
	if key == nil || *key != want {
		t.Fatalf("recording key = %v, want %s", key, want)
	}
	gz, err := gzip.NewReader(bytes.NewReader(blobBody(t, blob, want)))
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	var got []service.RecordedEvent
	sc := bufio.NewScanner(gz)
	for sc.Scan() {
		var ev service.RecordedEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		got = append(got, ev)
	}
	if len(got) != 2 || got[0].Seq != 1 || got[1].Kind != "edit" || got[0].ProblemID == nil {
		t.Fatalf("compacted stream = %+v, want both events with their problem", got)
	}
	if n := f.jobs(t, queue.KindSignalsCompute); n != 1 {
		t.Errorf("%d signals.compute jobs, want 1", n)
	}
}

// Events the server numbered but never received leave a gap, and a recording
// with a gap cannot be replayed in full.
func TestFinalizeFlagsAnIncompleteRecording(t *testing.T) {
	f := newExecutionFixture(t, adderImport(t, "Gappy Adder"))
	ctx := context.Background()
	att := f.start(t)
	cand := f.candidateOf(att.ID)
	if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, []service.AttemptEvent{
		{Seq: 7, T: f.now.UnixMilli(), ProblemID: f.problems[0].ID, Type: "focus", Data: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := f.attempts.Finish(ctx, cand, att.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.finalize(t, newFakeBlob(), att.ID, 0); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if _, _, _, _, recording, _ := f.scored(t, att.ID); recording != service.RecordingIncomplete {
		t.Errorf("recording = %s, want incomplete", recording)
	}
}

// Finalization stands aside for submissions still in the runner: one call to
// the runner can take minutes, so the job re-enqueues itself with a delay
// rather than burning a delivery.
func TestFinalizeReschedulesItselfWhileSubmissionsAreRunning(t *testing.T) {
	f := newExecutionFixture(t, adderImport(t, "Pending Adder"))
	ctx := context.Background()
	att := f.start(t)
	cand := f.candidateOf(att.ID)
	if _, err := f.attempts.Submit(ctx, cand, att.ID, f.problems[0].ID, "python", "print(3)"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.Finish(ctx, cand, att.ID); err != nil {
		t.Fatal(err)
	}
	before := f.jobs(t, queue.KindAttemptFinalize)
	if err := f.finalize(t, newFakeBlob(), att.ID, 3); err != nil {
		t.Fatalf("finalize with a queued submission must not fail: %v", err)
	}
	if status, _, _, _, _, _ := f.scored(t, att.ID); status == service.AttemptScored {
		t.Fatal("the attempt must not be scored while a submission is still running")
	}
	if got := f.jobs(t, queue.KindAttemptFinalize); got != before+1 {
		t.Fatalf("%d finalize jobs, want one more than the %d before", got, before)
	}
	var waits []int
	rows, err := f.sys.Query(ctx, `select (args->'payload'->>'wait')::int from river_job
		where kind = $1 and args->>'payload' like '%' || $2 || '%' and args->'payload' ? 'wait'`,
		queue.KindAttemptFinalize, att.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var w int
		if err := rows.Scan(&w); err != nil {
			t.Fatal(err)
		}
		waits = append(waits, w)
	}
	if len(waits) != 1 || waits[0] != 4 {
		t.Errorf("rescheduled waits = %v, want one job counting the fourth wait", waits)
	}
	var scheduled, now time.Time
	if err := f.sys.QueryRow(ctx, `select min(scheduled_at), now() from river_job
		where kind = $1 and args->'payload'->>'wait' = '4' and args->>'payload' like '%' || $2 || '%'`,
		queue.KindAttemptFinalize, att.ID.String()).Scan(&scheduled, &now); err != nil {
		t.Fatal(err)
	}
	if d := scheduled.Sub(now); d < 20*time.Second || d > service.FinalizeWaitDelay+10*time.Second {
		t.Errorf("rescheduled in %v, want about %v", d, service.FinalizeWaitDelay)
	}
}

// Once the wait is spent, submissions the runner never answered for are given
// up on so the score and the error count describe what actually happened, and
// a reply that lands afterwards changes nothing.
func TestFinalizeAbandonsStillRunningSubmissionsOnceTheWaitIsSpent(t *testing.T) {
	f := newExecutionFixture(t, adderImport(t, "Abandoned Adder"))
	ctx := context.Background()
	att := f.start(t)
	cand := f.candidateOf(att.ID)
	sub, err := f.attempts.Submit(ctx, cand, att.ID, f.problems[0].ID, "python", "print(3)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.Finish(ctx, cand, att.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.finalize(t, newFakeBlob(), att.ID, service.MaxFinalizeWaits); err != nil {
		t.Fatalf("the last wait must score the attempt: %v", err)
	}
	status, score, breakdown, errCount, _, _ := f.scored(t, att.ID)
	if status != service.AttemptScored || score == nil || *score != 0 {
		t.Fatalf("attempt = %s score %v, want scored at 0", status, score)
	}
	if errCount != 1 {
		t.Errorf("error count = %d, want the abandoned submission", errCount)
	}
	if len(breakdown) != 1 || breakdown[0].SubmissionID != uuid.Nil {
		t.Errorf("breakdown = %+v, want no submission credited", breakdown)
	}
	subStatus, subScore, result := f.submissionRow(t, sub.ID)
	if subStatus != service.SubmissionError || subScore != nil {
		t.Fatalf("submission = %s score %v, want error with no score", subStatus, subScore)
	}
	if !bytes.Contains(result, []byte("abandoned at finalize")) {
		t.Errorf("stored result = %s, want it to say why", result)
	}

	// The runner finally replying must not resurrect the submission.
	if err := f.execute(t, &recordingExecutor{}, sub.ID, 1); err != nil {
		t.Fatalf("a late reply: %v", err)
	}
	if subStatus, _, _ := f.submissionRow(t, sub.ID); subStatus != service.SubmissionError {
		t.Errorf("submission after a late reply = %s, want it left in error", subStatus)
	}
}

// Two workers delivering the same finalize job must score the attempt once
// and queue the signals once.
func TestFinalizeScoresOnceUnderConcurrentDeliveries(t *testing.T) {
	f := newExecutionFixture(t, adderImport(t, "Raced Adder"))
	ctx := context.Background()
	att := f.start(t)
	cand := f.candidateOf(att.ID)
	sub, err := f.attempts.Submit(ctx, cand, att.ID, f.problems[0].ID, "python", "print(3)")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.execute(t, &recordingExecutor{}, sub.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.Finish(ctx, cand, att.ID); err != nil {
		t.Fatal(err)
	}
	blob := newFakeBlob()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = f.finalize(t, blob, att.ID, 0)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
	}
	if status, _, _, _, _, _ := f.scored(t, att.ID); status != service.AttemptScored {
		t.Fatalf("status = %s, want scored", status)
	}
	if n := f.jobs(t, queue.KindSignalsCompute); n != 1 {
		t.Errorf("%d signals.compute jobs, want exactly 1", n)
	}
}

// A runner that was down during the attempt shows up as an error count the
// vetter can read against the score.
func TestFinalizeSurfacesTheSubmissionErrorCount(t *testing.T) {
	f := newExecutionFixture(t, adderImport(t, "Errored Adder"))
	ctx := context.Background()
	att := f.start(t)
	cand := f.candidateOf(att.ID)
	sub, err := f.attempts.Submit(ctx, cand, att.ID, f.problems[0].ID, "python", "print(3)")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.execute(t, &deadExecutor{}, sub.ID, queue.MaxAttempts); err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.Finish(ctx, cand, att.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.finalize(t, newFakeBlob(), att.ID, 0); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	_, score, _, errCount, _, _ := f.scored(t, att.ID)
	if errCount != 1 {
		t.Errorf("error count = %d, want the one unanswered submission", errCount)
	}
	if score == nil || *score != 0 {
		t.Errorf("score = %v, want 0 for a problem the runner never judged", score)
	}
}

// failingExecutor answers that nothing passed.
type failingExecutor struct{}

func (failingExecutor) Execute(_ context.Context, req server.Request) (server.Response, error) {
	res := server.Response{ID: req.ID, Status: server.StatusOK}
	for _, tc := range req.Tests {
		res.Results = append(res.Results, server.TestResult{TestID: tc.ID, Status: server.TestFail})
	}
	return res, nil
}

// publicOnlyExecutor passes the problem's public cases and fails the rest.
type publicOnlyExecutor struct{ cases []service.ProblemTestCase }

func (e publicOnlyExecutor) Execute(_ context.Context, req server.Request) (server.Response, error) {
	public := map[string]bool{}
	for _, c := range e.cases {
		if c.Visibility == "public" {
			public[c.ID.String()] = true
		}
	}
	res := server.Response{ID: req.ID, Status: server.StatusOK}
	for _, tc := range req.Tests {
		status := server.TestFail
		if public[tc.ID] {
			status = server.TestPass
		}
		res.Results = append(res.Results, server.TestResult{TestID: tc.ID, Status: status})
	}
	return res, nil
}
