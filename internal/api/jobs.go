package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

// Job is one job as the API renders it.
type Job struct {
	ID                uuid.UUID `json:"id"`
	ClientCompanyID   uuid.UUID `json:"client_company_id"`
	ClientCompanyName string    `json:"client_company_name,omitempty"`
	Title             string    `json:"title"`
	Slug              string    `json:"slug"`
	Description       string    `json:"description" doc:"Markdown"`
	Skills            []string  `json:"skills"`
	Seniority         string    `json:"seniority,omitempty"`
	Location          string    `json:"location,omitempty"`
	RemotePolicy      string    `json:"remote_policy,omitempty"`
	SalaryMin         int       `json:"salary_min"`
	SalaryMax         int       `json:"salary_max"`
	BlindMode         bool      `json:"blind_mode"`
	Status            string    `json:"status"`
}

// JobInput is the body of a job create or update.
type JobInput struct {
	ClientCompanyID uuid.UUID `json:"client_company_id"`
	Title           string    `json:"title" minLength:"1"`
	Slug            string    `json:"slug,omitempty"`
	Description     string    `json:"description,omitempty"`
	Skills          []string  `json:"skills,omitempty"`
	Seniority       string    `json:"seniority,omitempty"`
	Location        string    `json:"location,omitempty"`
	RemotePolicy    string    `json:"remote_policy,omitempty"`
	SalaryMin       int       `json:"salary_min,omitempty"`
	SalaryMax       int       `json:"salary_max,omitempty"`
	BlindMode       bool      `json:"blind_mode,omitempty"`
	Status          string    `json:"status,omitempty"`
}

func (in JobInput) service() service.NewJob {
	return service.NewJob{
		ClientCompanyID: in.ClientCompanyID, Title: in.Title, Slug: in.Slug,
		Description: in.Description, Skills: in.Skills, Seniority: in.Seniority,
		Location: in.Location, RemotePolicy: in.RemotePolicy,
		SalaryMin: in.SalaryMin, SalaryMax: in.SalaryMax,
		BlindMode: in.BlindMode, Status: in.Status,
	}
}

func jobView(j service.Job) Job {
	return Job{
		ID: j.ID, ClientCompanyID: j.ClientCompanyID, ClientCompanyName: j.ClientCompanyName,
		Title: j.Title, Slug: j.Slug, Description: j.Description, Skills: list(j.Skills),
		Seniority: j.Seniority, Location: j.Location, RemotePolicy: j.RemotePolicy,
		SalaryMin: j.SalaryMin, SalaryMax: j.SalaryMax, BlindMode: j.BlindMode, Status: j.Status,
	}
}

// Stage is one column of a job's pipeline.
type Stage struct {
	ID       uuid.UUID `json:"id"`
	Position int       `json:"position"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind" enum:"generic,interview,assessment,client_review,terminal"`
	Unblind  bool      `json:"unblind"`
	Terminal string    `json:"terminal,omitempty" doc:"Outcome a terminal stage closes an application with"`
}

// StageInput is the body of a stage create or update.
type StageInput struct {
	Name     string `json:"name" minLength:"1"`
	Kind     string `json:"kind" enum:"generic,interview,assessment,client_review,terminal"`
	Terminal string `json:"terminal,omitempty" doc:"Required on a terminal stage"`
	Unblind  bool   `json:"unblind,omitempty"`
}

func (in StageInput) service() service.StageInput {
	return service.StageInput{
		Name: in.Name, Kind: domain.StageKind(in.Kind),
		Terminal: domain.ApplicationStatus(in.Terminal), Unblind: in.Unblind,
	}
}

func stageView(s domain.Stage) Stage {
	return Stage{ID: s.ID, Position: s.Position, Name: s.Name, Kind: string(s.Kind), Unblind: s.Unblind, Terminal: string(s.Terminal)}
}

func stageViews(in []domain.Stage) []Stage {
	out := make([]Stage, 0, len(in))
	for _, s := range in {
		out = append(out, stageView(s))
	}
	return out
}

// list turns a nil slice into an empty one so the JSON carries [] and not
// null; every collection field of the API answers the same shape.
func list[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}

