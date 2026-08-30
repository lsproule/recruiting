package apply

import (
	"strings"

	"recruiting/internal/service"
)

// applyForm is what the applicant typed, kept so a rejected submission never
// has to be retyped.
type applyForm struct {
	Name  string
	Email string
	Phone string
	Links string
	Error string
}

// applyPath is the job's own public URL: the org slug, then the job slug.
func applyPath(j service.PublicJob) string {
	return Prefix + "/" + j.OrgSlug + "/" + j.Slug
}

// jobSummary is the one-line description under the job title: seniority,
// location, and remote policy, whichever the job set.
func jobSummary(j service.PublicJob) string {
	parts := make([]string, 0, 3)
	for _, p := range []string{j.Seniority, j.Location, j.RemotePolicy} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}
