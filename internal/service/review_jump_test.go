package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestReplayJumpsNameTheOutcomesWorthScrubbingTo(t *testing.T) {
	start := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return start.Add(time.Duration(m) * time.Minute) }
	subs := []ReviewSubmission{
		// Never built: not a compile the reviewer would jump to.
		{ID: uuid.New(), CreatedAt: at(1), Kind: SubmissionRun, RunnerStatus: "compile_error"},
		// Built, but every case failed.
		{ID: uuid.New(), CreatedAt: at(2), Kind: SubmissionRun, RunnerStatus: "ok", TestsTotal: 3},
		// The first case to pass.
		{ID: uuid.New(), CreatedAt: at(3), Kind: SubmissionRun, RunnerStatus: "ok", TestsPassed: 1, TestsTotal: 3},
		{ID: uuid.New(), CreatedAt: at(4), Kind: SubmissionRun, RunnerStatus: "timeout", TestsPassed: 1, TestsTotal: 3, TimedOut: true},
		{ID: uuid.New(), CreatedAt: at(5), Kind: SubmissionSubmit, RunnerStatus: "ok", TestsPassed: 3, TestsTotal: 3},
	}

	jumps := ReplayJumps(subs)

	var keys []string
	for _, j := range jumps {
		keys = append(keys, j.Key)
		if j.Label == "" {
			t.Errorf("jump %s has no label", j.Key)
		}
	}
	want := []string{JumpCompiled, JumpFirstPass, JumpTimeout, JumpAllPass}
	if len(keys) != len(want) {
		t.Fatalf("jumps = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("jumps = %v, want %v", keys, want)
		}
	}
	if !jumps[0].At.Equal(at(2)) {
		t.Errorf("first compile at %s, want %s", jumps[0].At, at(2))
	}
	if jumps[0].SubmissionID != subs[1].ID {
		t.Errorf("first compile names submission %s, want %s", jumps[0].SubmissionID, subs[1].ID)
	}
	if jumps[3].SubmissionID != subs[4].ID {
		t.Errorf("all pass names submission %s, want %s", jumps[3].SubmissionID, subs[4].ID)
	}
}

func TestReplayJumpsAreEmptyWithoutOutcomes(t *testing.T) {
	if jumps := ReplayJumps(nil); len(jumps) != 0 {
		t.Fatalf("jumps = %v, want none", jumps)
	}
	queued := []ReviewSubmission{{ID: uuid.New(), Kind: SubmissionRun, Status: SubmissionQueued}}
	if jumps := ReplayJumps(queued); len(jumps) != 0 {
		t.Fatalf("jumps = %v, want none", jumps)
	}
}
