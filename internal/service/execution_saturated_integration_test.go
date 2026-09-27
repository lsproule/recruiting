//go:build integration

package service_test

import (
	"context"
	"testing"
	"time"

	"recruiting/internal/queue"
	runnerclient "recruiting/internal/runner/client"
	"recruiting/internal/runner/server"
	"recruiting/internal/service"
)

// saturatedExecutor stands in for a runner whose queue stayed full for the
// client's whole retry budget.
type saturatedExecutor struct{ calls int }

func (e *saturatedExecutor) Execute(context.Context, server.Request) (server.Response, error) {
	e.calls++
	return server.Response{}, &runnerclient.SaturatedError{RetryAfter: 7 * time.Second, Attempts: 3, Waited: time.Minute}
}

// A saturated runner is busy, not broken: the job is snoozed for the
// interval the runner asked for, on any delivery, without the submission
// being given up on, and a later delivery finishes it normally.
func TestRunnerSaturatedSnoozesTheJob(t *testing.T) {
	f := newExecutionFixture(t, adderImport(t, "Busy Adder"))
	ctx := context.Background()
	att := f.start(t)
	sub, err := f.attempts.Submit(ctx, f.candidateOf(att.ID), att.ID, f.problems[0].ID, "python", "print(3)")
	if err != nil {
		t.Fatal(err)
	}
	busy := &saturatedExecutor{}
	for _, attempt := range []int{1, queue.MaxAttempts} {
		err := f.execute(t, busy, sub.ID, attempt)
		d, ok := queue.IsSnooze(err)
		if !ok || d != 7*time.Second {
			t.Fatalf("delivery %d against a saturated runner: err = %v, want a 7s snooze", attempt, err)
		}
		if status, _, _ := f.submissionRow(t, sub.ID); status == service.SubmissionError || status == service.SubmissionDone {
			t.Fatalf("delivery %d: submission reached %s while the runner was only busy", attempt, status)
		}
	}
	if busy.calls != 2 {
		t.Fatalf("runner called %d times, want once per delivery", busy.calls)
	}
	if err := f.execute(t, &recordingExecutor{}, sub.ID, 1); err != nil {
		t.Fatalf("execute once the runner has room: %v", err)
	}
	if status, _, _ := f.submissionRow(t, sub.ID); status != service.SubmissionDone {
		t.Fatalf("submission = %s after the runner answered, want done", status)
	}
}
