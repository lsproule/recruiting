package jobs

import (
	"strconv"
	"strings"

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

// userLabel names a user in a select: their name, or their email when they
// have none.
func userLabel(u service.OrgUser) string {
	if u.Name != "" {
		return u.Name
	}
	return u.Email
}

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
