package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/domain"
	"recruiting/internal/queue"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// Job posting statuses; they mirror the job_posting.status check.
const (
	PostingQueued  = "queued"
	PostingPosting = "posting"
	PostingPosted  = "posted"
	PostingFailed  = "failed"
	PostingRemoved = "removed"
)

var (
	// ErrBadBoard is a board the platform has no adapter for.
	ErrBadBoard = errors.New("service: that is not a board the platform posts to")
	// ErrPostingClosedJob refuses to advertise a job that is not open.
	ErrPostingClosedJob = errors.New("service: only an open job can be posted")
	// ErrNoPoster is a worker with no browser automation configured: the
	// posting is recorded as failed with this reason rather than retried
	// into the void.
	ErrNoPoster = errors.New("service: job posting is not configured (set JOBPOST_CMD to the jobpost tool)")
)

// JobPosting is one placement of a job on a board.
type JobPosting struct {
	ID          uuid.UUID
	JobID       uuid.UUID
	JobTitle    string
	Board       string
	BoardLabel  string
	Status      string
	Title       string
	Body        string
	ApplyURL    string
	ExternalURL string
	ExternalID  string
	Error       string
	Attempts    int
	PostedAt    time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// BoardPreview is the copy a job would carry on one board, before posting.
type BoardPreview struct {
	Board   domain.Board
	Posting domain.Posting
}

// PostRequest is what the browser tool is asked to place.
type PostRequest struct {
	Board    string `json:"board"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	ApplyURL string `json:"apply_url"`
}

// PostResult is where a posting landed.
type PostResult struct {
	URL string `json:"url"`
	ID  string `json:"id"`
}

// Poster places a posting on a board. The worker's is the jobpost CLI
// driving a browser; tests substitute their own.
type Poster interface {
	Post(ctx context.Context, req PostRequest) (PostResult, error)
}

// JobPostingService writes postings: the copy from the job's own fields, the
// record of each placement, and the job that carries it out.
type JobPostingService struct {
	st      *store.Store
	q       Enqueuer
	baseURL string
}

func NewJobPostingService(st *store.Store, q Enqueuer, baseURL string) *JobPostingService {
	return &JobPostingService{st: st, q: q, baseURL: strings.TrimRight(baseURL, "/")}
}

// Preview is the copy for every board, as the panel shows it before a post.
func (s *JobPostingService) Preview(ctx context.Context, p Principal, jobID uuid.UUID) ([]BoardPreview, error) {
	if err := requireRecruiter(p); err != nil {
		return nil, err
	}
	var out []BoardPreview
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		in, err := s.postingInput(ctx, tx, p.OrgID, jobID)
		if err != nil {
			return err
		}
		for _, b := range domain.Boards {
			out = append(out, BoardPreview{Board: b, Posting: domain.WritePosting(in)})
		}
		return nil
	})
	if err != nil {
		return nil, wrapPosting("preview posting", err)
	}
	return out, nil
}

// Post records a placement and queues the job that carries it out. The copy
// is written now, from the job as it stands, so what the board shows is what
// the recruiter previewed.
func (s *JobPostingService) Post(ctx context.Context, p Principal, jobID uuid.UUID, board string) (JobPosting, error) {
	if err := requireRecruiter(p); err != nil {
		return JobPosting{}, err
	}
	b, ok := domain.BoardByID(strings.ToLower(strings.TrimSpace(board)))
	if !ok {
		return JobPosting{}, ErrBadBoard
	}
	var out JobPosting
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		in, err := s.postingInput(ctx, tx, p.OrgID, jobID)
		if err != nil {
			return err
		}
		copy := domain.WritePosting(in)
		row, err := tx.Q.CreateJobPosting(ctx, db.CreateJobPostingParams{
			OrgID: p.OrgID, JobID: jobID, Board: b.ID, Title: copy.Title, Body: copy.Body, ApplyUrl: in.ApplyURL,
			CreatedBy: uuid.NullUUID{UUID: p.UserID, Valid: p.UserID != uuid.Nil},
		})
		if err != nil {
			return err
		}
		if s.q != nil {
			if _, err := s.q.Enqueue(ctx, tx, queue.KindJobPostPublish, JobPostPublishPayload{PostingID: row.ID, OrgID: p.OrgID}); err != nil {
				return err
			}
		}
		out = toPosting(row, in.Title)
		return nil
	})
	if err != nil {
		return JobPosting{}, wrapPosting("post job", err)
	}
	return out, nil
}

// Retry queues a failed posting again.
func (s *JobPostingService) Retry(ctx context.Context, p Principal, id uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetJobPosting(ctx, id)
		if err != nil {
			return err
		}
		if row.Status != PostingFailed {
			return fmt.Errorf("%w: only a failed posting can be retried", ErrBadDecision)
		}
		if s.q == nil {
			return nil
		}
		_, err = s.q.Enqueue(ctx, tx, queue.KindJobPostPublish, JobPostPublishPayload{PostingID: row.ID, OrgID: p.OrgID})
		return err
	})
	if err != nil {
		return wrapPosting("retry posting", err)
	}
	return nil
}

// Remove marks a posting taken down. Boards are taken down by hand for now;
// the record says the platform no longer counts it as live.
func (s *JobPostingService) Remove(ctx context.Context, p Principal, id uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		n, err := tx.Q.RemoveJobPosting(ctx, db.RemoveJobPostingParams{ID: id, OrgID: p.OrgID})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return wrapPosting("remove posting", err)
	}
	return nil
}

// List is a job's postings, newest first.
func (s *JobPostingService) List(ctx context.Context, p Principal, jobID uuid.UUID) ([]JobPosting, error) {
	if err := requireRecruiter(p); err != nil {
		return nil, err
	}
	var out []JobPosting
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		job, err := tx.Q.GetJob(ctx, jobID)
		if err != nil {
			return err
		}
		rows, err := tx.Q.ListJobPostings(ctx, jobID)
		if err != nil {
			return err
		}
		out = make([]JobPosting, 0, len(rows))
		for _, r := range rows {
			out = append(out, toPosting(r, job.Title))
		}
		return nil
	})
	if err != nil {
		return nil, wrapPosting("list postings", err)
	}
	return out, nil
}

// postingInput reads the job and the org as the copy needs them.
func (s *JobPostingService) postingInput(ctx context.Context, tx *store.Tx, orgID, jobID uuid.UUID) (domain.PostingInput, error) {
	job, err := tx.Q.GetJob(ctx, jobID)
	if err != nil {
		return domain.PostingInput{}, err
	}
	if job.Status != "open" {
		return domain.PostingInput{}, ErrPostingClosedJob
	}
	org, err := tx.Q.GetOrg(ctx, orgID)
	if err != nil {
		return domain.PostingInput{}, err
	}
	company, err := tx.Q.GetClientCompany(ctx, db.GetClientCompanyParams{ID: job.ClientCompanyID, OrgID: orgID})
	if err != nil {
		return domain.PostingInput{}, err
	}
	return domain.PostingInput{
		Title: job.Title, Company: company.Name, Agency: org.Name, Description: job.Description,
		Skills: job.Skills, Seniority: text(job.Seniority), Location: text(job.Location), RemotePolicy: text(job.RemotePolicy),
		SalaryMin: int(derefInt32(job.SalaryMin)), SalaryMax: int(derefInt32(job.SalaryMax)),
		ApplyURL: s.baseURL + "/apply/" + org.Slug + "/" + job.Slug,
		Blind:    job.BlindMode,
	}, nil
}

func toPosting(r db.JobPosting, jobTitle string) JobPosting {
	out := JobPosting{
		ID: r.ID, JobID: r.JobID, JobTitle: jobTitle, Board: r.Board, Status: r.Status,
		Title: r.Title, Body: r.Body, ApplyURL: r.ApplyUrl,
		ExternalURL: text(r.ExternalUrl), ExternalID: text(r.ExternalID), Error: text(r.Error),
		Attempts:  int(r.Attempts),
		CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
	}
	if b, ok := domain.BoardByID(r.Board); ok {
		out.BoardLabel = b.Label
	} else {
		out.BoardLabel = r.Board
	}
	if r.PostedAt.Valid {
		out.PostedAt = r.PostedAt.Time.UTC()
	}
	return out
}

func wrapPosting(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrNotFound), errors.Is(err, ErrBadBoard),
		errors.Is(err, ErrPostingClosedJob), errors.Is(err, ErrBadDecision):
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}

// JobPostPublishPayload is the jobpost.publish payload.
type JobPostPublishPayload struct {
	PostingID uuid.UUID `json:"posting_id"`
	OrgID     uuid.UUID `json:"org_id"`
}

// JobPostPublishHandler works jobpost.publish: it claims the posting, asks
// the poster to place it, and records where it landed or why it did not. A
// poster failure is recorded on the row and retried by the queue; a posting
// already posted or removed is left alone, so a redelivered job posts
// nothing twice.
func JobPostPublishHandler(st *store.Store, poster Poster, logger *slog.Logger) queue.Handler {
	return func(ctx context.Context, job queue.Job) error {
		var p JobPostPublishPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("jobpost.publish payload: %w", err)
		}
		if p.PostingID == uuid.Nil || p.OrgID == uuid.Nil {
			return errors.New("jobpost.publish payload names no posting or org")
		}
		scope := orgScoped(p.OrgID)
		var req PostRequest
		var claimed bool
		err := st.WithTx(ctx, scope, func(ctx context.Context, tx *store.Tx) error {
			row, err := tx.Q.GetJobPostingForUpdate(ctx, p.PostingID)
			if err != nil {
				return err
			}
			if row.Status == PostingPosted || row.Status == PostingRemoved {
				return nil
			}
			n, err := tx.Q.StartJobPosting(ctx, row.ID)
			if err != nil || n == 0 {
				return err
			}
			claimed = true
			req = PostRequest{Board: row.Board, Title: row.Title, Body: row.Body, ApplyURL: row.ApplyUrl}
			return nil
		})
		if err != nil {
			return fmt.Errorf("jobpost.publish: %w", err)
		}
		if !claimed {
			return nil
		}
		var res PostResult
		postErr := ErrNoPoster
		if poster != nil {
			res, postErr = poster.Post(ctx, req)
		}
		if postErr != nil && logger != nil {
			logger.Warn("job posting failed", "posting_id", p.PostingID, "board", req.Board, "error", postErr)
		}
		err = st.WithTx(ctx, scope, func(ctx context.Context, tx *store.Tx) error {
			if postErr != nil {
				return tx.Q.FailJobPosting(ctx, db.FailJobPostingParams{ID: p.PostingID, Error: nullable(tailLine(postErr.Error()))})
			}
			return tx.Q.FinishJobPosting(ctx, db.FinishJobPostingParams{ID: p.PostingID, ExternalUrl: nullable(res.URL), ExternalID: nullable(res.ID)})
		})
		if err != nil {
			return fmt.Errorf("jobpost.publish: %w", err)
		}
		if postErr != nil && !errors.Is(postErr, ErrNoPoster) {
			// The board said no (or the browser lost its way): let the queue
			// try again with backoff, the row already saying why.
			return fmt.Errorf("jobpost.publish: %w", postErr)
		}
		return nil
	}
}
