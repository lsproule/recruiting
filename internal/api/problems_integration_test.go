//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/runner/server"
	"recruiting/internal/service"
)

// passingRunner answers that every reference solved every case, so the
// problem operations can be exercised without a sandbox.
type passingRunner struct{}

func (passingRunner) Execute(_ context.Context, req server.Request) (server.Response, error) {
	res := make([]server.TestResult, len(req.Tests))
	for i, t := range req.Tests {
		res[i] = server.TestResult{TestID: t.ID, Status: server.TestPass}
	}
	return server.Response{ID: req.ID, Status: server.StatusOK, Results: res}, nil
}

// apiCase is a case as a problem read returns it.
type apiCase struct {
	ID            uuid.UUID `json:"id"`
	Position      int       `json:"position"`
	Name          string    `json:"name"`
	Class         string    `json:"class"`
	Visibility    string    `json:"visibility"`
	Weight        float64   `json:"weight"`
	InputBytes    int       `json:"input_bytes"`
	ExpectedBytes int       `json:"expected_bytes"`
	Input         *string   `json:"input"`
	Expected      *string   `json:"expected"`
}

type apiProblem struct {
	ID        uuid.UUID         `json:"id"`
	Stubs     map[string]string `json:"stubs"`
	TestCases []apiCase         `json:"test_cases"`
}

// A problem read lists its cases by size and never carries a payload — a
// perf case is megabytes — and the case resource is where one is read whole.
func TestProblemReadsListCasesBySizeAndTheCaseResourceCarriesThePayload(t *testing.T) {
	f := newAPIFixture(t)
	_, token, err := f.tokens.Issue(f.ctx, f.admin(), service.NewAPIToken{Name: "ci", UserID: f.adminID})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	big := strings.Repeat("7 ", 40<<10)
	// A function problem, for the stubs a read derives from its signature.
	body := `{"kind":"function","title":"API Perf ` + f.orgID.String() + `","statement":"Add them.","difficulty":"easy",
		"tags":["math"],"allowed_languages":["python"],
		"signature":{"name":"add","params":[{"name":"a","type":"int"},{"name":"b","type":"int"}],"returns":"int"},
		"reference_solutions":[{"language":"python","source":"def add(a, b):\n    return a + b\n"}],
		"test_cases":[{"name":"sample","args":[1,2],"returns":3,"visibility":"public"},
			{"name":"more","input":"[3, 4]","expected":"7","visibility":"hidden","weight":2}]}`
	// A code problem with a perf case wide enough that a read must not
	// carry it.
	code := `{"kind":"code","title":"API Perf Code ` + f.orgID.String() + `","statement":"Print 3.","difficulty":"easy",
		"tags":["math"],"allowed_languages":["python"],
		"reference_solutions":[{"language":"python","source":"print(3)"}],
		"test_cases":[{"name":"sample","input":"1 2","expected":"3","visibility":"public"},
			{"name":"perf","class":"perf","input":"` + big + `","expected":"3","visibility":"hidden","weight":2}]}`

	status, raw := f.postJSON(t, http.MethodPost, "/api/v1/problems", token, body)
	if status != http.StatusCreated {
		t.Fatalf("create function problem: %d %s", status, raw)
	}
	var fn apiProblem
	if err := json.Unmarshal(raw, &fn); err != nil {
		t.Fatal(err)
	}
	if fn.Stubs["python"] == "" || !strings.Contains(fn.Stubs["python"], "def add(") {
		t.Errorf("stubs = %v, want the python stub from the signature", fn.Stubs)
	}

	status, raw = f.postJSON(t, http.MethodPost, "/api/v1/problems", token, code)
	if status != http.StatusCreated {
		t.Fatalf("create code problem: %d %s", status, raw)
	}
	var created apiProblem
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	checkCases := func(what string, cases []apiCase) apiCase {
		t.Helper()
		if len(cases) != 2 {
			t.Fatalf("%s carries %d cases, want 2", what, len(cases))
		}
		for _, c := range cases {
			if c.Input != nil || c.Expected != nil {
				t.Errorf("%s carries a payload for %q", what, c.Name)
			}
			if c.ID == uuid.Nil || c.Position == 0 || c.Name == "" || c.Class == "" || c.Visibility == "" || c.Weight == 0 {
				t.Errorf("%s is missing case metadata: %+v", what, c)
			}
		}
		if perf := cases[1]; perf.InputBytes != len(big) || perf.ExpectedBytes != 1 || perf.Weight != 2 {
			t.Errorf("%s sizes the perf case at %d/%d weight %v, want %d/1 weight 2", what, perf.InputBytes, perf.ExpectedBytes, perf.Weight, len(big))
		}
		return cases[1]
	}
	checkCases("create", created.TestCases)

	status, raw = f.call(t, http.MethodGet, "/api/v1/problems/"+created.ID.String(), token)
	if status != http.StatusOK {
		t.Fatalf("get: %d %s", status, raw)
	}
	var got apiProblem
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	perf := checkCases("get", got.TestCases)

	status, raw = f.call(t, http.MethodGet, "/api/v1/problems/"+created.ID.String()+"/cases/"+perf.ID.String(), token)
	if status != http.StatusOK {
		t.Fatalf("get case: %d %s", status, raw)
	}
	var whole apiCase
	if err := json.Unmarshal(raw, &whole); err != nil {
		t.Fatal(err)
	}
	if whole.Input == nil || *whole.Input != big || whole.Expected == nil || *whole.Expected != "3" {
		t.Errorf("the case resource carries %d bytes, want the %d-byte input and its expected output", len(strings.Join([]string{ptr(whole.Input), ptr(whole.Expected)}, "")), len(big))
	}
	if whole.ID != perf.ID || whole.Name != "perf" || whole.Position != 2 || whole.InputBytes != len(big) {
		t.Errorf("the case resource's metadata = %+v", whole)
	}

	if status, _ := f.call(t, http.MethodGet, "/api/v1/problems/"+fn.ID.String()+"/cases/"+perf.ID.String(), token); status != http.StatusNotFound {
		t.Errorf("a case read under another problem = %d, want 404", status)
	}

	status, raw = f.postJSON(t, http.MethodPost, "/api/v1/problems/"+created.ID.String()+"/clone", token, "")
	if status != http.StatusCreated {
		t.Fatalf("clone: %d %s", status, raw)
	}
	var cloned apiProblem
	if err := json.Unmarshal(raw, &cloned); err != nil {
		t.Fatal(err)
	}
	checkCases("clone", cloned.TestCases)
}

func ptr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
