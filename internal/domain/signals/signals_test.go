package signals_test

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain/signals"
)

var problemID = uuid.MustParse("11111111-1111-1111-1111-111111111111")

// weights mirror the org defaults: fractions of 100 that sum to 100.
var weights = map[string]float64{
	"paste_ratio": 25, "paste_then_pass": 25, "burst_typing": 10, "edit_ratio": 10,
	"blur_then_solution": 15, "speed_vs_difficulty": 5, "reference_similarity": 10,
}

func load(t *testing.T, name string) []signals.Event {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []signals.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev signals.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out = append(out, ev)
	}
	return out
}

// input builds the fixture's input: one easy problem, its final source
// reconstructed from the stream, and a passing submit at the submit event.
func input(t *testing.T, name string, final string) signals.Input {
	t.Helper()
	events := load(t, name)
	var subs []signals.Submission
	for _, ev := range events {
		if ev.Kind == "submit" {
			subs = append(subs, signals.Submission{ProblemID: problemID, Kind: "submit", At: ev.At(), Passed: true, Source: final})
		}
	}
	return signals.Input{
		StartedAt:   events[0].At(),
		Events:      events,
		Submissions: subs,
		Problems:    []signals.Problem{{ID: problemID, Difficulty: "easy", FinalSource: final}},
	}
}

func byName(sigs []signals.Signal) map[string]signals.Signal {
	m := make(map[string]signals.Signal, len(sigs))
	for _, s := range sigs {
		m[s.Name] = s
	}
	return m
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.05 }

func TestReconstructsTheSourceFromChangesets(t *testing.T) {
	tl := signals.Build(load(t, "clean.jsonl"))
	src := tl[problemID].Source
	if len(src) != 480 {
		t.Fatalf("reconstructed %d chars, want 480", len(src))
	}
	// Each line's "1\n" is deleted before the next line is typed.
	if src[:16] != "x = x + x = x + " {
		t.Errorf("source starts %q", src[:16])
	}
	if !tl[problemID].Reconstructed {
		t.Error("reconstruction reported as failed")
	}
}

func TestCleanBaselineScoresLow(t *testing.T) {
	in := input(t, "clean.jsonl", signals.Build(load(t, "clean.jsonl"))[problemID].Source)
	in.Problems[0].References = []string{"def solve(a, b):\n    return a + b\n"}
	sigs := signals.Compute(in)
	if len(sigs) != 7 {
		t.Fatalf("%d signals, want 7", len(sigs))
	}
	got := byName(sigs)
	for _, name := range []string{"paste_ratio", "paste_then_pass", "burst_typing", "edit_ratio", "blur_then_solution"} {
		if s := got[name]; s.Value != 0 || s.Confidence != signals.ConfidenceNormal {
			t.Errorf("%s = %+v, want 0 at normal confidence", name, s)
		}
	}
	// Unrelated code shares the odd trigram with any reference.
	if s := got["reference_similarity"]; s.Value > 0.05 || s.Confidence != signals.ConfidenceNormal {
		t.Errorf("reference_similarity = %+v, want ~0 at normal confidence", s)
	}
	// Ten minutes on an easy problem sits at the band median: not fast.
	if s := got["speed_vs_difficulty"]; s.Value != 0 {
		t.Errorf("speed_vs_difficulty = %v, want 0", s.Value)
	}
	if r := signals.Risk(sigs, weights); r > 1 {
		t.Errorf("risk = %v, want ~0", r)
	}
}

func TestPasteHeavyStream(t *testing.T) {
	in := input(t, "paste_heavy.jsonl", signals.Build(load(t, "paste_heavy.jsonl"))[problemID].Source)
	got := byName(signals.Compute(in))
	if s := got["paste_ratio"]; !near(s.Value, 200.0/210) || len(s.Evidence) != 1 {
		t.Errorf("paste_ratio = %+v, want ~0.95 with one paste in evidence", s)
	}
	if s := got["paste_then_pass"]; s.Value != 1 || len(s.Evidence) != 1 {
		t.Errorf("paste_then_pass = %+v, want 1", s)
	}
	// The pasted text is not typing: it counts for neither bursts nor the
	// edit ratio, which has too little typing to judge.
	if s := got["burst_typing"]; s.Value != 0 {
		t.Errorf("burst_typing = %v, want 0", s.Value)
	}
	if s := got["edit_ratio"]; s.Confidence != signals.ConfidenceLow {
		t.Errorf("edit_ratio confidence = %s, want low", s.Confidence)
	}
}

