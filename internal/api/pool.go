package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/service"
)

// PoolEntry is one candidate kept in the talent pool.
type PoolEntry struct {
	ID               uuid.UUID          `json:"id"`
	CandidateID      uuid.UUID          `json:"candidate_id"`
	CandidateName    string             `json:"candidate_name"`
	CandidateEmail   string             `json:"candidate_email"`
	Skills           []string           `json:"skills"`
	Seniority        string             `json:"seniority,omitempty"`
	Location         string             `json:"location,omitempty"`
	RemoteOK         bool               `json:"remote_ok"`
	BestScores       map[string]float64 `json:"best_scores" doc:"Best assessment score per problem tag"`
	ScorecardSummary string             `json:"scorecard_summary,omitempty"`
	Notes            string             `json:"notes,omitempty"`
	SourceJobIDs     []uuid.UUID        `json:"source_job_ids"`
	Source           string             `json:"source,omitempty"`
	UpdatedAt        time.Time          `json:"updated_at"`
}

// MatchBreakdown is the arithmetic behind a suggestion's score.
type MatchBreakdown struct {
	Skills     float64 `json:"skills"`
	Seniority  float64 `json:"seniority"`
	Location   float64 `json:"location"`
	Assessment float64 `json:"assessment"`
}

// PoolSuggestion is one pool entry ranked against a job.
type PoolSuggestion struct {
	Entry     PoolEntry      `json:"entry"`
	Score     float64        `json:"score"`
	Breakdown MatchBreakdown `json:"breakdown"`
}

func poolEntryView(e service.PoolEntry) PoolEntry {
	scores := e.BestScores
	if scores == nil {
		scores = map[string]float64{}
	}
	return PoolEntry{
		ID: e.ID, CandidateID: e.CandidateID, CandidateName: e.CandidateName,
		CandidateEmail: e.CandidateEmail, Skills: list(e.Skills), Seniority: e.Seniority,
		Location: e.Location, RemoteOK: e.RemoteOK, BestScores: scores,
		ScorecardSummary: e.ScorecardSummary, Notes: e.Notes,
		SourceJobIDs: list(e.SourceJobIDs), Source: e.Source, UpdatedAt: e.UpdatedAt,
	}
}

type poolHandlers struct{ d Deps }

func (m *mounter) mountPool() {
	h := poolHandlers{d: m.d}
	register(m, accessOrg, huma.Operation{
		OperationID: "list-pool-entries", Method: http.MethodGet, Path: "/pool",
		Summary: "Browse the talent pool", Tags: []string{"pool"},
	}, h.list)
	register(m, accessOrg, huma.Operation{
		OperationID: "create-pool-entry", Method: http.MethodPost, Path: "/pool",
		Summary: "Keep an application's candidate in the pool", Tags: []string{"pool"}, DefaultStatus: http.StatusCreated,
	}, h.create)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-pool-entry", Method: http.MethodGet, Path: "/pool/{entry_id}",
		Summary: "One pool entry", Tags: []string{"pool"},
	}, h.get)
	register(m, accessOrg, huma.Operation{
		OperationID: "update-pool-entry", Method: http.MethodPut, Path: "/pool/{entry_id}",
		Summary: "Edit a pool entry", Tags: []string{"pool"},
	}, h.update)
	register(m, accessOrg, huma.Operation{
		OperationID: "delete-pool-entry", Method: http.MethodDelete, Path: "/pool/{entry_id}",
		Summary: "Remove a pool entry", Tags: []string{"pool"}, DefaultStatus: http.StatusNoContent,
	}, h.remove)
	register(m, accessOrg, huma.Operation{
		OperationID: "list-pool-suggestions", Method: http.MethodGet, Path: "/jobs/{job_id}/pool-suggestions",
		Summary: "Pool entries ranked against a job", Tags: []string{"pool"},
	}, h.suggestions)
	register(m, accessOrg, huma.Operation{
		OperationID: "add-pool-entry-to-job", Method: http.MethodPost, Path: "/jobs/{job_id}/pool-suggestions/{entry_id}",
		Summary: "Open an application for a pool entry", Tags: []string{"pool"}, DefaultStatus: http.StatusCreated,
	}, h.addToJob)
}

