package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/domain"
	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// Application event kinds this file writes. applied is in candidate.go.
const (
	EventMoved      = "moved"
	EventWithdrawn  = "withdrawn"
	EventReleased   = "released"
	EventUnreleased = "unreleased"
)

// BookingLinkTTL is how long a candidate has to pick an interview time from
// the invite a move into an interview stage sends.
const BookingLinkTTL = 7 * 24 * time.Hour

// bookingPath is where a book link lands; the booking surface serves it.
const bookingPath = "/book/"

var (
	ErrNotActive = errors.New("service: the application is already closed")
	ErrStale     = errors.New("service: the application moved; reload")
)

// Application is one candidate on one job, as the pipeline screens show it.
type Application struct {
	ID             uuid.UUID
	JobID          uuid.UUID
	JobTitle       string
	CandidateID    uuid.UUID
	CandidateName  string
	CandidateEmail string
	StageID        uuid.UUID
	StageName      string
	StagePosition  int
	StageKind      domain.StageKind
	Status         domain.ApplicationStatus
	Released       bool
	ReleasedAt     time.Time
	HighQuality    bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Active reports whether the application can still move.
func (a Application) Active() bool { return a.Status == domain.StatusActive }

// ApplicationEvent is one line of an application's timeline.
type ApplicationEvent struct {
	ID        uuid.UUID
	ActorKind string
	ActorID   uuid.UUID
	Kind      string
	FromStage string
	ToStage   string
	Reason    string
	Override  bool
	CreatedAt time.Time
}

// ApplicationDetail is the application page: the application, the pipeline
// it moves through, and its timeline.
type ApplicationDetail struct {
	Application Application
	Stages      []domain.Stage
	Events      []ApplicationEvent
}

// BoardColumn is one stage of the board with the applications in it.
type BoardColumn struct {
	Stage domain.Stage
	Cards []Application
}

// Board is a job's pipeline laid out as columns.
type Board struct {
	Job     Job
	Columns []BoardColumn
}

// ListFilter narrows a job's application list. Zero values do not filter.
type ListFilter struct {
	StageID uuid.UUID
	Status  domain.ApplicationStatus
	Query   string // matched against candidate name and email, case-insensitively
}

// MoveRequest is the service form of domain.MoveRequest.
type MoveRequest = domain.MoveRequest

// PrereqLoader reports what the application's current stage has collected.
// The default checks for a scorecard on the stage and a verdict on an
// attempt of the stage; the scorecard and assessment surfaces may replace it.
type PrereqLoader func(ctx context.Context, tx *store.Tx, app domain.Application, from domain.Stage) (domain.Prereqs, error)

// ApplicationService moves applications through their pipelines, writes the
// audit trail, and queues the side effects a stage entry triggers.
type ApplicationService struct {
	st      *store.Store
	q       *queue.Client
	baseURL string
	// Prereqs is consulted before an application leaves an interview or
	// assessment stage.
	Prereqs PrereqLoader
}

// NewApplicationService wires the store, the queue the side effects go to,
// and the base URL the booking link is built on. A nil queue disables the
// side effects, which suits tests of the moves alone.
func NewApplicationService(st *store.Store, q *queue.Client, baseURL string) *ApplicationService {
	return &ApplicationService{st: st, q: q, baseURL: strings.TrimRight(baseURL, "/"), Prereqs: defaultPrereqs}
}

// defaultPrereqs reads the scorecard and review tables directly.
func defaultPrereqs(ctx context.Context, tx *store.Tx, app domain.Application, from domain.Stage) (domain.Prereqs, error) {
	var p domain.Prereqs
	var err error
	switch from.Kind {
	case domain.StageInterview:
		p.HasScorecard, err = tx.Q.HasScorecardForStage(ctx, db.HasScorecardForStageParams{ApplicationID: app.ID, StageID: from.ID})
	case domain.StageAssessment:
		p.HasVerdict, err = tx.Q.HasVerdictForStage(ctx, db.HasVerdictForStageParams{ApplicationID: app.ID, StageID: from.ID})
	}
	return p, err
}

// actorRole is the role p moves under. An org user with several roles acts
// under the strongest; the domain matrix is monotone in that order.
func actorRole(p Principal) domain.ActorRole {
	switch p.Kind {
	case PrincipalOrgUser:
		switch {
		case p.HasRole(RoleAdmin):
			return domain.ActorAdmin
		case p.HasRole(RoleRecruiter):
			return domain.ActorRecruiter
		case p.HasRole(RoleVetter):
			return domain.ActorVetter
		}
	case PrincipalClientUser:
		return domain.ActorClient
	}
	return domain.ActorSystem
}

// actorKind is the application_event actor_kind for p.
func actorKind(p Principal) string {
	switch p.Kind {
	case PrincipalOrgUser:
		return "org_user"
	case PrincipalClientUser:
		return "client_user"
	case PrincipalMagicLink:
		return "candidate"
	}
	return "system"
}

// Move takes an application to another stage of its job. The domain rules
// decide whether p may; the event and any stage-entry side effect commit
// with the move or not at all. The whole move is one transaction holding a
// row lock, so concurrent moves of one application serialise and the loser
// sees the winner's stage and is refused.
func (s *ApplicationService) Move(ctx context.Context, p Principal, req MoveRequest) (Application, error) {
	if p.Kind != PrincipalOrgUser && p.Kind != PrincipalClientUser {
		return Application{}, ErrForbidden
	}
	req.Reason = strings.TrimSpace(req.Reason)
	role := actorRole(p)
	var out Application
	err := s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		app, err := lockApplication(ctx, tx, p, req.ApplicationID)
		if err != nil {
			return err
		}
		stages, err := listStages(ctx, tx, app.JobID)
		if err != nil {
			return err
		}
		from, ok := findStage(stages, app.StageID)
		if !ok {
			return fmt.Errorf("application %s sits in a stage its job does not have", app.ID)
		}
		to, ok := findStage(stages, req.ToStageID)
		if !ok {
			return ErrNotFound
		}
		if app.Status == string(domain.StatusActive) && to.ID == from.ID {
			// Another mover got here first, or the screen was stale.
			return ErrStale
		}
		dapp := domain.Application{ID: app.ID, StageID: app.StageID, Status: domain.ApplicationStatus(app.Status)}
		prereqs, err := s.Prereqs(ctx, tx, dapp, from)
		if err != nil {
			return err
		}
		if err := domain.ValidateMove(role, dapp, from, to, prereqs, req); err != nil {
			return err
		}
		status := domain.StatusActive
		if to.Kind == domain.StageTerminal {
			status = to.Terminal
		}
		if _, err := tx.Q.MoveApplication(ctx, db.MoveApplicationParams{ID: app.ID, StageID: to.ID, Status: string(status), FromStageID: from.ID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrStale
			}
			return err
		}
		override := req.OverridePrereq && !prereqs.Satisfies(from.Kind)
		_, err = tx.Q.CreateApplicationEvent(ctx, db.CreateApplicationEventParams{
			OrgID: p.OrgID, ApplicationID: app.ID, ActorKind: actorKind(p),
			ActorID:     uuid.NullUUID{UUID: p.UserID, Valid: p.UserID != uuid.Nil},
			Kind:        EventMoved,
			FromStageID: uuid.NullUUID{UUID: from.ID, Valid: true},
			ToStageID:   uuid.NullUUID{UUID: to.ID, Valid: true},
			Reason:      nullable(req.Reason),
			Payload:     []byte(fmt.Sprintf(`{"override":%t}`, override)),
		})
		if err != nil {
			return err
		}
		card, err := tx.Q.GetApplicationCard(ctx, app.ID)
		if err != nil {
			return err
		}
		if err := s.onEnter(ctx, tx, p.OrgID, card, to); err != nil {
			return err
		}
		out, err = s.card(ctx, tx, app.ID)
		return err
	})
	if err != nil {
		return Application{}, wrapMove("move application", err)
	}
	return out, nil
}

