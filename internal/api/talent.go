package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/service"
)

// TalentRequest is what a company is looking for, outside any job it has
// opened, and how far the introductions against it have got.
type TalentRequest struct {
	ID              uuid.UUID  `json:"id"`
	ClientCompanyID uuid.UUID  `json:"client_company_id"`
	CompanyName     string     `json:"company_name"`
	JobID           *uuid.UUID `json:"job_id,omitempty" doc:"The job an accepted introduction opens an application on"`
	JobTitle        string     `json:"job_title,omitempty"`
	Title           string     `json:"title"`
	Skills          []string   `json:"skills"`
	Seniority       string     `json:"seniority,omitempty"`
	Location        string     `json:"location,omitempty"`
	RemotePolicy    string     `json:"remote_policy,omitempty"`
	Note            string     `json:"note,omitempty"`
	Status          string     `json:"status" doc:"open or closed"`
	IntroCount      int        `json:"intro_count" doc:"Introductions asked for so far"`
	WaitingCount    int        `json:"waiting_count" doc:"Of those, not yet sent to the person by a recruiter"`
	CreatedAt       time.Time  `json:"created_at"`
}

// TalentBreakdown is the arithmetic behind a match's score.
type TalentBreakdown struct {
	Skills    float64 `json:"skills"`
	Seniority float64 `json:"seniority"`
	Location  float64 `json:"location"`
	Text      float64 `json:"text" doc:"How well the résumé answers the request's terms, relative to the best match"`
}

// TalentMatch is one person the network holds for a request, anonymised:
// what they do and want, never who they are. The id is the handle an
// introduction is requested by.
type TalentMatch struct {
	ID            uuid.UUID       `json:"id"`
	Source        string          `json:"source" doc:"network: joined the talent network; pool: a past applicant the org rates highly"`
	Score         float64         `json:"score" doc:"0–1"`
	Breakdown     TalentBreakdown `json:"breakdown"`
	SharedSkills  []string        `json:"shared_skills" doc:"The request's skills this person lists"`
	Skills        []string        `json:"skills"`
	Seniority     string          `json:"seniority,omitempty"`
	Location      string          `json:"location,omitempty"`
	RemotePolicy  string          `json:"remote_policy,omitempty" doc:"What the person wants: remote, hybrid, or onsite"`
	Headline      string          `json:"headline,omitempty"`
	AvailableFrom *time.Time      `json:"available_from,omitempty"`
	HasResume     bool            `json:"has_resume"`
	IntroStatus   string          `json:"intro_status,omitempty" doc:"Set once the company has asked to meet this person"`
}

// TalentIntro is one introduction as the company reads it. The person is
// unnamed until they accept; then the application is theirs to read.
type TalentIntro struct {
	ID            uuid.UUID  `json:"id"`
	Label         string     `json:"label" doc:"An anonymous label until accepted, then the candidate's name"`
	Source        string     `json:"source"`
	Score         float64    `json:"score"`
	Status        string     `json:"status" doc:"requested, sent, accepted, declined, or dismissed"`
	ApplicationID *uuid.UUID `json:"application_id,omitempty" doc:"Set once accepted: the released application to read"`
	RequestedAt   time.Time  `json:"requested_at"`
	SentAt        *time.Time `json:"sent_at,omitempty"`
	AnsweredAt    *time.Time `json:"answered_at,omitempty"`
}

// OrgTalentIntro is an introduction as the recruiter works it: named.
type OrgTalentIntro struct {
	ID             uuid.UUID  `json:"id"`
	RequestID      uuid.UUID  `json:"request_id"`
	CandidateID    uuid.UUID  `json:"candidate_id"`
	CandidateName  string     `json:"candidate_name"`
	CandidateEmail string     `json:"candidate_email"`
	Source         string     `json:"source"`
	Score          float64    `json:"score"`
	Status         string     `json:"status"`
	JobID          *uuid.UUID `json:"job_id,omitempty"`
	ApplicationID  *uuid.UUID `json:"application_id,omitempty"`
	RequestedAt    time.Time  `json:"requested_at"`
	SentAt         *time.Time `json:"sent_at,omitempty"`
	AnsweredAt     *time.Time `json:"answered_at,omitempty"`
}

