package domain

import (
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Weights of the four parts of a talent-pool match; they sum to 1.
const (
	poolSkillsWeight     = 0.6
	poolSeniorityWeight  = 0.2
	poolLocationWeight   = 0.1
	poolAssessmentWeight = 0.1
)

// PoolThreshold is the score a suggestion must reach to be worth showing.
const PoolThreshold = 0.2

// PoolLimit caps how many suggestions one job shows.
const PoolLimit = 20

// PoolRejectionWindowMonths is how long a client company's rejection keeps a
// candidate out of that company's suggestions.
const PoolRejectionWindowMonths = 6

// AssessmentScoreMax is the top of the assessment scale the last part of the
// match normalises against.
const AssessmentScoreMax = 100

// remotePolicyRemote is the job policy that makes any location a fit. The
// service layer owns the full vocabulary; the ranker needs only this one.
const remotePolicyRemote = "remote"

// seniorityLadder orders the rungs a job and an entry are compared on:
// neighbours are half a fit, anything further apart is none.
var seniorityLadder = []string{"junior", "mid", "senior", "staff"}

// PoolEntry is one talent-pool entry as the ranker sees it.
type PoolEntry struct {
	ID          uuid.UUID
	CandidateID uuid.UUID
	Skills      []string
	Seniority   string
	Location    string
	RemoteOK    bool
	// BestAssessmentScore is the candidate's best assessment score on the
	// 0–AssessmentScoreMax scale; zero when they have never sat one.
	BestAssessmentScore float64
}

// PoolJob is the role being filled, as the ranker sees it.
type PoolJob struct {
	Skills       []string
	Seniority    string
	Location     string
	RemotePolicy string
}

// MatchBreakdown is the "why" behind a suggestion: each part before its
// weight, so a recruiter reads what earned the score.
type MatchBreakdown struct {
	Skills     float64
	Seniority  float64
	Location   float64
	Assessment float64
}

// PoolSuggestion is one ranked entry with the arithmetic behind it.
type PoolSuggestion struct {
	Entry     PoolEntry
	Score     float64
	Breakdown MatchBreakdown
}

// PoolExclusions names the candidates a job must not be offered. InJob holds
// those already on it; RejectedAt the most recent rejection by the job's own
// client company, whoever the job was.
type PoolExclusions struct {
	InJob      map[uuid.UUID]bool
	RejectedAt map[uuid.UUID]time.Time
}

// ScorePoolMatch scores one entry against one job and reports the parts it
// came from. A part with nothing to compare — no skills on either side, an
// unnamed seniority — scores zero rather than matching itself.
func ScorePoolMatch(job PoolJob, entry PoolEntry) (float64, MatchBreakdown) {
	b := MatchBreakdown{
		Skills:     jaccard(job.Skills, entry.Skills),
		Seniority:  seniorityFit(job.Seniority, entry.Seniority),
		Location:   locationFit(job, entry),
		Assessment: clamp01(entry.BestAssessmentScore / AssessmentScoreMax),
	}
	score := poolSkillsWeight*b.Skills + poolSeniorityWeight*b.Seniority +
		poolLocationWeight*b.Location + poolAssessmentWeight*b.Assessment
	return score, b
}

// RankPool is the suggestion list for one job: excluded candidates dropped,
// entries below the threshold hidden, best first, at most PoolLimit of them.
// Equal scores keep a stable order so a redraw does not reshuffle the panel.
func RankPool(job PoolJob, entries []PoolEntry, ex PoolExclusions, now time.Time) []PoolSuggestion {
	cutoff := now.AddDate(0, -PoolRejectionWindowMonths, 0)
	out := make([]PoolSuggestion, 0, len(entries))
	for _, e := range entries {
		if ex.InJob[e.CandidateID] {
			continue
		}
		if at, ok := ex.RejectedAt[e.CandidateID]; ok && at.After(cutoff) {
			continue
		}
		score, b := ScorePoolMatch(job, e)
		if score < PoolThreshold {
			continue
		}
		out = append(out, PoolSuggestion{Entry: e, Score: score, Breakdown: b})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Entry.ID.String() < out[j].Entry.ID.String()
	})
	if len(out) > PoolLimit {
		out = out[:PoolLimit]
	}
	return out
}

// jaccard is the overlap of two tag sets over their union, comparing tags
// case- and space-insensitively.
func jaccard(a, b []string) float64 {
	setA, setB := tagSet(a), tagSet(b)
	if len(setA) == 0 || len(setB) == 0 {
		return 0
	}
	shared := 0
	for tag := range setA {
		if setB[tag] {
			shared++
		}
	}
	return float64(shared) / float64(len(setA)+len(setB)-shared)
}

func tagSet(tags []string) map[string]bool {
	out := make(map[string]bool, len(tags))
	for _, t := range tags {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			out[t] = true
		}
	}
	return out
}

// seniorityFit is 1 for the same rung, 0.5 for a neighbouring one, and 0 when
// either side names no rung the ladder knows.
func seniorityFit(job, entry string) float64 {
	i, j := ladderIndex(job), ladderIndex(entry)
	switch {
	case i < 0 || j < 0:
		return 0
	case i == j:
		return 1
	case i-j == 1 || j-i == 1:
		return 0.5
	}
	return 0
}

func ladderIndex(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	for i, rung := range seniorityLadder {
		if rung == s {
			return i
		}
	}
	return -1
}

// locationFit is 1 when the role can be done from where the candidate is:
// the job is remote, the candidate works remotely, or both name one place.
func locationFit(job PoolJob, entry PoolEntry) float64 {
	if strings.EqualFold(strings.TrimSpace(job.RemotePolicy), remotePolicyRemote) || entry.RemoteOK {
		return 1
	}
	a := strings.ToLower(strings.TrimSpace(job.Location))
	b := strings.ToLower(strings.TrimSpace(entry.Location))
	if a != "" && a == b {
		return 1
	}
	return 0
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}