// lockApplication loads the application under a row lock. The transaction
// runs as the org, so a client's visibility — released, and their company's
// — is enforced here rather than by RLS; anything else is not found.
func lockApplication(ctx context.Context, tx *store.Tx, p Principal, id uuid.UUID) (db.Application, error) {
	app, err := tx.Q.GetApplicationForUpdate(ctx, id)
	if err != nil {
		return db.Application{}, err
	}
	if p.Kind == PrincipalClientUser && (!app.ReleasedAt.Valid || app.ClientCompanyID != p.ClientCompanyID) {
		return db.Application{}, ErrNotFound
	}
	return app, nil
}

// onEnter queues what entering a stage triggers. It runs inside the move's
// transaction, and the queue insert is transactional, so a move that rolls
// back sends nothing.
func (s *ApplicationService) onEnter(ctx context.Context, tx *store.Tx, orgID uuid.UUID, app db.GetApplicationCardRow, to domain.Stage) error {
	if s.q == nil {
		return nil
	}
	switch to.Kind {
	case domain.StageInterview:
		// A link from an earlier visit to an interview stage would still
		// book; only the newest invite may.
		if err := tx.Q.RevokeBookLinks(ctx, app.ID); err != nil {
			return err
		}
		token, hash, err := newToken()
		if err != nil {
			return err
		}
		expires := time.Now().Add(BookingLinkTTL)
		if _, err := tx.Q.CreateMagicLink(ctx, db.CreateMagicLinkParams{
			OrgID: orgID, TokenHash: hash, Purpose: LinkBook, SubjectID: app.ID, ExpiresAt: ts(expires),
		}); err != nil {
			return err
		}
		return enqueued(s.q.Enqueue(ctx, tx, queue.KindEmailSend, queue.EmailPayload{
			Template: mail.TemplateBookingInvite, To: app.CandidateEmail, OrgID: orgID,
			Data: map[string]any{
				"CandidateName": app.CandidateName,
				"JobTitle":      app.JobTitle,
				"BookingURL":    s.baseURL + bookingPath + token,
				"ExpiresAt":     expires.UTC().Format("2006-01-02 15:04 UTC"),
			},
		}))
	case domain.StageAssessment:
		// No attempt exists yet; the assessment surface creates one from the
		// application and stage the payload names.
		return enqueued(s.q.Enqueue(ctx, tx, queue.KindAssessmentInvite, AssessmentInvitePayload{
			ApplicationID: app.ID, StageID: to.ID, OrgID: orgID,
		}))
	}
	return nil
}

