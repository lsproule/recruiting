package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/google/uuid"

	"recruiting/internal/service"
	"recruiting/internal/web/middleware"
)

// replayOp is the operation the replay middleware authenticates.
const replayOp = "get-attempt-replay"

// ReplayDeps is what MountReplay needs. Resolve turns the request into the
// signed-in org user's principal; the API mux carries no session middleware
// of its own, so ResolveWith adapts the HTML surface's. Nil reads the
// principal already on the request context.
type ReplayDeps struct {
	Reviews *service.ReviewService
	Resolve func(*http.Request) (service.Principal, bool)
}

func (d ReplayDeps) resolve(r *http.Request) (service.Principal, bool) {
	if d.Resolve != nil {
		return d.Resolve(r)
	}
	return middleware.PrincipalFrom(r.Context())
}

// MountReplay registers the replay manifest the reviewer's viewer reads. It
// sits under /replay rather than /attempts because the candidate operations
// there are guarded by the sealed assessment cookie; this one authenticates
// as an org user, and the service decides which attempts that user may open.
func MountReplay(api huma.API, d ReplayDeps) {
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		if ctx.Operation().OperationID != replayOp {
			next(ctx)
			return
		}
		r, _ := humachi.Unwrap(ctx)
		p, ok := d.resolve(r)
		if !ok || p.Kind != service.PrincipalOrgUser {
			_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, "sign in to replay an attempt")
			return
		}
		next(huma.WithValue(ctx, principalKey{}, p))
	})
	h := replayHandlers{d: d}
	huma.Register(api, huma.Operation{
		OperationID: replayOp, Method: http.MethodGet, Path: "/replay/{attemptID}",
		Summary: "Replay manifest for a reviewed attempt", Tags: []string{"reviews"},
	}, h.replay)
}

type replayHandlers struct{ d ReplayDeps }

// ReplayEvent is one recorded event of the manifest. At is Unix
// milliseconds on the client clock, clamped to the server's.
type ReplayEvent struct {
	Seq       int64           `json:"seq"`
	Kind      string          `json:"kind"`
	ProblemID string          `json:"problem_id,omitempty"`
	At        int64           `json:"at"`
	Payload   json.RawMessage `json:"payload"`
}

// ReplayMarker is a point on the timeline the viewer offers to jump to.
type ReplayMarker struct {
	Seq       int64  `json:"seq"`
	Kind      string `json:"kind"`
	ProblemID string `json:"problem_id"`
	At        int64  `json:"at"`
	Note      string `json:"note"`
}

// ReplayProblem is one editor of the replay. Applying the stream's edits to
// initial_source in seq order reproduces final_source.
type ReplayProblem struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Language      string `json:"language"`
	InitialSource string `json:"initial_source"`
	FinalSource   string `json:"final_source"`
}

// ReplayManifest is one page of the sitting: the editors (first page only),
// the stream in seq order, and the markers. snapshot_every is how many events
// the viewer folds before keeping a document snapshot for scrubbing;
// next_after_seq is the after_seq of the page still to come, zero at the end.
type ReplayManifest struct {
	AttemptID       uuid.UUID       `json:"attempt_id"`
	Status          string          `json:"status"`
	RecordingStatus string          `json:"recording_status" doc:"pending, complete, or incomplete; incomplete means the stream has a gap"`
	StartedAt       int64           `json:"started_at" doc:"Unix milliseconds; zero when never started"`
	FinishedAt      int64           `json:"finished_at" doc:"Unix milliseconds; zero while open"`
	SnapshotEvery   int             `json:"snapshot_every"`
	Problems        []ReplayProblem `json:"problems"`
	Events          []ReplayEvent   `json:"events"`
	Markers         []ReplayMarker  `json:"markers"`
	NextAfterSeq    int64           `json:"next_after_seq"`
}

type replayInput struct {
	AttemptID uuid.UUID `path:"attemptID"`
	AfterSeq  int64     `query:"after_seq" minimum:"0" doc:"Return events after this seq; zero starts the stream"`
	Limit     int       `query:"limit" minimum:"1" maximum:"5000" doc:"Events per page; defaults to 5000"`
}

type replayOutput struct{ Body ReplayManifest }

func (h replayHandlers) replay(ctx context.Context, in *replayInput) (*replayOutput, error) {
	rep, err := h.d.Reviews.Replay(ctx, principal(ctx), in.AttemptID,
		service.ReplayQuery{AfterSeq: in.AfterSeq, Limit: in.Limit})
	if err != nil {
		return nil, problemDetail(err)
	}
	return &replayOutput{Body: replayManifest(rep)}, nil
}

func replayManifest(rep service.Replay) ReplayManifest {
	out := ReplayManifest{
		AttemptID: rep.AttemptID, Status: rep.Status, RecordingStatus: rep.RecordingStatus,
		SnapshotEvery: rep.SnapshotEvery, NextAfterSeq: rep.NextAfterSeq,
		Problems: make([]ReplayProblem, 0, len(rep.Problems)),
		Events:   make([]ReplayEvent, 0, len(rep.Events)),
		Markers:  make([]ReplayMarker, 0, len(rep.Markers)),
	}
	if !rep.StartedAt.IsZero() {
		out.StartedAt = rep.StartedAt.UnixMilli()
	}
	if !rep.FinishedAt.IsZero() {
		out.FinishedAt = rep.FinishedAt.UnixMilli()
	}
	for _, p := range rep.Problems {
		out.Problems = append(out.Problems, ReplayProblem{
			ID: p.ID.String(), Title: p.Title, Language: p.Language,
			InitialSource: p.InitialSource, FinalSource: p.FinalSource,
		})
	}
	for _, ev := range rep.Events {
		re := ReplayEvent{Seq: ev.Seq, Kind: ev.Kind, At: service.RecordedEventAt(ev).UnixMilli(), Payload: ev.Payload}
		if ev.ProblemID != nil {
			re.ProblemID = ev.ProblemID.String()
		}
		out.Events = append(out.Events, re)
	}
	for _, m := range rep.Markers {
		out.Markers = append(out.Markers, ReplayMarker{
			Seq: m.Seq, Kind: m.Kind, ProblemID: m.ProblemID.String(), At: m.At.UnixMilli(), Note: m.Note,
		})
	}
	return out
}
