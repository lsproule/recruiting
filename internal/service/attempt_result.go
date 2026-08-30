package service

import (
	"encoding/json"

	"recruiting/internal/runner/server"
)

// CandidateResult is what a candidate sees of a submission's outcome: each
// public case by outcome, hidden cases as a count, never their inputs or
// expected outputs. The runner's raw response stays server-side.
type CandidateResult struct {
	Status        string                `json:"status"`
	CompileOutput string                `json:"compile_output,omitempty"`
	Public        []CandidateTestResult `json:"public"`
	HiddenPassed  int                   `json:"hidden_passed"`
	HiddenTotal   int                   `json:"hidden_total"`
}

// CandidateTestResult is one public case's outcome. Actual is what the
// runner reports about the output: its stderr tail on failure, since the
// protocol carries a stdout hash rather than the text.
type CandidateTestResult struct {
	Position int    `json:"position"`
	Status   string `json:"status"`
	Actual   string `json:"actual,omitempty"`
	TimeMs   int64  `json:"time_ms"`
}

// CandidateResultOf projects a stored runner response onto the problem's
// test cases. Results naming a case that no longer exists are ignored; a run
// only carries public cases, so hidden counts stay zero for it.
func CandidateResultOf(raw json.RawMessage, cases []ProblemTestCase) CandidateResult {
	var res server.Response
	_ = json.Unmarshal(raw, &res)
	out := CandidateResult{Status: res.Status, CompileOutput: res.CompileOutput, Public: []CandidateTestResult{}}
	byID := make(map[string]ProblemTestCase, len(cases))
	for _, c := range cases {
		byID[c.ID.String()] = c
	}
	for _, r := range res.Results {
		c, ok := byID[r.TestID]
		if !ok {
			continue
		}
		if c.Visibility == "public" {
			out.Public = append(out.Public, CandidateTestResult{Position: c.Position, Status: r.Status, Actual: r.StderrTail, TimeMs: r.TimeMs})
			continue
		}
		out.HiddenTotal++
		if r.Status == server.TestPass {
			out.HiddenPassed++
		}
	}
	return out
}
