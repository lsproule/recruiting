package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/google/uuid"

	"recruiting/internal/service"
	"recruiting/internal/web/middleware"
)

// AttemptsDeps is what MountAttempts needs. Resolve turns the request into
// the candidate's principal. Nil reads the principal already attached to
// the request context, which is the case when the candidate page forwards
// /assess/api through the auth cookie middleware; ResolveWith adapts that
// middleware for a direct mount.
type AttemptsDeps struct {
	Attempts *service.AttemptService
	Resolve  func(*http.Request) (service.Principal, bool)
}

func (d AttemptsDeps) resolve(r *http.Request) (service.Principal, bool) {
	if d.Resolve != nil {
		return d.Resolve(r)
	}
	return middleware.PrincipalFrom(r.Context())
}

// ResolveWith adapts a principal-attaching chi middleware (the auth
// package's assessment cookie middleware) into a resolver: the middleware
// runs against a discarded response, and the principal it attached, if any,
// is captured from the request context it hands on.
func ResolveWith(mw func(http.Handler) http.Handler) func(*http.Request) (service.Principal, bool) {
	return func(r *http.Request) (service.Principal, bool) {
		var p service.Principal
		var ok bool
		capture := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			p, ok = middleware.PrincipalFrom(r.Context())
		})
		mw(capture).ServeHTTP(&discardWriter{h: http.Header{}}, r)
		return p, ok
	}
}

// discardWriter swallows the middleware's own error page; the API answers
// with its own 401.
type discardWriter struct{ h http.Header }

func (d *discardWriter) Header() http.Header         { return d.h }
func (d *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardWriter) WriteHeader(int)             {}

// attemptsPrefix is the operation path prefix the cookie middleware guards.
const attemptsPrefix = "/attempts/"

type principalKey struct{}

// MountAttempts registers the candidate session operations under
// /attempts. They authenticate with the sealed assessment cookie, not a
// session, so the HTML surface's CSRF middleware does not apply; the cookie
// is SameSite=Lax and scoped to /assess/, and every write here is JSON.
func MountAttempts(api huma.API, d AttemptsDeps) {
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		if !strings.HasPrefix(ctx.Operation().Path, attemptsPrefix) {
			next(ctx)
			return
		}
		r, _ := humachi.Unwrap(ctx)
		p, ok := d.resolve(r)
		if !ok {
			_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, "the assessment session is not valid")
			return
		}
		next(huma.WithValue(ctx, principalKey{}, p))
	})
	h := attemptHandlers{d: d}

	huma.Register(api, huma.Operation{
		OperationID: "get-attempt", Method: http.MethodGet, Path: "/attempts/{id}", Summary: "Attempt status and timer",
		Tags: []string{"attempts"},
	}, h.get)
	huma.Register(api, huma.Operation{
		OperationID: "record-attempt-events", Method: http.MethodPost, Path: "/attempts/{id}/events", Summary: "Append recorded editor events",
		Tags: []string{"attempts"},
	}, h.events)
	huma.Register(api, huma.Operation{
		OperationID: "save-attempt-source", Method: http.MethodPut, Path: "/attempts/{id}/problems/{problem_id}/source", Summary: "Sync the editor text",
		Tags: []string{"attempts"}, DefaultStatus: http.StatusNoContent,
	}, h.saveSource)
	huma.Register(api, huma.Operation{
		OperationID: "run-attempt-problem", Method: http.MethodPost, Path: "/attempts/{id}/problems/{problem_id}/run", Summary: "Run against the public cases",
		Tags: []string{"attempts"}, DefaultStatus: http.StatusAccepted,
	}, h.run)
	huma.Register(api, huma.Operation{
		OperationID: "submit-attempt-problem", Method: http.MethodPost, Path: "/attempts/{id}/problems/{problem_id}/submit", Summary: "Submit against all cases",
		Tags: []string{"attempts"}, DefaultStatus: http.StatusAccepted,
	}, h.submit)
	huma.Register(api, huma.Operation{
		OperationID: "get-attempt-submission", Method: http.MethodGet, Path: "/attempts/{id}/submissions/{submission_id}", Summary: "Poll a submission",
		Tags: []string{"attempts"},
	}, h.submission)
	huma.Register(api, huma.Operation{
		OperationID: "finish-attempt", Method: http.MethodPost, Path: "/attempts/{id}/finish", Summary: "End the attempt",
		Tags: []string{"attempts"},
	}, h.finish)
}

type attemptHandlers struct{ d AttemptsDeps }

func principal(ctx context.Context) service.Principal {
	p, _ := ctx.Value(principalKey{}).(service.Principal)
	return p
}

// AttemptStatus is the timer view the page polls and resyncs against.
type AttemptStatus struct {
	ID          uuid.UUID `json:"id"`
	Status      string    `json:"status"`
	ExpiresAt   int64     `json:"expires_at" doc:"Unix milliseconds; zero until started"`
	RemainingMs int64     `json:"remaining_ms"`
	LastSeq     int64     `json:"last_seq"`
}

