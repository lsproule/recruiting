package candidates

import (
	"strconv"
	"strings"

	"recruiting/internal/service"
)

// candidateForm is the manual-add form's state, including what a rejected
// submission typed so nothing has to be retyped.
type candidateForm struct {
	Name  string
	Email string
	Phone string
	Links string
	JobID string
	Jobs  []service.Job
	Error string
}

// openJobs is the subset a new application may be filed against; a draft or
// closed role is not taking candidates.
func openJobs(all []service.Job) []service.Job {
	out := make([]service.Job, 0, len(all))
	for _, j := range all {
		if j.Status == service.JobOpen {
			out = append(out, j)
		}
	}
	return out
}

func candidatePath(c service.Candidate) string { return Prefix + "/" + c.ID.String() }

func resumePath(r service.Resume) string {
	return Prefix + "/" + r.CandidateID.String() + "/resumes/" + r.ID.String()
}

func applicationCount(c service.Candidate) string { return strconv.Itoa(c.ApplicationCount) }

// resumeNote says why a resume is not searchable, and nothing at all when it
// is.
func resumeNote(r service.Resume) string {
	switch r.TextStatus {
	case service.ResumeFailed:
		return "text could not be read"
	case service.ResumePending:
		return "text not extracted yet"
	}
	return ""
}

// headline is the one line under a name: their network headline, else what
// the row can say from the rest.
func headline(c service.Candidate) string {
	if c.Headline != "" {
		if c.Location != "" {
			return c.Headline + " · " + c.Location
		}
		return c.Headline
	}
	if c.Email != "" {
		return c.Email
	}
	return ""
}

// networkLine sums up a profile's terms.
func networkLine(n *service.NetworkProfile) string {
	var parts []string
	if n.Seniority != "" {
		parts = append(parts, n.Seniority)
	}
	if n.Location != "" {
		parts = append(parts, n.Location)
	}
	if n.RemotePolicy != "" {
		parts = append(parts, n.RemotePolicy)
	}
	if !n.JoinedAt.IsZero() {
		parts = append(parts, "joined "+n.JoinedAt.Format("2 Jan 2006"))
	}
	return strings.Join(parts, " · ")
}

func itoa(n int) string { return strconv.Itoa(n) }
