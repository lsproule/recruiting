package domain

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// StageKind is what happens when an application enters a stage.
type StageKind string

const (
	StageGeneric      StageKind = "generic"
	StageInterview    StageKind = "interview"
	StageAssessment   StageKind = "assessment"
	StageClientReview StageKind = "client_review"
	StageTerminal     StageKind = "terminal"
)

// StageKinds is every kind, in the order editors list them.
var StageKinds = []StageKind{StageGeneric, StageInterview, StageAssessment, StageClientReview, StageTerminal}

// Valid reports whether k is one of the known kinds.
func (k StageKind) Valid() bool {
	for _, known := range StageKinds {
		if k == known {
			return true
		}
	}
	return false
}

// ApplicationStatus is where an application stands: active, or the outcome it
// closed with.
type ApplicationStatus string

const (
	StatusActive    ApplicationStatus = "active"
	StatusHired     ApplicationStatus = "hired"
	StatusRejected  ApplicationStatus = "rejected"
	StatusWithdrawn ApplicationStatus = "withdrawn"
)

// ActorRole is the role a mover acts under.
type ActorRole string

const (
	ActorAdmin     ActorRole = "admin"
	ActorRecruiter ActorRole = "recruiter"
	ActorVetter    ActorRole = "vetter"
	ActorClient    ActorRole = "client"
	ActorSystem    ActorRole = "system"
)

// Stage is one column of a job's pipeline. Terminal carries the outcome a
// terminal stage closes an application with and is empty on every other kind.
type Stage struct {
	ID       uuid.UUID
	Position int
	Name     string
	Kind     StageKind
	Unblind  bool
	Terminal ApplicationStatus
}

// Application is the subset of an application the move rules read.
type Application struct {
	ID      uuid.UUID
	StageID uuid.UUID
	Status  ApplicationStatus
}

// Prereqs is what the source stage has collected for the application.
type Prereqs struct{ HasScorecard, HasVerdict bool }

// Satisfies reports whether leaving a stage of kind k needs nothing more.
func (p Prereqs) Satisfies(k StageKind) bool {
	switch k {
	case StageInterview:
		return p.HasScorecard
	case StageAssessment:
		return p.HasVerdict
	}
	return true
}

// MoveRequest is one attempt to move an application to another stage.
type MoveRequest struct {
	ApplicationID  uuid.UUID
	ToStageID      uuid.UUID
	Reason         string // required for reject and for overrides
	OverridePrereq bool   // bypass scorecard/verdict prerequisite; Reason required
}

var (
	ErrForbiddenMove  = errors.New("domain: that move is not permitted")
	ErrPrereqMissing  = errors.New("domain: the stage's prerequisite is missing")
	ErrReasonRequired = errors.New("domain: a reason is required")
	ErrTerminal       = errors.New("domain: the application is already closed")
)

// ValidateMove decides whether actor may move app from one stage to another.
// It is pure: the caller loads the application, both stages, and the
// prerequisites the source stage has collected.
//
// Who may leave a stage depends on its kind: a generic stage is the
// recruiter's (or admin's); an interview or assessment stage also lets the
// vetter advance the application once their scorecard or verdict is in; a
// client review stage lets the client move to another client review or
// reject; a terminal stage is left by nobody. Rejecting always needs a
// reason. A missing prerequisite blocks the move unless a recruiter or admin
// overrides it, with a reason.
func ValidateMove(actor ActorRole, app Application, from, to Stage, prereqs Prereqs, req MoveRequest) error {
	if app.Status != StatusActive || from.Kind == StageTerminal {
		return ErrTerminal
	}
	if req.ApplicationID != app.ID || req.ToStageID != to.ID || app.StageID != from.ID || from.ID == to.ID {
		return ErrForbiddenMove
	}
	reason := strings.TrimSpace(req.Reason) != ""
	rejecting := to.Kind == StageTerminal && to.Terminal == StatusRejected
	owner := actor == ActorAdmin || actor == ActorRecruiter
	if req.OverridePrereq && !owner {
		return ErrForbiddenMove
	}
	switch from.Kind {
	case StageGeneric:
		if !owner {
			return ErrForbiddenMove
		}
	case StageInterview, StageAssessment:
		// A vetter advances only: forward, and never into a terminal stage.
		if !owner && !(actor == ActorVetter && to.Kind != StageTerminal && to.Position > from.Position) {
			return ErrForbiddenMove
		}
	case StageClientReview:
		if !owner && !(actor == ActorClient && (to.Kind == StageClientReview || rejecting)) {
			return ErrForbiddenMove
		}
	default:
		return ErrForbiddenMove
	}
	if rejecting && !reason {
		return ErrReasonRequired
	}
	if req.OverridePrereq && !reason {
		return ErrReasonRequired
	}
	if !prereqs.Satisfies(from.Kind) && !req.OverridePrereq {
		return ErrPrereqMissing
	}
	return nil
}

// ErrInvalidPipeline is every pipeline-shape rejection; the wrapped text names
// the rule that broke so a form can show it inline.
var ErrInvalidPipeline = errors.New("pipeline is not valid")

// ValidatePipeline enforces a job pipeline's shape: every stage named and of a
// known kind, exactly one terminal stage per outcome, and somewhere for an
// application to live before it closes.
func ValidatePipeline(stages []Stage) error {
	var hired, rejected, open int
	for _, s := range stages {
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("%w: every stage needs a name", ErrInvalidPipeline)
		}
		if !s.Kind.Valid() {
			return fmt.Errorf("%w: %q is not a stage kind", ErrInvalidPipeline, s.Kind)
		}
		if s.Kind != StageTerminal {
			if s.Terminal != "" {
				return fmt.Errorf("%w: only a terminal stage closes an application, and %q is %s", ErrInvalidPipeline, s.Name, s.Kind)
			}
			open++
			continue
		}
		switch s.Terminal {
		case StatusHired:
			hired++
		case StatusRejected:
			rejected++
		default:
			return fmt.Errorf("%w: terminal stage %q must close applications as hired or rejected", ErrInvalidPipeline, s.Name)
		}
	}
	switch {
	case hired != 1:
		return fmt.Errorf("%w: exactly one stage must close applications as hired, found %d", ErrInvalidPipeline, hired)
	case rejected != 1:
		return fmt.Errorf("%w: exactly one stage must close applications as rejected, found %d", ErrInvalidPipeline, rejected)
	case open == 0:
		return fmt.Errorf("%w: a pipeline needs at least one stage that is not terminal", ErrInvalidPipeline)
	}
	return nil
}
