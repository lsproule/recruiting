package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/service"
)

// Assessment is one timed problem set a stage can hand out.
type Assessment struct {
	ID               uuid.UUID `json:"id"`
	Name             string    `json:"name"`
	DurationMinutes  int       `json:"duration_minutes"`
	LanguageOverride string    `json:"language_override,omitempty"`
	InviteWindowDays int       `json:"invite_window_days"`
	ProblemCount     int       `json:"problem_count"`
	Problems         []Problem `json:"problems"`
}

// AssessmentInput is the body of an assessment create or update.
type AssessmentInput struct {
	Name             string      `json:"name" minLength:"1"`
	DurationMinutes  int         `json:"duration_minutes" minimum:"1"`
	LanguageOverride string      `json:"language_override,omitempty"`
	InviteWindowDays int         `json:"invite_window_days" minimum:"1"`
	ProblemIDs       []uuid.UUID `json:"problem_ids" minItems:"1" doc:"In the order the candidate sees them"`
}

func assessmentView(a service.Assessment) Assessment {
	return Assessment{
		ID: a.ID, Name: a.Name, DurationMinutes: a.DurationMinutes,
		LanguageOverride: a.LanguageOverride, InviteWindowDays: a.InviteWindowDays,
		ProblemCount: a.ProblemCount, Problems: problemViews(a.Problems),
	}
}

type assessmentsHandlers struct{ d Deps }

func (m *mounter) mountAssessments() {
	h := assessmentsHandlers{d: m.d}
	register(m, accessOrg, huma.Operation{
		OperationID: "list-assessments", Method: http.MethodGet, Path: "/assessments",
		Summary: "The org's assessments", Tags: []string{"assessments"},
	}, h.list)
	register(m, accessOrg, huma.Operation{
		OperationID: "create-assessment", Method: http.MethodPost, Path: "/assessments",
		Summary: "Assemble an assessment", Tags: []string{"assessments"}, DefaultStatus: http.StatusCreated,
	}, h.create)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-assessment", Method: http.MethodGet, Path: "/assessments/{assessment_id}",
		Summary: "One assessment with its problems", Tags: []string{"assessments"},
	}, h.get)
	register(m, accessOrg, huma.Operation{
		OperationID: "update-assessment", Method: http.MethodPut, Path: "/assessments/{assessment_id}",
		Summary: "Edit an assessment", Tags: []string{"assessments"},
	}, h.update)
	register(m, accessOrg, huma.Operation{
		OperationID: "delete-assessment", Method: http.MethodDelete, Path: "/assessments/{assessment_id}",
		Summary: "Remove an unattempted assessment", Tags: []string{"assessments"}, DefaultStatus: http.StatusNoContent,
	}, h.remove)
	register(m, accessOrg, huma.Operation{
		OperationID: "attach-stage-assessment", Method: http.MethodPut, Path: "/jobs/{job_id}/stages/{stage_id}/assessment",
		Summary: "Give an assessment stage its assessment", Tags: []string{"assessments"}, DefaultStatus: http.StatusNoContent,
	}, h.attach)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-stage-assessment", Method: http.MethodGet, Path: "/stages/{stage_id}/assessment",
		Summary: "The assessment a stage hands out", Tags: []string{"assessments"},
	}, h.stageAssessment)
}

type assessmentsOutput struct {
	Body struct {
		Assessments []Assessment `json:"assessments"`
	}
}

type assessmentOutput struct{ Body Assessment }

type assessmentInput struct {
	AssessmentID uuid.UUID `path:"assessment_id"`
}

type createAssessmentInput struct{ Body AssessmentInput }

type updateAssessmentInput struct {
	AssessmentID uuid.UUID `path:"assessment_id"`
	Body         AssessmentInput
}

type attachAssessmentInput struct {
	JobID   uuid.UUID `path:"job_id"`
	StageID uuid.UUID `path:"stage_id"`
	Body    struct {
		AssessmentID uuid.UUID `json:"assessment_id"`
	}
}

type stageAssessmentInput struct {
	StageID uuid.UUID `path:"stage_id"`
}

func (in AssessmentInput) service() service.AssessmentInput {
	return service.AssessmentInput{
		Name: in.Name, DurationMinutes: in.DurationMinutes,
		LanguageOverride: in.LanguageOverride, InviteWindowDays: in.InviteWindowDays,
		ProblemIDs: in.ProblemIDs,
	}
}

func (h assessmentsHandlers) list(ctx context.Context, _ *struct{}) (*assessmentsOutput, error) {
	as, err := h.d.Assessments.List(ctx, principal(ctx))
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &assessmentsOutput{}
	out.Body.Assessments = make([]Assessment, 0, len(as))
	for _, a := range as {
		out.Body.Assessments = append(out.Body.Assessments, assessmentView(a))
	}
	return out, nil
}

func (h assessmentsHandlers) get(ctx context.Context, in *assessmentInput) (*assessmentOutput, error) {
	a, err := h.d.Assessments.Get(ctx, principal(ctx), in.AssessmentID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &assessmentOutput{Body: assessmentView(a)}, nil
}

func (h assessmentsHandlers) create(ctx context.Context, in *createAssessmentInput) (*assessmentOutput, error) {
	a, err := h.d.Assessments.Create(ctx, principal(ctx), in.Body.service())
	if err != nil {
		return nil, problemDetail(err)
	}
	return &assessmentOutput{Body: assessmentView(a)}, nil
}

func (h assessmentsHandlers) update(ctx context.Context, in *updateAssessmentInput) (*assessmentOutput, error) {
	a, err := h.d.Assessments.Update(ctx, principal(ctx), in.AssessmentID, in.Body.service())
	if err != nil {
		return nil, problemDetail(err)
	}
	return &assessmentOutput{Body: assessmentView(a)}, nil
}

func (h assessmentsHandlers) remove(ctx context.Context, in *assessmentInput) (*struct{}, error) {
	if err := h.d.Assessments.Delete(ctx, principal(ctx), in.AssessmentID); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

func (h assessmentsHandlers) attach(ctx context.Context, in *attachAssessmentInput) (*struct{}, error) {
	if err := h.d.Assessments.AttachToStage(ctx, principal(ctx), in.JobID, in.StageID, in.Body.AssessmentID); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

func (h assessmentsHandlers) stageAssessment(ctx context.Context, in *stageAssessmentInput) (*assessmentOutput, error) {
	a, err := h.d.Assessments.StageAssessment(ctx, principal(ctx), in.StageID)
	if err != nil {
		return nil, problemDetail(err)
	}
	if a == nil {
		return nil, problemDetail(service.ErrNotFound)
	}
	return &assessmentOutput{Body: assessmentView(*a)}, nil
}