// AssessmentInvitePayload is the assessment.invite payload a stage entry
// enqueues. AttemptID is empty until the assessment surface creates one.
type AssessmentInvitePayload struct {
	ApplicationID uuid.UUID `json:"application_id"`
	StageID       uuid.UUID `json:"stage_id"`
	OrgID         uuid.UUID `json:"org_id"`
	AttemptID     uuid.UUID `json:"attempt_id,omitempty"`
}

// Withdraw closes an active application as withdrawn where it stands. It is
// a recruiter's or admin's record of the candidate's decision, not a move.
func (s *ApplicationService) Withdraw(ctx context.Context, p Principal, id uuid.UUID, reason string) (Application, error) {
	if err := requireRecruiter(p); err != nil {
		return Application{}, err
	}
	reason = strings.TrimSpace(reason)
	var out Application
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		app, err := tx.Q.GetApplicationForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if app.Status != string(domain.StatusActive) {
			return ErrNotActive
		}
		if _, err := tx.Q.CloseApplication(ctx, db.CloseApplicationParams{ID: id, Status: string(domain.StatusWithdrawn)}); err != nil {
			return err
		}
		if err := s.event(ctx, tx, p, id, EventWithdrawn, reason); err != nil {
			return err
		}
		out, err = s.card(ctx, tx, id)
		return err
	})
	if err != nil {
		return Application{}, wrapMove("withdraw application", err)
	}
	return out, nil
}

// Release makes the application visible to the client company's users.
func (s *ApplicationService) Release(ctx context.Context, p Principal, id uuid.UUID) (Application, error) {
	return s.setReleased(ctx, p, id, true)
}

// Unrelease hides the application from the client again.
func (s *ApplicationService) Unrelease(ctx context.Context, p Principal, id uuid.UUID) (Application, error) {
	return s.setReleased(ctx, p, id, false)
}

