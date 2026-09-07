package intake

import (
	"strconv"
	"strings"

	"github.com/google/uuid"

	"recruiting/internal/service"
)

// wizardView is everything the swapped region draws: the draft, whatever the
// current step needs loaded, and the reason the last post was refused.
type wizardView struct {
	Draft     service.IntakeDraft
	Templates []service.IntakeTemplate
	Slots     []service.IntakeSlot
	// Bank is what a hand-assembled set may draw on: the problems the bank
	// has cleared for use.
	Bank  []service.Problem
	Error string
	CSRF  string
}

// railStep is one entry in the step rail.
type railStep struct {
	Number int
	Label  string
	Active bool
	Done   bool
}

func (v wizardView) steps() []railStep {
	out := make([]railStep, 0, len(service.IntakeStepNames))
	for i, label := range service.IntakeStepNames {
		n := i + 1
		out = append(out, railStep{Number: n, Label: label, Active: n == v.Draft.Step, Done: n < v.Draft.Step})
	}
	return out
}

func (v wizardView) step() int { return v.Draft.Step }

// kicker names the client the intake is for once step one has said who it
// is, so no later step has to be taken on trust.
func (v wizardView) kicker() string {
	if company := strings.TrimSpace(v.Draft.Payload.Client.Company); company != "" {
		return "Intake · " + company
	}
	return "Intake"
}

func (v wizardView) client() service.IntakeClient { return v.Draft.Payload.Client }
func (v wizardView) job() service.IntakeJob       { return v.Draft.Payload.Job }

func (v wizardView) skillsText() string { return strings.Join(v.Draft.Payload.Skills, ", ") }

func (v wizardView) source() string {
	if s := v.Draft.Payload.Assessment.Source; s != "" {
		return s
	}
	return service.IntakeSourceDefault
}

// stepPath is where the current panel posts.
func (v wizardView) stepPath() string {
	return draftPath(v.Draft.ID) + "/" + strconv.Itoa(v.Draft.Step)
}

func (v wizardView) createPath() string  { return draftPath(v.Draft.ID) + "/create" }
func (v wizardView) discardPath() string { return draftPath(v.Draft.ID) + "/discard" }

// authorPath sends the recruiter off to write a problem and back here with
// it; the draft is stored, so the detour costs nothing.
func (v wizardView) authorPath() string { return "/app/problems/new?return=intake" }

// nextLabel names what the primary button does next, the way the rail reads.
func (v wizardView) nextLabel() string {
	if v.Draft.Step >= service.IntakeStepReview {
		return ""
	}
	return "Next: " + service.IntakeStepNames[v.Draft.Step]
}

func (v wizardView) prevStep() int {
	if v.Draft.Step <= service.IntakeStepClient {
		return 0
	}
	return v.Draft.Step - 1
}

func (v wizardView) nextStep() int {
	if v.Draft.Step >= service.IntakeStepReview {
		return 0
	}
	return v.Draft.Step + 1
}

// reviewRow is one line of the last step: what the intake is about to create.
type reviewRow struct {
	Label string
	Value string
}

func (v wizardView) review() []reviewRow {
	in := v.Draft.Payload
	client := in.Client.Company
	if in.Client.Industry != "" {
		client += " · " + in.Client.Industry
	}
	job := in.Job.Title
	if in.Job.Seniority != "" {
		job += " · " + in.Job.Seniority
	}
	if in.Job.Location != "" {
		job += " · " + in.Job.Location
	}
	if band := salaryBand(in.Job); band != "" {
		job += " · " + band
	}
	return []reviewRow{
		{"Client", client},
		{"Hiring contact", in.Client.ContactName + " · " + in.Client.ContactEmail},
		{"Shortlist SLA", slaLabel(in.Client.ShortlistSLADays)},
		{"Job", job},
		{"Pipeline", v.templateName()},
		{"Skills tested", orNone(strings.Join(in.Skills, ", "))},
		{"Assessment", v.setLabel()},
	}
}

// templateName is the pipeline the job will run. The name is only loaded on
// the step that chooses it, so the review falls back to the org's default.
func (v wizardView) templateName() string {
	for _, t := range v.Templates {
		if t.ID == v.Draft.Payload.Job.TemplateID {
			return t.Name
		}
	}
	return "The org's default pipeline"
}

func (v wizardView) setLabel() string {
	titles := make([]string, 0, len(v.Slots))
	for _, slot := range v.Slots {
		if slot.Chosen.ID != uuid.Nil {
			titles = append(titles, slot.Chosen.Title)
		}
	}
	if len(titles) == 0 {
		return "No problem picked yet"
	}
	return strings.Join(titles, " · ")
}

// sourceOptions are the three ways step four gets its coding question.
type sourceOption struct {
	Value string
	Title string
	Body  string
}

func sourceOptions() []sourceOption {
	return []sourceOption{
		{service.IntakeSourceDefault, "Use the recommended set",
			"Picked from the bank by the skills you ordered, at the difficulty this seniority calls for. Swap any slot."},
		{service.IntakeSourceBank, "Choose from the problem bank",
			"Assemble the set yourself from everything the bank has cleared for use."},
		{service.IntakeSourceAuthor, "Write your own",
			"Author a problem for this job. It runs its reference solution before it can be attached, then comes back here."},
	}
}

// showsPipeline reports whether the template's stages are the ones to
// preview: the one the recruiter chose, or the default when they have not
// chosen yet.
func (v wizardView) showsPipeline(t service.IntakeTemplate) bool {
	if v.Draft.Payload.Job.TemplateID != uuid.Nil {
		return t.ID == v.Draft.Payload.Job.TemplateID
	}
	return len(v.Templates) > 0 && v.Templates[0].ID == t.ID
}

// pickedProblems are the problems a hand-assembled set already holds, so the
// bank list can show them ticked.
func (v wizardView) picked() map[uuid.UUID]bool {
	out := make(map[uuid.UUID]bool, len(v.Draft.Payload.Assessment.ProblemIDs))
	for _, id := range v.Draft.Payload.Assessment.ProblemIDs {
		out[id] = true
	}
	return out
}

func draftPath(id uuid.UUID) string { return Prefix + "/" + id.String() }

func salaryBand(j service.IntakeJob) string {
	switch {
	case j.SalaryMin > 0 && j.SalaryMax > 0:
		return numberValue(j.SalaryMin) + "–" + numberValue(j.SalaryMax)
	case j.SalaryMin > 0:
		return "from " + numberValue(j.SalaryMin)
	case j.SalaryMax > 0:
		return "up to " + numberValue(j.SalaryMax)
	}
	return ""
}

func slaLabel(days int) string {
	if days <= 0 {
		return "Not agreed"
	}
	if days == 1 {
		return "1 business day"
	}
	return strconv.Itoa(days) + " business days"
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "Nothing picked yet"
	}
	return s
}

// numberValue leaves an unset number blank rather than showing a zero.
func numberValue(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func itoa(n int) string { return strconv.Itoa(n) }

// mergeStep folds a refused panel back into the draft so the recruiter sees
// what they typed rather than what the database still holds.
func mergeStep(stored service.IntakePayload, step int, in service.IntakePayload) service.IntakePayload {
	switch step {
	case service.IntakeStepClient:
		stored.Client = in.Client
	case service.IntakeStepJob:
		stored.Job = in.Job
	case service.IntakeStepSkills:
		stored.Skills = in.Skills
	case service.IntakeStepQuestion:
		stored.Assessment = in.Assessment
	}
	return stored
}
