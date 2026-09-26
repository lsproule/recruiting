package domain

import "strings"

// ProcessStage is one stage of a process in the built-in library: the shape
// a template stage takes before it has an id.
type ProcessStage struct {
	Name            string
	Kind            StageKind
	Unblind         bool
	InterviewFormat string
	DurationMinutes int
	RoundSeconds    int
	BreakSeconds    int
}

// ProcessSpec is one entry of the library: a named hiring process an org
// can copy into its own templates and edit from there.
type ProcessSpec struct {
	Key         string
	Name        string
	Description string
	Stages      []ProcessStage
}

// Library keys, stable across releases so an org's copy can say where it
// came from.
const (
	ProcessAgencyStandard  = "agency_standard"
	ProcessEngineeringLoop = "engineering_loop"
	ProcessFastTrack       = "fast_track"
)

// ProcessLibrary is the built-in processes, in the order the screen offers
// them. The first is what a new org's default template is seeded from.
var ProcessLibrary = []ProcessSpec{
	{
		Key:         ProcessAgencyStandard,
		Name:        "Agency standard",
		Description: "Screen, phone interview, proctored assessment, shortlist, then the client takes over.",
		Stages: []ProcessStage{
			{Name: "Applied", Kind: StageGeneric},
			{Name: "Screened", Kind: StageGeneric},
			{Name: "Phone Interview", Kind: StageInterview, InterviewFormat: FormatCall, DurationMinutes: 30},
			{Name: "Assessment", Kind: StageAssessment},
			{Name: "Shortlist", Kind: StageGeneric},
			{Name: "Client Review", Kind: StageClientReview},
			{Name: "Client Interview", Kind: StageClientReview, Unblind: true},
			{Name: "Offer", Kind: StageClientReview},
			{Name: "Hired", Kind: StageTerminal},
			{Name: "Rejected", Kind: StageTerminal},
		},
	},
	{
		Key:         ProcessEngineeringLoop,
		Name:        "Engineering loop",
		Description: "Recruiter call, take-home, a screening sprint to shortlist, then a full technical interview and an HR round.",
		Stages: []ProcessStage{
			{Name: "Applied", Kind: StageGeneric},
			{Name: "Recruiter call", Kind: StageInterview, InterviewFormat: FormatCall, DurationMinutes: 20},
			{Name: "Take-home", Kind: StageAssessment},
			{Name: "Screening sprint", Kind: StageSprint, RoundSeconds: 5 * 60, BreakSeconds: 60},
			{Name: "Technical interview", Kind: StageInterview, InterviewFormat: FormatVideo, DurationMinutes: 60},
			{Name: "HR interview", Kind: StageInterview, InterviewFormat: FormatVideo, DurationMinutes: 45},
			{Name: "Offer", Kind: StageGeneric},
			{Name: "Hired", Kind: StageTerminal},
			{Name: "Rejected", Kind: StageTerminal},
		},
	},
	{
		Key:         ProcessFastTrack,
		Name:        "Fast track",
		Description: "A screening sprint straight from the application, then one technical interview.",
		Stages: []ProcessStage{
			{Name: "Applied", Kind: StageGeneric},
			{Name: "Screening sprint", Kind: StageSprint, RoundSeconds: 5 * 60, BreakSeconds: 60},
			{Name: "Technical interview", Kind: StageInterview, InterviewFormat: FormatVideo, DurationMinutes: 60},
			{Name: "Hired", Kind: StageTerminal},
			{Name: "Rejected", Kind: StageTerminal},
		},
	},
}

// ProcessByKey finds a library entry.
func ProcessByKey(key string) (ProcessSpec, bool) {
	for _, p := range ProcessLibrary {
		if p.Key == key {
			return p, true
		}
	}
	return ProcessSpec{}, false
}

// Stage is the library stage as a pipeline stage, normalised, with the
// terminal outcome read off the name the way template stages are read.
func (ps ProcessStage) Stage() Stage {
	s := Stage{
		Name: ps.Name, Kind: ps.Kind, Unblind: ps.Unblind,
		InterviewFormat: ps.InterviewFormat, DurationMinutes: ps.DurationMinutes,
		RoundSeconds: ps.RoundSeconds, BreakSeconds: ps.BreakSeconds,
	}
	if ps.Kind == StageTerminal {
		s.Terminal = TerminalOutcomeFor(ps.Name)
	}
	return NormalizeStage(s)
}

// PipelineStages is every stage of the process as pipeline stages, positioned.
func (p ProcessSpec) PipelineStages() []Stage {
	out := make([]Stage, 0, len(p.Stages))
	for i, ps := range p.Stages {
		s := ps.Stage()
		s.Position = i + 1
		out = append(out, s)
	}
	return out
}

// TerminalOutcomeFor reads a terminal stage's outcome off its name, the way
// template stages carry it: anything that is not a rejection closes the
// application as hired.
func TerminalOutcomeFor(name string) ApplicationStatus {
	if strings.Contains(strings.ToLower(name), "reject") {
		return StatusRejected
	}
	return StatusHired
}