func (s *ApplicationService) setReleased(ctx context.Context, p Principal, id uuid.UUID, released bool) (Application, error) {
	if err := requireRecruiter(p); err != nil {
		return Application{}, err
	}
	var out Application
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		app, err := tx.Q.GetApplicationForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if app.ReleasedAt.Valid == released {
			// Already in the asked-for state: nothing to record.
			out, err = s.card(ctx, tx, id)
			return err
		}
		at := ts(time.Now())
		if !released {
			at.Valid = false
		}
		if _, err := tx.Q.SetApplicationReleased(ctx, db.SetApplicationReleasedParams{ID: id, ReleasedAt: at}); err != nil {
			return err
		}
		kind := EventUnreleased
		if released {
			kind = EventReleased
		}
		if err := s.event(ctx, tx, p, id, kind, ""); err != nil {
			return err
		}
		out, err = s.card(ctx, tx, id)
		return err
	})
	if err != nil {
		return Application{}, wrapMove("release application", err)
	}
	return out, nil
}

// event appends a stage-less event to the application's timeline.
func (s *ApplicationService) event(ctx context.Context, tx *store.Tx, p Principal, id uuid.UUID, kind, reason string) error {
	_, err := tx.Q.CreateApplicationEvent(ctx, db.CreateApplicationEventParams{
		OrgID: p.OrgID, ApplicationID: id, ActorKind: actorKind(p),
		ActorID: uuid.NullUUID{UUID: p.UserID, Valid: p.UserID != uuid.Nil},
		Kind:    kind, Reason: nullable(reason), Payload: []byte("{}"),
	})
	return err
}

// Application loads one application for any org user, or for a client user
// once it is released to their company.
func (s *ApplicationService) Application(ctx context.Context, p Principal, id uuid.UUID) (Application, error) {
	if p.Kind != PrincipalOrgUser && p.Kind != PrincipalClientUser {
		return Application{}, ErrForbidden
	}
	var out Application
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		out, err = s.card(ctx, tx, id)
		return err
	})
	if err != nil {
		return Application{}, wrapMove("get application", err)
	}
	return out, nil
}

// Detail is the application page: the application, its job's stages, and
// its timeline.
func (s *ApplicationService) Detail(ctx context.Context, p Principal, id uuid.UUID) (ApplicationDetail, error) {
	if p.Kind != PrincipalOrgUser && p.Kind != PrincipalClientUser {
		return ApplicationDetail{}, ErrForbidden
	}
	var out ApplicationDetail
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		if out.Application, err = s.card(ctx, tx, id); err != nil {
			return err
		}
		if out.Stages, err = listStages(ctx, tx, out.Application.JobID); err != nil {
			return err
		}
		out.Events, err = events(ctx, tx, id, out.Stages)
		return err
	})
	if err != nil {
		return ApplicationDetail{}, wrapMove("get application", err)
	}
	return out, nil
}

// Events is the application's timeline, oldest first.
func (s *ApplicationService) Events(ctx context.Context, p Principal, id uuid.UUID) ([]ApplicationEvent, error) {
	d, err := s.Detail(ctx, p, id)
	if err != nil {
		return nil, err
	}
	return d.Events, nil
}

// Board lays a job's applications out by stage. Closed applications sit in
// the terminal column that closed them; a withdrawn one stays where it was.
func (s *ApplicationService) Board(ctx context.Context, p Principal, jobID uuid.UUID) (Board, error) {
	if p.Kind != PrincipalOrgUser {
		return Board{}, ErrForbidden
	}
	var out Board
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		job, err := tx.Q.GetJob(ctx, jobID)
		if err != nil {
			return err
		}
		names, err := companyNames(ctx, tx, p.OrgID)
		if err != nil {
			return err
		}
		out.Job = toJob(job, names[job.ClientCompanyID])
		stages, err := listStages(ctx, tx, jobID)
		if err != nil {
			return err
		}
		rows, err := tx.Q.ListJobApplicationCards(ctx, jobID)
		if err != nil {
			return err
		}
		byStage := make(map[uuid.UUID][]Application, len(stages))
		for _, row := range rows {
			byStage[row.StageID] = append(byStage[row.StageID], cardFromList(row))
		}
		out.Columns = make([]BoardColumn, 0, len(stages))
		for _, st := range stages {
			cards := byStage[st.ID]
			if cards == nil {
				cards = []Application{}
			}
			out.Columns = append(out.Columns, BoardColumn{Stage: st, Cards: cards})
		}
		return nil
	})
	if err != nil {
		return Board{}, wrapMove("board", err)
	}
	return out, nil
}

