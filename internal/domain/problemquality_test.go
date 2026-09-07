package domain

import (
	"strings"
	"testing"
)

func qualityCase(cases int, public int, tags int, statement int, proven int) QualityInput {
	in := QualityInput{
		Statement:       strings.Repeat("x", statement),
		ProvenLanguages: make([]string, proven),
	}
	for i := range tags {
		in.Tags = append(in.Tags, string(rune('a'+i)))
	}
	for i := range cases {
		v := VisibilityHidden
		if i < public {
			v = VisibilityPublic
		}
		in.TestCases = append(in.TestCases, ImportTestCase{Visibility: v})
	}
	return in
}

func TestProblemQualityScoresEveryRule(t *testing.T) {
	full := qualityCase(6, 1, 1, 200, 2)
	if got := ProblemQuality(full); got != 100 {
		t.Fatalf("a problem meeting every rule scores %d, want 100", got)
	}
	if got := ProblemQuality(QualityInput{}); got != 0 {
		t.Fatalf("an empty problem scores %d, want 0", got)
	}
}

// Each rule is scored on its own, so an author sees which one is missing
// rather than one opaque number.
func TestProblemQualityDropsExactlyTheFailedRule(t *testing.T) {
	full := qualityCase(6, 1, 1, 200, 2)
	for _, tc := range []struct {
		name string
		in   QualityInput
	}{
		{"five cases", qualityCase(5, 1, 1, 200, 2)},
		{"no public case", qualityCase(6, 0, 1, 200, 2)},
		{"two hidden cases", qualityCase(6, 4, 1, 200, 2)},
		{"no tag", qualityCase(6, 1, 0, 200, 2)},
		{"short statement", qualityCase(6, 1, 1, 199, 2)},
		{"one proven language", qualityCase(6, 1, 1, 200, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ProblemQuality(tc.in)
			if got >= ProblemQuality(full) {
				t.Fatalf("score %d did not drop below the full %d", got, ProblemQuality(full))
			}
			var failed int
			for _, c := range QualityChecks(tc.in) {
				if !c.OK {
					failed++
				}
			}
			if failed != 1 {
				t.Fatalf("%d checks failed, want exactly 1", failed)
			}
		})
	}
}

// The floor is what an assessment refuses below, so the boundary is pinned:
// exactly 60 attaches, 59 does not.
func TestProblemQualityFloorBoundary(t *testing.T) {
	at := qualityCase(6, 0, 0, 199, 2) // enough cases, hidden cases, and proven languages
	if got := ProblemQuality(at); got != ProblemQualityFloor {
		t.Fatalf("boundary problem scores %d, want %d", got, ProblemQualityFloor)
	}
	below := qualityCase(6, 0, 0, 199, 1)
	if got := ProblemQuality(below); got >= ProblemQualityFloor {
		t.Fatalf("a problem proven in one language scores %d, want below %d", got, ProblemQualityFloor)
	}
}

func TestQualityChecksExplainThemselves(t *testing.T) {
	for _, c := range QualityChecks(qualityCase(2, 1, 0, 10, 1)) {
		if c.Label == "" || c.Note == "" {
			t.Errorf("check %+v has no label or note", c)
		}
	}
}
