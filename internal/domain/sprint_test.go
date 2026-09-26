package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
)

func ids(n int) []uuid.UUID {
	out := make([]uuid.UUID, n)
	for i := range out {
		out[i] = uuid.New()
	}
	return out
}

type pairKey struct{ i, a uuid.UUID }

// checkPlan proves the three properties every plan must have: each pair
// meets exactly once, nobody is booked twice in a round, and the round
// count is the larger side.
func checkPlan(t *testing.T, candidates, interviewers []uuid.UUID, plan []domain.Pairing) {
	t.Helper()
	n, k := len(candidates), len(interviewers)
	if len(plan) != n*k {
		t.Fatalf("%d×%d: %d pairings, want %d", n, k, len(plan), n*k)
	}
	seen := map[pairKey]int{}
	busy := map[[2]string]bool{}
	rounds := 0
	for _, p := range plan {
		seen[pairKey{p.InterviewerID, p.ApplicationID}]++
		for _, who := range []string{"i:" + p.InterviewerID.String(), "a:" + p.ApplicationID.String()} {
			key := [2]string{who, string(rune(p.Round))}
			if busy[key] {
				t.Fatalf("%d×%d: %s booked twice in round %d", n, k, who, p.Round)
			}
			busy[key] = true
		}
		if p.Round+1 > rounds {
			rounds = p.Round + 1
		}
	}
	for _, c := range candidates {
		for _, i := range interviewers {
			if seen[pairKey{i, c}] != 1 {
				t.Fatalf("%d×%d: pair met %d times", n, k, seen[pairKey{i, c}])
			}
		}
	}
	if want := domain.SprintRounds(n, k); rounds != want {
		t.Fatalf("%d×%d: %d rounds, want %d", n, k, rounds, want)
	}
}

func TestPlanRoundsMeetsEveryoneOnce(t *testing.T) {
	for _, tc := range [][2]int{{12, 6}, {6, 12}, {5, 5}, {1, 4}, {4, 1}, {1, 1}, {7, 3}} {
		candidates, interviewers := ids(tc[0]), ids(tc[1])
		checkPlan(t, candidates, interviewers, domain.PlanRounds(candidates, interviewers))
	}
}

func TestPlanRoundsKeepsACandidateBlockContiguous(t *testing.T) {
	candidates, interviewers := ids(12), ids(6)
	plan := domain.PlanRounds(candidates, interviewers)
	// The first candidate meets interviewer j in round j: six consecutive
	// rounds from the start, which is what the invite promises.
	for _, p := range plan {
		if p.ApplicationID != candidates[0] {
			continue
		}
		if p.Round > 5 {
			t.Fatalf("first candidate booked in round %d", p.Round)
		}
	}
}

func TestPlanRoundsWithNobody(t *testing.T) {
	if got := domain.PlanRounds(nil, ids(3)); got != nil {
		t.Fatalf("no candidates planned %d pairings", len(got))
	}
	if got := domain.PlanRounds(ids(3), nil); got != nil {
		t.Fatalf("no interviewers planned %d pairings", len(got))
	}
}

func TestSprintClockPhases(t *testing.T) {
	start := time.Date(2026, time.October, 1, 10, 0, 0, 0, time.UTC)
	c := domain.SprintClock{StartsAt: start, Rounds: 3, RoundSeconds: 300, BreakSeconds: 60}
	sec := func(s int) time.Time { return start.Add(time.Duration(s) * time.Second) }
	cases := []struct {
		at    time.Time
		phase domain.SprintPhase
		round int
		next  time.Time
	}{
		{sec(-1), domain.PhaseBefore, 0, start},
		{sec(0), domain.PhaseRound, 0, sec(300)},
		{sec(299), domain.PhaseRound, 0, sec(300)},
		{sec(300), domain.PhaseBreak, 0, sec(360)},
		{sec(360), domain.PhaseRound, 1, sec(660)},
		{sec(720), domain.PhaseRound, 2, sec(1020)},
		{sec(1019), domain.PhaseRound, 2, sec(1020)},
		// No break after the last round: it ends and the sprint is over.
		{sec(1020), domain.PhaseAfter, 2, sec(1020)},
		{sec(5000), domain.PhaseAfter, 2, sec(1020)},
	}
	for _, tc := range cases {
		got := c.At(tc.at)
		if got.Phase != tc.phase || got.Round != tc.round || !got.Next.Equal(tc.next) {
			t.Fatalf("at %s: %+v, want %s round %d next %s", tc.at.Sub(start), got, tc.phase, tc.round, tc.next.Sub(start))
		}
	}
	if !c.EndsAt().Equal(sec(1020)) {
		t.Fatalf("ends at %s", c.EndsAt().Sub(start))
	}
}

func TestSprintClockRoomWindow(t *testing.T) {
	start := time.Date(2026, time.October, 1, 10, 0, 0, 0, time.UTC)
	c := domain.SprintClock{StartsAt: start, Rounds: 2, RoundSeconds: 300, BreakSeconds: 60}
	sec := func(s int) time.Time { return start.Add(time.Duration(s) * time.Second) }
	for _, tc := range []struct {
		round int
		at    time.Time
		open  bool
	}{
		{0, sec(-1), false},
		{0, sec(0), true},
		{0, sec(359), true}, // the break after a round still lets it run over
		{0, sec(360), false},
		{1, sec(359), false},
		{1, sec(360), true},
		{1, sec(719), true}, // the last round gets the same grace
		{1, sec(720), false},
		{2, sec(400), false},
	} {
		if got := c.RoomOpen(tc.round, tc.at); got != tc.open {
			t.Fatalf("round %d at %s: open %v, want %v", tc.round, tc.at.Sub(start), got, tc.open)
		}
	}
}

func TestSprintClockEmpty(t *testing.T) {
	c := domain.SprintClock{StartsAt: time.Now()}
	if got := c.At(time.Now()); got.Phase != domain.PhaseAfter {
		t.Fatalf("an empty sprint is %s", got.Phase)
	}
}
