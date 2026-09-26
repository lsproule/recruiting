package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/service"
)

// JobPosting is one placement of a job on an external board.
type JobPosting struct {
	ID          uuid.UUID `json:"id"`
	JobID       uuid.UUID `json:"job_id"`
	Board       string    `json:"board" doc:"linkedin, indeed, glassdoor, or demo"`
	Status      string    `json:"status" enum:"queued,posting,posted,failed,removed"`
	Title       string    `json:"title"`
	Body        string    `json:"body" doc:"The plain-text ad the board shows, written from the job's own fields"`
	ApplyURL    string    `json:"apply_url" doc:"Where the board sends applicants: this job's apply page on the platform"`
	ExternalURL string    `json:"external_url,omitempty" doc:"The posting on the board, once it is live"`
	ExternalID  string    `json:"external_id,omitempty"`
	Error       string    `json:"error,omitempty" doc:"Why the last attempt failed"`
	Attempts    int       `json:"attempts"`
	PostedAt    time.Time `json:"posted_at,omitzero"`
	CreatedAt   time.Time `json:"created_at"`
}

// PostingPreview is the ad a board would carry, before it is posted.
type PostingPreview struct {
	Board string `json:"board"`
	Label string `json:"label"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

type jobPostingsHandlers struct{ d Deps }

func (m *mounter) mountJobPostings() {
	h := jobPostingsHandlers{d: m.d}
	register(m, accessOrg, huma.Operation{
		OperationID: "list-job-postings", Method: http.MethodGet, Path: "/jobs/{job_id}/postings",
		Summary: "Where the job has been posted", Tags: []string{"jobs"},
		Description: "Every placement of the job on an external board, newest first, with its status and, once live, its URL.",
	}, h.list)
	register(m, accessOrg, huma.Operation{
		OperationID: "preview-job-postings", Method: http.MethodGet, Path: "/jobs/{job_id}/postings/preview",
		Summary: "The ad each board would carry", Tags: []string{"jobs"},
		Description: "The posting copy, written from the job's own fields, for every board the platform posts to. What is previewed is what gets posted.",
	}, h.preview)
	register(m, accessOrg, huma.Operation{
		OperationID: "create-job-posting", Method: http.MethodPost, Path: "/jobs/{job_id}/postings",
		Summary: "Post the job to a board", Tags: []string{"jobs"}, DefaultStatus: http.StatusAccepted,
		Description: "Records the placement and queues the browser automation that carries it out. The posting is returned as queued; read it back to see where it landed or why it failed.",
	}, h.create)
}

type jobPostingsOutput struct {
	Body struct {
		Postings []JobPosting `json:"postings"`
	}
}

type postingPreviewsOutput struct {
	Body struct {
		Previews []PostingPreview `json:"previews"`
	}
}

type createJobPostingInput struct {
	JobID uuid.UUID `path:"job_id"`
	Body  struct {
		Board string `json:"board" enum:"linkedin,indeed,glassdoor,demo"`
	}
}

type jobPostingOutput struct{ Body JobPosting }

func (h jobPostingsHandlers) list(ctx context.Context, in *jobInput) (*jobPostingsOutput, error) {
	if h.d.Postings == nil {
		return nil, huma.Error503ServiceUnavailable("job posting is not configured")
	}
	rows, err := h.d.Postings.List(ctx, principal(ctx), in.JobID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &jobPostingsOutput{}
	out.Body.Postings = make([]JobPosting, 0, len(rows))
	for _, r := range rows {
		out.Body.Postings = append(out.Body.Postings, postingView(r))
	}
	return out, nil
}

func (h jobPostingsHandlers) preview(ctx context.Context, in *jobInput) (*postingPreviewsOutput, error) {
	if h.d.Postings == nil {
		return nil, huma.Error503ServiceUnavailable("job posting is not configured")
	}
	previews, err := h.d.Postings.Preview(ctx, principal(ctx), in.JobID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &postingPreviewsOutput{}
	out.Body.Previews = make([]PostingPreview, 0, len(previews))
	for _, pv := range previews {
		out.Body.Previews = append(out.Body.Previews, PostingPreview{Board: pv.Board.ID, Label: pv.Board.Label, Title: pv.Posting.Title, Body: pv.Posting.Body})
	}
	return out, nil
}

func (h jobPostingsHandlers) create(ctx context.Context, in *createJobPostingInput) (*jobPostingOutput, error) {
	if h.d.Postings == nil {
		return nil, huma.Error503ServiceUnavailable("job posting is not configured")
	}
	p, err := h.d.Postings.Post(ctx, principal(ctx), in.JobID, in.Body.Board)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &jobPostingOutput{Body: postingView(p)}, nil
}

func postingView(p service.JobPosting) JobPosting {
	return JobPosting{
		ID: p.ID, JobID: p.JobID, Board: p.Board, Status: p.Status, Title: p.Title, Body: p.Body, ApplyURL: p.ApplyURL,
		ExternalURL: p.ExternalURL, ExternalID: p.ExternalID, Error: p.Error, Attempts: p.Attempts,
		PostedAt: p.PostedAt, CreatedAt: p.CreatedAt,
	}
}
