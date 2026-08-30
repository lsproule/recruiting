package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/service"
)

// Criterion is one line of a stage's rubric.
type Criterion struct {
	Name        string `json:"name" minLength:"1"`
	Description string `json:"description,omitempty"`
}

// CriterionScore is one criterion as an interviewer scored it.
type CriterionScore struct {
	Name  string `json:"name" minLength:"1"`
	Score int    `json:"score" minimum:"1" maximum:"5"`
	Notes string `json:"notes,omitempty"`
}

// Rubric is the criteria an interview stage is scored against.
type Rubric struct {
	ID        uuid.UUID   `json:"id"`
	JobID     uuid.UUID   `json:"job_id"`
	StageID   uuid.UUID   `json:"stage_id"`
	StageName string      `json:"stage_name,omitempty"`
	Name      string      `json:"name,omitempty"`
	Criteria  []Criterion `json:"criteria"`
}

// Scorecard is one filed interview scorecard.
type Scorecard struct {
	ID            uuid.UUID        `json:"id"`
	ApplicationID uuid.UUID        `json:"application_id"`
	StageID       uuid.UUID        `json:"stage_id"`
	StageName     string           `json:"stage_name,omitempty"`
	VetterID      uuid.UUID        `json:"vetter_id"`
	VetterName    string           `json:"vetter_name,omitempty"`
	Criteria      []Criterion      `json:"criteria"`
	Scores        []CriterionScore `json:"scores"`
	Overall       string           `json:"overall"`
	Notes         string           `json:"notes,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

// ScorecardAssignment is one interview an org user still owes a scorecard for.
type ScorecardAssignment struct {
	ApplicationID  uuid.UUID `json:"application_id"`
	StageID        uuid.UUID `json:"stage_id"`
	CandidateName  string    `json:"candidate_name"`
	CandidateEmail string    `json:"candidate_email"`
	JobTitle       string    `json:"job_title"`
	StageName      string    `json:"stage_name"`
	Submitted      bool      `json:"submitted"`
	Overall        string    `json:"overall,omitempty"`
}

func criterionViews(in []service.Criterion) []Criterion {
	out := make([]Criterion, 0, len(in))
	for _, c := range in {
		out = append(out, Criterion{Name: c.Name, Description: c.Description})
	}
	return out
}

func serviceCriteria(in []Criterion) []service.Criterion {
	out := make([]service.Criterion, 0, len(in))
	for _, c := range in {
		out = append(out, service.Criterion{Name: c.Name, Description: c.Description})
	}
	return out
}

func scoreViews(in []service.CriterionScore) []CriterionScore {
	out := make([]CriterionScore, 0, len(in))
	for _, s := range in {
		out = append(out, CriterionScore{Name: s.Name, Score: s.Score, Notes: s.Notes})
	}
	return out
}

func serviceScores(in []CriterionScore) []service.CriterionScore {
	out := make([]service.CriterionScore, 0, len(in))
	for _, s := range in {
		out = append(out, service.CriterionScore{Name: s.Name, Score: s.Score, Notes: s.Notes})
	}
	return out
}

func rubricView(r service.Rubric) Rubric {
	return Rubric{ID: r.ID, JobID: r.JobID, StageID: r.StageID, StageName: r.StageName, Name: r.Name, Criteria: criterionViews(r.Criteria)}
}

func scorecardView(s service.Scorecard) Scorecard {
	return Scorecard{
		ID: s.ID, ApplicationID: s.ApplicationID, StageID: s.StageID, StageName: s.StageName,
		VetterID: s.VetterID, VetterName: s.VetterName,
		Criteria: criterionViews(s.Criteria), Scores: scoreViews(s.Scores),
		Overall: s.Overall, Notes: s.Notes, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

type scorecardsHandlers struct{ d Deps }

func (m *mounter) mountScorecards() {
	h := scorecardsHandlers{d: m.d}
	register(m, accessOrg, huma.Operation{
		OperationID: "list-scorecards", Method: http.MethodGet, Path: "/applications/{application_id}/scorecards",
		Summary: "Scorecards filed on an application", Tags: []string{"scorecards"},
	}, h.list)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-scorecard", Method: http.MethodGet, Path: "/applications/{application_id}/stages/{stage_id}/scorecard",
		Summary: "The stage's scorecard form and the caller's card, if filed", Tags: []string{"scorecards"},
	}, h.form)
	register(m, accessOrg, huma.Operation{
		OperationID: "create-scorecard", Method: http.MethodPost, Path: "/scorecards",
		Summary: "File or amend a scorecard", Tags: []string{"scorecards"}, DefaultStatus: http.StatusCreated,
	}, h.save)
	register(m, accessOrg, huma.Operation{
		OperationID: "list-scorecard-assignments", Method: http.MethodGet, Path: "/scorecard-assignments",
		Summary: "Interviews the caller owes a scorecard for", Tags: []string{"scorecards"},
	}, h.assignments)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-rubric", Method: http.MethodGet, Path: "/jobs/{job_id}/stages/{stage_id}/rubric",
		Summary: "The stage's rubric", Tags: []string{"scorecards"},
	}, h.rubric)
	register(m, accessOrg, huma.Operation{
		OperationID: "set-rubric", Method: http.MethodPut, Path: "/jobs/{job_id}/stages/{stage_id}/rubric",
		Summary: "Replace the stage's rubric", Tags: []string{"scorecards"},
	}, h.setRubric)
}

type scorecardsOutput struct {
	Body struct {
		Scorecards []Scorecard `json:"scorecards"`
	}
}

type scorecardFormInput struct {
	ApplicationID uuid.UUID `path:"application_id"`
	StageID       uuid.UUID `path:"stage_id"`
}

type scorecardFormOutput struct {
	Body struct {
		ApplicationID uuid.UUID   `json:"application_id"`
		StageID       uuid.UUID   `json:"stage_id"`
		CandidateName string      `json:"candidate_name"`
		JobTitle      string      `json:"job_title"`
		StageName     string      `json:"stage_name"`
		Criteria      []Criterion `json:"criteria"`
		Scorecard     *Scorecard  `json:"scorecard,omitempty"`
	}
}

type saveScorecardInput struct {
	Body struct {
		ID            uuid.UUID        `json:"id,omitempty" doc:"Set to amend a card the caller filed"`
		ApplicationID uuid.UUID        `json:"application_id"`
		StageID       uuid.UUID        `json:"stage_id"`
		Scores        []CriterionScore `json:"scores" minItems:"1"`
		Overall       string           `json:"overall"`
		Notes         string           `json:"notes,omitempty"`
	}
}

type scorecardOutput struct{ Body Scorecard }

type scorecardAssignmentsOutput struct {
	Body struct {
		Assignments []ScorecardAssignment `json:"assignments"`
	}
}

type rubricInput struct {
	JobID   uuid.UUID `path:"job_id"`
	StageID uuid.UUID `path:"stage_id"`
}

type setRubricInput struct {
	JobID   uuid.UUID `path:"job_id"`
	StageID uuid.UUID `path:"stage_id"`
	Body    struct {
		Name     string      `json:"name,omitempty"`
		Criteria []Criterion `json:"criteria" minItems:"1"`
	}
}

type rubricOutput struct{ Body Rubric }

func (h scorecardsHandlers) list(ctx context.Context, in *applicationInput) (*scorecardsOutput, error) {
	cards, err := h.d.Scorecards.List(ctx, principal(ctx), in.ApplicationID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &scorecardsOutput{}
	out.Body.Scorecards = make([]Scorecard, 0, len(cards))
	for _, c := range cards {
		out.Body.Scorecards = append(out.Body.Scorecards, scorecardView(c))
	}
	return out, nil
}

func (h scorecardsHandlers) form(ctx context.Context, in *scorecardFormInput) (*scorecardFormOutput, error) {
	f, err := h.d.Scorecards.Form(ctx, principal(ctx), in.ApplicationID, in.StageID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &scorecardFormOutput{}
	out.Body.ApplicationID, out.Body.StageID = f.ApplicationID, f.StageID
	out.Body.CandidateName, out.Body.JobTitle, out.Body.StageName = f.CandidateName, f.JobTitle, f.StageName
	out.Body.Criteria = criterionViews(f.Criteria)
	if f.Card != nil {
		card := scorecardView(*f.Card)
		out.Body.Scorecard = &card
	}
	return out, nil
}

func (h scorecardsHandlers) save(ctx context.Context, in *saveScorecardInput) (*scorecardOutput, error) {
	card, err := h.d.Scorecards.Save(ctx, principal(ctx), service.ScorecardInput{
		ID: in.Body.ID, ApplicationID: in.Body.ApplicationID, StageID: in.Body.StageID,
		Scores: serviceScores(in.Body.Scores), Overall: in.Body.Overall, Notes: in.Body.Notes,
	})
	if err != nil {
		return nil, problemDetail(err)
	}
	return &scorecardOutput{Body: scorecardView(card)}, nil
}

func (h scorecardsHandlers) assignments(ctx context.Context, _ *struct{}) (*scorecardAssignmentsOutput, error) {
	as, err := h.d.Scorecards.Assignments(ctx, principal(ctx))
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &scorecardAssignmentsOutput{}
	out.Body.Assignments = make([]ScorecardAssignment, 0, len(as))
	for _, a := range as {
		out.Body.Assignments = append(out.Body.Assignments, ScorecardAssignment{
			ApplicationID: a.ApplicationID, StageID: a.StageID,
			CandidateName: a.CandidateName, CandidateEmail: a.CandidateEmail,
			JobTitle: a.JobTitle, StageName: a.StageName, Submitted: a.Submitted, Overall: a.Overall,
		})
	}
	return out, nil
}

func (h scorecardsHandlers) rubric(ctx context.Context, in *rubricInput) (*rubricOutput, error) {
	r, err := h.d.Scorecards.Rubric(ctx, principal(ctx), in.JobID, in.StageID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &rubricOutput{Body: rubricView(r)}, nil
}

func (h scorecardsHandlers) setRubric(ctx context.Context, in *setRubricInput) (*rubricOutput, error) {
	r, err := h.d.Scorecards.SetRubric(ctx, principal(ctx), in.JobID, in.StageID, service.RubricInput{
		Name: in.Body.Name, Criteria: serviceCriteria(in.Body.Criteria),
	})
	if err != nil {
		return nil, problemDetail(err)
	}
	return &rubricOutput{Body: rubricView(r)}, nil
}
