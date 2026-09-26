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
	// StageSprint is a screening sprint: every candidate in the stage meets
	// every interviewer for a few minutes each, on a clock.
	StageSprint   StageKind = "sprint"
	StageTerminal StageKind = "terminal"
)

// StageKinds is every kind, in the order editors list them.
var StageKinds = []StageKind{StageGeneric, StageInterview, StageAssessment, StageSprint, StageClientReview, StageTerminal}

// Interview formats: a phone call the platform only books, or a video room
// it hosts with a shared editor.
const (
	FormatCall  = "call"
	FormatVideo = "video"
)

// InterviewFormats is every format, in the order editors list them.
var InterviewFormats = []string{FormatCall, FormatVideo}

// Defaults a stage takes when its kind needs a number and the editor gave
// none, and the bounds the editor is held to.
const (
	DefaultInterviewMinutes = 30
	MinInterviewMinutes     = 5
	MaxInterviewMinutes     = 8 * 60
	DefaultRoundSeconds     = 5 * 60
	MinRoundSeconds         = 15
	MaxRoundSeconds         = 60 * 60
	DefaultBreakSeconds     = 60
	MaxBreakSeconds         = 60 * 60
)

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
	// DefaultVetterID is the interviewer an interview stage assigns when the
	// application names none; Nil when the stage has no default.
	DefaultVetterID uuid.UUID
	// InterviewFormat is FormatCall or FormatVideo on an interview stage and
	// empty elsewhere; DurationMinutes is how long the interview is booked
	// for.
	InterviewFormat string
	DurationMinutes int
	// RoundSeconds and BreakSeconds are a sprint stage's round clock and
	// zero elsewhere.
	RoundSeconds int
	BreakSeconds int
}

// NormalizeStage fills in the defaults a stage's kind needs and clears the
// settings that belong to other kinds, so a stage stored after a kind change
// carries nothing stale.
func NormalizeStage(s Stage) Stage {
	switch s.Kind {
	case StageInterview:
		if s.InterviewFormat == "" {
			s.InterviewFormat = FormatCall
		}
		if s.DurationMinutes == 0 {
			s.DurationMinutes = DefaultInterviewMinutes
		}
		s.RoundSeconds, s.BreakSeconds = 0, 0
	case StageSprint:
		if s.RoundSeconds == 0 {
			s.RoundSeconds = DefaultRoundSeconds
		}
		if s.BreakSeconds == 0 {
			s.BreakSeconds = DefaultBreakSeconds
		}
		s.InterviewFormat, s.DurationMinutes, s.DefaultVetterID = "", 0, uuid.Nil
	default:
		s.InterviewFormat, s.DurationMinutes, s.DefaultVetterID = "", 0, uuid.Nil
		s.RoundSeconds, s.BreakSeconds = 0, 0
	}
	if s.Kind != StageTerminal {
		s.Terminal = ""
	}
	return s
}

// ValidateStageSettings checks the numbers a kind carries against their
// bounds. It expects a normalised stage.
func ValidateStageSettings(s Stage) error {
	switch s.Kind {
	case StageInterview:
		if s.InterviewFormat != FormatCall && s.InterviewFormat != FormatVideo {
			return fmt.Errorf("%w: %q is not an interview format", ErrInvalidPipeline, s.InterviewFormat)
		}
		if s.DurationMinutes < MinInterviewMinutes || s.DurationMinutes > MaxInterviewMinutes {
			return fmt.Errorf("%w: interview %q must last between %d and %d minutes", ErrInvalidPipeline, s.Name, MinInterviewMinutes, MaxInterviewMinutes)
		}
	case StageSprint:
		if s.RoundSeconds < MinRoundSeconds || s.RoundSeconds > MaxRoundSeconds {
			return fmt.Errorf("%w: sprint %q rounds must last between %d seconds and an hour", ErrInvalidPipeline, s.Name, MinRoundSeconds)
		}
		if s.BreakSeconds < 0 || s.BreakSeconds > MaxBreakSeconds {
			return fmt.Errorf("%w: sprint %q breaks must be between zero and an hour", ErrInvalidPipeline, s.Name)
		}
	}
	return nil
}

// Application is the subset of an application the move rules read.
type Application struct {
	ID      uuid.UUID
	StageID uuid.UUID
	Status  ApplicationStatus
}

// Prereqs is what the source stage has collected for the application: a
// scorecard on an interview stage, a verdict on an assessment stage, at
// least one interviewer's rating on a sprint stage.
type Prereqs struct{ HasScorecard, HasVerdict, HasRating bool }

// Satisfies reports whether leaving a stage of kind k needs nothing more.
func (p Prereqs) Satisfies(k StageKind) bool {
	switch k {
	case StageInterview:
		return p.HasScorecard
	case StageAssessment:
		return p.HasVerdict
	case StageSprint:
		return p.HasRating
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
	case StageGeneric, StageSprint:
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
		if err := ValidateStageSettings(s); err != nil {
			return err
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
