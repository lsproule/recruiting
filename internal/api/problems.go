package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

// ProblemReference is a solution that must pass every case of the problem.
type ProblemReference struct {
	Language string `json:"language" minLength:"1"`
	Source   string `json:"source" minLength:"1"`
}

// ProblemTestCase is one case a submission is scored against.
type ProblemTestCase struct {
	ID         uuid.UUID `json:"id,omitempty"`
	Position   int       `json:"position,omitempty"`
	Input      string    `json:"input"`
	Expected   string    `json:"expected"`
	Visibility string    `json:"visibility" enum:"public,hidden"`
	Weight     float64   `json:"weight,omitempty"`
	Unordered  bool      `json:"unordered,omitempty" doc:"Compare SQL rows as a multiset"`
}

// Problem is one bank problem with everything an author edits.
type Problem struct {
	ID               uuid.UUID          `json:"id"`
	Kind             string             `json:"kind"`
	Title            string             `json:"title"`
	Statement        string             `json:"statement"`
	Difficulty       string             `json:"difficulty"`
	Tags             []string           `json:"tags"`
	AllowedLanguages []string           `json:"allowed_languages"`
	TimeLimitMs      int                `json:"time_limit_ms"`
	MemoryLimitKB    int                `json:"memory_limit_kb"`
	SQLSchema        string             `json:"sql_schema,omitempty"`
	SQLSeed          string             `json:"sql_seed,omitempty"`
	References       []ProblemReference `json:"reference_solutions"`
	TestCases        []ProblemTestCase  `json:"test_cases"`
	Platform         bool               `json:"platform" doc:"A platform seed problem, which is read-only"`
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
}

// ProblemInput is the body of a problem create or update; it is the same
// shape as one entry of the import document.
type ProblemInput struct {
	Kind             string             `json:"kind"`
	Title            string             `json:"title" minLength:"1"`
	Statement        string             `json:"statement"`
	Difficulty       string             `json:"difficulty"`
	Tags             []string           `json:"tags,omitempty"`
	AllowedLanguages []string           `json:"allowed_languages,omitempty"`
	TimeLimitMs      int                `json:"time_limit_ms,omitempty"`
	MemoryLimitKB    int                `json:"memory_limit_kb,omitempty"`
	SQLSchema        string             `json:"sql_schema,omitempty"`
	SQLSeed          string             `json:"sql_seed,omitempty"`
	References       []ProblemReference `json:"reference_solutions,omitempty"`
	TestCases        []ProblemTestCase  `json:"test_cases"`
}

func (in ProblemInput) importProblem() domain.ImportProblem {
	out := domain.ImportProblem{
		Kind: in.Kind, Title: in.Title, Statement: in.Statement, Difficulty: in.Difficulty,
		Tags: in.Tags, AllowedLanguages: in.AllowedLanguages,
		TimeLimitMs: in.TimeLimitMs, MemoryLimitKB: in.MemoryLimitKB,
		SQLSchema: in.SQLSchema, SQLSeed: in.SQLSeed,
	}
	for _, r := range in.References {
		out.References = append(out.References, domain.ImportReference{Language: r.Language, Source: r.Source})
	}
	for _, c := range in.TestCases {
		tc := domain.ImportTestCase{Input: c.Input, Expected: c.Expected, Visibility: c.Visibility, Unordered: c.Unordered}
		if c.Weight != 0 {
			w := c.Weight
			tc.Weight = &w
		}
		out.TestCases = append(out.TestCases, tc)
	}
	return out
}

