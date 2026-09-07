package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"recruiting/internal/domain"
	"recruiting/internal/runner/server"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// PlatformOrgID owns the seed problem bank every org may read but none may
// write; it matches platform_org_id() in the schema.
var PlatformOrgID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// ProblemListLimit caps a bank listing. The bank is browsed, not exported.
const ProblemListLimit = 500

// DefaultProblemConcurrency bounds how many reference solutions are in the
// runner at once, so one import cannot fill the sandbox pool. It matches the
// runner's own default concurrency; a deployment that gives the runner more
// slots raises ProblemService.Concurrency to match.
const DefaultProblemConcurrency = 2

// ImportTimeout bounds a whole import, however many solutions it has to
// prove. Past it the batch is refused rather than held open.
const ImportTimeout = 10 * time.Minute

var (
	// ErrNoExecutor refuses a write that cannot be proven against the runner.
	ErrNoExecutor = errors.New("service: no runner is configured, so reference solutions cannot be checked")
	// ErrPlatformProblem refuses an edit of the shared seed bank.
	ErrPlatformProblem = errors.New("service: platform problems are read-only")
	// ErrProblemTitleTaken names the real cause of a unique-title violation.
	ErrProblemTitleTaken = errors.New("service: a problem with that title already exists")
	// ErrProblemInvalid refuses a request the problem itself cannot answer —
	// a language it does not allow, a run with nothing to run against.
	ErrProblemInvalid = errors.New("service: invalid problem request")
)

// Problem is one bank entry with everything needed to run it.
type Problem struct {
	ID               uuid.UUID
	OrgID            uuid.UUID
	Kind             string
	Title            string
	Statement        string
	Difficulty       string
	Tags             []string
	AllowedLanguages []string
	TimeLimitMs      int
	MemoryLimitKB    int
	SQLSchema        string
	SQLSeed          string
	// RecommendedMinutes sizes an assessment built from the problem.
	RecommendedMinutes int
	// Guidelines are what an interviewer watches for; internal only.
	Guidelines string
	// OriginProblemID names the problem this one was cloned from, if any.
	OriginProblemID uuid.UUID
	// Quality is domain.ProblemQuality at the last save; a draft is 0.
	Quality int
	// ProvenLanguages are the languages a reference solution passed every
	// case in at the last verified save. A draft has none.
	ProvenLanguages []string
	// CaseCount is how many test cases the problem has. A listing carries it
	// without the cases themselves, which it does not load.
	CaseCount  int
	References []ProblemReference
	TestCases  []ProblemTestCase
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Attachable reports whether the problem scores well enough to be attached to
// an assessment.
func (p Problem) Attachable() bool { return p.Quality >= domain.ProblemQualityFloor }

// Platform reports whether the problem belongs to the shared seed bank, which
// every org reads and none edits.
func (p Problem) Platform() bool { return p.OrgID == PlatformOrgID }

// ProblemReference is a solution that passes every test case.
type ProblemReference struct {
	Language string
	Source   string
}

// ProblemTestCase is one case a submission is scored against.
type ProblemTestCase struct {
	ID         uuid.UUID
	Position   int
	Name       string
	Class      string
	Input      string
	Expected   string
	Visibility string
	Weight     float64
	Unordered  bool
}

// ProblemFilter narrows a bank listing; an empty field means "any".
type ProblemFilter struct {
	Kind       string
	Difficulty string
	Tag        string
	Query      string
}

// Executor runs one execution request against the sandboxed runner. The
// import path needs the runner synchronously — a reference solution is proof
// the problem is solvable — so it calls this rather than the queue.
// internal/runner/client.Client is the wire implementation.
type Executor interface {
	Execute(ctx context.Context, req server.Request) (server.Response, error)
}

// ProblemService owns the problem bank: what is in it, the JSON import that
// fills it, and the platform seed every org reads.
type ProblemService struct {
	st   *store.Store
	exec Executor
	// Concurrency is how many reference solutions may be in the runner at
	// once. Zero means DefaultProblemConcurrency.
	Concurrency int
}

// concurrency is the bound to run validation at.
func (s *ProblemService) concurrency() int {
	if s.Concurrency > 0 {
		return s.Concurrency
	}
	return DefaultProblemConcurrency
}

// NewProblemService builds the service. exec may be nil, in which case every
// write refuses with ErrNoExecutor rather than storing an unproven problem.
func NewProblemService(st *store.Store, exec Executor) *ProblemService {
	return &ProblemService{st: st, exec: exec}
}

// List returns the org's problems and the platform seed, narrowed by filter.
// Only the scalar fields are loaded; the detail screen reads the rest.
func (s *ProblemService) List(ctx context.Context, p Principal, f ProblemFilter) ([]Problem, error) {
	if err := requireRecruiter(p); err != nil {
		return nil, err
	}
	var out []Problem
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.FilterProblems(ctx, db.FilterProblemsParams{
			Kind:       strings.ToLower(strings.TrimSpace(f.Kind)),
			Difficulty: strings.ToLower(strings.TrimSpace(f.Difficulty)),
			Tag:        strings.ToLower(strings.TrimSpace(f.Tag)),
			Query:      strings.TrimSpace(f.Query),
			RowLimit:   ProblemListLimit,
		})
		if err != nil {
			return err
		}
		out = make([]Problem, 0, len(rows))
		for _, r := range rows {
			p := toProblem(db.Problem{
				ID: r.ID, OrgID: r.OrgID, Kind: r.Kind, Title: r.Title, Statement: r.Statement,
				Difficulty: r.Difficulty, Tags: r.Tags, AllowedLanguages: r.AllowedLanguages,
				TimeLimitMs: r.TimeLimitMs, MemoryLimitKb: r.MemoryLimitKb,
				SqlSchema: r.SqlSchema, SqlSeed: r.SqlSeed,
				RecommendedMinutes: r.RecommendedMinutes, Guidelines: r.Guidelines,
				OriginProblemID: r.OriginProblemID, Quality: r.Quality,
				ProvenLanguages: r.ProvenLanguages,
				CreatedAt:       r.CreatedAt, UpdatedAt: r.UpdatedAt,
			})
			p.CaseCount = int(r.CaseCount)
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list problems: %w", err)
	}
	return out, nil
}

// Get loads one problem with its reference solutions and test cases.
func (s *ProblemService) Get(ctx context.Context, p Principal, id uuid.UUID) (Problem, error) {
	if err := requireRecruiter(p); err != nil {
		return Problem{}, err
	}
	var out Problem
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		out, err = loadProblem(ctx, tx, id)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Problem{}, ErrNotFound
	}
	if err != nil {
		return Problem{}, fmt.Errorf("get problem: %w", err)
	}
	return out, nil
}

