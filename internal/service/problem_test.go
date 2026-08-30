package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"recruiting/internal/domain"
	"recruiting/internal/runner/server"
)

// stubExecutor answers from a per-source verdict and records how many calls
// were in flight at once.
type stubExecutor struct {
	fail    map[string]bool
	compile map[string]string

	mu       sync.Mutex
	inFlight int
	peak     int
	calls    atomic.Int64
	// arrived reports each call as it starts and release holds calls until
	// it is closed, so a test can observe how many run at once.
	arrived chan struct{}
	release chan struct{}
}

func newStubExecutor() *stubExecutor {
	return &stubExecutor{fail: map[string]bool{}, compile: map[string]string{}}
}

func (s *stubExecutor) Execute(_ context.Context, req server.Request) (server.Response, error) {
	s.calls.Add(1)
	s.mu.Lock()
	s.inFlight++
	if s.inFlight > s.peak {
		s.peak = s.inFlight
	}
	s.mu.Unlock()
	if s.arrived != nil {
		s.arrived <- struct{}{}
	}
	if s.release != nil {
		<-s.release
	}
	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.mu.Unlock()
	}()
	if out, ok := s.compile[req.Source]; ok {
		return server.Response{ID: req.ID, Status: server.StatusCompileError, CompileOutput: out}, nil
	}
	res := make([]server.TestResult, len(req.Tests))
	for i, t := range req.Tests {
		res[i] = server.TestResult{TestID: t.ID, Status: server.TestPass}
		if s.fail[req.Source] && i == 0 {
			res[i].Status = server.TestFail
		}
	}
	return server.Response{ID: req.ID, Status: server.StatusOK, Results: res}, nil
}

func importProblem(title, source string) domain.ImportProblem {
	p := domain.ImportProblem{
		Kind: domain.ProblemKindCode, Title: title, Statement: "s",
		AllowedLanguages: []string{"python"},
		References:       []domain.ImportReference{{Language: "python", Source: source}},
		TestCases: []domain.ImportTestCase{
			{Input: "1", Expected: "1", Visibility: domain.VisibilityPublic},
			{Input: "2", Expected: "2", Visibility: domain.VisibilityHidden},
		},
	}
	p.Normalize()
	return p
}

func TestValidateProblemReferencesRejectsTheWholeBatchOnOneFailure(t *testing.T) {
	exec := newStubExecutor()
	exec.fail["bad"] = true
	problems := []domain.ImportProblem{
		importProblem("Good", "good"),
		importProblem("Bad", "bad"),
		importProblem("Also good", "fine"),
	}
	err := validateProblemReferences(context.Background(), exec, DefaultProblemConcurrency, problems)
	var errs domain.ProblemImportErrors
	if !errors.As(err, &errs) {
		t.Fatalf("err = %v, want ProblemImportErrors", err)
	}
	if len(errs) != 1 || errs[0].Index != 1 || errs[0].Title != "Bad" {
		t.Fatalf("errs = %+v, want one error on the second problem", errs)
	}
	if !strings.Contains(strings.Join(errs[0].Errors, "; "), "python") {
		t.Errorf("error %q does not name the failing language", errs[0].Errors)
	}
}

func TestValidateProblemReferencesReportsEveryFailingProblem(t *testing.T) {
	exec := newStubExecutor()
	exec.fail["bad"] = true
	exec.compile["broken"] = "SyntaxError: bad token"
	problems := []domain.ImportProblem{
		importProblem("Good", "good"),
		importProblem("Bad", "bad"),
		importProblem("Broken", "broken"),
	}
	err := validateProblemReferences(context.Background(), exec, DefaultProblemConcurrency, problems)
	var errs domain.ProblemImportErrors
	if !errors.As(err, &errs) {
		t.Fatalf("err = %v, want ProblemImportErrors", err)
	}
	if len(errs) != 2 {
		t.Fatalf("got %d problem errors, want 2: %+v", len(errs), errs)
	}
	if errs[0].Index != 1 || errs[1].Index != 2 {
		t.Errorf("errors are not in document order: %+v", errs)
	}
	if !strings.Contains(strings.Join(errs[1].Errors, "; "), "SyntaxError") {
		t.Errorf("compile output is missing from %q", errs[1].Errors)
	}
}

func TestValidateProblemReferencesAcceptsAPassingBatch(t *testing.T) {
	exec := newStubExecutor()
	problems := []domain.ImportProblem{importProblem("Good", "good"), importProblem("Fine", "fine")}
	if err := validateProblemReferences(context.Background(), exec, DefaultProblemConcurrency, problems); err != nil {
		t.Fatalf("passing batch rejected: %v", err)
	}
	if got := exec.calls.Load(); got != 2 {
		t.Errorf("runner called %d times, want one call per reference solution (2)", got)
	}
}

