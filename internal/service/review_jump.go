package service

import (
	"sort"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/runner/server"
)

// The moments a reviewer skips to. They are the shape of the sitting: where
// the code first built, where a case first passed, where every case passed,
// and where the runner ran out of time.
const (
	JumpCompiled  = "compiled"
	JumpFirstPass = "first_pass"
	JumpAllPass   = "all_pass"
	JumpTimeout   = "timeout"
)

// jumpLabels name a chip in the reviewer's words.
var jumpLabels = map[string]string{
	JumpCompiled:  "First compile",
	JumpFirstPass: "First case passed",
	JumpAllPass:   "All cases passed",
	JumpTimeout:   "Timed out",
}

// ReplayJump is one chip under the replay scrubber: a moment named by what
// the runner made of the submission at it.
type ReplayJump struct {
	Key          string
	Label        string
	At           time.Time
	SubmissionID uuid.UUID
}

// ReplayJumps derives the chips from the sitting's runs and submits, earliest
// first. Each outcome is worth one chip — the first time it happened — and a
// submission the runner never answered for earns none.
func ReplayJumps(subs []ReviewSubmission) []ReplayJump {
	first := map[string]ReplayJump{}
	mark := func(key string, sub ReviewSubmission) {
		if _, seen := first[key]; seen {
			return
		}
		first[key] = ReplayJump{Key: key, Label: jumpLabels[key], At: sub.CreatedAt, SubmissionID: sub.ID}
	}
	for _, sub := range ordered(subs) {
		if sub.RunnerStatus == "" {
			continue
		}
		if sub.RunnerStatus != server.StatusCompileError {
			mark(JumpCompiled, sub)
		}
		if sub.TestsPassed > 0 {
			mark(JumpFirstPass, sub)
		}
		if sub.TestsTotal > 0 && sub.TestsPassed == sub.TestsTotal {
			mark(JumpAllPass, sub)
		}
		if sub.TimedOut {
			mark(JumpTimeout, sub)
		}
	}
	out := make([]ReplayJump, 0, len(first))
	for _, j := range first {
		out = append(out, j)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// ordered is the submissions oldest first, whatever order they arrived in.
func ordered(subs []ReviewSubmission) []ReviewSubmission {
	out := append([]ReviewSubmission(nil), subs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}