// Create validates a problem, proves every reference solution against the
// runner, and stores it under the caller's org.
func (s *ProblemService) Create(ctx context.Context, p Principal, in domain.ImportProblem) (Problem, error) {
	if err := requireRecruiter(p); err != nil {
		return Problem{}, err
	}
	if err := s.checkOne(ctx, &in); err != nil {
		return Problem{}, err
	}
	var out Problem
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		out, err = writeProblem(ctx, tx, p.OrgID, uuid.Nil, in, verifiedSpec(in))
		return err
	})
	if err != nil {
		return Problem{}, problemWriteError("create problem", err)
	}
	return out, nil
}

// Update replaces a problem's fields, reference solutions, and test cases.
// Platform problems belong to the seed bank and refuse the edit.
func (s *ProblemService) Update(ctx context.Context, p Principal, id uuid.UUID, in domain.ImportProblem) (Problem, error) {
	if err := requireRecruiter(p); err != nil {
		return Problem{}, err
	}
	// Ownership first: refusing an edit of the shared bank must not cost a
	// runner execution.
	if err := s.requireOwn(ctx, p, id); err != nil {
		return Problem{}, err
	}
	if err := s.checkOne(ctx, &in); err != nil {
		return Problem{}, err
	}
	var out Problem
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		existing, err := tx.Q.GetProblem(ctx, id)
		if err != nil {
			return err
		}
		if existing.OrgID != p.OrgID {
			return ErrPlatformProblem
		}
		out, err = writeProblem(ctx, tx, p.OrgID, id, in, verifiedSpec(in))
		return err
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Problem{}, ErrNotFound
	case errors.Is(err, ErrPlatformProblem):
		return Problem{}, ErrPlatformProblem
	case err != nil:
		return Problem{}, problemWriteError("update problem", err)
	}
	return out, nil
}

