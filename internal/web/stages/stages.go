// Package stages is the stage editor two screens share: a job's own
// pipeline and a reusable process. Both post the same form and draw the
// same cards; only the routes differ, which the view carries as paths.
package stages

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

// Editor is one rendered editor: the stages, where its forms post, and the
// extras a job's editor has that a process's does not.
type Editor struct {
	// ID is the htmx swap target; every form re-renders the editor in place.
	ID string
	// Stages in order.
	Stages []domain.Stage
	// Vetters is who an interview stage may name as its default
	// interviewer; empty on a process, which has no job to book against.
	Vetters []service.OrgUser
	// OrderAction, AddAction take the reorder and the add forms;
	// StageAction and DeleteAction take a stage's own.
	OrderAction  string
	AddAction    string
	StageAction  func(stageID uuid.UUID) string
	DeleteAction func(stageID uuid.UUID) string
	// SetupLink is the per-stage link to what a kind needs set up (a
	// rubric, an assessment); nil or an empty return hides it.
	SetupLink func(s domain.Stage) (label, href string)
	// Error is a refused change, shown above the editor.
	Error string
	CSRF  string
	// ReadOnly draws the stages without forms, for a reader who may not
	// edit them.
	ReadOnly bool
}

// FromForm reads the stage form. Rounds are entered in minutes and breaks in
// seconds, which is how people think of them; the service stores seconds.
func FromForm(r *http.Request) service.StageInput {
	in := service.StageInput{
		Name:            strings.TrimSpace(r.PostFormValue("name")),
		Kind:            domain.StageKind(strings.TrimSpace(r.PostFormValue("kind"))),
		Terminal:        domain.ApplicationStatus(strings.TrimSpace(r.PostFormValue("terminal_status"))),
		Unblind:         r.PostFormValue("unblind") == "1",
		InterviewFormat: strings.TrimSpace(r.PostFormValue("interview_format")),
	}
	in.DurationMinutes, _ = strconv.Atoi(strings.TrimSpace(r.PostFormValue("duration_minutes")))
	if raw := strings.TrimSpace(r.PostFormValue("round_minutes")); raw != "" {
		if minutes, err := strconv.ParseFloat(raw, 64); err == nil && minutes > 0 {
			in.RoundSeconds = int(minutes*60 + 0.5)
		}
	}
	if raw := strings.TrimSpace(r.PostFormValue("round_seconds")); raw != "" {
		in.RoundSeconds, _ = strconv.Atoi(raw)
	}
	if raw := strings.TrimSpace(r.PostFormValue("break_seconds")); raw != "" {
		in.BreakSeconds, _ = strconv.Atoi(raw)
	}
	if raw := strings.TrimSpace(r.PostFormValue("default_vetter_id")); raw != "" {
		if id, err := uuid.Parse(raw); err == nil {
			in.DefaultVetterID = id
		}
	}
	in.PassScore, _ = strconv.Atoi(strings.TrimSpace(r.PostFormValue("pass_score")))
	in.AutoAdvance = r.PostFormValue("auto_advance") == "1"
	in.AutoReject = r.PostFormValue("auto_reject") == "1"
	return in
}

// passScoreValue is the pass mark box's value: blank for a stage that sets
// none rather than a misleading zero.
func passScoreValue(s domain.Stage) string {
	if s.PassScore == 0 {
		return ""
	}
	return strconv.Itoa(s.PassScore)
}

// KindLabel names a kind the way the editor lists it.
func KindLabel(k domain.StageKind) string {
	switch k {
	case domain.StageGeneric:
		return "Step"
	case domain.StageInterview:
		return "Interview"
	case domain.StageAssessment:
		return "Assessment"
	case domain.StageSprint:
		return "Screening sprint"
	case domain.StageClientReview:
		return "Client review"
	case domain.StageTerminal:
		return "Closes the application"
	}
	return string(k)
}