// The bound is the point: it must fill every configured slot and never
// exceed it, whatever the runner's own limit is.
func TestValidateProblemReferencesUsesTheConfiguredConcurrency(t *testing.T) {
	for _, wanted := range []int{1, 3} {
		exec := newStubExecutor()
		exec.arrived = make(chan struct{}, 32)
		exec.release = make(chan struct{})
		problems := make([]domain.ImportProblem, 0, 12)
		for i := range 12 {
			problems = append(problems, importProblem(string(rune('A'+i)), string(rune('a'+i))))
		}
		done := make(chan error, 1)
		go func() { done <- validateProblemReferences(context.Background(), exec, wanted, problems) }()
		for range wanted {
			select {
			case <-exec.arrived:
			case <-time.After(5 * time.Second):
				t.Fatalf("concurrency %d: only %d calls started; the bound is too low", wanted, exec.calls.Load())
			}
		}
		exec.mu.Lock()
		peak := exec.peak
		exec.mu.Unlock()
		if peak != wanted {
			t.Errorf("concurrency %d: %d calls in flight, want the slots filled and no more", wanted, peak)
		}
		close(exec.release)
		if err := <-done; err != nil {
			t.Fatalf("concurrency %d: %v", wanted, err)
		}
		exec.mu.Lock()
		peak = exec.peak
		exec.mu.Unlock()
		if peak > wanted {
			t.Errorf("concurrency %d: peak was %d", wanted, peak)
		}
	}
}

func TestValidateProblemReferencesReportsARunnerError(t *testing.T) {
	err := validateProblemReferences(context.Background(), executorFunc(func(context.Context, server.Request) (server.Response, error) {
		return server.Response{}, errors.New("connection refused")
	}), DefaultProblemConcurrency, []domain.ImportProblem{importProblem("Good", "good")})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v, want the runner failure reported", err)
	}
}

type executorFunc func(context.Context, server.Request) (server.Response, error)

func (f executorFunc) Execute(ctx context.Context, req server.Request) (server.Response, error) {
	return f(ctx, req)
}

func TestSeedProblemsCoverEveryDifficultyAndBothKinds(t *testing.T) {
	problems, err := SeedProblems()
	if err != nil {
		t.Fatalf("SeedProblems: %v", err)
	}
	code, sql := 0, 0
	difficulties := map[string]int{}
	for _, p := range problems {
		difficulties[p.Difficulty]++
		switch p.Kind {
		case domain.ProblemKindCode:
			code++
			if !contains(p.AllowedLanguages, "python") {
				t.Errorf("%q offers no python reference", p.Title)
			}
		case domain.ProblemKindSQL:
			sql++
		}
	}
	if code < 5 {
		t.Errorf("%d code problems, want at least 5", code)
	}
	if sql < 3 {
		t.Errorf("%d sql problems, want at least 3", sql)
	}
	for _, d := range domain.ProblemDifficulties {
		if difficulties[d] == 0 {
			t.Errorf("no seed problem is %s", d)
		}
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// A saturated runner answers 503 with Retry-After instead of queueing, so the
// caller waits it out rather than failing the import.
func TestHTTPExecutorWaitsOutASaturatedRunner(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "runner saturated; retry later", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(server.Response{ID: "abc", Status: server.StatusOK})
	}))
	defer srv.Close()

	res, err := NewHTTPExecutor(srv.URL, "s").Execute(context.Background(), server.Request{ID: "abc"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != server.StatusOK {
		t.Errorf("status = %q, want ok", res.Status)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("runner called %d times, want 2 refusals then the answer", got)
	}
}

func TestHTTPExecutorGivesUpOnAPermanentlySaturatedRunner(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		http.Error(w, "runner saturated; retry later", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := NewHTTPExecutor(srv.URL, "s").Execute(context.Background(), server.Request{ID: "abc"})
	if err == nil || !strings.Contains(err.Error(), "saturated") {
		t.Fatalf("err = %v, want the saturation reported", err)
	}
	if got := calls.Load(); got != executorRetryLimit {
		t.Errorf("runner called %d times, want the %d-attempt limit", got, executorRetryLimit)
	}
}

func TestHTTPExecutorReadsRetryAfterInBothForms(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{
		{"", executorRetryDelay},
		{"2", 2 * time.Second},
		{"0", time.Millisecond},
		{"nonsense", executorRetryDelay},
		{time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), time.Millisecond},
	} {
		if got := retryAfter(tc.header); got != tc.want {
			t.Errorf("retryAfter(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestProblemServiceConcurrencyDefaultsToTheRunnerDefault(t *testing.T) {
	if got := (&ProblemService{}).concurrency(); got != DefaultProblemConcurrency {
		t.Errorf("default concurrency = %d, want %d", got, DefaultProblemConcurrency)
	}
	if got := (&ProblemService{Concurrency: 7}).concurrency(); got != 7 {
		t.Errorf("configured concurrency = %d, want 7", got)
	}
}