// requireOwn resolves a problem the caller may write: absent is ErrNotFound,
// and one the org can only read is a platform problem.
func (s *ProblemService) requireOwn(ctx context.Context, p Principal, id uuid.UUID) error {
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetProblem(ctx, id)
		if err != nil {
			return err
		}
		if row.OrgID != p.OrgID {
			return ErrPlatformProblem
		}
		return nil
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, ErrPlatformProblem):
		return ErrPlatformProblem
	case err != nil:
		return fmt.Errorf("get problem: %w", err)
	}
	return nil
}

// Delete removes one of the org's own problems.
func (s *ProblemService) Delete(ctx context.Context, p Principal, id uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	if err := s.requireOwn(ctx, p, id); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		n, err := tx.Q.DeleteProblem(ctx, db.DeleteProblemParams{ID: id, OrgID: p.OrgID})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("delete problem: %w", err)
	}
	return nil
}

// Import parses a JSON batch, runs every reference solution through the
// runner, and stores the whole batch or none of it. The returned error is a
// domain.ProblemImportErrors when individual problems are at fault, so the
// caller can show a per-problem report.
func (s *ProblemService) Import(ctx context.Context, p Principal, doc []byte) ([]Problem, error) {
	if err := requireRecruiter(p); err != nil {
		return nil, err
	}
	problems, err := domain.ParseProblemImport(doc)
	if err != nil {
		return nil, err
	}
	if s.exec == nil {
		return nil, ErrNoExecutor
	}
	ctx, cancel := context.WithTimeout(ctx, ImportTimeout)
	defer cancel()
	if err := validateProblemReferences(ctx, s.exec, s.concurrency(), problems); err != nil {
		return nil, err
	}
	var out []Problem
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		out, err = replaceProblems(ctx, tx, p.OrgID, problems)
		return err
	})
	if err != nil {
		return nil, problemWriteError("import problems", err)
	}
	return out, nil
}

// ImportPlatformSeed loads the embedded seed bank under the platform org. It
// takes a transaction because it runs on the RLS-bypassing owner connection:
// no tenant may write the platform org, so the admin CLI is the only way in.
// Every seed problem is re-imported by title, so a second run refreshes the
// bank rather than duplicating it.
func (s *ProblemService) ImportPlatformSeed(ctx context.Context, tx *store.Tx) ([]Problem, error) {
	problems, err := SeedProblems()
	if err != nil {
		return nil, err
	}
	if s.exec == nil {
		return nil, ErrNoExecutor
	}
	ctx, cancel := context.WithTimeout(ctx, ImportTimeout)
	defer cancel()
	if err := validateProblemReferences(ctx, s.exec, s.concurrency(), problems); err != nil {
		return nil, err
	}
	out, err := replaceProblems(ctx, tx, PlatformOrgID, problems)
	if err != nil {
		return nil, problemWriteError("seed problems", err)
	}
	return out, nil
}

// checkOne validates a single problem the way an import batch is validated,
// so a hand-written problem is held to the same standard.
func (s *ProblemService) checkOne(ctx context.Context, in *domain.ImportProblem) error {
	in.Normalize()
	if errs := in.Validate(); len(errs) > 0 {
		return domain.ProblemImportErrors{{Index: 0, Title: in.Title, Errors: errs}}
	}
	if s.exec == nil {
		return ErrNoExecutor
	}
	return validateProblemReferences(ctx, s.exec, s.concurrency(), []domain.ImportProblem{*in})
}

