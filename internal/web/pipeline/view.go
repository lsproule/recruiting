package pipeline

import (
	"strconv"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

// moveProblem is a refused move: the screen re-renders around it so the
// mover can add the reason, or confirm the override, and try again.
type moveProblem struct {
	ApplicationID uuid.UUID
	ToStageID     uuid.UUID
	Reason        string
	Message       string
	NeedsOverride bool // prerequisite missing: show the override confirmation
	NeedsReason   bool
}

func (m moveProblem) Empty() bool { return m.Message == "" }

// listFilter is the list screen's query string, kept as typed so the form
// redraws with what was asked.
type listFilter struct {
	StageID string
	Status  string
	Query   string
}

// Statuses an application can be filtered by.
var statuses = []domain.ApplicationStatus{domain.StatusActive, domain.StatusHired, domain.StatusRejected, domain.StatusWithdrawn}

func applicationPath(id uuid.UUID) string { return ApplicationPrefix + "/" + id.String() }
func boardPath(jobID uuid.UUID) string    { return BoardPrefix + "/" + jobID.String() }
func listPath(jobID uuid.UUID) string     { return boardPath(jobID) + "/list" }

func when(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

func count(n int) string { return strconv.Itoa(n) }

// eventLine describes one timeline entry in words.
func eventLine(e service.ApplicationEvent) string {
	switch e.Kind {
	case service.EventMoved:
		s := "Moved from " + e.FromStage + " to " + e.ToStage
		if e.Override {
			s += " (prerequisite overridden)"
		}
		return s
	case service.EventWithdrawn:
		return "Withdrawn"
	case service.EventReleased:
		return "Released to the client"
	case service.EventUnreleased:
		return "Hidden from the client"
	case "applied":
		if e.ToStage != "" {
			return "Applied into " + e.ToStage
		}
		return "Applied"
	}
	return e.Kind
}

// moveTargets lists the stages an application can be moved to from the page:
// every other stage of the job.
func moveTargets(d service.ApplicationDetail) []domain.Stage {
	out := make([]domain.Stage, 0, len(d.Stages))
	for _, s := range d.Stages {
		if s.ID != d.Application.StageID {
			out = append(out, s)
		}
	}
	return out
}

func stageLabel(s domain.Stage) string {
	if s.Kind == domain.StageTerminal {
		return s.Name + " (closes as " + string(s.Terminal) + ")"
	}
	return s.Name + " (" + string(s.Kind) + ")"
}