type jobsHandlers struct{ d Deps }

func (m *mounter) mountJobs() {
	h := jobsHandlers{d: m.d}
	register(m, accessOrg, huma.Operation{
		OperationID: "list-jobs", Method: http.MethodGet, Path: "/jobs",
		Summary: "Jobs of the org", Tags: []string{"jobs"},
	}, h.list)
	register(m, accessOrg, huma.Operation{
		OperationID: "create-job", Method: http.MethodPost, Path: "/jobs",
		Summary: "Open a job", Tags: []string{"jobs"}, DefaultStatus: http.StatusCreated,
	}, h.create)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-job", Method: http.MethodGet, Path: "/jobs/{job_id}",
		Summary: "One job", Tags: []string{"jobs"},
	}, h.get)
	register(m, accessOrg, huma.Operation{
		OperationID: "update-job", Method: http.MethodPut, Path: "/jobs/{job_id}",
		Summary: "Edit a job", Tags: []string{"jobs"},
	}, h.update)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-job-board", Method: http.MethodGet, Path: "/jobs/{job_id}/board",
		Summary: "The job's pipeline board", Tags: []string{"jobs"},
	}, h.board)
}

type jobsOutput struct {
	Body struct {
		Jobs []Job `json:"jobs"`
	}
}

type jobOutput struct{ Body Job }

type jobInput struct {
	JobID uuid.UUID `path:"job_id"`
}

type createJobInput struct{ Body JobInput }

type updateJobInput struct {
	JobID uuid.UUID `path:"job_id"`
	Body  JobInput
}

func (h jobsHandlers) list(ctx context.Context, _ *struct{}) (*jobsOutput, error) {
	jobs, err := h.d.Jobs.ListJobs(ctx, principal(ctx))
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &jobsOutput{}
	out.Body.Jobs = make([]Job, 0, len(jobs))
	for _, j := range jobs {
		out.Body.Jobs = append(out.Body.Jobs, jobView(j))
	}
	return out, nil
}

func (h jobsHandlers) get(ctx context.Context, in *jobInput) (*jobOutput, error) {
	j, err := h.d.Jobs.Job(ctx, principal(ctx), in.JobID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &jobOutput{Body: jobView(j)}, nil
}

func (h jobsHandlers) create(ctx context.Context, in *createJobInput) (*jobOutput, error) {
	j, err := h.d.Jobs.CreateJob(ctx, principal(ctx), in.Body.service())
	if err != nil {
		return nil, problemDetail(err)
	}
	return &jobOutput{Body: jobView(j)}, nil
}

func (h jobsHandlers) update(ctx context.Context, in *updateJobInput) (*jobOutput, error) {
	j, err := h.d.Jobs.UpdateJob(ctx, principal(ctx), in.JobID, in.Body.service())
	if err != nil {
		return nil, problemDetail(err)
	}
	return &jobOutput{Body: jobView(j)}, nil
}

// BoardColumn is one stage of the board with the cards standing in it.
type BoardColumn struct {
	Stage Stage         `json:"stage"`
	Cards []Application `json:"cards"`
}

type boardOutput struct {
	Body struct {
		Job     Job           `json:"job"`
		Columns []BoardColumn `json:"columns"`
	}
}

func (h jobsHandlers) board(ctx context.Context, in *jobInput) (*boardOutput, error) {
	b, err := h.d.Applications.Board(ctx, principal(ctx), in.JobID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &boardOutput{}
	out.Body.Job = jobView(b.Job)
	out.Body.Columns = make([]BoardColumn, 0, len(b.Columns))
	for _, c := range b.Columns {
		col := BoardColumn{Stage: stageView(c.Stage), Cards: make([]Application, 0, len(c.Cards))}
		for _, card := range c.Cards {
			col.Cards = append(col.Cards, applicationView(card))
		}
		out.Body.Columns = append(out.Body.Columns, col)
	}
	return out, nil
}

type stagesHandlers struct{ d Deps }

func (m *mounter) mountStages() {
	h := stagesHandlers{d: m.d}
	register(m, accessOrg, huma.Operation{
		OperationID: "list-stages", Method: http.MethodGet, Path: "/jobs/{job_id}/stages",
		Summary: "Pipeline stages of a job", Tags: []string{"stages"},
	}, h.list)
	register(m, accessOrg, huma.Operation{
		OperationID: "create-stage", Method: http.MethodPost, Path: "/jobs/{job_id}/stages",
		Summary: "Append a stage", Tags: []string{"stages"}, DefaultStatus: http.StatusCreated,
	}, h.create)
	register(m, accessOrg, huma.Operation{
		OperationID: "reorder-stages", Method: http.MethodPost, Path: "/jobs/{job_id}/stages/reorder",
		Summary: "Reorder the stages", Tags: []string{"stages"}, DefaultStatus: http.StatusNoContent,
	}, h.reorder)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-stage", Method: http.MethodGet, Path: "/jobs/{job_id}/stages/{stage_id}",
		Summary: "One stage", Tags: []string{"stages"},
	}, h.get)
	register(m, accessOrg, huma.Operation{
		OperationID: "update-stage", Method: http.MethodPut, Path: "/jobs/{job_id}/stages/{stage_id}",
		Summary: "Edit a stage", Tags: []string{"stages"},
	}, h.update)
	register(m, accessOrg, huma.Operation{
		OperationID: "delete-stage", Method: http.MethodDelete, Path: "/jobs/{job_id}/stages/{stage_id}",
		Summary: "Remove an empty stage", Tags: []string{"stages"}, DefaultStatus: http.StatusNoContent,
	}, h.remove)
}