// TalentProfile is one member of the network as the org reads them.
type TalentProfile struct {
	ID            uuid.UUID  `json:"id"`
	CandidateID   uuid.UUID  `json:"candidate_id"`
	Name          string     `json:"name"`
	Email         string     `json:"email"`
	Headline      string     `json:"headline,omitempty"`
	Skills        []string   `json:"skills"`
	Seniority     string     `json:"seniority,omitempty"`
	Roles         []string   `json:"roles"`
	Location      string     `json:"location,omitempty"`
	RemotePolicy  string     `json:"remote_policy,omitempty"`
	SalaryMin     int        `json:"salary_min,omitempty"`
	AvailableFrom *time.Time `json:"available_from,omitempty"`
	ConsentAt     time.Time  `json:"consent_at"`
	Withdrawn     bool       `json:"withdrawn"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func talentRequestView(r service.TalentRequest) TalentRequest {
	out := TalentRequest{
		ID: r.ID, ClientCompanyID: r.ClientCompanyID, CompanyName: r.ClientCompanyName, JobTitle: r.JobTitle,
		Title: r.Title, Skills: list(r.Skills), Seniority: r.Seniority, Location: r.Location, RemotePolicy: r.RemotePolicy,
		Note: r.Note, Status: r.Status, IntroCount: r.IntroCount, WaitingCount: r.WaitingCount, CreatedAt: r.CreatedAt.UTC(),
	}
	out.JobID = optionalID(r.JobID)
	return out
}

func talentMatchView(m service.TalentMatch) TalentMatch {
	return TalentMatch{
		ID: m.ID, Source: m.Source, Score: m.Score,
		Breakdown:    TalentBreakdown{Skills: m.Breakdown.Skills, Seniority: m.Breakdown.Seniority, Location: m.Breakdown.Location, Text: m.Breakdown.Text},
		SharedSkills: list(m.SharedSkills), Skills: list(m.Skills), Seniority: m.Seniority, Location: m.Location,
		RemotePolicy: m.RemotePolicy, Headline: m.Headline, AvailableFrom: m.AvailableFrom, HasResume: m.HasResume,
		IntroStatus: m.IntroStatus,
	}
}

func talentIntroView(i service.ClientIntro) TalentIntro {
	return TalentIntro{
		ID: i.ID, Label: i.Label, Source: i.Source, Score: i.Score, Status: i.Status,
		ApplicationID: optionalID(i.ApplicationID), RequestedAt: i.RequestedAt.UTC(), SentAt: i.SentAt, AnsweredAt: i.AnsweredAt,
	}
}

func orgTalentIntroView(i service.TalentIntro) OrgTalentIntro {
	return OrgTalentIntro{
		ID: i.ID, RequestID: i.RequestID, CandidateID: i.CandidateID, CandidateName: i.CandidateName, CandidateEmail: i.CandidateEmail,
		Source: i.Source, Score: i.Score, Status: i.Status, JobID: optionalID(i.JobID), ApplicationID: optionalID(i.ApplicationID),
		RequestedAt: i.RequestedAt.UTC(), SentAt: i.SentAt, AnsweredAt: i.AnsweredAt,
	}
}

func talentProfileView(p service.TalentProfile) TalentProfile {
	return TalentProfile{
		ID: p.ID, CandidateID: p.CandidateID, Name: p.Name, Email: p.Email, Headline: p.Headline, Skills: list(p.Skills),
		Seniority: p.Seniority, Roles: list(p.Roles), Location: p.Location, RemotePolicy: p.RemotePolicy, SalaryMin: p.SalaryMin,
		AvailableFrom: p.AvailableFrom, ConsentAt: p.ConsentAt.UTC(), Withdrawn: p.Withdrawn, UpdatedAt: p.UpdatedAt.UTC(),
	}
}

// optionalID is a uuid the encoder omits when unset.
func optionalID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

type talentHandlers struct{ d Deps }

// mountTalent registers the talent network: the company's requests and
// anonymised matches under /portal, and the recruiter's side under
// /talent-requests and /talent-profiles.
func (m *mounter) mountTalent() {
	h := talentHandlers{d: m.d}
	register(m, accessPortal, huma.Operation{
		OperationID: "list-portal-talent-requests", Method: http.MethodGet, Path: "/portal/talent-requests",
		Summary: "The company's talent requests", Tags: []string{"talent-network"},
		Description: "Everything the company has asked the org's talent network for, newest first.",
	}, h.requests)
	register(m, accessPortal, huma.Operation{
		OperationID: "create-portal-talent-request", Method: http.MethodPost, Path: "/portal/talent-requests",
		Summary: "Describe who the company is looking for", Tags: []string{"talent-network"}, DefaultStatus: http.StatusCreated,
		Description: "Files a request: a title, the skills that matter, and optionally seniority, location, remote " +
			"policy, a note for the recruiter, and the job an accepted introduction should open an application on. " +
			"Matches are ranked on the fly by `list-portal-talent-matches`.",
	}, h.createRequest)
	register(m, accessPortal, huma.Operation{
		OperationID: "get-portal-talent-request", Method: http.MethodGet, Path: "/portal/talent-requests/{request_id}",
		Summary: "One talent request with its introductions", Tags: []string{"talent-network"},
		Description: "The request and every introduction asked for on it. A person stays an anonymous label " +
			"until they accept; then `application_id` names the released application to read.",
	}, h.request)
	register(m, accessPortal, huma.Operation{
		OperationID: "close-portal-talent-request", Method: http.MethodPost, Path: "/portal/talent-requests/{request_id}/close",
		Summary: "Close a talent request", Tags: []string{"talent-network"}, DefaultStatus: http.StatusNoContent,
		Description: "Stops the request taking new introductions; those already asked for run their course.",
	}, h.closeRequest)
	register(m, accessPortal, huma.Operation{
		OperationID: "list-portal-talent-matches", Method: http.MethodGet, Path: "/portal/talent-requests/{request_id}/matches",
		Summary: "Anonymised people who fit the request", Tags: []string{"talent-network"},
		Description: "The org's talent network and pool ranked against the request: skills in common, seniority, " +
			"location and remote fit, and how well the résumé answers the request's terms. No name, contact, " +
			"or résumé is returned; ask for an introduction and the recruiter approaches the person, who decides. " +
			"People already in the company's pipeline are left out.",
	}, h.matches)
	register(m, accessPortal, huma.Operation{
		OperationID: "introduce-portal-talent-match", Method: http.MethodPost, Path: "/portal/talent-requests/{request_id}/matches/{match_id}/introduce",
		Summary: "Ask to be introduced to a match", Tags: []string{"talent-network"}, DefaultStatus: http.StatusCreated,
		Description: "Records the request. A recruiter sends the person the opportunity; if they accept, an " +
			"application opens on the request's job, released to the company, and shows up in the change feed. " +
			"409 when an introduction to this person was already asked for.",
	}, h.introduce)

	register(m, accessOrg, huma.Operation{
		OperationID: "list-talent-requests", Method: http.MethodGet, Path: "/talent-requests",
		Summary: "Every company's talent requests", Tags: []string{"talent-network"},
		Description: "The recruiter's list, open requests first, with how many introductions wait to be sent.",
	}, h.allRequests)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-talent-request", Method: http.MethodGet, Path: "/talent-requests/{request_id}",
		Summary: "One talent request with its named introductions", Tags: []string{"talent-network"},
	}, h.recruiterRequest)
	register(m, accessOrg, huma.Operation{
		OperationID: "send-talent-opportunity", Method: http.MethodPost, Path: "/talent-requests/{request_id}/introductions/{intro_id}/send",
		Summary: "Send the person the opportunity", Tags: []string{"talent-network"},
		Description: "Emails the candidate about the role with a one-click answer. `job_id` picks one of the " +
			"company's open jobs; the request's own job is the default.",
	}, h.send)
	register(m, accessOrg, huma.Operation{
		OperationID: "dismiss-talent-introduction", Method: http.MethodPost, Path: "/talent-requests/{request_id}/introductions/{intro_id}/dismiss",
		Summary: "Decline to make an introduction", Tags: []string{"talent-network"}, DefaultStatus: http.StatusNoContent,
	}, h.dismiss)
	register(m, accessOrg, huma.Operation{
		OperationID: "list-talent-profiles", Method: http.MethodGet, Path: "/talent-profiles",
		Summary: "Members of the org's talent network", Tags: []string{"talent-network"},
		Description: "Everyone who joined and has not withdrawn, newest first; `q` searches names, headlines, skills, roles, and résumé text.",
	}, h.profiles)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-talent-profile", Method: http.MethodGet, Path: "/talent-profiles/{profile_id}",
		Summary: "One member of the talent network", Tags: []string{"talent-network"},
	}, h.profile)
}

type talentRequestsOutput struct {
	Body struct {
		Requests []TalentRequest `json:"requests"`
	}
}

type createTalentRequestInput struct {
	Body struct {
		Title        string    `json:"title" minLength:"1"`
		Skills       []string  `json:"skills" minItems:"1"`
		Seniority    string    `json:"seniority,omitempty" enum:"junior,mid,senior,staff,"`
		Location     string    `json:"location,omitempty"`
		RemotePolicy string    `json:"remote_policy,omitempty" enum:"remote,hybrid,onsite,"`
		Note         string    `json:"note,omitempty" maxLength:"4000"`
		JobID        uuid.UUID `json:"job_id,omitempty" doc:"One of the company's open jobs"`
	}
}

type talentRequestInput struct {
	RequestID uuid.UUID `path:"request_id"`
}

type talentRequestOutput struct {
	Body struct {
		Request       TalentRequest `json:"request"`
		Introductions []TalentIntro `json:"introductions"`
	}
}

type talentMatchesOutput struct {
	Body struct {
		Matches []TalentMatch `json:"matches"`
	}
}

type talentIntroduceInput struct {
	RequestID uuid.UUID `path:"request_id"`
	MatchID   uuid.UUID `path:"match_id"`
}

type talentIntroOutput struct{ Body TalentIntro }

type recruiterTalentRequestOutput struct {
	Body struct {
		Request       TalentRequest    `json:"request"`
		Introductions []OrgTalentIntro `json:"introductions"`
		Jobs          []Job            `json:"jobs" doc:"The company's open jobs an opportunity may be sent for"`
	}
}

type talentSendInput struct {
	RequestID uuid.UUID `path:"request_id"`
	IntroID   uuid.UUID `path:"intro_id"`
	Body      struct {
		JobID uuid.UUID `json:"job_id,omitempty"`
	}
}

type talentIntroPathInput struct {
	RequestID uuid.UUID `path:"request_id"`
	IntroID   uuid.UUID `path:"intro_id"`
}

type orgTalentIntroOutput struct{ Body OrgTalentIntro }

type listTalentProfilesInput struct {
	Query     string `query:"q"`
	Withdrawn bool   `query:"withdrawn" doc:"Include members who withdrew"`
}

type talentProfilesOutput struct {
	Body struct {
		Profiles []TalentProfile `json:"profiles"`
	}
}

type talentProfileInput struct {
	ProfileID uuid.UUID `path:"profile_id"`
}

type talentProfileOutput struct{ Body TalentProfile }

func (h talentHandlers) requests(ctx context.Context, _ *struct{}) (*talentRequestsOutput, error) {
	reqs, err := h.d.Talent.Requests(ctx, principal(ctx))
	if err != nil {
		return nil, problemDetail(err)
	}
	return requestsOutput(reqs), nil
}

func requestsOutput(reqs []service.TalentRequest) *talentRequestsOutput {
	out := &talentRequestsOutput{}
	out.Body.Requests = make([]TalentRequest, 0, len(reqs))
	for _, r := range reqs {
		out.Body.Requests = append(out.Body.Requests, talentRequestView(r))
	}
	return out
}

func (h talentHandlers) createRequest(ctx context.Context, in *createTalentRequestInput) (*talentRequestOutput, error) {
	req, err := h.d.Talent.CreateRequest(ctx, principal(ctx), service.TalentRequestInput{
		Title: in.Body.Title, Skills: in.Body.Skills, Seniority: in.Body.Seniority, Location: in.Body.Location,
		RemotePolicy: in.Body.RemotePolicy, Note: in.Body.Note, JobID: in.Body.JobID,
	})
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &talentRequestOutput{}
	out.Body.Request, out.Body.Introductions = talentRequestView(req), []TalentIntro{}
	return out, nil
}

func (h talentHandlers) request(ctx context.Context, in *talentRequestInput) (*talentRequestOutput, error) {
	p := principal(ctx)
	req, err := h.d.Talent.Request(ctx, p, in.RequestID)
	if err != nil {
		return nil, problemDetail(err)
	}
	intros, err := h.d.Talent.Intros(ctx, p, in.RequestID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &talentRequestOutput{}
	out.Body.Request = talentRequestView(req)
	out.Body.Introductions = make([]TalentIntro, 0, len(intros))
	for _, i := range intros {
		out.Body.Introductions = append(out.Body.Introductions, talentIntroView(i))
	}
	return out, nil
}

func (h talentHandlers) closeRequest(ctx context.Context, in *talentRequestInput) (*struct{}, error) {
	if err := h.d.Talent.CloseRequest(ctx, principal(ctx), in.RequestID); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

func (h talentHandlers) matches(ctx context.Context, in *talentRequestInput) (*talentMatchesOutput, error) {
	matches, err := h.d.Talent.Matches(ctx, principal(ctx), in.RequestID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &talentMatchesOutput{}
	out.Body.Matches = make([]TalentMatch, 0, len(matches))
	for _, m := range matches {
		out.Body.Matches = append(out.Body.Matches, talentMatchView(m))
	}
	return out, nil
}

func (h talentHandlers) introduce(ctx context.Context, in *talentIntroduceInput) (*talentIntroOutput, error) {
	intro, err := h.d.Talent.Introduce(ctx, principal(ctx), in.RequestID, in.MatchID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &talentIntroOutput{Body: talentIntroView(intro)}, nil
}

func (h talentHandlers) allRequests(ctx context.Context, _ *struct{}) (*talentRequestsOutput, error) {
	reqs, err := h.d.Talent.AllRequests(ctx, principal(ctx))
	if err != nil {
		return nil, problemDetail(err)
	}
	return requestsOutput(reqs), nil
}

func (h talentHandlers) recruiterRequest(ctx context.Context, in *talentRequestInput) (*recruiterTalentRequestOutput, error) {
	d, err := h.d.Talent.RequestForRecruiter(ctx, principal(ctx), in.RequestID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &recruiterTalentRequestOutput{}
	out.Body.Request = talentRequestView(d.Request)
	out.Body.Introductions = make([]OrgTalentIntro, 0, len(d.Intros))
	for _, i := range d.Intros {
		out.Body.Introductions = append(out.Body.Introductions, orgTalentIntroView(i))
	}
	out.Body.Jobs = make([]Job, 0, len(d.Jobs))
	for _, j := range d.Jobs {
		out.Body.Jobs = append(out.Body.Jobs, jobView(j))
	}
	return out, nil
}

func (h talentHandlers) send(ctx context.Context, in *talentSendInput) (*orgTalentIntroOutput, error) {
	intro, err := h.d.Talent.SendOpportunity(ctx, principal(ctx), in.IntroID, in.Body.JobID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &orgTalentIntroOutput{Body: orgTalentIntroView(intro)}, nil
}

func (h talentHandlers) dismiss(ctx context.Context, in *talentIntroPathInput) (*struct{}, error) {
	if err := h.d.Talent.DismissIntro(ctx, principal(ctx), in.IntroID); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

func (h talentHandlers) profiles(ctx context.Context, in *listTalentProfilesInput) (*talentProfilesOutput, error) {
	profiles, err := h.d.Talent.Profiles(ctx, principal(ctx), in.Query, in.Withdrawn)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &talentProfilesOutput{}
	out.Body.Profiles = make([]TalentProfile, 0, len(profiles))
	for _, p := range profiles {
		out.Body.Profiles = append(out.Body.Profiles, talentProfileView(p))
	}
	return out, nil
}

func (h talentHandlers) profile(ctx context.Context, in *talentProfileInput) (*talentProfileOutput, error) {
	p, err := h.d.Talent.ProfileByID(ctx, principal(ctx), in.ProfileID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &talentProfileOutput{Body: talentProfileView(p)}, nil
}
