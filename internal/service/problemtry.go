package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/domain"
	"recruiting/internal/runner/server"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// TryResult is one Try execution as the authoring page shows it.
type TryResult struct {
	Status        string
	CompileOutput string
	Results       []TryCase
}

// TryCase is one public case's verdict. Try it is the author's own screen —
// they wrote the case and its expected output — so it carries what the program
// printed and what it complained about, which is what a run is debugged from.
// The candidate's path is not this one and still sees neither.
type TryCase struct {
	TestIndex  int
	Name       string
	Class      string
	Status     string
	TimeMs     int64
	Expected   string
	Actual     string
	Stderr     string
	ActualHash string
}

// Try runs code against a problem's public test cases and answers with the
// verdict, writing nothing: no attempt, no submission, no recording. It is how
// an author checks their own problem, and how the Try it screen runs.
func (s *ProblemService) Try(ctx context.Context, p Principal, id uuid.UUID, language, source string) (TryResult, error) {
	if err := requireRecruiter(p); err != nil {
		return TryResult{}, err
	}
	if s.exec == nil {
		return TryResult{}, ErrNoExecutor
	}
	problem, err := s.Get(ctx, p, id)
	if err != nil {
		return TryResult{}, err
	}
	language = domain.NormalizeLanguageID(language)
	if !containsString(problem.AllowedLanguages, language) {
		return TryResult{}, fmt.Errorf("%w: this problem does not allow %s", ErrProblemInvalid, language)
	}
	req, cases := tryRequest(problem, language, source)
	if len(cases) == 0 {
		return TryResult{}, fmt.Errorf("%w: this problem has no public test case to run against", ErrProblemInvalid)
	}
	res, err := s.exec.Execute(ctx, req)
	if err != nil {
		return TryResult{}, fmt.Errorf("%w: %v", ErrRunnerUnavailable, err)
	}
	return tryResult(res, req, cases), nil
}

// tryRequest builds the execution of one attempt-free run: the problem's
// public cases only, because the author is standing where the candidate
// stands and a hidden case's expected output is not theirs to see there.
func tryRequest(p Problem, language, source string) (server.Request, []ProblemTestCase) {
	cases := make([]ProblemTestCase, 0, len(p.TestCases))
	for _, c := range p.TestCases {
		if c.Visibility == domain.VisibilityPublic {
			cases = append(cases, c)
		}
	}
	tests := make([]server.Test, 0, len(cases))
	for i, c := range cases {
		tests = append(tests, server.Test{
			ID: tryTestID(p.ID, i), Input: c.Input, Expected: c.Expected,
			Weight: weightUnits(c.Weight), Unordered: c.Unordered,
		})
	}
	// The id is derived from what is being run, so the runner's cache answers
	// a repeated try without another sandbox.
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\x00%s\x00%s\x00%d\x00%s\x00", p.ID, language, source, len(tests), signatureKey(p.Signature))
	return server.Request{
		ID:        uuid.NewSHA1(tryNamespace, sum.Sum(nil)).String(),
		Language:  language,
		Source:    source,
		SQLSchema: sqlSeedScript(p.AsImport()),
		Signature: signatureFor(p.AsImport()),
		Tests:     tests,
		Limits:    problemLimits(p.Kind, p.TimeLimitMs, p.MemoryLimitKB),
	}, cases
}

// tryNamespace keeps try run ids apart from every other id the platform
// derives, so a try can never collide with a scored submission.
var tryNamespace = uuid.NewSHA1(uuid.NameSpaceOID, []byte("recruiting/problem-try"))

func tryTestID(problemID uuid.UUID, i int) string {
	return uuid.NewSHA1(tryNamespace, fmt.Appendf(nil, "%s\x00%d", problemID, i)).String()
}

// tryResult pairs the runner's answers back onto the cases that were sent, in
// the order they were sent, so a case the runner never reported still has a
// row.
func tryResult(res server.Response, req server.Request, cases []ProblemTestCase) TryResult {
	byID := make(map[string]server.TestResult, len(res.Results))
	for _, r := range res.Results {
		byID[r.TestID] = r
	}
	out := TryResult{Status: res.Status, CompileOutput: res.CompileOutput}
	out.Results = make([]TryCase, 0, len(cases))
	for i, c := range cases {
		row := TryCase{TestIndex: i, Name: c.Name, Class: c.Class, Status: server.TestError, Expected: c.Expected}
		if i < len(req.Tests) {
			if r, ok := byID[req.Tests[i].ID]; ok {
				row.Status, row.TimeMs, row.ActualHash = r.Status, r.TimeMs, r.StdoutHash
				row.Actual, row.Stderr = r.StdoutTail, r.StderrTail
			}
		}
		out.Results = append(out.Results, row)
	}
	return out
}

// ReferenceVerdict is one reference solution's run against every case of the
// problem: what the runner made of the program overall, and how each case
// went. Step 4 of the authoring wizard renders one of these per language.
type ReferenceVerdict struct {
	Language      string
	Status        string
	CompileOutput string
	Cases         []VerifiedCase
	// Err is a runner that could not be reached, as against a solution that
	// simply failed.
	Err string
}

// Passed reports whether the solution solved every case.
func (v ReferenceVerdict) Passed() bool {
	if v.Err != "" || v.Status != server.StatusOK || len(v.Cases) == 0 {
		return false
	}
	for _, c := range v.Cases {
		if c.Status != server.TestPass {
			return false
		}
	}
	return true
}