type stagesOutput struct {
	Body struct {
		Stages []Stage `json:"stages"`
	}
}

type stageOutput struct{ Body Stage }

type stageMemberInput struct {
	JobID   uuid.UUID `path:"job_id"`
	StageID uuid.UUID `path:"stage_id"`
}

type createStageInput struct {
	JobID uuid.UUID `path:"job_id"`
	Body  StageInput
}

type updateStageInput struct {
	JobID   uuid.UUID `path:"job_id"`
	StageID uuid.UUID `path:"stage_id"`
	Body    StageInput
}

type reorderStagesInput struct {
	JobID uuid.UUID `path:"job_id"`
	Body  struct {
		Order []uuid.UUID `json:"order" minItems:"1" doc:"Stage ids in their new order"`
	}
}

func (h stagesHandlers) list(ctx context.Context, in *jobInput) (*stagesOutput, error) {
	stages, err := h.d.Jobs.Stages(ctx, principal(ctx), in.JobID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &stagesOutput{}
	out.Body.Stages = stageViews(stages)
	return out, nil
}

func (h stagesHandlers) get(ctx context.Context, in *stageMemberInput) (*stageOutput, error) {
	stages, err := h.d.Jobs.Stages(ctx, principal(ctx), in.JobID)
	if err != nil {
		return nil, problemDetail(err)
	}
	for _, s := range stages {
		if s.ID == in.StageID {
			return &stageOutput{Body: stageView(s)}, nil
		}
	}
	return nil, problemDetail(service.ErrNotFound)
}

func (h stagesHandlers) create(ctx context.Context, in *createStageInput) (*stageOutput, error) {
	s, err := h.d.Jobs.AddStage(ctx, principal(ctx), in.JobID, in.Body.service())
	if err != nil {
		return nil, problemDetail(err)
	}
	return &stageOutput{Body: stageView(s)}, nil
}

func (h stagesHandlers) update(ctx context.Context, in *updateStageInput) (*stageOutput, error) {
	s, err := h.d.Jobs.UpdateStage(ctx, principal(ctx), in.JobID, in.StageID, in.Body.service())
	if err != nil {
		return nil, problemDetail(err)
	}
	return &stageOutput{Body: stageView(s)}, nil
}

func (h stagesHandlers) remove(ctx context.Context, in *stageMemberInput) (*struct{}, error) {
	if err := h.d.Jobs.DeleteStage(ctx, principal(ctx), in.JobID, in.StageID); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

func (h stagesHandlers) reorder(ctx context.Context, in *reorderStagesInput) (*struct{}, error) {
	if err := h.d.Jobs.ReorderStages(ctx, principal(ctx), in.JobID, in.Body.Order); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}
