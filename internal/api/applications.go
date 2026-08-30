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

// Application is one candidate's progress through one job's pipeline.
type Application struct {
	ID             uuid.UUID `json:"id"`
	JobID          uuid.UUID `json:"job_id"`
	JobTitle       string    `json:"job_title,omitempty"`
	CandidateID    uuid.UUID `json:"candidate_id"`
	CandidateName  string    `json:"candidate_name,omitempty"`
	CandidateEmail string    `json:"candidate_email,omitempty"`
	StageID        uuid.UUID `json:"stage_id"`
	StageName      string    `json:"stage_name,omitempty"`
	StagePosition  int       `json:"stage_position"`
	StageKind      string    `json:"stage_kind"`
	Status         string    `json:"status"`
	Released       bool      `json:"released"`
	ReleasedAt     time.Time `json:"released_at,omitempty"`
	HighQuality    bool      `json:"high_quality"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ApplicationEvent is one entry of the application's audit trail.
type ApplicationEvent struct {
	ID        uuid.UUID `json:"id"`
	ActorKind string    `json:"actor_kind"`
	ActorID   uuid.UUID `json:"actor_id"`
	Kind      string    `json:"kind"`
	FromStage string    `json:"from_stage,omitempty"`
	ToStage   string    `json:"to_stage,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Override  bool      `json:"override"`
	CreatedAt time.Time `json:"created_at"`
}

func applicationView(a service.Application) Application {
	return Application{
		ID: a.ID, JobID: a.JobID, JobTitle: a.JobTitle,
		CandidateID: a.CandidateID, CandidateName: a.CandidateName, CandidateEmail: a.CandidateEmail,
		StageID: a.StageID, StageName: a.StageName, StagePosition: a.StagePosition,
		StageKind: string(a.StageKind), Status: string(a.Status),
		Released: a.Released, ReleasedAt: a.ReleasedAt, HighQuality: a.HighQuality,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

func applicationEventViews(in []service.ApplicationEvent) []ApplicationEvent {
	out := make([]ApplicationEvent, 0, len(in))
	for _, e := range in {
		out = append(out, ApplicationEvent{
			ID: e.ID, ActorKind: e.ActorKind, ActorID: e.ActorID, Kind: e.Kind,
			FromStage: e.FromStage, ToStage: e.ToStage, Reason: e.Reason,
			Override: e.Override, CreatedAt: e.CreatedAt,
		})
	}
	return out
}

type applicationsHandlers struct{ d Deps }

func (m *mounter) mountApplications() {
	h := applicationsHandlers{d: m.d}
	register(m, accessOrg, huma.Operation{
		OperationID: "list-applications", Method: http.MethodGet, Path: "/jobs/{job_id}/applications",
		Summary: "Applications on a job", Tags: []string{"applications"},
	}, h.list)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-application", Method: http.MethodGet, Path: "/applications/{application_id}",
		Summary: "One application with its stages and history", Tags: []string{"applications"},
	}, h.get)
	register(m, accessOrg, huma.Operation{
		OperationID: "list-application-events", Method: http.MethodGet, Path: "/applications/{application_id}/events",
		Summary: "Audit trail of an application", Tags: []string{"applications"},
	}, h.events)
	register(m, accessOrg, huma.Operation{
		OperationID: "move-application", Method: http.MethodPost, Path: "/applications/{application_id}/move",
		Summary: "Move an application to another stage", Tags: []string{"applications"},
	}, h.move)
	register(m, accessOrg, huma.Operation{
		OperationID: "withdraw-application", Method: http.MethodPost, Path: "/applications/{application_id}/withdraw",
		Summary: "Withdraw an application", Tags: []string{"applications"},
	}, h.withdraw)
	register(m, accessOrg, huma.Operation{
		OperationID: "release-application", Method: http.MethodPost, Path: "/applications/{application_id}/release",
		Summary: "Release an application to the client portal", Tags: []string{"applications"},
	}, h.release)
	register(m, accessOrg, huma.Operation{
		OperationID: "unrelease-application", Method: http.MethodPost, Path: "/applications/{application_id}/unrelease",
		Summary: "Withdraw an application from the client portal", Tags: []string{"applications"},
	}, h.unrelease)
}

