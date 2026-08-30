package jobs

import (
	"encoding/json"
	"strconv"
	"strings"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

// jobForm is the create/edit form's state, including what a rejected
// submission typed so nothing has to be retyped.
type jobForm struct {
	Action    string
	Submit    string
	Job       service.Job
	Companies []service.ClientCompany
	Error     string
	Saved     bool
}

func skillsText(j service.Job) string { return strings.Join(j.Skills, ", ") }

// numberValue leaves an unset salary bound blank rather than showing a zero.
func numberValue(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func stagePath(jobID, stageID string) string {
	return Prefix + "/" + jobID + "/stages/" + stageID
}

// terminalLabel names what reaching a stage does to an application.
func terminalLabel(s domain.Stage) string {
	switch s.Terminal {
	case domain.StatusHired:
		return "closes as hired"
	case domain.StatusRejected:
		return "closes as rejected"
	}
	return ""
}

// stageOrderData is the pipeline handed to Alpine so reordering is local until
// the recruiter saves it.
type stageOrderData struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Note string `json:"note"`
}

func stagesJSON(stages []domain.Stage) string {
	out := make([]stageOrderData, 0, len(stages))
	for _, s := range stages {
		note := terminalLabel(s)
		if note == "" && s.Unblind {
			note = "unblinds the candidate"
		}
		out = append(out, stageOrderData{ID: s.ID.String(), Name: s.Name, Kind: string(s.Kind), Note: note})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func positionLabel(s domain.Stage) string { return strconv.Itoa(s.Position) }