// validateProblemReferences runs every reference solution of every problem
// against that problem's test cases. Failures are collected for the whole
// batch — an author fixing an import wants every broken problem at once —
// and any failure rejects all of it.
func validateProblemReferences(ctx context.Context, exec Executor, concurrency int, problems []domain.ImportProblem) error {
	if concurrency <= 0 {
		concurrency = DefaultProblemConcurrency
	}
	type job struct{ problem, ref int }
	var jobs []job
	for i := range problems {
		for j := range problems[i].References {
			jobs = append(jobs, job{i, j})
		}
	}
	failures := make([][]string, len(problems))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for _, jb := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			p, ref := problems[jb.problem], problems[jb.problem].References[jb.ref]
			if msg := checkReference(ctx, exec, p, ref); msg != "" {
				mu.Lock()
				failures[jb.problem] = append(failures[jb.problem], msg)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	var report domain.ProblemImportErrors
	for i, msgs := range failures {
		if len(msgs) == 0 {
			continue
		}
		sort.Strings(msgs)
		report = append(report, domain.ProblemImportError{Index: i, Title: problems[i].Title, Errors: msgs})
	}
	if len(report) > 0 {
		return report
	}
	return nil
}

// checkReference runs one solution and returns why it is not a reference, or
// "" when it solved every case.
func checkReference(ctx context.Context, exec Executor, p domain.ImportProblem, ref domain.ImportReference) string {
	req := runnerRequest(p, ref)
	res, err := exec.Execute(ctx, req)
	if err != nil {
		return fmt.Sprintf("the %s reference solution could not be run: %v", ref.Language, err)
	}
	if res.Status != server.StatusOK {
		detail := strings.TrimSpace(res.CompileOutput)
		if detail == "" {
			detail = res.Status
		}
		return fmt.Sprintf("the %s reference solution did not run (%s): %s", ref.Language, res.Status, tailLine(detail))
	}
	byID := make(map[string]server.TestResult, len(res.Results))
	for _, r := range res.Results {
		byID[r.TestID] = r
	}
	var bad []string
	for i, t := range req.Tests {
		r, ok := byID[t.ID]
		switch {
		case !ok:
			bad = append(bad, fmt.Sprintf("case %d was not run", i+1))
		case r.Status != server.TestPass:
			detail := strings.TrimSpace(r.StderrTail)
			if detail != "" {
				detail = ": " + tailLine(detail)
			}
			bad = append(bad, fmt.Sprintf("case %d %s%s", i+1, r.Status, detail))
		}
	}
	if len(bad) == 0 {
		return ""
	}
	return fmt.Sprintf("the %s reference solution does not solve the problem (%s)", ref.Language, strings.Join(bad, ", "))
}

// runnerRequest builds the execution of one reference solution against every
// test case. The id is derived from the request so re-validating an unchanged
// problem is answered from the runner's own cache.
func runnerRequest(p domain.ImportProblem, ref domain.ImportReference) server.Request {
	memMB := p.MemoryLimitKB / 1024
	if memMB <= 0 {
		memMB = 1
	}
	limits := server.Limits{CPUMs: p.TimeLimitMs, WallMs: p.TimeLimitMs, MemMB: memMB}
	// The id is the whole request: the runner caches by it, so anything that
	// changes the verdict — the limits included — must change the id.
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00",
		ref.Language, ref.Source, p.SQLSchema, p.SQLSeed, limits.CPUMs, limits.WallMs, limits.MemMB)
	tests := make([]server.Test, 0, len(p.TestCases))
	for i, tc := range p.TestCases {
		fmt.Fprintf(sum, "%s\x00%s\x00%v\x00", tc.Input, tc.Expected, tc.Unordered)
		tests = append(tests, server.Test{
			ID:        testCaseRunID(ref, i),
			Input:     tc.Input,
			Expected:  tc.Expected,
			Weight:    1,
			Unordered: tc.Unordered,
		})
	}
	return server.Request{
		ID:        uuid.NewSHA1(problemRunNamespace, sum.Sum(nil)).String(),
		Language:  ref.Language,
		Source:    ref.Source,
		SQLSchema: sqlSeedScript(p),
		Tests:     tests,
		Limits:    limits,
	}
}

// problemRunNamespace names the ids derived for reference validation, keeping
// them apart from any other uuid v5 the platform derives.
var problemRunNamespace = uuid.NewSHA1(uuid.NameSpaceOID, []byte("recruiting/problem-reference"))

func testCaseRunID(ref domain.ImportReference, i int) string {
	return uuid.NewSHA1(problemRunNamespace, []byte(ref.Language+"\x00"+strconv.Itoa(i))).String()
}

