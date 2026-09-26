package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/service"
)

// SprintRating is what an interviewer filed after one conversation.
type SprintRating struct {
	Score          int       `json:"score" minimum:"1" maximum:"5"`
	Recommendation string    `json:"recommendation" enum:"strong_yes,yes,no,strong_no"`
	Note           string    `json:"note,omitempty"`
	RatedAt        time.Time `json:"rated_at"`
}

// SprintPairing is one conversation of a sprint.
type SprintPairing struct {
	ID              uuid.UUID     `json:"id"`
	Round           int           `json:"round" doc:"Zero-based"`
	InterviewerID   uuid.UUID     `json:"interviewer_id"`
	InterviewerName string        `json:"interviewer_name"`
	ApplicationID   uuid.UUID     `json:"application_id"`
	CandidateName   string        `json:"candidate_name"`
	StartsAt        time.Time     `json:"starts_at"`
	EndsAt          time.Time     `json:"ends_at"`
	Rating          *SprintRating `json:"rating,omitempty"`
}

// SprintParticipant is one interviewer or one candidate.
type SprintParticipant struct {
	ID            uuid.UUID `json:"id" doc:"The user for an interviewer; the sprint's own candidate row for a candidate"`
	ApplicationID uuid.UUID `json:"application_id,omitempty"`
	Name          string    `json:"name"`
	Email         string    `json:"email"`
}

// Sprint is a screening sprint: everyone in it, its clock, and every
// conversation with its rating once filed.
type Sprint struct {
	ID           uuid.UUID           `json:"id"`
	JobID        uuid.UUID           `json:"job_id"`
	JobTitle     string              `json:"job_title"`
	StageID      uuid.UUID           `json:"stage_id"`
	Name         string              `json:"name"`
	Status       string              `json:"status" enum:"draft,scheduled,cancelled" doc:"Live and done are read off the clock: see phase"`
	Phase        string              `json:"phase" enum:"before,round,break,after"`
	Round        int                 `json:"round" doc:"The round the phase refers to, zero-based"`
	Rounds       int                 `json:"rounds"`
	StartsAt     time.Time           `json:"starts_at"`
	EndsAt       time.Time           `json:"ends_at"`
	RoundSeconds int                 `json:"round_seconds"`
	BreakSeconds int                 `json:"break_seconds"`
	Interviewers []SprintParticipant `json:"interviewers"`
	Candidates   []SprintParticipant `json:"candidates"`
	Pairings     []SprintPairing     `json:"pairings"`
}

// SprintSummaryRow is one candidate ranked by the ratings.
type SprintSummaryRow struct {
	ApplicationID uuid.UUID `json:"application_id"`
	CandidateName string    `json:"candidate_name"`
	Rated         int       `json:"rated"`
	Mean          float64   `json:"mean"`
	StrongYes     int       `json:"strong_yes"`
	Yes           int       `json:"yes"`
	No            int       `json:"no"`
	StrongNo      int       `json:"strong_no"`
}

func sprintView(sp service.Sprint, now time.Time) Sprint {
	clock := sp.Clock()
	state := clock.At(now)
	out := Sprint{
		ID: sp.ID, JobID: sp.JobID, JobTitle: sp.JobTitle, StageID: sp.StageID, Name: sp.Name, Status: sp.Status,
		Phase: string(state.Phase), Round: state.Round, Rounds: sp.Rounds(),
		StartsAt: sp.StartsAt, EndsAt: clock.EndsAt(), RoundSeconds: sp.RoundSeconds, BreakSeconds: sp.BreakSeconds,
		Interviewers: []SprintParticipant{}, Candidates: []SprintParticipant{}, Pairings: []SprintPairing{},
	}
	for _, i := range sp.Interviewers {
		out.Interviewers = append(out.Interviewers, SprintParticipant{ID: i.ID, Name: i.Name, Email: i.Email})
	}
	for _, c := range sp.Candidates {
		out.Candidates = append(out.Candidates, SprintParticipant{ID: c.ID, ApplicationID: c.ApplicationID, Name: c.Name, Email: c.Email})
	}
	for _, pr := range sp.Pairings {
		out.Pairings = append(out.Pairings, pairingView(pr, sp))
	}
	return out
}

func pairingView(pr service.SprintPairing, sp service.Sprint) SprintPairing {
	clock := sp.Clock()
	out := SprintPairing{
		ID: pr.ID, Round: pr.Round, InterviewerID: pr.InterviewerID, InterviewerName: pr.InterviewerName,
		ApplicationID: pr.ApplicationID, CandidateName: pr.CandidateName,
		StartsAt: clock.RoundStart(pr.Round), EndsAt: clock.RoundEnd(pr.Round),
	}
	if pr.Rating != nil {
		out.Rating = &SprintRating{Score: pr.Rating.Score, Recommendation: pr.Rating.Recommendation, Note: pr.Rating.Note, RatedAt: pr.Rating.RatedAt}
	}
	return out
}