type attemptInput struct {
	ID uuid.UUID `path:"id"`
}

type attemptStatusOutput struct{ Body AttemptStatus }

func (h attemptHandlers) get(ctx context.Context, in *attemptInput) (*attemptStatusOutput, error) {
	p := principal(ctx)
	// The sealed cookie names the attempt; the path must agree, or the
	// caller is asking about someone else's sitting.
	if p.SubjectID != in.ID {
		return nil, problemDetail(service.ErrNotFound)
	}
	s, err := h.d.Attempts.Session(ctx, p)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := AttemptStatus{ID: s.Attempt.ID, Status: s.Attempt.Status, LastSeq: s.Attempt.LastEventSeq, RemainingMs: s.Remaining.Milliseconds()}
	if !s.Attempt.ExpiresAt.IsZero() {
		out.ExpiresAt = s.Attempt.ExpiresAt.UnixMilli()
	}
	return &attemptStatusOutput{Body: out}, nil
}

// EventBatch is the ingest body: {"attempt_id","events":[...]}.
type EventBatch struct {
	AttemptID uuid.UUID              `json:"attempt_id"`
	Events    []service.AttemptEvent `json:"events" maxItems:"500"`
}

type eventsInput struct {
	ID   uuid.UUID `path:"id"`
	Body EventBatch
}

type eventsOutput struct {
	Body struct {
		LastSeq int64 `json:"last_seq"`
	}
}

func (h attemptHandlers) events(ctx context.Context, in *eventsInput) (*eventsOutput, error) {
	if in.Body.AttemptID != in.ID {
		return nil, huma.Error422UnprocessableEntity("attempt_id does not match the path")
	}
	last, err := h.d.Attempts.RecordEvents(ctx, principal(ctx), in.ID, in.Body.Events)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &eventsOutput{}
	out.Body.LastSeq = last
	return out, nil
}

// SourceBody is the editor text for one problem.
type SourceBody struct {
	Language string `json:"language" minLength:"1"`
	Source   string `json:"source" maxLength:"262144"`
}

type sourceInput struct {
	ID        uuid.UUID `path:"id"`
	ProblemID uuid.UUID `path:"problem_id"`
	Body      SourceBody
}

func (h attemptHandlers) saveSource(ctx context.Context, in *sourceInput) (*struct{}, error) {
	if err := h.d.Attempts.SaveSource(ctx, principal(ctx), in.ID, in.ProblemID, in.Body.Language, in.Body.Source); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

// SubmissionView is what the page polls until the runner has answered. The
// result is the candidate projection: public cases by outcome, hidden cases
// as counts.
type SubmissionView struct {
	ID     uuid.UUID                `json:"id"`
	Kind   string                   `json:"kind"`
	Status string                   `json:"status"`
	Result *service.CandidateResult `json:"result,omitempty"`
	Score  *float64                 `json:"score"`
}

type submissionOutput struct{ Body SubmissionView }

type queuedOutput struct {
	Body struct {
		SubmissionID uuid.UUID `json:"submission_id"`
		Status       string    `json:"status"`
	}
}

func (h attemptHandlers) run(ctx context.Context, in *sourceInput) (*queuedOutput, error) {
	sub, err := h.d.Attempts.Run(ctx, principal(ctx), in.ID, in.ProblemID, in.Body.Language, in.Body.Source)
	return queued(sub, err)
}

func (h attemptHandlers) submit(ctx context.Context, in *sourceInput) (*queuedOutput, error) {
	sub, err := h.d.Attempts.Submit(ctx, principal(ctx), in.ID, in.ProblemID, in.Body.Language, in.Body.Source)
	return queued(sub, err)
}

func queued(sub service.Submission, err error) (*queuedOutput, error) {
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &queuedOutput{}
	out.Body.SubmissionID, out.Body.Status = sub.ID, sub.Status
	return out, nil
}

type submissionInput struct {
	ID           uuid.UUID `path:"id"`
	SubmissionID uuid.UUID `path:"submission_id"`
}

func (h attemptHandlers) submission(ctx context.Context, in *submissionInput) (*submissionOutput, error) {
	sub, err := h.d.Attempts.Submission(ctx, principal(ctx), in.ID, in.SubmissionID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &submissionOutput{Body: SubmissionView{ID: sub.ID, Kind: sub.Kind, Status: sub.Status, Result: sub.CandidateResult, Score: sub.Score}}, nil
}

type finishOutput struct {
	Body struct {
		Status string `json:"status"`
	}
}

func (h attemptHandlers) finish(ctx context.Context, in *attemptInput) (*finishOutput, error) {
	att, err := h.d.Attempts.Finish(ctx, principal(ctx), in.ID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &finishOutput{}
	out.Body.Status = att.Status
	return out, nil
}
