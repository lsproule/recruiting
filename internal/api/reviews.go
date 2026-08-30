package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/domain/signals"
	"recruiting/internal/service"
)

// ReviewAssignment is one attempt waiting on the caller's verdict.
type ReviewAssignment struct {
	AttemptID       uuid.UUID `json:"attempt_id"`
	ApplicationID   uuid.UUID `json:"application_id"`
	StageID         uuid.UUID `json:"stage_id"`
	CandidateName   string    `json:"candidate_name"`
	CandidateEmail  string    `json:"candidate_email"`
	JobTitle        string    `json:"job_title"`
	StageName       string    `json:"stage_name"`
	Status          string    `json:"status"`
	Score           *float64  `json:"score"`
	RiskScore       *float64  `json:"risk_score"`
	ErrorCount      int       `json:"error_count"`
	RecordingStatus string    `json:"recording_status"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
	Verdict         string    `json:"verdict,omitempty"`
	Reviewed        bool      `json:"reviewed"`
}

// Review is one filed verdict on an attempt.
type Review struct {
	ID         uuid.UUID `json:"id"`
	AttemptID  uuid.UUID `json:"attempt_id"`
	VetterID   uuid.UUID `json:"vetter_id"`
	VetterName string    `json:"vetter_name,omitempty"`
	Verdict    string    `json:"verdict"`
	Notes      string    `json:"notes,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// AttemptSignal is one integrity signal with the evidence behind it. Signals
// inform a human verdict; they never move an application on their own.
type AttemptSignal struct {
	Name       string             `json:"name"`
	Value      float64            `json:"value"`
	Weight     float64            `json:"weight"`
	Confidence string             `json:"confidence"`
	Evidence   []signals.Evidence `json:"evidence"`
}

// ReviewProblem is one problem of the attempt with the score it earned.
type ReviewProblem struct {
	ID         uuid.UUID `json:"id"`
	Title      string    `json:"title"`
	Difficulty string    `json:"difficulty"`
	Score      float64   `json:"score" doc:"Weighted share of the cases passed, 0-1"`
	Status     string    `json:"status,omitempty"`
}

// ReviewSubmission is one run or submit the candidate made.
type ReviewSubmission struct {
	ID           uuid.UUID `json:"id"`
	ProblemID    uuid.UUID `json:"problem_id"`
	ProblemTitle string    `json:"problem_title"`
	Kind         string    `json:"kind"`
	Language     string    `json:"language"`
	Status       string    `json:"status"`
	Score        *float64  `json:"score"`
	CreatedAt    time.Time `json:"created_at"`
}

// AttemptSummary is one attempt of an application as the pipeline shows it.
type AttemptSummary struct {
	AttemptID   uuid.UUID `json:"attempt_id"`
	StageID     uuid.UUID `json:"stage_id"`
	StageName   string    `json:"stage_name"`
	Status      string    `json:"status"`
	Score       *float64  `json:"score"`
	RiskScore   *float64  `json:"risk_score"`
	ErrorCount  int       `json:"error_count"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	Verdict     string    `json:"verdict,omitempty"`
	ReviewNotes string    `json:"review_notes,omitempty"`
	VetterName  string    `json:"vetter_name,omitempty"`
	ReviewedAt  time.Time `json:"reviewed_at,omitempty"`
}

type reviewsHandlers struct{ d Deps }

func (m *mounter) mountReviews() {
	h := reviewsHandlers{d: m.d}
	register(m, accessOrg, huma.Operation{
		OperationID: "list-review-assignments", Method: http.MethodGet, Path: "/reviews",
		Summary: "Attempts waiting on the caller's verdict", Tags: []string{"reviews"},
	}, h.list)
	register(m, accessOrg, huma.Operation{
		OperationID: "create-review", Method: http.MethodPost, Path: "/reviews",
		Summary: "File or amend a verdict", Tags: []string{"reviews"}, DefaultStatus: http.StatusCreated,
	}, h.save)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-review", Method: http.MethodGet, Path: "/reviews/{attempt_id}",
		Summary: "One attempt with its scores, signals, and verdict", Tags: []string{"reviews"},
	}, h.get)
	register(m, accessOrg, huma.Operation{
		OperationID: "list-application-reviews", Method: http.MethodGet, Path: "/applications/{application_id}/reviews",
		Summary: "Reviewed attempts of an application", Tags: []string{"reviews"},
	}, h.summaries)
}

type reviewAssignmentsOutput struct {
	Body struct {
		Assignments []ReviewAssignment `json:"assignments"`
	}
}

type reviewInput struct {
	AttemptID uuid.UUID `path:"attempt_id"`
}

type attemptReviewOutput struct {
	Body struct {
		AttemptID       uuid.UUID          `json:"attempt_id"`
		ApplicationID   uuid.UUID          `json:"application_id"`
		StageID         uuid.UUID          `json:"stage_id"`
		CandidateName   string             `json:"candidate_name"`
		JobTitle        string             `json:"job_title"`
		StageName       string             `json:"stage_name"`
		Status          string             `json:"status"`
		Score           *float64           `json:"score"`
		RiskScore       *float64           `json:"risk_score"`
		ErrorCount      int                `json:"error_count"`
		RecordingStatus string             `json:"recording_status"`
		StartedAt       time.Time          `json:"started_at,omitempty"`
		FinishedAt      time.Time          `json:"finished_at,omitempty"`
		Problems        []ReviewProblem    `json:"problems"`
		Signals         []AttemptSignal    `json:"signals"`
		Submissions     []ReviewSubmission `json:"submissions"`
		Review          *Review            `json:"review,omitempty"`
		CanReview       bool               `json:"can_review"`
	}
}

type saveReviewInput struct {
	Body struct {
		AttemptID uuid.UUID `json:"attempt_id"`
		Verdict   string    `json:"verdict"`
		Notes     string    `json:"notes,omitempty"`
	}
}

type reviewOutput struct{ Body Review }

type attemptSummariesOutput struct {
	Body struct {
		Attempts []AttemptSummary `json:"attempts"`
	}
}

func reviewView(r service.Review) Review {
	return Review{ID: r.ID, AttemptID: r.AttemptID, VetterID: r.VetterID, VetterName: r.VetterName, Verdict: r.Verdict, Notes: r.Notes, CreatedAt: r.CreatedAt}
}

func (h reviewsHandlers) list(ctx context.Context, _ *struct{}) (*reviewAssignmentsOutput, error) {
	as, err := h.d.Reviews.Assignments(ctx, principal(ctx))
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &reviewAssignmentsOutput{}
	out.Body.Assignments = make([]ReviewAssignment, 0, len(as))
	for _, a := range as {
		out.Body.Assignments = append(out.Body.Assignments, ReviewAssignment{
			AttemptID: a.AttemptID, ApplicationID: a.ApplicationID, StageID: a.StageID,
			CandidateName: a.CandidateName, CandidateEmail: a.CandidateEmail,
			JobTitle: a.JobTitle, StageName: a.StageName, Status: a.Status,
			Score: a.Score, RiskScore: a.RiskScore, ErrorCount: a.ErrorCount,
			RecordingStatus: a.RecordingStatus, FinishedAt: a.FinishedAt,
			Verdict: a.Verdict, Reviewed: a.Reviewed,
		})
	}
	return out, nil
}

func (h reviewsHandlers) get(ctx context.Context, in *reviewInput) (*attemptReviewOutput, error) {
	r, err := h.d.Reviews.Attempt(ctx, principal(ctx), in.AttemptID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &attemptReviewOutput{}
	b := &out.Body
	b.AttemptID, b.ApplicationID, b.StageID = r.AttemptID, r.ApplicationID, r.StageID
	b.CandidateName, b.JobTitle, b.StageName, b.Status = r.CandidateName, r.JobTitle, r.StageName, r.Status
	b.Score, b.RiskScore, b.ErrorCount, b.RecordingStatus = r.Score, r.RiskScore, r.ErrorCount, r.RecordingStatus
	b.StartedAt, b.FinishedAt, b.CanReview = r.StartedAt, r.FinishedAt, r.CanReview
	b.Problems = make([]ReviewProblem, 0, len(r.Problems))
	for _, p := range r.Problems {
		b.Problems = append(b.Problems, ReviewProblem{
			ID: p.ID, Title: p.Title, Difficulty: p.Difficulty,
			Score: p.Score.Score, Status: p.Score.Status,
		})
	}
	b.Signals = make([]AttemptSignal, 0, len(r.Signals))
	for _, s := range r.Signals {
		b.Signals = append(b.Signals, AttemptSignal{
			Name: s.Name, Value: s.Value, Weight: s.Weight,
			Confidence: s.Confidence, Evidence: list(s.Evidence),
		})
	}
	b.Submissions = make([]ReviewSubmission, 0, len(r.Submissions))
	for _, s := range r.Submissions {
		b.Submissions = append(b.Submissions, ReviewSubmission{
			ID: s.ID, ProblemID: s.ProblemID, ProblemTitle: s.ProblemTitle,
			Kind: s.Kind, Language: s.Language, Status: s.Status,
			Score: s.Score, CreatedAt: s.CreatedAt,
		})
	}
	if r.Review != nil {
		rev := reviewView(*r.Review)
		b.Review = &rev
	}
	return out, nil
}

func (h reviewsHandlers) save(ctx context.Context, in *saveReviewInput) (*reviewOutput, error) {
	r, err := h.d.Reviews.Save(ctx, principal(ctx), service.ReviewInput{
		AttemptID: in.Body.AttemptID, Verdict: in.Body.Verdict, Notes: in.Body.Notes,
	})
	if err != nil {
		return nil, problemDetail(err)
	}
	return &reviewOutput{Body: reviewView(r)}, nil
}

func (h reviewsHandlers) summaries(ctx context.Context, in *applicationInput) (*attemptSummariesOutput, error) {
	ss, err := h.d.Reviews.Summaries(ctx, principal(ctx), in.ApplicationID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &attemptSummariesOutput{}
	out.Body.Attempts = make([]AttemptSummary, 0, len(ss))
	for _, s := range ss {
		out.Body.Attempts = append(out.Body.Attempts, AttemptSummary{
			AttemptID: s.AttemptID, StageID: s.StageID, StageName: s.StageName,
			Status: s.Status, Score: s.Score, RiskScore: s.RiskScore, ErrorCount: s.ErrorCount,
			FinishedAt: s.FinishedAt, Verdict: s.Verdict, ReviewNotes: s.ReviewNotes,
			VetterName: s.VetterName, ReviewedAt: s.ReviewedAt,
		})
	}
	return out, nil
}