// KindHint says what entering a stage of the kind does.
func KindHint(k domain.StageKind) string {
	switch k {
	case domain.StageGeneric:
		return "A column on the board. Nothing happens on entry; recruiters move candidates on."
	case domain.StageInterview:
		return "Emails a booking link. The candidate books a slot with the interviewer; a scorecard is needed to move on."
	case domain.StageAssessment:
		return "Emails an assessment invite for the set attached to this stage; a verdict is needed to move on."
	case domain.StageSprint:
		return "Every candidate here meets every interviewer for a few minutes each, on a clock. A rating is needed to move on."
	case domain.StageClientReview:
		return "Released candidates are visible to the client, who advances or rejects them."
	case domain.StageTerminal:
		return "Reaching it closes the application as hired or rejected."
	}
	return ""
}

// Summary is the one-line reading of a stage's settings for a chip.
func Summary(s domain.Stage) string {
	switch s.Kind {
	case domain.StageAssessment:
		if s.PassScore == 0 {
			return ""
		}
		note := "pass mark " + strconv.Itoa(s.PassScore)
		switch {
		case s.AutoAdvance && s.AutoReject:
			note += " · decides on its own"
		case s.AutoAdvance:
			note += " · auto-advances"
		case s.AutoReject:
			note += " · auto-rejects"
		}
		return note
	case domain.StageInterview:
		format := "phone"
		if s.InterviewFormat == domain.FormatVideo {
			format = "video"
		}
		return format + " · " + strconv.Itoa(s.DurationMinutes) + " min"
	case domain.StageSprint:
		return RoundLabel(s.RoundSeconds) + " rounds · " + strconv.Itoa(s.BreakSeconds) + "s break"
	case domain.StageTerminal:
		return "closes as " + string(s.Terminal)
	case domain.StageClientReview:
		if s.Unblind {
			return "unblinds the candidate"
		}
	}
	return ""
}

// RoundLabel writes a round length in the unit it was set in.
func RoundLabel(seconds int) string {
	if seconds%60 == 0 {
		return strconv.Itoa(seconds/60) + " min"
	}
	if seconds < 60 {
		return strconv.Itoa(seconds) + " s"
	}
	return strconv.FormatFloat(float64(seconds)/60, 'f', 1, 64) + " min"
}

// RoundMinutes is the round length as the form shows it.
func RoundMinutes(seconds int) string {
	if seconds == 0 {
		seconds = domain.DefaultRoundSeconds
	}
	if seconds%60 == 0 {
		return strconv.Itoa(seconds / 60)
	}
	return strconv.FormatFloat(float64(seconds)/60, 'f', 2, 64)
}

// orderData is the pipeline handed to Alpine so reordering is local until
// saved.
type orderData struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Note string `json:"note"`
}

// OrderJSON encodes the stages for the reorder control.
func OrderJSON(stages []domain.Stage) string {
	out := make([]orderData, 0, len(stages))
	for _, s := range stages {
		out = append(out, orderData{ID: s.ID.String(), Name: s.Name, Kind: KindLabel(s.Kind), Note: Summary(s)})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func itoa(n int) string { return strconv.Itoa(n) }

// userLabel names a user in a select: their name, or their email when they
// have none.
func userLabel(u service.OrgUser) string {
	if u.Name != "" {
		return u.Name
	}
	return u.Email
}

func durationValue(s domain.Stage) string {
	if s.DurationMinutes == 0 {
		return itoa(domain.DefaultInterviewMinutes)
	}
	return itoa(s.DurationMinutes)
}

func breakValue(s domain.Stage) string {
	if s.Kind != domain.StageSprint {
		return itoa(domain.DefaultBreakSeconds)
	}
	return itoa(s.BreakSeconds)
}

func formatValue(s domain.Stage) string {
	if s.InterviewFormat == "" {
		return domain.FormatCall
	}
	return s.InterviewFormat
}

// setup is the link a stage's kind needs, or nothing.
func (e Editor) setup(s domain.Stage) (string, string) {
	if e.SetupLink == nil {
		return "", ""
	}
	return e.SetupLink(s)
}
