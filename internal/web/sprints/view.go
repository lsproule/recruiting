package sprints

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

// sprintForm is the setup form's state, kept so a refused submission
// redraws with what was typed.
type sprintForm struct {
	Name         string
	StartsLocal  string
	Timezone     string
	RoundMinutes string
	BreakSeconds string
	Candidates   []uuid.UUID
	Interviewers []uuid.UUID
}

func (f sprintForm) has(ids []uuid.UUID, id uuid.UUID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// setupView is the setup screen: the stage's candidates, the interviewers
// to pick from, and the form.
type setupView struct {
	Job          service.Job
	Stage        domain.Stage
	Candidates   []service.Application
	Interviewers []service.OrgUser
	Form         sprintForm
	Error        string
}

// summaryView is the ranked summary with the moves it offers.
type summaryView struct {
	Summary   service.SprintSummary
	Now       time.Time
	CanManage bool
	UserID    uuid.UUID
	Next      domain.Stage
	Reject    domain.Stage
}

func (v summaryView) phase() domain.SprintState { return v.Summary.Sprint.Clock().At(v.Now) }

// consoleView is the interviewer's console.
type consoleView struct {
	Sprint      service.Sprint
	UserID      uuid.UUID
	Now         time.Time
	Interviewer bool
	Config      string
}

// lobbyView is the candidate's page.
type lobbyView struct {
	Lobby  service.SprintLobby
	Now    time.Time
	Base   string
	Config string
}

func itoa(n int) string { return strconv.Itoa(n) }

func when(t time.Time) string  { return t.UTC().Format("Mon 2 Jan 2006 15:04 UTC") }
func clock(t time.Time) string { return t.UTC().Format("15:04") }

func minutes(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 1 {
		return "under a minute"
	}
	if m == 1 {
		return "1 minute"
	}
	return itoa(m) + " minutes"
}

// statusLabel reads a sprint's standing at now: what is stored, then what
// the clock says.
func statusLabel(status string, clock domain.SprintClock, now time.Time) string {
	switch status {
	case service.SprintDraft:
		return "Draft"
	case service.SprintCancelled:
		return "Cancelled"
	}
	switch clock.At(now).Phase {
	case domain.PhaseBefore:
		return "Scheduled"
	case domain.PhaseRound, domain.PhaseBreak:
		return "Live"
	}
	return "Done"
}

func statusTag(label string) string {
	switch label {
	case "Live":
		return "tag-accent"
	case "Draft", "Cancelled":
		return "tag-outline"
	}
	return "tag-neutral"
}

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
	return "—"
}

func mean(v float64) string {
	if v == 0 {
		return "—"
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

func userLabel(u service.OrgUser) string {
	if u.Name != "" {
		return u.Name
	}
	return u.Email
}

func roles(u service.OrgUser) string { return strings.Join(u.Roles, ", ") }

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// consoleConfig is the console island's boot JSON: where to poll, the room
// island's constants, and who the viewer is.
func consoleConfig(sp service.Sprint, userID uuid.UUID, stateURL, csrf, ice string) string {
	var iceServers json.RawMessage = json.RawMessage("[]")
	if strings.TrimSpace(ice) != "" && json.Valid([]byte(ice)) {
		iceServers = json.RawMessage(ice)
	}
	b, _ := json.Marshal(map[string]any{
		"state_url": stateURL, "csrf": csrf, "ice": iceServers, "role": "interviewer",
		"interviewer": sp.IsInterviewer(userID), "languages": domain.CodeLanguageIDs(),
		"summary_url": Path(sp.ID),
	})
	return string(b)
}

func lobbyConfig(l service.SprintLobby, base, csrf, ice string) string {
	var iceServers json.RawMessage = json.RawMessage("[]")
	if strings.TrimSpace(ice) != "" && json.Valid([]byte(ice)) {
		iceServers = json.RawMessage(ice)
	}
	b, _ := json.Marshal(map[string]any{
		"state_url": base + "/state", "csrf": csrf, "ice": iceServers, "role": "candidate",
		"interviewer": false, "languages": domain.CodeLanguageIDs(), "name": l.CandidateName,
	})
	return string(b)
}

// ratedCount is how many of one interviewer's rounds carry a rating.
func ratedCount(pairings []service.SprintPairing) int {
	n := 0
	for _, p := range pairings {
		if p.Rating != nil {
			n++
		}
	}
	return n
}

// pairingName is who an interviewer meets in a round, or a dash.
func pairingName(sp service.Sprint, round int, interviewerID uuid.UUID) string {
	for _, pr := range sp.Pairings {
		if pr.Round == round && pr.InterviewerID == interviewerID {
			return pr.CandidateName
		}
	}
	return "—"
}

// recSummary counts a row's recommendations in words.
func recSummary(row service.SprintSummaryRow) string {
	if row.Rated == 0 {
		return "not rated yet"
	}
	parts := []string{}
	if row.StrongYes > 0 {
		parts = append(parts, itoa(row.StrongYes)+" strong yes")
	}
	if row.Yes > 0 {
		parts = append(parts, itoa(row.Yes)+" yes")
	}
	if row.No > 0 {
		parts = append(parts, itoa(row.No)+" no")
	}
	if row.StrongNo > 0 {
		parts = append(parts, itoa(row.StrongNo)+" strong no")
	}
	return strings.Join(parts, " · ") + " · " + itoa(row.Rated) + " of " + itoa(len(row.Ratings)) + " rated"
}

func ratingNote(pr service.SprintPairing) string {
	if pr.Rating == nil {
		return ""
	}
	return pr.Rating.Note
}
