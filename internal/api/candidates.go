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

// Candidate is one person in the org's records.
type Candidate struct {
	ID               uuid.UUID `json:"id"`
	Email            string    `json:"email"`
	Name             string    `json:"name"`
	Phone            string    `json:"phone,omitempty"`
	Links            []string  `json:"links"`
	ApplicationCount int       `json:"application_count"`
}

// CandidateApplication is one of a candidate's applications, as the detail
// screen lists them.
type CandidateApplication struct {
	ID        uuid.UUID `json:"id"`
	JobID     uuid.UUID `json:"job_id"`
	JobTitle  string    `json:"job_title"`
	JobSlug   string    `json:"job_slug"`
	StageName string    `json:"stage_name"`
	Status    string    `json:"status"`
}

// Resume is one uploaded file on a candidate's record.
type Resume struct {
	ID          uuid.UUID `json:"id"`
	CandidateID uuid.UUID `json:"candidate_id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	TextStatus  string    `json:"text_status"`
	CreatedAt   time.Time `json:"created_at"`
}

// ResumeUpload is a file sent inline with the candidate that owns it.
type ResumeUpload struct {
	Filename string `json:"filename" minLength:"1"`
	Data     []byte `json:"data" format:"byte" doc:"The file itself, base64 encoded"`
}

func candidateView(c service.Candidate) Candidate {
	return Candidate{ID: c.ID, Email: c.Email, Name: c.Name, Phone: c.Phone, Links: list(c.Links), ApplicationCount: c.ApplicationCount}
}

func resumeViews(in []service.Resume) []Resume {
	out := make([]Resume, 0, len(in))
	for _, r := range in {
		out = append(out, Resume{
			ID: r.ID, CandidateID: r.CandidateID, Filename: r.Filename,
			ContentType: r.ContentType, SizeBytes: r.SizeBytes,
			TextStatus: r.TextStatus, CreatedAt: r.CreatedAt,
		})
	}
	return out
}

type candidatesHandlers struct{ d Deps }

func (m *mounter) mountCandidates() {
	h := candidatesHandlers{d: m.d}
	register(m, accessOrg, huma.Operation{
		OperationID: "list-candidates", Method: http.MethodGet, Path: "/candidates",
		Summary: "Search the org's candidates", Tags: []string{"candidates"},
	}, h.list)
	register(m, accessOrg, huma.Operation{
		OperationID: "create-candidate", Method: http.MethodPost, Path: "/candidates",
		Summary: "Add a candidate, optionally onto a job and with a resume",
		Tags:    []string{"candidates"}, DefaultStatus: http.StatusCreated,
		// The resume rides in the body base64 encoded, which costs a third
		// again in size; the slack covers the rest of the JSON.
		MaxBodyBytes: domain.MaxResumeBytes*4/3 + 64<<10,
	}, h.create)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-candidate", Method: http.MethodGet, Path: "/candidates/{candidate_id}",
		Summary: "One candidate with their applications and resumes", Tags: []string{"candidates"},
	}, h.get)
	register(m, accessOrg, huma.Operation{
		OperationID: "list-resumes", Method: http.MethodGet, Path: "/candidates/{candidate_id}/resumes",
		Summary: "Resumes on a candidate's record", Tags: []string{"resumes"},
	}, h.resumes)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-resume", Method: http.MethodGet, Path: "/candidates/{candidate_id}/resumes/{resume_id}",
		Summary: "A signed, short-lived download URL for one resume", Tags: []string{"resumes"},
	}, h.resumeURL)
}

type listCandidatesInput struct {
	Query string `query:"q" doc:"Matched against name, email, and resume text"`
}

type candidatesOutput struct {
	Body struct {
		Candidates []Candidate `json:"candidates"`
	}
}

type candidateOutput struct{ Body Candidate }

type candidateInput struct {
	CandidateID uuid.UUID `path:"candidate_id"`
}

type candidateDetailOutput struct {
	Body struct {
		Candidate    Candidate              `json:"candidate"`
		Applications []CandidateApplication `json:"applications"`
		Resumes      []Resume               `json:"resumes"`
	}
}

type createCandidateInput struct {
	Body struct {
		Name   string        `json:"name" minLength:"1"`
		Email  string        `json:"email" minLength:"3"`
		Phone  string        `json:"phone,omitempty"`
		Links  []string      `json:"links,omitempty"`
		JobID  uuid.UUID     `json:"job_id,omitempty" doc:"Opens an application on this job"`
		Resume *ResumeUpload `json:"resume,omitempty"`
	}
}

type resumesOutput struct {
	Body struct {
		Resumes []Resume `json:"resumes"`
	}
}

type resumeInput struct {
	CandidateID uuid.UUID `path:"candidate_id"`
	ResumeID    uuid.UUID `path:"resume_id"`
}

type resumeURLOutput struct {
	Body struct {
		URL       string `json:"url"`
		ExpiresIn int    `json:"expires_in" doc:"Seconds the URL stays signed for"`
	}
}

func (h candidatesHandlers) list(ctx context.Context, in *listCandidatesInput) (*candidatesOutput, error) {
	cands, err := h.d.Candidates.Search(ctx, principal(ctx), in.Query)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &candidatesOutput{}
	out.Body.Candidates = make([]Candidate, 0, len(cands))
	for _, c := range cands {
		out.Body.Candidates = append(out.Body.Candidates, candidateView(c))
	}
	return out, nil
}

func (h candidatesHandlers) get(ctx context.Context, in *candidateInput) (*candidateDetailOutput, error) {
	d, err := h.d.Candidates.Detail(ctx, principal(ctx), in.CandidateID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &candidateDetailOutput{}
	out.Body.Candidate = candidateView(d.Candidate)
	out.Body.Applications = make([]CandidateApplication, 0, len(d.Applications))
	for _, a := range d.Applications {
		out.Body.Applications = append(out.Body.Applications, CandidateApplication{
			ID: a.ID, JobID: a.JobID, JobTitle: a.JobTitle, JobSlug: a.JobSlug,
			StageName: a.StageName, Status: a.Status,
		})
	}
	out.Body.Resumes = resumeViews(d.Resumes)
	return out, nil
}

func (h candidatesHandlers) create(ctx context.Context, in *createCandidateInput) (*candidateOutput, error) {
	nc := service.NewCandidate{
		Name: in.Body.Name, Email: in.Body.Email, Phone: in.Body.Phone,
		Links: in.Body.Links, JobID: in.Body.JobID,
	}
	if in.Body.Resume != nil {
		nc.Resume = &service.ResumeUpload{Filename: in.Body.Resume.Filename, Data: in.Body.Resume.Data}
	}
	c, err := h.d.Candidates.Add(ctx, principal(ctx), nc)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &candidateOutput{Body: candidateView(c)}, nil
}

func (h candidatesHandlers) resumes(ctx context.Context, in *candidateInput) (*resumesOutput, error) {
	d, err := h.d.Candidates.Detail(ctx, principal(ctx), in.CandidateID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &resumesOutput{}
	out.Body.Resumes = resumeViews(d.Resumes)
	return out, nil
}

func (h candidatesHandlers) resumeURL(ctx context.Context, in *resumeInput) (*resumeURLOutput, error) {
	url, err := h.d.Candidates.ResumeURL(ctx, principal(ctx), in.CandidateID, in.ResumeID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &resumeURLOutput{}
	out.Body.URL, out.Body.ExpiresIn = url, int(service.ResumeURLTTL.Seconds())
	return out, nil
}