func TestBlurThenSolutionStream(t *testing.T) {
	in := input(t, "blur_then_solution.jsonl", signals.Build(load(t, "blur_then_solution.jsonl"))[problemID].Source)
	got := byName(signals.Compute(in))
	if s := got["blur_then_solution"]; s.Value != 1 || len(s.Evidence) != 1 {
		t.Errorf("blur_then_solution = %+v, want 1 with the blur in evidence", s)
	}
	if s := got["paste_ratio"]; s.Value != 0 {
		t.Errorf("paste_ratio = %v, want 0", s.Value)
	}
}

func TestBurstStream(t *testing.T) {
	in := input(t, "burst.jsonl", signals.Build(load(t, "burst.jsonl"))[problemID].Source)
	got := byName(signals.Compute(in))
	if s := got["burst_typing"]; !near(s.Value, 600.0/660) || len(s.Evidence) != 1 {
		t.Errorf("burst_typing = %+v, want ~0.91 with one burst", s)
	}
	// 660 chars typed and nothing deleted: the lowest edit ratio there is.
	if s := got["edit_ratio"]; s.Value != 1 || s.Confidence != signals.ConfidenceNormal {
		t.Errorf("edit_ratio = %+v, want 1", s)
	}
}

func TestSpeedAgainstTheDifficultyBand(t *testing.T) {
	in := input(t, "paste_heavy.jsonl", "x")
	in.Problems[0].Difficulty = "hard" // solved in 50s against a 45m median
	got := byName(signals.Compute(in))
	if s := got["speed_vs_difficulty"]; !near(s.Value, 1-50.0/(45*60)) {
		t.Errorf("speed_vs_difficulty = %+v, want ~0.98", s)
	}
	in.Submissions[0].Passed = false
	if s := byName(signals.Compute(in))["speed_vs_difficulty"]; s.Value != 0 {
		t.Errorf("no pass: speed_vs_difficulty = %v, want 0", s.Value)
	}
}

func TestReferenceSimilarity(t *testing.T) {
	ref := "def solve(a, b):\n    return a + b\n"
	in := input(t, "clean.jsonl", "def solve(x, y):\n    return x + y\n")
	in.Problems[0].References = []string{ref}
	got := byName(signals.Compute(in))
	if s := got["reference_similarity"]; s.Value < 0.9 || len(s.Evidence) != 1 {
		t.Errorf("renamed copy of the reference: %+v, want > 0.9", s)
	}
	in.Problems[0].References = nil
	in.Problems[0].Others = []string{"import sys\nprint(sum(map(int, sys.stdin.read().split())))\n"}
	if s := byName(signals.Compute(in))["reference_similarity"]; s.Value > 0.3 {
		t.Errorf("unrelated submission: %+v, want < 0.3", s)
	}
}

func TestRiskAppliesTheOrgWeights(t *testing.T) {
	in := input(t, "paste_heavy.jsonl", signals.Build(load(t, "paste_heavy.jsonl"))[problemID].Source)
	sigs := signals.Compute(in)
	base := signals.Risk(sigs, weights)
	if base <= 40 || base > 100 {
		t.Fatalf("risk = %v, want a paste-driven score in (40, 100]", base)
	}
	heavier := map[string]float64{}
	for k, v := range weights {
		heavier[k] = v
	}
	heavier["paste_ratio"], heavier["paste_then_pass"] = 60, 60
	if r := signals.Risk(sigs, heavier); r != 100 {
		t.Errorf("risk with doubled paste weights = %v, want clamped to 100", r)
	}
	if r := signals.Risk(sigs, map[string]float64{"paste_ratio": 10}); !near(r, 10*200.0/210) {
		t.Errorf("risk with only paste_ratio weighted = %v", r)
	}
}

func TestIncompleteRecordingLowersConfidenceOnly(t *testing.T) {
	in := input(t, "paste_heavy.jsonl", signals.Build(load(t, "paste_heavy.jsonl"))[problemID].Source)
	in.Incomplete = true
	sigs := signals.Compute(in)
	for _, s := range sigs {
		if s.Confidence != signals.ConfidenceLow {
			t.Errorf("%s confidence = %s, want low", s.Name, s.Confidence)
		}
	}
	if r := signals.Risk(sigs, weights); r <= 40 {
		t.Errorf("risk = %v; a gap must not suppress the score", r)
	}
}

func TestThresholdsAreTheSpecValues(t *testing.T) {
	if signals.PasteThenPassWindow != 60*time.Second || signals.BlurMinDuration != 30*time.Second ||
		signals.BlurSolutionWindow != 2*time.Minute || signals.BurstRate != 8 || signals.BurstMinDuration != 5*time.Second {
		t.Error("thresholds drifted from the spec")
	}
}