type sprintHandlers struct{ d Deps }

func (m *mounter) mountSprints() {
	h := sprintHandlers{d: m.d}
	register(m, accessOrg, huma.Operation{
		OperationID: "list-sprints", Method: http.MethodGet, Path: "/sprints",
		Summary: "Every screening sprint", Tags: []string{"sprints"},
	}, h.list)
	register(m, accessOrg, huma.Operation{
		OperationID: "create-sprint", Method: http.MethodPost, Path: "/sprints",
		Summary: "Plan a sprint", Tags: []string{"sprints"}, DefaultStatus: http.StatusCreated,
		Description: "Plans the rotation so every candidate meets every interviewer once, as a draft. Nothing is sent until it is scheduled.",
	}, h.create)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-sprint", Method: http.MethodGet, Path: "/sprints/{sprint_id}",
		Summary: "One sprint with its clock and pairings", Tags: []string{"sprints"},
	}, h.get)
	register(m, accessOrg, huma.Operation{
		OperationID: "update-sprint", Method: http.MethodPut, Path: "/sprints/{sprint_id}",
		Summary: "Replan a draft", Tags: []string{"sprints"},
	}, h.update)
	register(m, accessOrg, huma.Operation{
		OperationID: "schedule-sprint", Method: http.MethodPost, Path: "/sprints/{sprint_id}/schedule",
		Summary: "Schedule it and invite the candidates", Tags: []string{"sprints"},
	}, h.schedule)
	register(m, accessOrg, huma.Operation{
		OperationID: "start-sprint", Method: http.MethodPost, Path: "/sprints/{sprint_id}/start",
		Summary: "Start it now", Tags: []string{"sprints"},
		Description: "Moves a scheduled sprint's start to this moment.",
	}, h.start)
	register(m, accessOrg, huma.Operation{
		OperationID: "cancel-sprint", Method: http.MethodPost, Path: "/sprints/{sprint_id}/cancel",
		Summary: "Cancel it", Tags: []string{"sprints"}, DefaultStatus: http.StatusNoContent,
	}, h.cancel)
	register(m, accessOrg, huma.Operation{
		OperationID: "rate-sprint-pairing", Method: http.MethodPut, Path: "/sprints/{sprint_id}/pairings/{pairing_id}/rating",
		Summary: "File or change a rating", Tags: []string{"sprints"},
		Description: "Only the conversation's interviewer may, and not before the round starts.",
	}, h.rate)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-sprint-summary", Method: http.MethodGet, Path: "/sprints/{sprint_id}/summary",
		Summary: "Candidates ranked by their ratings", Tags: []string{"sprints"},
	}, h.summary)
}

type sprintsOutput struct {
	Body struct {
		Sprints []SprintListItem `json:"sprints"`
	}
}

