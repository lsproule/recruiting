package domain

import (
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Weights of the four parts of a talent match; they sum to 1. Skills carry
// most of it, as in the pool ranker; the text part is what the résumé says
// about the request's terms, which the pool ranker has no equivalent of.
const (
	talentSkillsWeight    = 0.45
	talentSeniorityWeight = 0.2
	talentLocationWeight  = 0.15
	talentTextWeight      = 0.2
)

// TalentWeights are the weights ScoreTalentMatch gives the four parts.
var TalentWeights = TalentBreakdown{
	Skills:    talentSkillsWeight,
	Seniority: talentSeniorityWeight,
	Location:  talentLocationWeight,
	Text:      talentTextWeight,
}

// TalentThreshold is the score a match must reach to be shown to a company.
const TalentThreshold = 0.15

// TalentLimit caps how many matches one request shows.
const TalentLimit = 25

// Talent sources: where a match came from.
const (
	TalentSourceNetwork = "network"
	TalentSourcePool    = "pool"
)

// TalentEntry is one person the matcher scores: a talent-network profile
// or a pool entry, reduced to the fields both carry. TextRank is the
// full-text rank of the request's terms against the person's résumé and
// name index, on whatever scale the index reports; the matcher normalises
// it against the best in the batch.
type TalentEntry struct {
	ID          uuid.UUID
	CandidateID uuid.UUID
	Source      string
	Skills      []string
	Seniority   string
	Location    string
	// RemotePolicy is what the person wants: remote, hybrid, onsite, or
	// empty when they did not say. A pool entry's "works remotely" maps to
	// remote.
	RemotePolicy string
	TextRank     float64
}

// TalentRequest is what a company is looking for, as the matcher sees it.
type TalentRequest struct {
	Skills       []string
	Seniority    string
	Location     string
	RemotePolicy string
}

// TalentBreakdown is the "why" behind a match: each part before its weight.
type TalentBreakdown struct {
	Skills    float64
	Seniority float64
	Location  float64
	Text      float64
}

// TalentMatch is one ranked entry with the arithmetic behind it.
type TalentMatch struct {
	Entry     TalentEntry
	Score     float64
	Breakdown TalentBreakdown
}

// ScoreTalentMatch scores one entry against one request. textScale is the
// best text rank in the batch, which maps the text part onto [0, 1]; zero
// means no entry answered the terms and the text part scores nothing.
func ScoreTalentMatch(req TalentRequest, e TalentEntry, textScale float64) (float64, TalentBreakdown) {
	b := TalentBreakdown{
		Skills:    jaccard(req.Skills, e.Skills),
		Seniority: seniorityFit(req.Seniority, e.Seniority),
		Location:  talentLocationFit(req, e),
	}
	if textScale > 0 {
		b.Text = clamp01(e.TextRank / textScale)
	}
	score := talentSkillsWeight*b.Skills + talentSeniorityWeight*b.Seniority +
		talentLocationWeight*b.Location + talentTextWeight*b.Text
	return score, b
}

// RankTalent is the match list for one request: excluded candidates
// dropped, entries below the threshold hidden, best first, at most
// TalentLimit of them. A candidate present twice (in the network and in
// the pool) keeps their better-scoring entry.
func RankTalent(req TalentRequest, entries []TalentEntry, exclude map[uuid.UUID]bool) []TalentMatch {
	scale := 0.0
	for _, e := range entries {
		if e.TextRank > scale {
			scale = e.TextRank
		}
	}
	best := make(map[uuid.UUID]TalentMatch, len(entries))
	for _, e := range entries {
		if exclude[e.CandidateID] {
			continue
		}
		score, b := ScoreTalentMatch(req, e, scale)
		if score < TalentThreshold {
			continue
		}
		if prior, ok := best[e.CandidateID]; ok && prior.Score >= score {
			continue
		}
		best[e.CandidateID] = TalentMatch{Entry: e, Score: score, Breakdown: b}
	}
	out := make([]TalentMatch, 0, len(best))
	for _, m := range best {
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Entry.ID.String() < out[j].Entry.ID.String()
	})
	if len(out) > TalentLimit {
		out = out[:TalentLimit]
	}
	return out
}

// talentLocationFit is 1 when the role can be done from where the person
// is or wants to be: the role is remote and the person did not rule remote
// work out, the person wants remote and the role allows it (remote or
// hybrid), or both name the same place. A hybrid role in the person's own
// city fits; a hybrid role elsewhere is half a fit for someone who wants
// remote. Nothing to compare scores nothing.
func talentLocationFit(req TalentRequest, e TalentEntry) float64 {
	role := strings.ToLower(strings.TrimSpace(req.RemotePolicy))
	want := strings.ToLower(strings.TrimSpace(e.RemotePolicy))
	a := strings.ToLower(strings.TrimSpace(req.Location))
	b := strings.ToLower(strings.TrimSpace(e.Location))
	samePlace := a != "" && a == b
	switch {
	case role == remotePolicyRemote:
		if want == "onsite" && !samePlace {
			return 0.5
		}
		return 1
	case samePlace:
		return 1
	case want == remotePolicyRemote && role == "hybrid":
		return 0.5
	case want == remotePolicyRemote && role == "":
		return 0.5
	}
	return 0
}

// SharedSkills is the request's skills the entry also lists, in the
// request's order and spelling, for a match card to show what earned it.
func SharedSkills(req []string, have []string) []string {
	set := tagSet(have)
	out := []string{}
	for _, s := range req {
		if set[strings.ToLower(strings.TrimSpace(s))] {
			out = append(out, s)
		}
	}
	return out
}
