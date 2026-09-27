//go:build integration

package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

// bigCaseBytes is a payload the size of a seed perf case: what a hundred
// thousand values come to as compact JSON.
const bigCaseBytes = 2 << 20

// bigCaseProblem is a code problem good enough to attach to an assessment,
// with one public case and, at position 2, a hidden case whose input is
// bigCaseBytes wide.
func bigCaseProblem(t *testing.T, title string) domain.ImportProblem {
	t.Helper()
	in := domain.ImportProblem{
		Kind: "code", Title: title, Statement: longStatement, Difficulty: "easy",
		Tags: []string{"perf"}, AllowedLanguages: []string{"python", "javascript"},
		References: []domain.ImportReference{
			{Language: "python", Source: "print(3)"}, {Language: "javascript", Source: "console.log(3)"},
		},
		TestCases: []domain.ImportTestCase{
			{Name: "sample", Input: "1 2", Expected: "3", Visibility: "public"},
			{Name: "perf", Class: "perf", Input: strings.Repeat("7 ", bigCaseBytes/2), Expected: "3", Visibility: "hidden"},
		},
	}
	for i := range 4 {
		in.TestCases = append(in.TestCases, domain.ImportTestCase{
			Name: "hidden " + string(rune('a'+i)), Input: "1 2", Expected: "3", Visibility: "hidden",
		})
	}
	in.Normalize()
	if errs := in.Validate(); len(errs) > 0 {
		t.Fatalf("fixture problem is invalid: %v", errs)
	}
	return in
}

// A problem read carries its cases as metadata: the sizes of the payloads,
// never the payloads. The reads that want them ask for them by name.
func TestProblemReadsCarryCaseMetadataUnlessAskedForPayloads(t *testing.T) {
	f := newProblemFixture(t)
	svc := service.NewProblemService(f.st, passExecutor{})
	ctx := context.Background()
	created, err := svc.Create(ctx, f.p, bigCaseProblem(t, "Big Case "+f.orgID.String()))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	perf := created.TestCases[1]
	if perf.Payload || perf.Input != "" || perf.Expected != "" {
		t.Errorf("a write answered with a payload: %d bytes loaded", len(perf.Input))
	}
	if perf.InputBytes != bigCaseBytes || perf.ExpectedBytes != 1 {
		t.Errorf("perf case sizes = %d/%d, want %d/1", perf.InputBytes, perf.ExpectedBytes, bigCaseBytes)
	}

	meta, err := svc.Get(ctx, f.p, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(meta.TestCases) != 6 || meta.CaseCount != 6 {
		t.Fatalf("metadata read carries %d cases (count %d), want 6", len(meta.TestCases), meta.CaseCount)
	}
	for _, c := range meta.TestCases {
		if c.Payload || c.Input != "" || c.Expected != "" {
			t.Errorf("metadata read of %q loaded a payload of %d bytes", c.Name, len(c.Input))
		}
		if c.Name == "" || c.Class == "" || c.Visibility == "" || c.Weight != 1 || c.Position == 0 {
			t.Errorf("metadata read of %q is missing a field: %+v", c.Name, c)
		}
	}
	if meta.TestCases[1].InputBytes != bigCaseBytes {
		t.Errorf("metadata read sizes the perf input at %d, want %d", meta.TestCases[1].InputBytes, bigCaseBytes)
	}

	whole, err := svc.GetWithCases(ctx, f.p, created.ID)
	if err != nil {
		t.Fatalf("get with cases: %v", err)
	}
	if !whole.TestCases[1].Payload || len(whole.TestCases[1].Input) != bigCaseBytes || whole.TestCases[1].Expected != "3" {
		t.Errorf("the payload read carries %d bytes, want %d", len(whole.TestCases[1].Input), bigCaseBytes)
	}

	public, err := svc.GetWithPublicCases(ctx, f.p, created.ID)
	if err != nil {
		t.Fatalf("get with public cases: %v", err)
	}
	if len(public.TestCases) != 1 || public.TestCases[0].Name != "sample" || public.TestCases[0].Input != "1 2" || public.CaseCount != 6 {
		t.Errorf("public read = %+v (count %d), want the sample case whole and a count of 6", public.TestCases, public.CaseCount)
	}

	inline, err := svc.GetWithCasesUpTo(ctx, f.p, created.ID, 16<<10)
	if err != nil {
		t.Fatalf("get with cases up to: %v", err)
	}
	if len(inline.TestCases) != 6 {
		t.Fatalf("inline read carries %d cases, want all 6", len(inline.TestCases))
	}
	for i, c := range inline.TestCases {
		switch {
		case i == 1 && (c.Payload || c.Input != "" || c.InputBytes != bigCaseBytes):
			t.Errorf("the big case was inlined: %d bytes", len(c.Input))
		case i != 1 && (!c.Payload || c.Input != "1 2"):
			t.Errorf("small case %q was not inlined: %+v", c.Name, c)
		}
	}

	one, err := svc.Case(ctx, f.p, created.ID, perf.ID)
	if err != nil {
		t.Fatalf("case: %v", err)
	}
	if !one.Payload || len(one.Input) != bigCaseBytes || one.Name != "perf" || one.Position != 2 {
		t.Errorf("case read = %d bytes, name %q, position %d", len(one.Input), one.Name, one.Position)
	}
	if _, err := svc.Case(ctx, f.p, uuid.New(), perf.ID); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("a case read under the wrong problem = %v, want not found", err)
	}
}

// A candidate polls a submission every couple of seconds while it runs. The
// poll maps result ids to visibility and position, which needs no payload:
// ten polls over a problem with a two-megabyte case allocate less than that
// one case. The execution itself does load the payload, since the runner
// needs it.
func TestSubmissionPollReadsNoCasePayload(t *testing.T) {
	f := newExecutionFixture(t, bigCaseProblem(t, "Poll Perf"))
	ctx := context.Background()
	att := f.start(t)
	cand := f.candidateOf(att.ID)
	problem := f.problems[0]

	sub, err := f.attempts.Submit(ctx, cand, att.ID, problem.ID, "python", "print(3)")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	exec := &recordingExecutor{}
	if err := f.execute(t, exec, sub.ID, 1); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(exec.requests) != 1 || len(exec.requests[0].Tests) != 6 || len(exec.requests[0].Tests[1].Input) != bigCaseBytes {
		t.Fatalf("the runner was not sent every case whole: %d requests", len(exec.requests))
	}

	poll := func() service.Submission {
		t.Helper()
		got, err := f.attempts.Submission(ctx, cand, att.ID, sub.ID)
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
		return got
	}
	first := poll()
	if first.CandidateResult == nil {
		t.Fatal("the poll carries no candidate result")
	}
	if got := first.CandidateResult; got.HiddenTotal != 5 || got.HiddenPassed != 5 || len(got.Public) != 1 || got.Public[0].Position != 1 {
		raw, _ := json.Marshal(got)
		t.Errorf("candidate result = %s, want one public case at position 1 and five hidden passes", raw)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	const polls = 10
	for range polls {
		poll()
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated >= bigCaseBytes {
		t.Errorf("%d polls allocated %d bytes; loading the %d-byte case each time would be at least %d", polls, allocated, bigCaseBytes, polls*bigCaseBytes)
	}
}