func problemView(p service.Problem) Problem {
	out := Problem{
		ID: p.ID, Kind: p.Kind, Title: p.Title, Statement: p.Statement, Difficulty: p.Difficulty,
		Tags: list(p.Tags), AllowedLanguages: list(p.AllowedLanguages),
		TimeLimitMs: p.TimeLimitMs, MemoryLimitKB: p.MemoryLimitKB,
		SQLSchema: p.SQLSchema, SQLSeed: p.SQLSeed,
		Platform:  p.OrgID == uuid.Nil,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
	out.References = make([]ProblemReference, 0, len(p.References))
	for _, r := range p.References {
		out.References = append(out.References, ProblemReference{Language: r.Language, Source: r.Source})
	}
	out.TestCases = make([]ProblemTestCase, 0, len(p.TestCases))
	for _, c := range p.TestCases {
		out.TestCases = append(out.TestCases, ProblemTestCase{
			ID: c.ID, Position: c.Position, Input: c.Input, Expected: c.Expected,
			Visibility: c.Visibility, Weight: c.Weight, Unordered: c.Unordered,
		})
	}
	return out
}

func problemViews(in []service.Problem) []Problem {
	out := make([]Problem, 0, len(in))
	for _, p := range in {
		out = append(out, problemView(p))
	}
	return out
}

type problemsHandlers struct{ d Deps }

func (m *mounter) mountProblems() {
	h := problemsHandlers{d: m.d}
	register(m, accessOrg, huma.Operation{
		OperationID: "list-problems", Method: http.MethodGet, Path: "/problems",
		Summary: "The problem bank", Tags: []string{"problems"},
	}, h.list)
	register(m, accessOrg, huma.Operation{
		OperationID: "create-problem", Method: http.MethodPost, Path: "/problems",
		Summary: "Author a problem", Tags: []string{"problems"}, DefaultStatus: http.StatusCreated,
	}, h.create)
	register(m, accessOrg, huma.Operation{
		OperationID: "import-problems", Method: http.MethodPost, Path: "/problems/import",
		Summary: "Import a batch of problems", Tags: []string{"problems"}, DefaultStatus: http.StatusCreated,
		MaxBodyBytes: 4 << 20,
	}, h.importDoc)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-problem", Method: http.MethodGet, Path: "/problems/{problem_id}",
		Summary: "One problem", Tags: []string{"problems"},
	}, h.get)
	register(m, accessOrg, huma.Operation{
		OperationID: "update-problem", Method: http.MethodPut, Path: "/problems/{problem_id}",
		Summary: "Edit a problem", Tags: []string{"problems"},
	}, h.update)
	register(m, accessOrg, huma.Operation{
		OperationID: "delete-problem", Method: http.MethodDelete, Path: "/problems/{problem_id}",
		Summary: "Remove an unused problem", Tags: []string{"problems"}, DefaultStatus: http.StatusNoContent,
	}, h.remove)
}

type listProblemsInput struct {
	Kind       string `query:"kind"`
	Difficulty string `query:"difficulty"`
	Tag        string `query:"tag"`
	Query      string `query:"q"`
}

type problemsOutput struct {
	Body struct {
		Problems []Problem `json:"problems"`
	}
}

type problemOutput struct{ Body Problem }

type problemInput struct {
	ProblemID uuid.UUID `path:"problem_id"`
}

type createProblemInput struct{ Body ProblemInput }

type updateProblemInput struct {
	ProblemID uuid.UUID `path:"problem_id"`
	Body      ProblemInput
}

type importProblemsInput struct {
	Body struct {
		Document []byte `json:"document" format:"byte" doc:"The import document, base64 encoded"`
	}
}

func (h problemsHandlers) list(ctx context.Context, in *listProblemsInput) (*problemsOutput, error) {
	ps, err := h.d.Problems.List(ctx, principal(ctx), service.ProblemFilter{
		Kind: in.Kind, Difficulty: in.Difficulty, Tag: in.Tag, Query: in.Query,
	})
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &problemsOutput{}
	out.Body.Problems = problemViews(ps)
	return out, nil
}

func (h problemsHandlers) get(ctx context.Context, in *problemInput) (*problemOutput, error) {
	p, err := h.d.Problems.Get(ctx, principal(ctx), in.ProblemID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &problemOutput{Body: problemView(p)}, nil
}

func (h problemsHandlers) create(ctx context.Context, in *createProblemInput) (*problemOutput, error) {
	p, err := h.d.Problems.Create(ctx, principal(ctx), in.Body.importProblem())
	if err != nil {
		return nil, problemDetail(err)
	}
	return &problemOutput{Body: problemView(p)}, nil
}

func (h problemsHandlers) update(ctx context.Context, in *updateProblemInput) (*problemOutput, error) {
	p, err := h.d.Problems.Update(ctx, principal(ctx), in.ProblemID, in.Body.importProblem())
	if err != nil {
		return nil, problemDetail(err)
	}
	return &problemOutput{Body: problemView(p)}, nil
}

func (h problemsHandlers) remove(ctx context.Context, in *problemInput) (*struct{}, error) {
	if err := h.d.Problems.Delete(ctx, principal(ctx), in.ProblemID); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

func (h problemsHandlers) importDoc(ctx context.Context, in *importProblemsInput) (*problemsOutput, error) {
	ps, err := h.d.Problems.Import(ctx, principal(ctx), in.Body.Document)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &problemsOutput{}
	out.Body.Problems = problemViews(ps)
	return out, nil
}
