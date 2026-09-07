package service

import (
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/runner/server"
)

func tryProblem() Problem {
	return Problem{
		ID: uuid.New(), Kind: domain.ProblemKindCode, Title: "Sum", Statement: "add them",
		Difficulty: "easy", AllowedLanguages: []string{"python"},
		TimeLimitMs: 2000, MemoryLimitKB: 262144,
		Guidelines: "watch for the eviction step",
		TestCases: []ProblemTestCase{
			{ID: uuid.New(), Position: 1, Input: "1 2", Expected: "3", Visibility: domain.VisibilityPublic, Weight: 1},
			{ID: uuid.New(), Position: 2, Input: "9 9", Expected: "18", Visibility: domain.VisibilityHidden, Weight: 1},
			{ID: uuid.New(), Position: 3, Input: "0 0", Expected: "0", Visibility: domain.VisibilityPublic, Weight: 1},
		},
	}
}

// Try is the author checking their own problem the way a candidate would:
// only what the candidate can see is run, so a hidden case never leaks its
// expected output back to the page.
func TestTryRequestCarriesOnlyThePublicCases(t *testing.T) {
	p := tryProblem()
	req, cases := tryRequest(p, "python", "print(3)")
	if len(cases) != 2 {
		t.Fatalf("%d cases were run, want the 2 public ones", len(cases))
	}
	if len(req.Tests) != 2 {
		t.Fatalf("the request carries %d tests, want 2", len(req.Tests))
	}
	for _, tc := range cases {
		if tc.Visibility != domain.VisibilityPublic {
			t.Errorf("a %s case reached the runner", tc.Visibility)
		}
	}
	if req.Language != "python" || req.Source != "print(3)" {
		t.Errorf("request = %q/%q", req.Language, req.Source)
	}
}

func TestTryResultReportsEveryCaseByIndex(t *testing.T) {
	p := tryProblem()
	req, cases := tryRequest(p, "python", "print(3)")
	res := server.Response{ID: req.ID, Status: server.StatusOK, Results: []server.TestResult{
		{TestID: req.Tests[1].ID, Status: server.TestFail, TimeMs: 7, StdoutHash: "beef"},
		{TestID: req.Tests[0].ID, Status: server.TestPass, TimeMs: 3, StdoutHash: "cafe"},
	}}
	out := tryResult(res, req, cases)
	if out.Status != server.StatusOK || len(out.Results) != 2 {
		t.Fatalf("result = %+v", out)
	}
	if out.Results[0].TestIndex != 0 || out.Results[0].Status != server.TestPass || out.Results[0].Expected != "3" {
		t.Errorf("case 0 = %+v", out.Results[0])
	}
	if out.Results[1].TestIndex != 1 || out.Results[1].Status != server.TestFail || out.Results[1].ActualHash != "beef" {
		t.Errorf("case 1 = %+v", out.Results[1])
	}
}

// A compile error is an answer, not a failure: the author sees the compiler
// output rather than a blank pane.
func TestTryResultKeepsCompileOutput(t *testing.T) {
	p := tryProblem()
	req, cases := tryRequest(p, "python", "print(")
	out := tryResult(server.Response{Status: server.StatusCompileError, CompileOutput: "SyntaxError"}, req, cases)
	if out.Status != server.StatusCompileError || !strings.Contains(out.CompileOutput, "SyntaxError") {
		t.Fatalf("result = %+v", out)
	}
}

// Interviewer guidelines are internal. The candidate's view of a problem is
// built field by field, so the type itself must not carry them.
func TestSessionProblemCarriesNoGuidelines(t *testing.T) {
	tp := reflect.TypeOf(SessionProblem{})
	for i := range tp.NumField() {
		if strings.Contains(strings.ToLower(tp.Field(i).Name), "guideline") {
			t.Fatalf("SessionProblem exposes %s to the candidate", tp.Field(i).Name)
		}
	}
}
