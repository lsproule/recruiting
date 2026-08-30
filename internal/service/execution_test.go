package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/runner/server"
)

func executionProblem() Problem {
	p := Problem{
		ID: uuid.New(), OrgID: uuid.New(), Kind: "code", Title: "Adder",
		AllowedLanguages: []string{"python"}, TimeLimitMs: 3000, MemoryLimitKB: 131072,
	}
	p.TestCases = []ProblemTestCase{
		{ID: uuid.New(), Position: 1, Input: "1", Expected: "1", Visibility: "public", Weight: 1},
		{ID: uuid.New(), Position: 2, Input: "2", Expected: "2", Visibility: "hidden", Weight: 2},
		{ID: uuid.New(), Position: 3, Input: "3", Expected: "3", Visibility: "hidden", Weight: 3, Unordered: true},
	}
	return p
}

// The runner is a separate service that never learns who it is running code
// for: the request carries the code, its cases, and the limits, nothing else.
func TestExecutionRequestCarriesNoTenantOrCandidateContext(t *testing.T) {
	p := executionProblem()
	id := uuid.New()
	req := executionRequest(id, SubmissionSubmit, "python", "print(1)", p)
	if req.ID != id.String() {
		t.Errorf("request id = %q, want the submission id %s", req.ID, id)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"id": true, "language": true, "source": true, "sql_schema": true, "tests": true, "limits": true}
	for name := range fields {
		if !allowed[name] {
			t.Errorf("request carries %q, which the runner must not receive", name)
		}
	}
	for _, forbidden := range []string{p.OrgID.String(), p.ID.String(), p.Title} {
		if forbidden != "" && strings.Contains(string(raw), forbidden) {
			t.Errorf("request leaks %q", forbidden)
		}
	}
}

func TestExecutionRequestSendsPublicCasesForARunAndEveryCaseForASubmit(t *testing.T) {
	p := executionProblem()
	run := executionRequest(uuid.New(), SubmissionRun, "python", "print(1)", p)
	if len(run.Tests) != 1 || run.Tests[0].ID != p.TestCases[0].ID.String() {
		t.Fatalf("run tests = %+v, want the single public case", run.Tests)
	}
	sub := executionRequest(uuid.New(), SubmissionSubmit, "python", "print(1)", p)
	if len(sub.Tests) != 3 {
		t.Fatalf("submit tests = %d, want every case", len(sub.Tests))
	}
	if sub.Tests[2].Weight != 3 || !sub.Tests[2].Unordered {
		t.Errorf("third case = %+v, want its weight and unordered flag carried", sub.Tests[2])
	}
	if sub.Limits.WallMs != 3000 || sub.Limits.MemMB != 128 {
		t.Errorf("limits = %+v, want the problem's time and memory", sub.Limits)
	}
}

// A SQL problem's query runs against the problem's own schema and seed.
func TestExecutionRequestSeedsSQLProblems(t *testing.T) {
	p := executionProblem()
	p.Kind = "sql"
	p.SQLSchema, p.SQLSeed = "create table t (a int);", "insert into t values (1);"
	req := executionRequest(uuid.New(), SubmissionSubmit, "sql", "select * from t", p)
	if req.SQLSchema == "" || !strings.Contains(req.SQLSchema, "insert into t") {
		t.Errorf("sql_schema = %q, want the schema and its seed", req.SQLSchema)
	}
	code := executionProblem()
	if got := executionRequest(uuid.New(), SubmissionSubmit, "python", "x", code).SQLSchema; got != "" {
		t.Errorf("code problem sql_schema = %q, want empty", got)
	}
}

func TestExecutionCasesAreTheCasesTheRequestScoredAgainst(t *testing.T) {
	p := executionProblem()
	if got := executionCases(SubmissionRun, p); len(got) != 1 {
		t.Errorf("run cases = %d, want 1", len(got))
	}
	if got := executionCases(SubmissionSubmit, p); len(got) != 3 {
		t.Errorf("submit cases = %d, want 3", len(got))
	}
}

// The runner reporting a failing verdict is a normal answer: the submission
// is done with a low score, not an error the candidate has to resubmit.
func TestSubmissionStatusOfSeparatesVerdictsFromRunnerFailures(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   string
	}{
		{server.StatusOK, SubmissionDone},
		{server.StatusCompileError, SubmissionDone},
		{server.StatusRuntimeError, SubmissionDone},
		{server.StatusTimeout, SubmissionDone},
		{server.StatusError, SubmissionError},
		{"", SubmissionError},
	} {
		if got := submissionStatusOf(tc.status); got != tc.want {
			t.Errorf("runner %q -> %q, want %q", tc.status, got, tc.want)
		}
	}
}
