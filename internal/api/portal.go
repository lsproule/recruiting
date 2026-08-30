package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/service"
)

// PortalJob is one of the client company's jobs.
type PortalJob struct {
	ID            uuid.UUID `json:"id"`
	Title         string    `json:"title"`
	Location      string    `json:"location,omitempty"`
	RemotePolicy  string    `json:"remote_policy,omitempty"`
	Seniority     string    `json:"seniority,omitempty"`
	Status        string    `json:"status"`
	BlindMode     bool      `json:"blind_mode"`
	ReleasedCount int       `json:"released_count"`
}

// PortalCandidate is the candidate as the client may see them: a label
// instead of a name while the application is still blind.
type PortalCandidate struct {
	Blind bool     `json:"blind"`
	Label string   `json:"label"`
	Email string   `json:"email,omitempty"`
	Phone string   `json:"phone,omitempty"`
	Links []string `json:"links"`
}

// PortalApplication is one released application.
type PortalApplication struct {
	ID               uuid.UUID       `json:"id"`
	JobID            uuid.UUID       `json:"job_id"`
	JobTitle         string          `json:"job_title"`
	StageID          uuid.UUID       `json:"stage_id"`
	StageName        string          `json:"stage_name"`
	Status           string          `json:"status"`
	Candidate        PortalCandidate `json:"candidate"`
	RecruiterSummary string          `json:"recruiter_summary,omitempty"`
	ReleasedAt       *time.Time      `json:"released_at,omitempty" doc:"Absent until the application is released"`
}

// PortalScore is one criterion as the client sees it. The interviewer's
// per-criterion notes are the org's, and are not part of it.
type PortalScore struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
}

// PortalScorecard is one interview scorecard, trimmed for the client.
type PortalScorecard struct {
	StageName  string        `json:"stage_name"`
	VetterName string        `json:"vetter_name"`
	Overall    string        `json:"overall"`
	Scores     []PortalScore `json:"scores"`
	FiledAt    time.Time     `json:"filed_at"`
}

func portalApplicationView(a service.ClientApplication) PortalApplication {
	return PortalApplication{
		ID: a.ID, JobID: a.JobID, JobTitle: a.JobTitle, StageID: a.StageID,
		StageName: a.StageName, Status: string(a.Status),
		Candidate: PortalCandidate{
			Blind: a.Candidate.Blind, Label: a.Candidate.Label, Email: a.Candidate.Email,
			Phone: a.Candidate.Phone, Links: list(a.Candidate.Links),
		},
		RecruiterSummary: a.RecruiterSummary, ReleasedAt: optionalTime(a.ReleasedAt),
	}
}

// optionalTime is a time that may be unset, as a pointer the encoder omits
// rather than a zero value it would print as year 1.
func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

type portalHandlers struct{ d Deps }

// mountPortal registers the client-portal reads. They are the only operations
// a client user's credential ever reaches, and they are read-only: a client
// advances or rejects through the portal's own HTML surface.
func (m *mounter) mountPortal() {
	h := portalHandlers{d: m.d}
	register(m, accessPortal, huma.Operation{
		OperationID: "get-portal-me", Method: http.MethodGet, Path: "/portal/me",
		Summary: "The signed-in client user", Tags: []string{"client-portal"},
	}, h.me)
	register(m, accessPortal, huma.Operation{
		OperationID: "list-portal-jobs", Method: http.MethodGet, Path: "/portal/jobs",
		Summary: "The client company's jobs", Tags: []string{"client-portal"},
	}, h.jobs)
	register(m, accessPortal, huma.Operation{
		OperationID: "get-portal-job", Method: http.MethodGet, Path: "/portal/jobs/{job_id}",
		Summary: "One job with its released applications", Tags: []string{"client-portal"},
	}, h.job)
	register(m, accessPortal, huma.Operation{
		OperationID: "get-portal-application", Method: http.MethodGet, Path: "/portal/applications/{application_id}",
		Summary: "One released application", Tags: []string{"client-portal"},
	}, h.application)
	register(m, accessPortal, huma.Operation{
		OperationID: "get-portal-resume", Method: http.MethodGet, Path: "/portal/applications/{application_id}/resume",
		Summary: "A signed, short-lived download URL for the candidate's resume", Tags: []string{"client-portal"},
	}, h.resume)
}