// sqlSeedScript is what a SQL execution runs before the candidate's query:
// the schema and then its seed rows.
func sqlSeedScript(p domain.ImportProblem) string {
	if p.Kind != domain.ProblemKindSQL {
		return ""
	}
	parts := make([]string, 0, 2)
	for _, s := range []string{p.SQLSchema, p.SQLSeed} {
		if strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

// replaceProblems writes a whole batch under orgID, replacing any problem
// with the same title so a re-import refreshes rather than duplicates.
func replaceProblems(ctx context.Context, tx *store.Tx, orgID uuid.UUID, problems []domain.ImportProblem) ([]Problem, error) {
	out := make([]Problem, 0, len(problems))
	for _, in := range problems {
		id := uuid.Nil
		existing, err := tx.Q.GetProblemByTitle(ctx, db.GetProblemByTitleParams{OrgID: orgID, Lower: in.Title})
		switch {
		case err == nil:
			id = existing.ID
		case !errors.Is(err, pgx.ErrNoRows):
			return nil, err
		}
		stored, err := writeProblem(ctx, tx, orgID, id, in, verifiedSpec(in))
		if err != nil {
			return nil, err
		}
		out = append(out, stored)
	}
	return out, nil
}

// writeSpec is what a write knows beyond the problem document: where a clone
// came from, and the verdict of the reference run. A draft carries no proven
// language and scores zero, so a half-finished problem can never be attached
// to an assessment.
type writeSpec struct {
	Origin  uuid.UUID
	Proven  []string
	Quality int
}

// verifiedSpec is the verdict of a save whose references have all just passed:
// every language with a solution is proven, and the score follows from that.
func verifiedSpec(in domain.ImportProblem) writeSpec {
	proven := make([]string, 0, len(in.References))
	for _, ref := range in.References {
		proven = append(proven, ref.Language)
	}
	sort.Strings(proven)
	return writeSpec{Proven: proven, Quality: domain.ProblemQuality(in.Quality(proven))}
}

// writeProblem inserts or replaces one problem with its reference solutions
// and test cases. A zero id inserts.
func writeProblem(ctx context.Context, tx *store.Tx, orgID, id uuid.UUID, in domain.ImportProblem, w writeSpec) (Problem, error) {
	params := db.CreateProblemParams{
		OrgID: orgID, Kind: in.Kind, Title: in.Title, Statement: in.Statement,
		Difficulty: in.Difficulty, Tags: in.Tags, AllowedLanguages: in.AllowedLanguages,
		TimeLimitMs: int32(in.TimeLimitMs), MemoryLimitKb: int32(in.MemoryLimitKB),
		SqlSchema: nullText(in.SQLSchema), SqlSeed: nullText(in.SQLSeed),
		RecommendedMinutes: int32(in.RecommendedMinutes), Guidelines: in.Guidelines,
		OriginProblemID: nullUUID(w.Origin), Quality: int32(w.Quality),
		// The column is not null: a problem proven in nothing stores the
		// empty array, never NULL.
		ProvenLanguages: append([]string{}, w.Proven...),
	}
	var row db.Problem
	var err error
	if id == uuid.Nil {
		row, err = tx.Q.CreateProblem(ctx, params)
	} else {
		row, err = tx.Q.UpdateProblem(ctx, db.UpdateProblemParams{
			ID: id, OrgID: orgID, Kind: params.Kind, Title: params.Title, Statement: params.Statement,
			Difficulty: params.Difficulty, Tags: params.Tags, AllowedLanguages: params.AllowedLanguages,
			TimeLimitMs: params.TimeLimitMs, MemoryLimitKb: params.MemoryLimitKb,
			SqlSchema: params.SqlSchema, SqlSeed: params.SqlSeed,
			RecommendedMinutes: params.RecommendedMinutes, Guidelines: params.Guidelines,
			Quality: params.Quality, ProvenLanguages: params.ProvenLanguages,
		})
		if err == nil {
			if err = tx.Q.DeleteProblemReferences(ctx, row.ID); err == nil {
				err = tx.Q.DeleteTestCases(ctx, row.ID)
			}
		}
	}
	if err != nil {
		return Problem{}, err
	}
	for _, ref := range in.References {
		if _, err := tx.Q.CreateProblemReference(ctx, db.CreateProblemReferenceParams{
			OrgID: orgID, ProblemID: row.ID, Language: ref.Language, Source: ref.Source,
		}); err != nil {
			return Problem{}, err
		}
	}
	for i, tc := range in.TestCases {
		if _, err := tx.Q.CreateTestCase(ctx, db.CreateTestCaseParams{
			OrgID: orgID, ProblemID: row.ID, Position: int32(i + 1),
			Input: tc.Input, ExpectedOutput: tc.Expected, Visibility: tc.Visibility,
			Weight: numeric(tc.WeightValue()), Unordered: tc.Unordered,
			Name: tc.Name, Class: tc.Class,
		}); err != nil {
			return Problem{}, err
		}
	}
	return loadProblem(ctx, tx, row.ID)
}

func loadProblem(ctx context.Context, tx *store.Tx, id uuid.UUID) (Problem, error) {
	row, err := tx.Q.GetProblem(ctx, id)
	if err != nil {
		return Problem{}, err
	}
	out := toProblem(row)
	refs, err := tx.Q.ListProblemReferences(ctx, id)
	if err != nil {
		return Problem{}, err
	}
	for _, r := range refs {
		out.References = append(out.References, ProblemReference{Language: r.Language, Source: r.Source})
	}
	cases, err := tx.Q.ListTestCases(ctx, id)
	if err != nil {
		return Problem{}, err
	}
	out.CaseCount = len(cases)
	for _, c := range cases {
		weight := 1.0
		if f, err := c.Weight.Float64Value(); err == nil && f.Valid {
			weight = f.Float64
		}
		out.TestCases = append(out.TestCases, ProblemTestCase{
			ID: c.ID, Position: int(c.Position), Name: c.Name, Class: c.Class,
			Input: c.Input, Expected: c.ExpectedOutput,
			Visibility: c.Visibility, Weight: weight, Unordered: c.Unordered,
		})
	}
	return out, nil
}

// AsImport turns a stored problem back into the import shape, which is what
// the edit form posts and the import document carries.
func (p Problem) AsImport() domain.ImportProblem {
	out := domain.ImportProblem{
		Kind: p.Kind, Title: p.Title, Statement: p.Statement, Difficulty: p.Difficulty,
		Tags: p.Tags, AllowedLanguages: p.AllowedLanguages,
		TimeLimitMs: p.TimeLimitMs, MemoryLimitKB: p.MemoryLimitKB,
		SQLSchema: p.SQLSchema, SQLSeed: p.SQLSeed,
		RecommendedMinutes: p.RecommendedMinutes, Guidelines: p.Guidelines,
	}
	for _, r := range p.References {
		out.References = append(out.References, domain.ImportReference{Language: r.Language, Source: r.Source})
	}
	for _, c := range p.TestCases {
		weight := c.Weight
		out.TestCases = append(out.TestCases, domain.ImportTestCase{
			Name: c.Name, Class: c.Class,
			Input: c.Input, Expected: c.Expected, Visibility: c.Visibility,
			Weight: &weight, Unordered: c.Unordered,
		})
	}
	return out
}

func toProblem(r db.Problem) Problem {
	return Problem{
		ID: r.ID, OrgID: r.OrgID, Kind: r.Kind, Title: r.Title, Statement: r.Statement,
		Difficulty: r.Difficulty, Tags: r.Tags, AllowedLanguages: r.AllowedLanguages,
		TimeLimitMs: int(r.TimeLimitMs), MemoryLimitKB: int(r.MemoryLimitKb),
		SQLSchema: text(r.SqlSchema), SQLSeed: text(r.SqlSeed),
		RecommendedMinutes: int(r.RecommendedMinutes), Guidelines: r.Guidelines,
		OriginProblemID: r.OriginProblemID.UUID, Quality: int(r.Quality),
		ProvenLanguages: r.ProvenLanguages,
		CreatedAt:       r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
	}
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

func nullText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func text(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func numeric(f float64) pgtype.Numeric {
	var n pgtype.Numeric
	if err := n.Scan(strconv.FormatFloat(f, 'f', -1, 64)); err != nil {
		return pgtype.Numeric{Int: nil, Valid: false}
	}
	return n
}

// tailLine keeps an error detail short enough for a report line.
func tailLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// problemWriteError maps a duplicate title onto its own error; everything
// else keeps the operation's name.
func problemWriteError(what string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation && pgErr.ConstraintName == "problem_org_title_idx" {
		return ErrProblemTitleTaken
	}
	return fmt.Errorf("%s: %w", what, err)
}