// SprintListItem is one sprint in a list.
type SprintListItem struct {
	ID           uuid.UUID `json:"id"`
	JobID        uuid.UUID `json:"job_id"`
	JobTitle     string    `json:"job_title"`
	StageID      uuid.UUID `json:"stage_id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	StartsAt     time.Time `json:"starts_at"`
	Rounds       int       `json:"rounds"`
	Candidates   int       `json:"candidates"`
	Interviewers int       `json:"interviewers"`
}

type sprintOutput struct{ Body Sprint }

type sprintInput struct {
	SprintID uuid.UUID `path:"sprint_id"`
}

// SprintInput is the setup form.
type SprintInput struct {
	JobID          uuid.UUID   `json:"job_id"`
	StageID        uuid.UUID   `json:"stage_id" doc:"A sprint stage of the job"`
	Name           string      `json:"name" minLength:"1"`
	StartsAt       time.Time   `json:"starts_at"`
	RoundSeconds   int         `json:"round_seconds,omitempty" minimum:"0" maximum:"3600" doc:"Defaults to 300"`
	BreakSeconds   int         `json:"break_seconds,omitempty" minimum:"0" maximum:"3600"`
	InterviewerIDs []uuid.UUID `json:"interviewer_ids" minItems:"1"`
	ApplicationIDs []uuid.UUID `json:"application_ids" minItems:"1" doc:"Active applications on the job"`
}

func (in SprintInput) service() service.SprintInput {
	return service.SprintInput{
		JobID: in.JobID, StageID: in.StageID, Name: in.Name, StartsAt: in.StartsAt,
		RoundSeconds: in.RoundSeconds, BreakSeconds: in.BreakSeconds,
		InterviewerIDs: in.InterviewerIDs, ApplicationIDs: in.ApplicationIDs,
	}
}

type createSprintInput struct{ Body SprintInput }

type updateSprintInput struct {
	SprintID uuid.UUID `path:"sprint_id"`
	Body     SprintInput
}

type rateInput struct {
	SprintID  uuid.UUID `path:"sprint_id"`
	PairingID uuid.UUID `path:"pairing_id"`
	Body      struct {
		Score          int    `json:"score" minimum:"1" maximum:"5"`
		Recommendation string `json:"recommendation" enum:"strong_yes,yes,no,strong_no"`
		Note           string `json:"note,omitempty" maxLength:"240"`
	}
}

type pairingOutput struct{ Body SprintPairing }

type sprintSummaryOutput struct {
	Body struct {
		Sprint Sprint             `json:"sprint"`
		Rows   []SprintSummaryRow `json:"rows" doc:"Best first"`
	}
}

func (h sprintHandlers) now() time.Time {
	if h.d.Sprints != nil && h.d.Sprints.Now != nil {
		return h.d.Sprints.Now()
	}
	return time.Now()
}

func (h sprintHandlers) list(ctx context.Context, _ *struct{}) (*sprintsOutput, error) {
	items, err := h.d.Sprints.List(ctx, principal(ctx))
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &sprintsOutput{}
	out.Body.Sprints = make([]SprintListItem, 0, len(items))
	for _, s := range items {
		out.Body.Sprints = append(out.Body.Sprints, SprintListItem{
			ID: s.ID, JobID: s.JobID, JobTitle: s.JobTitle, StageID: s.StageID, Name: s.Name, Status: s.Status,
			StartsAt: s.StartsAt, Rounds: s.Rounds(), Candidates: s.Candidates, Interviewers: s.Interviewers,
		})
	}
	return out, nil
}

func (h sprintHandlers) create(ctx context.Context, in *createSprintInput) (*sprintOutput, error) {
	sp, err := h.d.Sprints.Create(ctx, principal(ctx), in.Body.service())
	if err != nil {
		return nil, problemDetail(err)
	}
	return &sprintOutput{Body: sprintView(sp, h.now())}, nil
}

func (h sprintHandlers) get(ctx context.Context, in *sprintInput) (*sprintOutput, error) {
	sp, err := h.d.Sprints.Get(ctx, principal(ctx), in.SprintID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &sprintOutput{Body: sprintView(sp, h.now())}, nil
}

func (h sprintHandlers) update(ctx context.Context, in *updateSprintInput) (*sprintOutput, error) {
	sp, err := h.d.Sprints.Update(ctx, principal(ctx), in.SprintID, in.Body.service())
	if err != nil {
		return nil, problemDetail(err)
	}
	return &sprintOutput{Body: sprintView(sp, h.now())}, nil
}

func (h sprintHandlers) schedule(ctx context.Context, in *sprintInput) (*sprintOutput, error) {
	sp, err := h.d.Sprints.Schedule(ctx, principal(ctx), in.SprintID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &sprintOutput{Body: sprintView(sp, h.now())}, nil
}

func (h sprintHandlers) start(ctx context.Context, in *sprintInput) (*sprintOutput, error) {
	sp, err := h.d.Sprints.StartNow(ctx, principal(ctx), in.SprintID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &sprintOutput{Body: sprintView(sp, h.now())}, nil
}

func (h sprintHandlers) cancel(ctx context.Context, in *sprintInput) (*struct{}, error) {
	if err := h.d.Sprints.Cancel(ctx, principal(ctx), in.SprintID); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

func (h sprintHandlers) rate(ctx context.Context, in *rateInput) (*pairingOutput, error) {
	pr, err := h.d.Sprints.Rate(ctx, principal(ctx), in.PairingID, service.RatingInput{
		Score: in.Body.Score, Recommendation: in.Body.Recommendation, Note: in.Body.Note,
	})
	if err != nil {
		return nil, problemDetail(err)
	}
	sp, err := h.d.Sprints.Get(ctx, principal(ctx), in.SprintID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &pairingOutput{Body: pairingView(pr, sp)}, nil
}

func (h sprintHandlers) summary(ctx context.Context, in *sprintInput) (*sprintSummaryOutput, error) {
	sum, err := h.d.Sprints.Summary(ctx, principal(ctx), in.SprintID)
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &sprintSummaryOutput{}
	out.Body.Sprint = sprintView(sum.Sprint, h.now())
	out.Body.Rows = make([]SprintSummaryRow, 0, len(sum.Rows))
	for _, row := range sum.Rows {
		out.Body.Rows = append(out.Body.Rows, SprintSummaryRow{
			ApplicationID: row.Candidate.ApplicationID, CandidateName: row.Candidate.Name, Rated: row.Rated, Mean: row.Mean,
			StrongYes: row.StrongYes, Yes: row.Yes, No: row.No, StrongNo: row.StrongNo,
		})
	}
	return out, nil
}