type portalMeOutput struct {
	Body struct {
		ID              uuid.UUID `json:"id"`
		ClientCompanyID uuid.UUID `json:"client_company_id"`
		CompanyName     string    `json:"company_name"`
		Email           string    `json:"email"`
		Name            string    `json:"name"`
	}
}

type portalJobsOutput struct {
	Body struct {
		Jobs []PortalJob `json:"jobs"`
	}
}

type portalJobOutput struct {
	Body struct {
		Job          PortalJob           `json:"job"`
		Applications []PortalApplication `json:"applications"`
	}
}

type portalApplicationOutput struct {
	Body struct {
		Application PortalApplication `json:"application"`
		HasResume   bool              `json:"has_resume"`
		Scorecards  []PortalScorecard `json:"scorecards"`
		Assessment  struct {
			Score   string `json:"score,omitempty"`
			Verdict string `json:"verdict,omitempty"`
		} `json:"assessment"`
	}
}

func portalJobView(j service.ClientJob) PortalJob {
	return PortalJob{
		ID: j.ID, Title: j.Title, Location: j.Location, RemotePolicy: j.RemotePolicy,
		Seniority: j.Seniority, Status: j.Status, BlindMode: j.BlindMode, ReleasedCount: j.ReleasedCount,
	}
}

func (h portalHandlers) me(ctx context.Context, _ *struct{}) (*portalMeOutput, error) {
	u, err := h.d.Portal.Me(ctx, principal(ctx))
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &portalMeOutput{}
	out.Body.ID, out.Body.ClientCompanyID = u.ID, u.ClientCompanyID
	out.Body.CompanyName, out.Body.Email, out.Body.Name = u.CompanyName, u.Email, u.Name
	return out, nil
}

func (h portalHandlers) jobs(ctx context.Context, _ *struct{}) (*portalJobsOutput, error) {
	js, err := h.d.Portal.Jobs(ctx, principal(ctx))
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &portalJobsOutput{}
	out.Body.Jobs = make([]PortalJob, 0, len(js))
	for _, j := range js {
		out.Body.Jobs = append(out.Body.Jobs, portalJobView(j))
	}
	return out, nil
}

func (h portalHandlers) job(ctx context.Context, in *jobInput) (*portalJobOutput, error) {
	j, apps, err := h.d.Portal.Job(ctx, principal(ctx), in.JobID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &portalJobOutput{}
	out.Body.Job = portalJobView(j)
	out.Body.Applications = make([]PortalApplication, 0, len(apps))
	for _, a := range apps {
		out.Body.Applications = append(out.Body.Applications, portalApplicationView(a))
	}
	return out, nil
}

func (h portalHandlers) application(ctx context.Context, in *applicationInput) (*portalApplicationOutput, error) {
	d, err := h.d.Portal.Application(ctx, principal(ctx), in.ApplicationID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &portalApplicationOutput{}
	out.Body.Application = portalApplicationView(d.Application)
	out.Body.HasResume = d.HasResume
	out.Body.Assessment.Score, out.Body.Assessment.Verdict = d.Assessment.Score, d.Assessment.Verdict
	out.Body.Scorecards = make([]PortalScorecard, 0, len(d.Scorecards))
	for _, c := range d.Scorecards {
		card := PortalScorecard{StageName: c.StageName, VetterName: c.VetterName, Overall: c.Overall, FiledAt: c.FiledAt}
		card.Scores = make([]PortalScore, 0, len(c.Scores))
		for _, s := range c.Scores {
			card.Scores = append(card.Scores, PortalScore{Name: s.Name, Score: s.Score})
		}
		out.Body.Scorecards = append(out.Body.Scorecards, card)
	}
	return out, nil
}

func (h portalHandlers) resume(ctx context.Context, in *applicationInput) (*resumeURLOutput, error) {
	url, err := h.d.Portal.ResumeURL(ctx, principal(ctx), in.ApplicationID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &resumeURLOutput{}
	out.Body.URL, out.Body.ExpiresIn = url, int(service.ResumeURLTTL.Seconds())
	return out, nil
}
