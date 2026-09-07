package domain_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/domain"
)

// bank is a small stand-in for the problem bank: two of every difficulty, one
// of each pair tagged with a skill an intake might ask for.
func bank() []domain.SetProblem {
	return []domain.SetProblem{
		{ID: uuid.MustParse("00000000-0000-0000-0000-0000000000e1"), Title: "Easy tagged", Difficulty: "easy", Tags: []string{"go", "testing"}, Quality: 70},
		{ID: uuid.MustParse("00000000-0000-0000-0000-0000000000e2"), Title: "Easy plain", Difficulty: "easy", Quality: 90},
		{ID: uuid.MustParse("00000000-0000-0000-0000-0000000000d1"), Title: "Medium tagged", Difficulty: "medium", Tags: []string{"concurrency"}, Quality: 65},
		{ID: uuid.MustParse("00000000-0000-0000-0000-0000000000d2"), Title: "Medium plain", Difficulty: "medium", Quality: 95},
		{ID: uuid.MustParse("00000000-0000-0000-0000-0000000000a1"), Title: "Hard tagged", Difficulty: "hard", Tags: []string{"concurrency", "go"}, Quality: 61},
		{ID: uuid.MustParse("00000000-0000-0000-0000-0000000000a2"), Title: "Hard plain", Difficulty: "hard", Quality: 88},
	}
}

func titles(set []domain.SetProblem) []string {
	out := make([]string, 0, len(set))
	for _, p := range set {
		out = append(out, p.Title)
	}
	return out
}

func TestDefaultSetDifficultiesFollowSeniority(t *testing.T) {
	for _, tc := range []struct {
		seniority string
		want      []string
	}{
		{"junior", []string{"easy", "medium"}},
		{"mid", []string{"easy", "medium"}},
		{"senior", []string{"medium", "hard"}},
		{"staff", []string{"medium", "hard"}},
		{"principal", []string{"medium", "hard"}},
		{"Staff", []string{"medium", "hard"}},
		{"", []string{"easy", "medium"}},
	} {
		if got := domain.DefaultSetDifficulties(tc.seniority); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("DefaultSetDifficulties(%q) = %v, want %v", tc.seniority, got, tc.want)
		}
	}
}

func TestDefaultSetChangesWithSeniority(t *testing.T) {
	junior := domain.DefaultSet(bank(), nil, "junior")
	senior := domain.DefaultSet(bank(), nil, "senior")
	if got := []string{junior[0].Difficulty, junior[1].Difficulty}; !reflect.DeepEqual(got, []string{"easy", "medium"}) {
		t.Errorf("junior difficulties = %v", got)
	}
	if got := []string{senior[0].Difficulty, senior[1].Difficulty}; !reflect.DeepEqual(got, []string{"medium", "hard"}) {
		t.Errorf("senior difficulties = %v", got)
	}
}

func TestDefaultSetPrefersTagOverlapOverQuality(t *testing.T) {
	set := domain.DefaultSet(bank(), []string{"concurrency"}, "senior")
	if got := titles(set); !reflect.DeepEqual(got, []string{"Medium tagged", "Hard tagged"}) {
		t.Errorf("titles = %v, want the tagged pair", got)
	}
	// Without a skill to match, the higher-quality problems win the slots.
	if got := titles(domain.DefaultSet(bank(), nil, "senior")); !reflect.DeepEqual(got, []string{"Medium plain", "Hard plain"}) {
		t.Errorf("untagged titles = %v", got)
	}
}

func TestDefaultSetWeightsEarlierSkillsHigher(t *testing.T) {
	problems := []domain.SetProblem{
		{ID: uuid.MustParse("00000000-0000-0000-0000-000000000101"), Title: "Covers first", Difficulty: "medium", Tags: []string{"concurrency"}, Quality: 60},
		{ID: uuid.MustParse("00000000-0000-0000-0000-000000000102"), Title: "Covers second", Difficulty: "medium", Tags: []string{"sql"}, Quality: 99},
	}
	set := domain.DefaultSet(problems, []string{"concurrency", "sql"}, "senior")
	if len(set) != 1 || set[0].Title != "Covers first" {
		t.Errorf("set = %v, want the problem covering the first skill", titles(set))
	}
	flipped := domain.DefaultSet(problems, []string{"sql", "concurrency"}, "senior")
	if len(flipped) != 1 || flipped[0].Title != "Covers second" {
		t.Errorf("flipped = %v, want the problem covering the first skill", titles(flipped))
	}
}

func TestDefaultSetExcludesProblemsBelowTheQualityFloor(t *testing.T) {
	problems := []domain.SetProblem{
		{ID: uuid.MustParse("00000000-0000-0000-0000-000000000201"), Title: "Below floor", Difficulty: "medium", Tags: []string{"go"}, Quality: domain.ProblemQualityFloor - 1},
		{ID: uuid.MustParse("00000000-0000-0000-0000-000000000202"), Title: "At floor", Difficulty: "medium", Quality: domain.ProblemQualityFloor},
	}
	if got := titles(domain.DefaultSet(problems, []string{"go"}, "junior")); !reflect.DeepEqual(got, []string{"At floor"}) {
		t.Errorf("titles = %v, want only the problem at the floor", got)
	}
}

func TestDefaultSetLeavesAnUnfillableSlotOut(t *testing.T) {
	problems := []domain.SetProblem{
		{ID: uuid.MustParse("00000000-0000-0000-0000-000000000301"), Title: "Only medium", Difficulty: "medium", Quality: 80},
	}
	set := domain.DefaultSet(problems, nil, "junior")
	if got := titles(set); !reflect.DeepEqual(got, []string{"Only medium"}) {
		t.Errorf("titles = %v, want the medium slot alone", got)
	}
}

func TestDefaultSetNeverRepeatsAProblem(t *testing.T) {
	// One problem cannot fill both slots even when it is the only match.
	problems := []domain.SetProblem{
		{ID: uuid.MustParse("00000000-0000-0000-0000-000000000401"), Title: "Medium", Difficulty: "medium", Quality: 80},
		{ID: uuid.MustParse("00000000-0000-0000-0000-000000000402"), Title: "Also medium", Difficulty: "medium", Quality: 80},
	}
	set := domain.DefaultSet(problems, nil, "junior")
	if len(set) != 1 {
		t.Fatalf("set = %v, want one problem: nothing fills the easy slot", titles(set))
	}
}

func TestDefaultSetIsDeterministic(t *testing.T) {
	// Same quality, same coverage: the order the bank was read in must not
	// decide the set.
	forwards := []domain.SetProblem{
		{ID: uuid.MustParse("00000000-0000-0000-0000-000000000501"), Title: "Beta", Difficulty: "medium", Quality: 80},
		{ID: uuid.MustParse("00000000-0000-0000-0000-000000000502"), Title: "Alpha", Difficulty: "medium", Quality: 80},
	}
	backwards := []domain.SetProblem{forwards[1], forwards[0]}
	a, b := domain.DefaultSet(forwards, nil, "junior"), domain.DefaultSet(backwards, nil, "junior")
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("order changed the set: %v vs %v", titles(a), titles(b))
	}
	if len(a) != 1 || a[0].Title != "Alpha" {
		t.Errorf("set = %v, want Alpha", titles(a))
	}
}

func TestRankSetProblemsOffersEverySwapForOneDifficulty(t *testing.T) {
	got := titles(domain.RankSetProblems(bank(), []string{"go"}, "hard"))
	if !reflect.DeepEqual(got, []string{"Hard tagged", "Hard plain"}) {
		t.Errorf("ranked = %v", got)
	}
}
