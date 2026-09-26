package domain

import (
	"time"

	"github.com/google/uuid"
)

// Pairing is one conversation of a sprint: in round Round, the interviewer
// meets the candidate's application.
type Pairing struct {
	Round         int
	InterviewerID uuid.UUID
	ApplicationID uuid.UUID
}

// PlanRounds schedules a sprint so that every candidate meets every
// interviewer exactly once. The larger side rotates: with n candidates and
// k interviewers there are max(n, k) rounds, and in round r the i-th member
// of the smaller side meets member (r - i) mod max(n, k) of the larger one.
// Candidate c therefore meets interviewer j in round c + j: a candidate's
// conversations fall in consecutive rounds starting at their own index
// (wrapping past the last round for the last few candidates), and no one is
// booked twice in a round. Either side empty plans nothing.
func PlanRounds(candidates, interviewers []uuid.UUID) []Pairing {
	n, k := len(candidates), len(interviewers)
	if n == 0 || k == 0 {
		return nil
	}
	rounds := SprintRounds(n, k)
	out := make([]Pairing, 0, n*k)
	for r := 0; r < rounds; r++ {
		if n >= k {
			for j, interviewer := range interviewers {
				out = append(out, Pairing{Round: r, InterviewerID: interviewer, ApplicationID: candidates[mod(r-j, n)]})
			}
			continue
		}
		for i, candidate := range candidates {
			out = append(out, Pairing{Round: r, InterviewerID: interviewers[mod(r-i, k)], ApplicationID: candidate})
		}
	}
	return out
}

// mod is the non-negative remainder.
func mod(a, n int) int { return ((a % n) + n) % n }

// SprintRounds is how many rounds a sprint of n candidates and k
// interviewers takes: enough for the larger side to meet everyone.
func SprintRounds(n, k int) int {
	if n == 0 || k == 0 {
		return 0
	}
	if n > k {
		return n
	}
	return k
}

// SprintPhase is where a sprint stands on its clock.
type SprintPhase string

const (
	PhaseBefore SprintPhase = "before" // not started
	PhaseRound  SprintPhase = "round"  // a round is running
	PhaseBreak  SprintPhase = "break"  // between two rounds
	PhaseAfter  SprintPhase = "after"  // the last round has ended
)

// SprintClock is a sprint's timetable: rounds of RoundSeconds separated by
// BreakSeconds, starting at StartsAt. Every page reads the same clock, so
// they rotate together without the server pushing anything.
type SprintClock struct {
	StartsAt     time.Time
	Rounds       int
	RoundSeconds int
	BreakSeconds int
}

// Period is one round plus the break after it.
func (c SprintClock) Period() time.Duration {
	return time.Duration(c.RoundSeconds+c.BreakSeconds) * time.Second
}

// RoundStart is when round r begins.
func (c SprintClock) RoundStart(r int) time.Time {
	return c.StartsAt.Add(time.Duration(r) * c.Period())
}

// RoundEnd is when round r's conversation ends and its break begins.
func (c SprintClock) RoundEnd(r int) time.Time {
	return c.RoundStart(r).Add(time.Duration(c.RoundSeconds) * time.Second)
}

// EndsAt is when the last round's conversation ends; there is no break
// after it.
func (c SprintClock) EndsAt() time.Time {
	if c.Rounds <= 0 {
		return c.StartsAt
	}
	return c.RoundEnd(c.Rounds - 1)
}

// SprintState is the clock read at one instant: the phase, the round it
// refers to (the running one, the one just ended during a break, the first
// before the start, the last after the end), and when the phase changes.
type SprintState struct {
	Phase SprintPhase
	Round int
	Next  time.Time
}

// At reads the clock at now.
func (c SprintClock) At(now time.Time) SprintState {
	if c.Rounds <= 0 || c.RoundSeconds <= 0 {
		return SprintState{Phase: PhaseAfter, Next: c.StartsAt}
	}
	if now.Before(c.StartsAt) {
		return SprintState{Phase: PhaseBefore, Round: 0, Next: c.StartsAt}
	}
	if !now.Before(c.EndsAt()) {
		return SprintState{Phase: PhaseAfter, Round: c.Rounds - 1, Next: c.EndsAt()}
	}
	elapsed := now.Sub(c.StartsAt)
	r := int(elapsed / c.Period())
	if r >= c.Rounds {
		r = c.Rounds - 1
	}
	if now.Before(c.RoundEnd(r)) {
		return SprintState{Phase: PhaseRound, Round: r, Next: c.RoundEnd(r)}
	}
	return SprintState{Phase: PhaseBreak, Round: r, Next: c.RoundStart(r + 1)}
}

// RoomOpen reports whether round r's room may be joined at now: from the
// round's start until the next round starts, so a conversation that runs a
// little long is not cut off mid-sentence, and never before the round.
func (c SprintClock) RoomOpen(r int, now time.Time) bool {
	if r < 0 || r >= c.Rounds {
		return false
	}
	if now.Before(c.RoundStart(r)) {
		return false
	}
	if r == c.Rounds-1 {
		return now.Before(c.EndsAt().Add(time.Duration(c.BreakSeconds) * time.Second))
	}
	return now.Before(c.RoundStart(r + 1))
}
