package domain_test

import (
	"testing"

	"recruiting/internal/domain"
)

// Every library process must be a valid pipeline as shipped, with both
// terminals and every stage carrying what its kind needs.
func TestProcessLibraryIsValid(t *testing.T) {
	if len(domain.ProcessLibrary) < 3 {
		t.Fatalf("library has %d processes", len(domain.ProcessLibrary))
	}
	keys := map[string]bool{}
	for _, p := range domain.ProcessLibrary {
		if p.Key == "" || p.Name == "" || p.Description == "" {
			t.Fatalf("process %+v is missing a key, name, or description", p)
		}
		if keys[p.Key] {
			t.Fatalf("duplicate process key %q", p.Key)
		}
		keys[p.Key] = true
		if err := domain.ValidatePipeline(p.PipelineStages()); err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		if _, ok := domain.ProcessByKey(p.Key); !ok {
			t.Fatalf("%s not found by key", p.Key)
		}
	}
	if _, ok := domain.ProcessByKey("nothing"); ok {
		t.Fatal("unknown key found")
	}
}

// The engineering loop is the user's own process, in that order.
func TestEngineeringLoopShape(t *testing.T) {
	p, ok := domain.ProcessByKey(domain.ProcessEngineeringLoop)
	if !ok {
		t.Fatal("no engineering loop")
	}
	want := []domain.StageKind{
		domain.StageGeneric, domain.StageInterview, domain.StageAssessment, domain.StageSprint,
		domain.StageInterview, domain.StageInterview, domain.StageGeneric, domain.StageTerminal, domain.StageTerminal,
	}
	if len(p.Stages) != len(want) {
		t.Fatalf("%d stages, want %d", len(p.Stages), len(want))
	}
	for i, k := range want {
		if p.Stages[i].Kind != k {
			t.Fatalf("stage %d is %s, want %s", i, p.Stages[i].Kind, k)
		}
	}
	stages := p.PipelineStages()
	if stages[1].InterviewFormat != domain.FormatCall || stages[1].DurationMinutes != 20 {
		t.Fatalf("recruiter call = %+v", stages[1])
	}
	if stages[4].InterviewFormat != domain.FormatVideo || stages[4].DurationMinutes != 60 {
		t.Fatalf("technical interview = %+v", stages[4])
	}
	if stages[3].RoundSeconds != 300 || stages[3].BreakSeconds != 60 {
		t.Fatalf("sprint = %+v", stages[3])
	}
	if stages[7].Terminal != domain.StatusHired || stages[8].Terminal != domain.StatusRejected {
		t.Fatalf("terminals = %+v %+v", stages[7], stages[8])
	}
}

func TestNormalizeStageFillsAndClears(t *testing.T) {
	iv := domain.NormalizeStage(domain.Stage{Name: "Call", Kind: domain.StageInterview, RoundSeconds: 99})
	if iv.InterviewFormat != domain.FormatCall || iv.DurationMinutes != domain.DefaultInterviewMinutes || iv.RoundSeconds != 0 {
		t.Fatalf("interview = %+v", iv)
	}
	sp := domain.NormalizeStage(domain.Stage{Name: "Sprint", Kind: domain.StageSprint, InterviewFormat: domain.FormatVideo, DurationMinutes: 60})
	if sp.RoundSeconds != domain.DefaultRoundSeconds || sp.BreakSeconds != domain.DefaultBreakSeconds || sp.InterviewFormat != "" || sp.DurationMinutes != 0 {
		t.Fatalf("sprint = %+v", sp)
	}
	g := domain.NormalizeStage(domain.Stage{Name: "Applied", Kind: domain.StageGeneric, Terminal: domain.StatusHired, DurationMinutes: 5})
	if g.Terminal != "" || g.DurationMinutes != 0 {
		t.Fatalf("generic = %+v", g)
	}
}

func TestValidatePipelineChecksStageSettings(t *testing.T) {
	base := []domain.Stage{
		{Name: "Applied", Kind: domain.StageGeneric},
		{Name: "Hired", Kind: domain.StageTerminal, Terminal: domain.StatusHired},
		{Name: "Rejected", Kind: domain.StageTerminal, Terminal: domain.StatusRejected},
	}
	with := func(s domain.Stage) []domain.Stage { return append([]domain.Stage{s}, base...) }
	for _, tc := range []struct {
		name  string
		stage domain.Stage
		ok    bool
	}{
		{"video interview", domain.Stage{Name: "Tech", Kind: domain.StageInterview, InterviewFormat: domain.FormatVideo, DurationMinutes: 60}, true},
		{"bad format", domain.Stage{Name: "Tech", Kind: domain.StageInterview, InterviewFormat: "telepathy", DurationMinutes: 60}, false},
		{"too short", domain.Stage{Name: "Tech", Kind: domain.StageInterview, InterviewFormat: domain.FormatCall, DurationMinutes: 1}, false},
		{"sprint", domain.Stage{Name: "Sprint", Kind: domain.StageSprint, RoundSeconds: 300, BreakSeconds: 60}, true},
		{"sprint round too short", domain.Stage{Name: "Sprint", Kind: domain.StageSprint, RoundSeconds: 5, BreakSeconds: 60}, false},
		{"sprint break negative", domain.Stage{Name: "Sprint", Kind: domain.StageSprint, RoundSeconds: 300, BreakSeconds: -1}, false},
	} {
		err := domain.ValidatePipeline(with(tc.stage))
		if (err == nil) != tc.ok {
			t.Fatalf("%s: err = %v, want ok %v", tc.name, err, tc.ok)
		}
	}
}