// VerifiedCase is one case of one reference run.
type VerifiedCase struct {
	Position   int
	Name       string
	Class      string
	Visibility string
	Status     string
	TimeMs     int64
	Detail     string
}

// Verify runs every reference solution of a candidate problem against every
// one of its cases and reports the grid, without storing anything. It is what
// step 4 of the wizard shows before the author commits to a save.
func (s *ProblemService) Verify(ctx context.Context, p Principal, in domain.ImportProblem) ([]ReferenceVerdict, error) {
	if err := requireRecruiter(p); err != nil {
		return nil, err
	}
	if s.exec == nil {
		return nil, ErrNoExecutor
	}
	in.Normalize()
	if errs := in.Validate(); len(errs) > 0 {
		return nil, domain.ProblemImportErrors{{Index: 0, Title: in.Title, Errors: errs}}
	}
	ctx, cancel := context.WithTimeout(ctx, ImportTimeout)
	defer cancel()

	out := make([]ReferenceVerdict, len(in.References))
	sem := make(chan struct{}, s.concurrency())
	var wg sync.WaitGroup
	for i, ref := range in.References {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = verifyReference(ctx, s.exec, in, ref)
		}()
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Language < out[j].Language })
	return out, nil
}

// verifyReference runs one solution and lays its answers back over the cases.
func verifyReference(ctx context.Context, exec Executor, p domain.ImportProblem, ref domain.ImportReference) ReferenceVerdict {
	req := runnerRequest(p, ref)
	v := ReferenceVerdict{Language: ref.Language}
	res, err := exec.Execute(ctx, req)
	if err != nil {
		v.Err = err.Error()
		v.Status = server.StatusError
		return v
	}
	v.Status, v.CompileOutput = res.Status, res.CompileOutput
	byID := make(map[string]server.TestResult, len(res.Results))
	for _, r := range res.Results {
		byID[r.TestID] = r
	}
	for i, tc := range p.TestCases {
		row := VerifiedCase{
			Position: i + 1, Name: tc.Name, Class: tc.Class,
			Visibility: tc.Visibility, Status: server.TestError,
			Detail: "the runner did not report this case",
		}
		if i < len(req.Tests) {
			if r, ok := byID[req.Tests[i].ID]; ok {
				row.Status, row.TimeMs, row.Detail = r.Status, r.TimeMs, tailLine(r.StderrTail)
			}
		}
		v.Cases = append(v.Cases, row)
	}
	return v
}

// SaveDraft stores a half-finished problem without running anything. A draft
// scores zero and has no proven language, so it is stored, listed, and
// editable, but never attachable to an assessment. A zero id creates.
func (s *ProblemService) SaveDraft(ctx context.Context, p Principal, id uuid.UUID, in domain.ImportProblem) (Problem, error) {
	if err := requireRecruiter(p); err != nil {
		return Problem{}, err
	}
	in.Normalize()
	if errs := in.ValidateDraft(); len(errs) > 0 {
		return Problem{}, domain.ProblemImportErrors{{Index: 0, Title: in.Title, Errors: errs}}
	}
	if id != uuid.Nil {
		if err := s.requireOwn(ctx, p, id); err != nil {
			return Problem{}, err
		}
	}
	var out Problem
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		out, err = writeProblem(ctx, tx, p.OrgID, id, in, writeSpec{})
		return err
	})
	if err != nil {
		return Problem{}, problemWriteError("save draft", err)
	}
	return out, nil
}

// Clone copies a problem — the org's own or one of the shared platform seed —
// into the caller's org, recording where it came from. The copy is an ordinary
// org problem: editable, and unable to change the original.
func (s *ProblemService) Clone(ctx context.Context, p Principal, id uuid.UUID) (Problem, error) {
	if err := requireRecruiter(p); err != nil {
		return Problem{}, err
	}
	source, err := s.Get(ctx, p, id)
	if err != nil {
		return Problem{}, err
	}
	in := source.AsImport()
	spec := writeSpec{Origin: source.ID, Proven: source.ProvenLanguages, Quality: source.Quality}
	var out Problem
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		title, err := freeCloneTitle(ctx, tx, p.OrgID, source.Title)
		if err != nil {
			return err
		}
		in.Title = title
		out, err = writeProblem(ctx, tx, p.OrgID, uuid.Nil, in, spec)
		return err
	})
	if err != nil {
		return Problem{}, problemWriteError("clone problem", err)
	}
	return out, nil
}

// maxCloneAttempts bounds the search for a free title so a bank full of
// copies fails with a message rather than looping.
const maxCloneAttempts = 50

// freeCloneTitle is the copy's title: the original's with a copy marker, and a
// number once that is taken too. Titles are unique per org, so the clone of a
// problem already cloned still lands.
func freeCloneTitle(ctx context.Context, tx *store.Tx, orgID uuid.UUID, title string) (string, error) {
	for i := range maxCloneAttempts {
		candidate := title + " (copy)"
		if i > 0 {
			candidate = fmt.Sprintf("%s (copy %d)", title, i+1)
		}
		_, err := tx.Q.GetProblemByTitle(ctx, db.GetProblemByTitleParams{OrgID: orgID, Lower: candidate})
		if errors.Is(err, pgx.ErrNoRows) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("%w: there are already %d copies of %q", ErrProblemInvalid, maxCloneAttempts, title)
}