type listApplicationsInput struct {
	JobID   uuid.UUID `path:"job_id"`
	StageID uuid.UUID `query:"stage_id"`
	Status  string    `query:"status" enum:"active,hired,rejected,withdrawn"`
	Query   string    `query:"q" doc:"Matched against candidate name and email"`
}

type applicationsOutput struct {
	Body struct {
		Applications []Application `json:"applications"`
	}
}

type applicationInput struct {
	ApplicationID uuid.UUID `path:"application_id"`
}

type applicationOutput struct{ Body Application }

type applicationDetailOutput struct {
	Body struct {
		Application Application        `json:"application"`
		Stages      []Stage            `json:"stages"`
		Events      []ApplicationEvent `json:"events"`
	}
}

type applicationEventsOutput struct {
	Body struct {
		Events []ApplicationEvent `json:"events"`
	}
}

type moveApplicationInput struct {
	ApplicationID uuid.UUID `path:"application_id"`
	Body          struct {
		ToStageID      uuid.UUID `json:"to_stage_id"`
		Reason         string    `json:"reason,omitempty" doc:"Required to reject and to override"`
		OverridePrereq bool      `json:"override_prereq,omitempty"`
	}
}

type withdrawApplicationInput struct {
	ApplicationID uuid.UUID `path:"application_id"`
	Body          struct {
		Reason string `json:"reason,omitempty"`
	}
}

func (h applicationsHandlers) list(ctx context.Context, in *listApplicationsInput) (*applicationsOutput, error) {
	apps, err := h.d.Applications.List(ctx, principal(ctx), in.JobID, service.ListFilter{
		StageID: in.StageID, Status: domain.ApplicationStatus(in.Status), Query: in.Query,
	})
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &applicationsOutput{}
	out.Body.Applications = make([]Application, 0, len(apps))
	for _, a := range apps {
		out.Body.Applications = append(out.Body.Applications, applicationView(a))
	}
	return out, nil
}

func (h applicationsHandlers) get(ctx context.Context, in *applicationInput) (*applicationDetailOutput, error) {
	d, err := h.d.Applications.Detail(ctx, principal(ctx), in.ApplicationID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &applicationDetailOutput{}
	out.Body.Application = applicationView(d.Application)
	out.Body.Stages = stageViews(d.Stages)
	out.Body.Events = applicationEventViews(d.Events)
	return out, nil
}

func (h applicationsHandlers) events(ctx context.Context, in *applicationInput) (*applicationEventsOutput, error) {
	events, err := h.d.Applications.Events(ctx, principal(ctx), in.ApplicationID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &applicationEventsOutput{}
	out.Body.Events = applicationEventViews(events)
	return out, nil
}

func (h applicationsHandlers) move(ctx context.Context, in *moveApplicationInput) (*applicationOutput, error) {
	a, err := h.d.Applications.Move(ctx, principal(ctx), service.MoveRequest{
		ApplicationID: in.ApplicationID, ToStageID: in.Body.ToStageID,
		Reason: in.Body.Reason, OverridePrereq: in.Body.OverridePrereq,
	})
	if err != nil {
		return nil, problemDetail(err)
	}
	return &applicationOutput{Body: applicationView(a)}, nil
}

func (h applicationsHandlers) withdraw(ctx context.Context, in *withdrawApplicationInput) (*applicationOutput, error) {
	a, err := h.d.Applications.Withdraw(ctx, principal(ctx), in.ApplicationID, in.Body.Reason)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &applicationOutput{Body: applicationView(a)}, nil
}

func (h applicationsHandlers) release(ctx context.Context, in *applicationInput) (*applicationOutput, error) {
	a, err := h.d.Releases.Release(ctx, principal(ctx), in.ApplicationID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &applicationOutput{Body: applicationView(a)}, nil
}

func (h applicationsHandlers) unrelease(ctx context.Context, in *applicationInput) (*applicationOutput, error) {
	a, err := h.d.Releases.Unrelease(ctx, principal(ctx), in.ApplicationID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &applicationOutput{Body: applicationView(a)}, nil
}
