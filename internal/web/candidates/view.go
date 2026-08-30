package candidates

import (
	"strconv"

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
