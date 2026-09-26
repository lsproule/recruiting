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

// mountPortal registers the company surface: everything a client company's
// credential can reach. It mirrors the client portal's own HTML pages — the
// company's jobs, the applications released to it, the recruiter's
// shortlist packets, the three actions a client may take — plus what an
// integration needs and a page does not: a company-wide application list
// with paging, a change feed to poll, and the company's own API tokens.
// Nothing here reaches an unreleased application, a blind candidate's
// identity, an interviewer's notes, or an integrity signal.
func (m *mounter) mountPortal() {
	h := portalHandlers{d: m.d}
	register(m, accessPortal, huma.Operation{
		OperationID: "get-portal-me", Method: http.MethodGet, Path: "/portal/me",
		Summary: "The signed-in client user", Tags: []string{"client-portal"},
		Description: "Who the credential acts as: the client user and the company they belong to. " +
			"Every other company operation is scoped to that company.",
	}, h.me)
	register(m, accessPortal, huma.Operation{
		OperationID: "list-portal-jobs", Method: http.MethodGet, Path: "/portal/jobs",
		Summary: "The client company's jobs", Tags: []string{"client-portal"},
		Description: "Every job the recruiter has opened for the company, newest first, with how many " +
			"applications have been released on each. Drafts the recruiter has not announced are not listed.",
	}, h.jobs)
	register(m, accessPortal, huma.Operation{
		OperationID: "get-portal-job", Method: http.MethodGet, Path: "/portal/jobs/{job_id}",
		Summary: "One job with its released applications", Tags: []string{"client-portal"},
		Description: "The job and the applications the recruiter has released to the company on it, in " +
			"pipeline order. While a blind-mode job keeps a candidate anonymous, `candidate.blind` is true " +
			"and `candidate.label` stands in for their name.",
	}, h.job)
	register(m, accessPortal, huma.Operation{
		OperationID: "get-portal-shortlist", Method: http.MethodGet, Path: "/portal/jobs/{job_id}/shortlist",
		Summary: "The shortlist packet the recruiter sent for a job", Tags: []string{"client-portal"},
		Description: "The recruiter's ranked recommendation and note. 404 until a packet has been sent.",
	}, h.shortlist)
	register(m, accessPortal, huma.Operation{
		OperationID: "list-portal-applications", Method: http.MethodGet, Path: "/portal/applications",
		Summary: "Released applications across the company's jobs", Tags: []string{"client-portal"},
		Description: "The company-wide list, most recently changed first, for syncing into another system. " +
			"Narrow it by job or status, or to rows changed since a timestamp; page with `limit` and `offset`. " +
			"For changes as they happen, poll `GET /portal/events` instead.",
	}, h.applications)
	register(m, accessPortal, huma.Operation{
		OperationID: "get-portal-application", Method: http.MethodGet, Path: "/portal/applications/{application_id}",
		Summary: "One released application", Tags: []string{"client-portal"},
		Description: "The application with the interviewers' scores (never their notes), the assessment " +
			"outcome (score and verdict, never the integrity signals), and whether a résumé is on file.",
	}, h.application)
	register(m, accessPortal, huma.Operation{
		OperationID: "get-portal-resume", Method: http.MethodGet, Path: "/portal/applications/{application_id}/resume",
		Summary: "A signed, short-lived download URL for the candidate's resume", Tags: []string{"client-portal"},
		Description: "The URL is valid for `expires_in` seconds and is fetched without credentials. " +
			"403 while the candidate is still blind; 404 when they sent no résumé.",
	}, h.resume)
	register(m, accessPortal, huma.Operation{
		OperationID: "advance-portal-application", Method: http.MethodPost, Path: "/portal/applications/{application_id}/advance",
		Summary: "Move the application to a later client-review stage", Tags: []string{"client-portal"},
		DefaultStatus: http.StatusNoContent,
		Description: "The same action as the portal's Advance button. Only a later client-review stage of " +
			"the job is allowed; `get-portal-application` lists the stages the application may advance to.",
	}, h.advance)
	register(m, accessPortal, huma.Operation{
		OperationID: "reject-portal-application", Method: http.MethodPost, Path: "/portal/applications/{application_id}/reject",
		Summary: "Reject the application", Tags: []string{"client-portal"}, DefaultStatus: http.StatusNoContent,
		Description: "Closes the application as rejected. A reason is required and reaches the recruiter, not the candidate.",
	}, h.reject)
	register(m, accessPortal, huma.Operation{
		OperationID: "request-portal-info", Method: http.MethodPost, Path: "/portal/applications/{application_id}/request-info",
		Summary: "Ask the recruiter a question about the application", Tags: []string{"client-portal"},
		DefaultStatus: http.StatusNoContent,
		Description: "Writes the question to the application's timeline and emails the org's recruiters. " +
			"It shows up in the recruiter's work queue until someone answers.",
	}, h.requestInfo)
	register(m, accessPortal, huma.Operation{
		OperationID: "list-portal-events", Method: http.MethodGet, Path: "/portal/events",
		Summary: "The company's change feed", Tags: []string{"client-portal"},
		Description: "Every change on an application released to the company, from the moment it was " +
			"released, in the order it happened. `seq` is a cursor: remember the last one you saw and pass " +
			"it back as `since` to read only what happened after it. An empty page means nothing new; " +
			"poll every minute or so. Kinds include `released`, `moved`, `withdrawn`, `client_request_info`, " +
			"and `scorecard_strong_yes`; `from_stage` and `to_stage` name the stages a move went between.",
	}, h.events)
	register(m, accessPortal, huma.Operation{
		OperationID: "list-portal-tokens", Method: http.MethodGet, Path: "/portal/tokens",
		Summary: "The signed-in client user's live API tokens", Tags: []string{"client-portal"},
		Description: "Tokens this user has issued for themselves and not revoked. The secret is never listed.",
	}, h.tokens)
	register(m, accessPortal, huma.Operation{
		OperationID: "create-portal-token", Method: http.MethodPost, Path: "/portal/tokens",
		Summary: "Issue an API token for the signed-in client user", Tags: []string{"client-portal"},
		DefaultStatus: http.StatusCreated,
		Description: "Mints a bearer token that acts as this user and reaches exactly what they can. The " +
			"secret is returned once and cannot be read back; store it. A user keeps at most ten live tokens.",
	}, h.createToken)
	register(m, accessPortal, huma.Operation{
		OperationID: "revoke-portal-token", Method: http.MethodDelete, Path: "/portal/tokens/{token_id}",
		Summary: "Revoke one of the signed-in client user's tokens", Tags: []string{"client-portal"},
		DefaultStatus: http.StatusNoContent,
		Description:   "Takes effect at once. Someone else's token, or one already revoked, is 404.",
	}, h.revokeToken)
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

// PortalEvent is one change on a released application.
type PortalEvent struct {
	Seq           int64     `json:"seq" doc:"Cursor: pass the last one seen back as since"`
	ID            uuid.UUID `json:"id"`
	ApplicationID uuid.UUID `json:"application_id"`
	JobID         uuid.UUID `json:"job_id"`
	JobTitle      string    `json:"job_title"`
	Kind          string    `json:"kind"`
	ActorKind     string    `json:"actor_kind" doc:"org_user, client_user, candidate, or system"`
	FromStage     string    `json:"from_stage,omitempty"`
	ToStage       string    `json:"to_stage,omitempty"`
	Reason        string    `json:"reason,omitempty" doc:"Only for events the company's own users wrote"`
	CreatedAt     time.Time `json:"created_at"`
}

// PortalShortlistPick is one candidate of the packet at their rank.
type PortalShortlistPick struct {
	ApplicationID uuid.UUID `json:"application_id"`
	Rank          int       `json:"rank"`
	Blind         bool      `json:"blind"`
	Label         string    `json:"label" doc:"The name, or an anonymous label while blind"`
	Score         string    `json:"score,omitempty" doc:"Best assessment score, absent when they never sat one"`
}

type listPortalApplicationsInput struct {
	JobID        uuid.UUID `query:"job_id" doc:"Only this job"`
	Status       string    `query:"status" enum:"active,hired,rejected,withdrawn," doc:"Only applications in this state"`
	UpdatedSince time.Time `query:"updated_since" doc:"Only rows changed at or after this time (RFC 3339)"`
	Limit        int       `query:"limit" minimum:"1" maximum:"200" doc:"Page size, 50 by default"`
	Offset       int       `query:"offset" minimum:"0"`
}

type portalApplicationsOutput struct {
	Body struct {
		Applications []PortalApplication `json:"applications"`
		Limit        int                 `json:"limit"`
		Offset       int                 `json:"offset"`
	}
}

type portalEventsInput struct {
	Since int64 `query:"since" minimum:"0" doc:"The last seq already seen; 0 reads from the beginning"`
	Limit int   `query:"limit" minimum:"1" maximum:"200" doc:"Page size, 50 by default"`
}

type portalEventsOutput struct {
	Body struct {
		Events []PortalEvent `json:"events"`
		// NextSince is the cursor for the next poll: the last seq returned,
		// or the one passed in when the page was empty.
		NextSince int64 `json:"next_since"`
	}
}

type portalShortlistOutput struct {
	Body struct {
		PacketID uuid.UUID             `json:"packet_id"`
		JobID    uuid.UUID             `json:"job_id"`
		JobTitle string                `json:"job_title"`
		Note     string                `json:"note,omitempty"`
		SentAt   time.Time             `json:"sent_at"`
		Picks    []PortalShortlistPick `json:"picks" doc:"In rank order"`
	}
}

type portalAdvanceInput struct {
	ApplicationID uuid.UUID `path:"application_id"`
	Body          struct {
		ToStageID uuid.UUID `json:"to_stage_id"`
	}
}

type portalReasonInput struct {
	ApplicationID uuid.UUID `path:"application_id"`
	Body          struct {
		Reason string `json:"reason" minLength:"1" maxLength:"500"`
	}
}

type portalMessageInput struct {
	ApplicationID uuid.UUID `path:"application_id"`
	Body          struct {
		Message string `json:"message" minLength:"1" maxLength:"4000"`
	}
}

type createPortalTokenInput struct {
	Body struct {
		Name      string     `json:"name" minLength:"1" doc:"What the token is for; the only way to tell tokens apart later"`
		ExpiresAt *time.Time `json:"expires_at,omitempty" doc:"Absent for a token that lives until revoked"`
	}
}

func (h portalHandlers) applications(ctx context.Context, in *listPortalApplicationsInput) (*portalApplicationsOutput, error) {
	f := service.ClientApplicationFilter{JobID: in.JobID, Status: in.Status, UpdatedSince: in.UpdatedSince, Limit: in.Limit, Offset: in.Offset}
	apps, err := h.d.Portal.Applications(ctx, principal(ctx), f)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &portalApplicationsOutput{}
	out.Body.Applications = make([]PortalApplication, 0, len(apps))
	for _, a := range apps {
		out.Body.Applications = append(out.Body.Applications, portalApplicationView(a))
	}
	out.Body.Limit, out.Body.Offset = in.Limit, in.Offset
	if out.Body.Limit < 1 || out.Body.Limit > service.ClientListMax {
		out.Body.Limit = service.ClientListDefault
	}
	return out, nil
}

func (h portalHandlers) events(ctx context.Context, in *portalEventsInput) (*portalEventsOutput, error) {
	events, err := h.d.Portal.Events(ctx, principal(ctx), in.Since, in.Limit)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &portalEventsOutput{}
	out.Body.Events = make([]PortalEvent, 0, len(events))
	out.Body.NextSince = in.Since
	for _, e := range events {
		out.Body.Events = append(out.Body.Events, PortalEvent{
			Seq: e.Seq, ID: e.ID, ApplicationID: e.ApplicationID, JobID: e.JobID, JobTitle: e.JobTitle,
			Kind: e.Kind, ActorKind: e.ActorKind, FromStage: e.FromStage, ToStage: e.ToStage, Reason: e.Reason,
			CreatedAt: e.CreatedAt.UTC(),
		})
		out.Body.NextSince = e.Seq
	}
	return out, nil
}

func (h portalHandlers) shortlist(ctx context.Context, in *jobInput) (*portalShortlistOutput, error) {
	if h.d.Shortlists == nil {
		return nil, huma.Error404NotFound("no shortlist has been sent for this job")
	}
	packet, err := h.d.Shortlists.ClientShortlist(ctx, principal(ctx), in.JobID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &portalShortlistOutput{}
	out.Body.PacketID, out.Body.JobID, out.Body.JobTitle = packet.PacketID, packet.JobID, packet.JobTitle
	out.Body.Note, out.Body.SentAt = packet.Note, packet.SentAt.UTC()
	out.Body.Picks = make([]PortalShortlistPick, 0, len(packet.Picks))
	for _, p := range packet.Picks {
		out.Body.Picks = append(out.Body.Picks, PortalShortlistPick{
			ApplicationID: p.ApplicationID, Rank: p.Rank, Blind: p.Blind, Label: p.Label, Score: p.Score,
		})
	}
	return out, nil
}

func (h portalHandlers) advance(ctx context.Context, in *portalAdvanceInput) (*struct{}, error) {
	if err := h.d.Portal.Advance(ctx, principal(ctx), in.ApplicationID, in.Body.ToStageID); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

func (h portalHandlers) reject(ctx context.Context, in *portalReasonInput) (*struct{}, error) {
	if err := h.d.Portal.Reject(ctx, principal(ctx), in.ApplicationID, in.Body.Reason); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

func (h portalHandlers) requestInfo(ctx context.Context, in *portalMessageInput) (*struct{}, error) {
	if err := h.d.Portal.RequestInfo(ctx, principal(ctx), in.ApplicationID, in.Body.Message); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

func (h portalHandlers) tokens(ctx context.Context, _ *struct{}) (*apiTokensOutput, error) {
	tokens, err := h.d.APITokens.ListOwn(ctx, principal(ctx))
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &apiTokensOutput{}
	out.Body.Tokens = make([]APIToken, 0, len(tokens))
	for _, t := range tokens {
		out.Body.Tokens = append(out.Body.Tokens, apiTokenView(t))
	}
	return out, nil
}

func (h portalHandlers) createToken(ctx context.Context, in *createPortalTokenInput) (*createAPITokenOutput, error) {
	tok, secret, err := h.d.APITokens.IssueOwn(ctx, principal(ctx), in.Body.Name, in.Body.ExpiresAt)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &createAPITokenOutput{}
	out.Body.Token, out.Body.Secret = apiTokenView(tok), secret
	return out, nil
}

func (h portalHandlers) revokeToken(ctx context.Context, in *apiTokenInput) (*struct{}, error) {
	if err := h.d.APITokens.RevokeOwn(ctx, principal(ctx), in.TokenID); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}
