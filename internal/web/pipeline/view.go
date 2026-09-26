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

// userLabel names a user in a select: their name, or their email when they
// have none.
func userLabel(u service.OrgUser) string {
	if u.Name != "" {
		return u.Name
	}
	return u.Email
}

func stageLabel(s domain.Stage) string {
	if s.Kind == domain.StageTerminal {
		return s.Name + " (closes as " + string(s.Terminal) + ")"
	}
	return s.Name + " (" + string(s.Kind) + ")"
}

// candidateView is the candidate detail screen: the application, what its
// assessment recorded, how the candidate fits the job, and the résumés on
// file. Every part after the application is optional — the screen is drawn
// for an application that has never been assessed too.
type candidateView struct {
	Detail  service.ApplicationDetail
	Vetters []service.OrgUser
	// Attempt is the sitting the screen replays, newest first; nil when the
	// candidate has not sat one.
	Attempt *service.AttemptReview
	// ReplayConfig is the island's boot JSON, empty when there is nothing to
	// replay.
	ReplayConfig string
	Fit          service.ApplicationFit
	HasFit       bool
	Resumes      []service.Resume
	// Advance and Reject are the stages the header's two moves lead to; a Nil
	// ID hides that button.
	Advance domain.Stage
	Reject  domain.Stage
	// Slot is the booked interview, RoomCode what was written in its room,
	// and SprintRatings what interviewers filed in sprints; each nil or
	// empty when there is none.
	Slot          *service.InterviewRow
	RoomCode      *service.RoomCode
	SprintRatings []service.ApplicationSprintRating
	Now           time.Time
}

// AssessmentSent reports whether an invite is already in flight, which is
// what turns the "no assessment yet" card into a note about waiting.
func (v candidateView) AssessmentSent() bool {
	return v.Attempt != nil && (v.Attempt.Status == service.AttemptInvited || v.Attempt.Status == service.AttemptStarted)
}

// nextStage is the stage an advance moves to: the next one along the
// pipeline that is not terminal. A candidate at the end of the pipeline
// advances nowhere.
func nextStage(d service.ApplicationDetail) domain.Stage {
	after := false
	for _, s := range d.Stages {
		if s.ID == d.Application.StageID {
			after = true
			continue
		}
		if after && s.Kind != domain.StageTerminal {
			return s
		}
	}
	return domain.Stage{}
}

// rejectStage is the pipeline's terminal rejection, which the header's
// Reject button moves to.
func rejectStage(d service.ApplicationDetail) domain.Stage {
	for _, s := range d.Stages {
		if s.Kind == domain.StageTerminal && s.Terminal == domain.StatusRejected {
			return s
		}
	}
	return domain.Stage{}
}

// pct renders a 0–1 share as whole percent for a bar's width.
func pct(v float64) string { return strconv.Itoa(int(v*100 + 0.5)) }

// fitScore is a 0–1 fit on the 0–100 scale the screens read scores in.
func fitScore(v float64) string { return strconv.Itoa(int(v*100 + 0.5)) }

// weightLabel is a term's weight as the panel prints it: "w0.6".
func weightLabel(w float64) string { return "w" + strconv.FormatFloat(w, 'f', -1, 64) }

// scoreOf renders a stored score, or a dash when there is none.
func scoreOf(v *float64) string {
	if v == nil {
		return "—"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

// testResultClass marks a case's outcome so the table reads at a glance. A
// case the runner never reported is unrun, not failed.
func testResultClass(status string) string {
	switch status {
	case "pass":
		return "result-pass"
	case "":
		return "result-unrun"
	}
	return "result-fail"
}

func testResultLabel(status string) string {
	if status == "" {
		return "not run"
	}
	return status
}

func ms(v int64) string {
	if v <= 0 {
		return "—"
	}
	return strconv.FormatInt(v, 10)
}

// caseLabel names a case: the author's own name, or its position when the
// case was never named.
func caseLabel(t service.ReviewTest) string {
	if t.Name != "" {
		return t.Name
	}
	return "Case " + strconv.Itoa(t.Position)
}

func resumePath(r service.Resume) string {
	return "/app/candidates/" + r.CandidateID.String() + "/resumes/" + r.ID.String()
}

func day(t time.Time) string { return t.UTC().Format("2 Jan 2006") }

func at(t time.Time) string {
	if t.IsZero() {
		return "not finished"
	}
	return t.UTC().Format("2 Jan 2006 15:04 UTC")
}

// num renders a plain number without trailing zeroes.
func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func recommendationLabel(v string) string {
	switch v {
	case service.OverallStrongYes:
		return "Strong yes"
	case service.OverallYes:
		return "Yes"
	case service.OverallNo:
		return "No"
	case service.OverallStrongNo:
		return "Strong no"
	}
	return v
}
