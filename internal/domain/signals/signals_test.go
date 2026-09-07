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
	"fullscreen_exits": 10, "snapshot_gaps": 5,
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
			var data struct {
				SubmissionID uuid.UUID `json:"submission_id"`
			}
			if err := json.Unmarshal(ev.Payload, &data); err != nil {
				t.Fatal(err)
			}
			subs = append(subs, signals.Submission{ID: data.SubmissionID, ProblemID: problemID, Kind: "submit", At: ev.ServerTs, Passed: true, Source: final})
		}
	}
	return signals.Input{
		StartedAt:   events[0].At(),
		Events:      events,
		Submissions: subs,
		Problems:    []signals.Problem{{ID: problemID, Difficulty: "easy", Language: "python", FinalSource: final}},
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
	tl := signals.Build(load(t, "clean.jsonl"), nil)
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
	in := input(t, "clean.jsonl", signals.Build(load(t, "clean.jsonl"), nil)[problemID].Source)
	in.Problems[0].References = []signals.Source{{Language: "python", Source: "def solve(a, b):\n    return a + b\n"}}
	sigs := signals.Compute(in)
	if len(sigs) != 9 {
		t.Fatalf("%d signals, want 9", len(sigs))
	}
	got := byName(sigs)
	for _, name := range []string{"paste_ratio", "paste_then_pass", "burst_typing", "edit_ratio", "blur_then_solution", "fullscreen_exits", "snapshot_gaps"} {
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
	in := input(t, "paste_heavy.jsonl", signals.Build(load(t, "paste_heavy.jsonl"), nil)[problemID].Source)
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
	in := input(t, "blur_then_solution.jsonl", signals.Build(load(t, "blur_then_solution.jsonl"), nil)[problemID].Source)
	got := byName(signals.Compute(in))
	if s := got["blur_then_solution"]; s.Value != 1 || len(s.Evidence) != 1 {
		t.Errorf("blur_then_solution = %+v, want 1 with the blur in evidence", s)
	}
	if s := got["paste_ratio"]; s.Value != 0 {
		t.Errorf("paste_ratio = %v, want 0", s.Value)
	}
}

func TestBurstStream(t *testing.T) {
	in := input(t, "burst.jsonl", signals.Build(load(t, "burst.jsonl"), nil)[problemID].Source)
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

// refSolution and renamedSolution are the same program with other names.
const (
	refSolution     = "def solve(a, b):\n    if a > b:\n        return a - b\n    return a + b\n\nprint(solve(1, 2))\n"
	renamedSolution = "def calc(x, y):\n    if x > y:\n        return x - y\n    return x + y\n\nprint(calc(1, 2))\n"
)

func TestReferenceSimilarity(t *testing.T) {
	ref := refSolution
	in := input(t, "clean.jsonl", renamedSolution)
	in.Problems[0].References = []signals.Source{{Language: "python", Source: ref}}
	got := byName(signals.Compute(in))
	if s := got["reference_similarity"]; s.Value < 0.9 || len(s.Evidence) != 1 {
		t.Errorf("renamed copy of the reference: %+v, want > 0.9", s)
	}
	in.Problems[0].References = nil
	in.Problems[0].Others = []signals.Source{{Language: "python", Source: "import sys\nprint(sum(map(int, sys.stdin.read().split())))\n"}}
	if s := byName(signals.Compute(in))["reference_similarity"]; s.Value > 0.3 {
		t.Errorf("unrelated submission: %+v, want < 0.3", s)
	}
}

func TestRiskAppliesTheOrgWeights(t *testing.T) {
	in := input(t, "paste_heavy.jsonl", signals.Build(load(t, "paste_heavy.jsonl"), nil)[problemID].Source)
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
	in := input(t, "paste_heavy.jsonl", signals.Build(load(t, "paste_heavy.jsonl"), nil)[problemID].Source)
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

func event(seq int64, kind, payload string) signals.Event {
	at := time.Date(2024, 8, 29, 3, 33, 20, 0, time.UTC).Add(time.Duration(seq) * time.Second)
	return signals.Event{Seq: seq, Kind: kind, ProblemID: &problemID, ClientTs: &at, ServerTs: at, Payload: json.RawMessage(payload)}
}

// The payload is candidate-controlled: nonsense must fail reconstruction,
// never panic.
func TestMalformedChangesetsNeverPanic(t *testing.T) {
	for _, payload := range []string{`[-3]`, `[-3,[-2,"x"]]`, `[[]]`, `null`, `{"changes":5}`, `[5]`, `[[1,"x"]]`, `{"len":-4}`, `"x"`, ``} {
		tl := signals.Build([]signals.Event{event(1, "edit", payload), event(2, "paste", payload)}, nil)
		if tl[problemID].Reconstructed {
			t.Errorf("%s: reconstruction reported ok", payload)
		}
		for _, p := range tl[problemID].Pastes {
			if p.Len < 0 {
				t.Errorf("%s: negative paste len %d", payload, p.Len)
			}
		}
	}
}

func FuzzChangeset(f *testing.F) {
	f.Add(`[0,[0,"a"]]`)
	f.Add(`[[0,"line1","line2"]]`)
	f.Fuzz(func(_ *testing.T, payload string) {
		signals.Build([]signals.Event{event(1, "edit", `[[0,"seed"]]`), event(2, "edit", payload)}, nil)
	})
}

// CodeMirror counts UTF-16 units: an emoji is two.
func TestReconstructsInUTF16Units(t *testing.T) {
	tl := signals.Build([]signals.Event{
		event(1, "edit", `[[0,"a😀b"]]`),
		event(2, "edit", `[3,[1]]`), // delete the "b" after the two-unit emoji
		event(3, "paste", `{"len":4,"sha256":"x","internal":false}`),
		event(4, "edit", `[3,[0,"😀😀"]]`),
	}, nil)[problemID]
	if !tl.Reconstructed || tl.Source != "a😀😀😀" {
		t.Fatalf("source = %q (reconstructed %v)", tl.Source, tl.Reconstructed)
	}
	if tl.Edits[0].Inserted != 4 || tl.Edits[1].Deleted != 1 || !tl.Edits[2].FromPaste {
		t.Errorf("edits = %+v", tl.Edits)
	}
}

func TestBuildStartsFromTheInitialSource(t *testing.T) {
	tl := signals.Build([]signals.Event{event(1, "edit", `[5,[0,"!"]]`)},
		map[uuid.UUID]string{problemID: "hello"})[problemID]
	if !tl.Reconstructed || tl.Source != "hello!" {
		t.Fatalf("source = %q (reconstructed %v)", tl.Source, tl.Reconstructed)
	}
}

func TestClientClockIsClampedToTheServer(t *testing.T) {
	ev := event(1, "focus", `{}`)
	far := ev.ServerTs.Add(-2 * time.Hour)
	ev.ClientTs = &far
	if got := ev.At(); got != ev.ServerTs.Add(-signals.MaxClientSkew) {
		t.Errorf("At() = %v, want clamped to %v", got, ev.ServerTs.Add(-signals.MaxClientSkew))
	}
}

// The submit's client time may be skewed; speed is read off server clocks.
func TestSpeedUsesServerTimes(t *testing.T) {
	in := input(t, "paste_heavy.jsonl", "x")
	in.Problems[0].Difficulty = "hard"
	in.Submissions[0].At = in.Events[0].ServerTs.Add(40 * time.Minute)
	if s := byName(signals.Compute(in))["speed_vs_difficulty"]; !near(s.Value, 1-40.0/45) {
		t.Errorf("speed_vs_difficulty = %+v, want ~0.11 from the server clock", s)
	}
}

func TestReferenceSimilarityIsLanguageAware(t *testing.T) {
	in := input(t, "clean.jsonl", renamedSolution)
	in.Problems[0].Language = "python"
	in.Problems[0].References = []signals.Source{{Language: "go", Source: refSolution}}
	if s := byName(signals.Compute(in))["reference_similarity"]; s.Value != 0 || s.Confidence != signals.ConfidenceLow {
		t.Errorf("other-language reference compared: %+v", s)
	}
	in.Problems[0].References[0].Language = "python"
	if s := byName(signals.Compute(in))["reference_similarity"]; s.Value < 0.9 {
		t.Errorf("same-language reference: %+v", s)
	}
	in.Problems[0].FinalSource = "print(3)"
	in.Problems[0].References[0].Source = "print(3)"
	if s := byName(signals.Compute(in))["reference_similarity"]; s.Confidence != signals.ConfidenceLow {
		t.Errorf("tiny sources compared at normal confidence: %+v", s)
	}
}

func TestEvidenceCarriesProblemAndSeq(t *testing.T) {
	in := input(t, "burst.jsonl", signals.Build(load(t, "burst.jsonl"), nil)[problemID].Source)
	in.Problems[0].References = []signals.Source{{Language: "python", Source: in.Problems[0].FinalSource}}
	got := byName(signals.Compute(in))
	if e := got["edit_ratio"].Evidence; len(e) != 1 || e[0].ProblemID != problemID || e[0].At != nil {
		t.Errorf("edit_ratio evidence = %+v", e)
	}
	if e := got["speed_vs_difficulty"].Evidence; len(e) != 1 || e[0].Seq == 0 || e[0].At == nil {
		t.Errorf("speed evidence = %+v", e)
	}
	if e := got["reference_similarity"].Evidence; len(e) != 1 || e[0].Seq == 0 {
		t.Errorf("reference evidence = %+v", e)
	}
}