type listPoolInput struct {
	Query string `query:"q" doc:"Matched against name, address, and tags"`
}

type poolEntriesOutput struct {
	Body struct {
		Entries []PoolEntry `json:"entries"`
	}
}

type poolEntryOutput struct{ Body PoolEntry }

type poolEntryInput struct {
	EntryID uuid.UUID `path:"entry_id"`
}

type createPoolEntryInput struct {
	Body struct {
		ApplicationID uuid.UUID `json:"application_id"`
	}
}

type updatePoolEntryInput struct {
	EntryID uuid.UUID `path:"entry_id"`
	Body    struct {
		Skills   []string `json:"skills,omitempty"`
		Location string   `json:"location,omitempty"`
		RemoteOK bool     `json:"remote_ok,omitempty"`
		Notes    string   `json:"notes,omitempty"`
	}
}

type poolSuggestionsOutput struct {
	Body struct {
		Suggestions []PoolSuggestion `json:"suggestions"`
	}
}

type addPoolEntryInput struct {
	JobID   uuid.UUID `path:"job_id"`
	EntryID uuid.UUID `path:"entry_id"`
}

type addPoolEntryOutput struct {
	Body struct {
		ApplicationID uuid.UUID `json:"application_id"`
	}
}

func (h poolHandlers) list(ctx context.Context, in *listPoolInput) (*poolEntriesOutput, error) {
	es, err := h.d.Pool.List(ctx, principal(ctx), in.Query)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &poolEntriesOutput{}
	out.Body.Entries = make([]PoolEntry, 0, len(es))
	for _, e := range es {
		out.Body.Entries = append(out.Body.Entries, poolEntryView(e))
	}
	return out, nil
}

func (h poolHandlers) get(ctx context.Context, in *poolEntryInput) (*poolEntryOutput, error) {
	e, err := h.d.Pool.Entry(ctx, principal(ctx), in.EntryID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &poolEntryOutput{Body: poolEntryView(e)}, nil
}

func (h poolHandlers) create(ctx context.Context, in *createPoolEntryInput) (*poolEntryOutput, error) {
	e, err := h.d.Pool.Flag(ctx, principal(ctx), in.Body.ApplicationID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &poolEntryOutput{Body: poolEntryView(e)}, nil
}

func (h poolHandlers) update(ctx context.Context, in *updatePoolEntryInput) (*poolEntryOutput, error) {
	e, err := h.d.Pool.Update(ctx, principal(ctx), in.EntryID, service.PoolEdit{
		Skills: in.Body.Skills, Location: in.Body.Location, RemoteOK: in.Body.RemoteOK, Notes: in.Body.Notes,
	})
	if err != nil {
		return nil, problemDetail(err)
	}
	return &poolEntryOutput{Body: poolEntryView(e)}, nil
}

func (h poolHandlers) remove(ctx context.Context, in *poolEntryInput) (*struct{}, error) {
	if err := h.d.Pool.Remove(ctx, principal(ctx), in.EntryID); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

func (h poolHandlers) suggestions(ctx context.Context, in *jobInput) (*poolSuggestionsOutput, error) {
	ss, err := h.d.Pool.Suggestions(ctx, principal(ctx), in.JobID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &poolSuggestionsOutput{}
	out.Body.Suggestions = make([]PoolSuggestion, 0, len(ss))
	for _, s := range ss {
		out.Body.Suggestions = append(out.Body.Suggestions, PoolSuggestion{
			Entry: poolEntryView(s.Entry), Score: s.Score,
			Breakdown: MatchBreakdown{
				Skills: s.Breakdown.Skills, Seniority: s.Breakdown.Seniority,
				Location: s.Breakdown.Location, Assessment: s.Breakdown.Assessment,
			},
		})
	}
	return out, nil
}

func (h poolHandlers) addToJob(ctx context.Context, in *addPoolEntryInput) (*addPoolEntryOutput, error) {
	id, err := h.d.Pool.AddToJob(ctx, principal(ctx), in.JobID, in.EntryID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &addPoolEntryOutput{}
	out.Body.ApplicationID = id
	return out, nil
}
