package domain

import (
	"sort"
	"strings"

	"github.com/google/uuid"
)

// SetProblem is one bank entry as the default-set picker sees it: enough to
// rank it, nothing that would make the picker read the bank itself.
type SetProblem struct {
	ID         uuid.UUID
	Title      string
	Difficulty string
	Tags       []string
	Quality    int
}

// defaultSetJunior and defaultSetSenior are the two shapes a default set
// takes: an easier pair for the rungs still being taught, a harder pair for
// the ones expected to design.
var (
	defaultSetJunior = []string{"easy", "medium"}
	defaultSetSenior = []string{"medium", "hard"}
)

// seniorSeniorities are the rungs that get the harder pair. "principal" is
// not a seniority a job can be saved with today, but intake copy offers it,
// so the picker reads it rather than falling back to the junior pair.
var seniorSeniorities = map[string]bool{"senior": true, "staff": true, "principal": true}

// DefaultSetDifficulties is the difficulty of each slot a default set fills,
// in order. An unknown or missing seniority takes the easier pair: guessing a
// harder set for a job that never said so wastes the candidate's hour.
func DefaultSetDifficulties(seniority string) []string {
	if seniorSeniorities[strings.ToLower(strings.TrimSpace(seniority))] {
		return append([]string(nil), defaultSetSenior...)
	}
	return append([]string(nil), defaultSetJunior...)
}

// DefaultSet picks one problem per slot of DefaultSetDifficulties, preferring
// overlap with skills — earlier skills count for more — and excluding
// anything under ProblemQualityFloor, which no assessment may carry anyway.
// Equal inputs always give the same set; a slot the bank cannot fill is left
// out rather than filled from another difficulty.
func DefaultSet(problems []SetProblem, skills []string, seniority string) []SetProblem {
	var out []SetProblem
	taken := map[uuid.UUID]bool{}
	for _, difficulty := range DefaultSetDifficulties(seniority) {
		for _, p := range RankSetProblems(problems, skills, difficulty) {
			if taken[p.ID] {
				continue
			}
			taken[p.ID] = true
			out = append(out, p)
			break
		}
	}
	return out
}

// RankSetProblems orders the bank's problems of one difficulty by how well
// they cover skills, best first. It is what a slot's swap list offers, and
// what DefaultSet takes the head of.
func RankSetProblems(problems []SetProblem, skills []string, difficulty string) []SetProblem {
	weights := skillWeights(skills)
	difficulty = strings.ToLower(strings.TrimSpace(difficulty))

	out := make([]SetProblem, 0, len(problems))
	for _, p := range problems {
		if p.Quality < ProblemQualityFloor {
			continue
		}
		if strings.ToLower(strings.TrimSpace(p.Difficulty)) != difficulty {
			continue
		}
		out = append(out, p)
	}
	// Title then id break every tie, so two runs over the same bank cannot
	// hand the recruiter a different set.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ca, cb := skillCover(a.Tags, weights), skillCover(b.Tags, weights); ca != cb {
			return ca > cb
		}
		if a.Quality != b.Quality {
			return a.Quality > b.Quality
		}
		if a.Title != b.Title {
			return a.Title < b.Title
		}
		return a.ID.String() < b.ID.String()
	})
	return out
}

// skillWeights scores the skills by the order the recruiter put them in: the
// first skill is the one the assessment most has to prove.
func skillWeights(skills []string) map[string]int {
	out := make(map[string]int, len(skills))
	rank := len(skills)
	for _, s := range skills {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || out[s] > 0 {
			continue
		}
		out[s] = rank
		rank--
	}
	return out
}

func skillCover(tags []string, weights map[string]int) int {
	total := 0
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if seen[tag] {
			continue
		}
		seen[tag] = true
		total += weights[tag]
	}
	return total
}
