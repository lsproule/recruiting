//go:build integration

package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/runner/server"
	"recruiting/internal/service"
)

// authoredProblem is a problem good enough to clear the quality floor: six
// cases, one of them public, three hidden, a tag, a long statement, and two
// languages with a reference solution.
func authoredProblem(title string) domain.ImportProblem {
	in := domain.ImportProblem{
		Kind: domain.ProblemKindCode, Title: title,
		Statement:        strings.Repeat("Add the two numbers on the line. ", 12),
		Difficulty:       "easy",
		Tags:             []string{"math"},
		AllowedLanguages: []string{"python", "go"},
		Guidelines:       "Watch whether they read the whole line before splitting it.",
		References: []domain.ImportReference{
			{Language: "python", Source: "print(3)"},
			{Language: "go", Source: "package main"},
		},
	}
	for i := range 6 {
		v := domain.VisibilityHidden
		if i == 0 {
			v = domain.VisibilityPublic
		}
		in.TestCases = append(in.TestCases, domain.ImportTestCase{
			Name: "case " + string(rune('a'+i)), Input: "1 2", Expected: "3", Visibility: v,
		})
	}
	in.Normalize()
	return in
}

func TestSaveScoresQualityAndProvenLanguages(t *testing.T) {
	f := newProblemFixture(t)
	ctx := context.Background()
	svc := service.NewProblemService(f.st, passExecutor{})

	good, err := svc.Create(ctx, f.p, authoredProblem("Quality Adder"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := good.Quality; got != 100 {
		t.Errorf("quality = %d, want 100", got)
	}
	if strings.Join(good.ProvenLanguages, ",") != "go,python" {
		t.Errorf("proven = %v, want go and python", good.ProvenLanguages)
	}
	if !good.Attachable() {
		t.Error("a problem meeting every rule is not attachable")
	}
	if good.Guidelines == "" {
		t.Errorf("authoring fields were not stored: %+v", good)
	}
	if good.TestCases[0].Name == "" || good.TestCases[0].Class != domain.CaseClassSample {
		t.Errorf("case name and class were not stored: %+v", good.TestCases[0])
	}

	thin := authoredProblem("Thin Adder")
	thin.Tags = nil
	thin.Statement = "Add them."
	thin.References = thin.References[:1]
	thin.TestCases = thin.TestCases[:2]
	thin.Normalize()
	weak, err := svc.Create(ctx, f.p, thin)
	if err != nil {
		t.Fatalf("create thin: %v", err)
	}
	if weak.Attachable() {
		t.Errorf("a problem scoring %d is attachable", weak.Quality)
	}
}

// A draft is stored so nothing an author typed is lost, but it is proven in
// nothing and scores zero, so it can never reach a candidate.
func TestSaveDraftStoresNothingProven(t *testing.T) {
	f := newProblemFixture(t)
	ctx := context.Background()
	exec := &countingExecutor{}
	svc := service.NewProblemService(f.st, exec)

	draft, err := svc.SaveDraft(ctx, f.p, uuid.Nil, domain.ImportProblem{
		Kind: domain.ProblemKindCode, Title: "Draft Adder", Difficulty: "easy",
	})
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}
	if draft.Quality != 0 || len(draft.ProvenLanguages) != 0 {
		t.Errorf("draft = quality %d, proven %v", draft.Quality, draft.ProvenLanguages)
	}
	if exec.calls != 0 {
		t.Errorf("a draft ran %d solutions through the runner", exec.calls)
	}

	again, err := svc.SaveDraft(ctx, f.p, draft.ID, domain.ImportProblem{
		Kind: domain.ProblemKindCode, Title: "Draft Adder", Difficulty: "hard",
	})
	if err != nil {
		t.Fatalf("re-save draft: %v", err)
	}
	if again.ID != draft.ID {
		t.Errorf("re-saving the draft made a second problem: %s then %s", draft.ID, again.ID)
	}
	if again.Difficulty != "hard" {
		t.Errorf("difficulty = %q, want hard", again.Difficulty)
	}
}

func TestAssessmentRefusesAProblemBelowTheQualityFloor(t *testing.T) {
	f := newProblemFixture(t)
	ctx := context.Background()
	svc := service.NewProblemService(f.st, passExecutor{})
	assessments := service.NewAssessmentService(f.st)

	good, err := svc.Create(ctx, f.p, authoredProblem("Attachable Adder"))
	if err != nil {
		t.Fatal(err)
	}
	weak, err := svc.SaveDraft(ctx, f.p, uuid.Nil, domain.ImportProblem{
		Kind: domain.ProblemKindCode, Title: "Unproven Adder", Difficulty: "easy",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := assessments.Create(ctx, f.p, service.AssessmentInput{
		Name: "Fine", DurationMinutes: 60, InviteWindowDays: 7, ProblemIDs: []uuid.UUID{good.ID},
	}); err != nil {
		t.Fatalf("attaching a good problem: %v", err)
	}
	_, err = assessments.Create(ctx, f.p, service.AssessmentInput{
		Name: "Refused", DurationMinutes: 60, InviteWindowDays: 7, ProblemIDs: []uuid.UUID{weak.ID},
	})
	if !errors.Is(err, service.ErrProblemQuality) {
		t.Fatalf("attaching a draft = %v, want ErrProblemQuality", err)
	}
}

func TestCloneCopiesASeedProblemAndLeavesTheOriginalAlone(t *testing.T) {
	f := newProblemFixture(t)
	ctx := context.Background()
	svc := service.NewProblemService(f.st, passExecutor{})

	seeded, err := f.seedPlatform(t, ctx, svc)
	if err != nil {
		t.Fatalf("seed the platform bank: %v", err)
	}
	original := seeded[0]

	if _, err := svc.Update(ctx, f.p, original.ID, original.AsImport()); !errors.Is(err, service.ErrPlatformProblem) {
		t.Fatalf("editing a platform problem = %v, want ErrPlatformProblem", err)
	}

	copied, err := svc.Clone(ctx, f.p, original.ID)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if copied.OriginProblemID != original.ID {
		t.Errorf("origin = %s, want %s", copied.OriginProblemID, original.ID)
	}
	if copied.Platform() {
		t.Error("the clone belongs to the platform bank")
	}
	if copied.Title == original.Title {
		t.Errorf("the clone reuses the original's title %q", copied.Title)
	}
	if len(copied.TestCases) != len(original.TestCases) || len(copied.References) != len(original.References) {
		t.Errorf("clone carries %d cases and %d references", len(copied.TestCases), len(copied.References))
	}

	edited := copied.AsImport()
	edited.Difficulty = "hard"
	if _, err := svc.Update(ctx, f.p, copied.ID, edited); err != nil {
		t.Fatalf("editing the clone: %v", err)
	}
	after, err := svc.Get(ctx, f.p, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Difficulty != original.Difficulty {
		t.Errorf("editing the clone changed the original to %q", after.Difficulty)
	}
}

// tryRecorder keeps the last request so a test can see exactly what
// reached the runner.
type tryRecorder struct{ last server.Request }

func (e *tryRecorder) Execute(_ context.Context, req server.Request) (server.Response, error) {
	e.last = req
	res := make([]server.TestResult, len(req.Tests))
	for i, tc := range req.Tests {
		res[i] = server.TestResult{TestID: tc.ID, Status: server.TestPass, TimeMs: 4, StdoutHash: "hash"}
	}
	return server.Response{ID: req.ID, Status: server.StatusOK, Results: res}, nil
}

func TestTryRunsPublicCasesAndWritesNothing(t *testing.T) {
	f := newProblemFixture(t)
	ctx := context.Background()
	created, err := service.NewProblemService(f.st, passExecutor{}).Create(ctx, f.p, authoredProblem("Try Adder"))
	if err != nil {
		t.Fatal(err)
	}

	exec := &tryRecorder{}
	res, err := service.NewProblemService(f.st, exec).Try(ctx, f.p, created.ID, "python", "print(3)")
	if err != nil {
		t.Fatalf("try: %v", err)
	}
	if len(res.Results) != 1 || res.Status != server.StatusOK {
		t.Fatalf("try result = %+v, want the one public case", res)
	}
	if len(exec.last.Tests) != 1 {
		t.Errorf("%d cases reached the runner, want the 1 public one", len(exec.last.Tests))
	}
	for _, tc := range exec.last.Tests {
		if tc.Expected != "3" {
			t.Errorf("a case the candidate cannot see reached the runner: %+v", tc)
		}
	}

	var submissions, attempts int
	if err := f.owner.QueryRow(ctx, `select count(*) from submission`).Scan(&submissions); err != nil {
		t.Fatal(err)
	}
	if err := f.owner.QueryRow(ctx, `select count(*) from attempt`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if submissions != 0 || attempts != 0 {
		t.Errorf("try wrote %d submissions and %d attempts", submissions, attempts)
	}

	if _, err := service.NewProblemService(f.st, exec).Try(ctx, f.p, created.ID, "haskell", "main = pure ()"); !errors.Is(err, service.ErrProblemInvalid) {
		t.Errorf("trying a language the problem does not allow = %v", err)
	}
}

func TestVerifyReportsEveryCaseForEveryLanguage(t *testing.T) {
	f := newProblemFixture(t)
	ctx := context.Background()
	svc := service.NewProblemService(f.st, passExecutor{fail: map[string]bool{"package main": true}})

	verdicts, err := svc.Verify(ctx, f.p, authoredProblem("Verify Adder"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(verdicts) != 2 {
		t.Fatalf("%d verdicts, want one per reference solution", len(verdicts))
	}
	byLang := map[string]service.ReferenceVerdict{}
	for _, v := range verdicts {
		byLang[v.Language] = v
		if len(v.Cases) != 6 {
			t.Errorf("%s reports %d cases, want 6", v.Language, len(v.Cases))
		}
	}
	if !byLang["python"].Passed() {
		t.Error("the passing solution is not reported as passing")
	}
	if byLang["go"].Passed() {
		t.Error("the failing solution is reported as passing")
	}
}