// List is a job's applications, filtered, in pipeline order. The search
// matches the candidate's name and resume text, or their email, and the
// result is capped like the candidate list.
func (s *ApplicationService) List(ctx context.Context, p Principal, jobID uuid.UUID, f ListFilter) ([]Application, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	var out []Application
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if err := requireJob(ctx, tx, jobID); err != nil {
			return err
		}
		rows, err := tx.Q.FilterJobApplicationCards(ctx, db.FilterJobApplicationCardsParams{
			JobID:   jobID,
			StageID: uuid.NullUUID{UUID: f.StageID, Valid: f.StageID != uuid.Nil},
			Status:  string(f.Status), Query: strings.TrimSpace(f.Query), RowLimit: SearchLimit,
		})
		if err != nil {
			return err
		}
		out = make([]Application, 0, len(rows))
		for _, row := range rows {
			out = append(out, cardFromList(db.ListJobApplicationCardsRow(row)))
		}
		return nil
	})
	if err != nil {
		return nil, wrapMove("list applications", err)
	}
	return out, nil
}

func (s *ApplicationService) card(ctx context.Context, tx *store.Tx, id uuid.UUID) (Application, error) {
	row, err := tx.Q.GetApplicationCard(ctx, id)
	if err != nil {
		return Application{}, err
	}
	a := cardFromList(db.ListJobApplicationCardsRow{
		ID: row.ID, JobID: row.JobID, CandidateID: row.CandidateID, StageID: row.StageID, Status: row.Status,
		ReleasedAt: row.ReleasedAt, HighQuality: row.HighQuality, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		CandidateName: row.CandidateName, CandidateEmail: row.CandidateEmail,
		StageName: row.StageName, StagePosition: row.StagePosition, StageKind: row.StageKind,
	})
	a.JobTitle = row.JobTitle
	return a, nil
}

func cardFromList(row db.ListJobApplicationCardsRow) Application {
	return Application{
		ID: row.ID, JobID: row.JobID, CandidateID: row.CandidateID,
		CandidateName: row.CandidateName, CandidateEmail: row.CandidateEmail,
		StageID: row.StageID, StageName: row.StageName, StagePosition: int(row.StagePosition),
		StageKind: domain.StageKind(row.StageKind), Status: domain.ApplicationStatus(row.Status),
		Released: row.ReleasedAt.Valid, ReleasedAt: row.ReleasedAt.Time,
		HighQuality: row.HighQuality, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func events(ctx context.Context, tx *store.Tx, id uuid.UUID, stages []domain.Stage) ([]ApplicationEvent, error) {
	rows, err := tx.Q.ListApplicationEvents(ctx, id)
	if err != nil {
		return nil, err
	}
	name := func(id uuid.NullUUID) string {
		if !id.Valid {
			return ""
		}
		if st, ok := findStage(stages, id.UUID); ok {
			return st.Name
		}
		return "(removed stage)"
	}
	out := make([]ApplicationEvent, 0, len(rows))
	for _, r := range rows {
		var payload struct {
			Override bool `json:"override"`
		}
		_ = json.Unmarshal(r.Payload, &payload)
		out = append(out, ApplicationEvent{
			ID: r.ID, ActorKind: r.ActorKind, ActorID: r.ActorID.UUID, Kind: r.Kind,
			FromStage: name(r.FromStageID), ToStage: name(r.ToStageID), Reason: deref(r.Reason),
			Override: payload.Override, CreatedAt: r.CreatedAt.Time,
		})
	}
	return out, nil
}

func findStage(stages []domain.Stage, id uuid.UUID) (domain.Stage, bool) {
	for _, st := range stages {
		if st.ID == id {
			return st, true
		}
	}
	return domain.Stage{}, false
}

// wrapMove keeps the errors a screen shows inline unwrapped.
func wrapMove(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, domain.ErrForbiddenMove), errors.Is(err, domain.ErrPrereqMissing),
		errors.Is(err, domain.ErrReasonRequired), errors.Is(err, domain.ErrTerminal),
		errors.Is(err, ErrNotFound), errors.Is(err, ErrForbidden), errors.Is(err, ErrNotActive), errors.Is(err, ErrStale):
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}
