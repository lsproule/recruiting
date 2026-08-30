package domain_test

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
)

func poolEntry(skills []string, seniority, location string, remoteOK bool, best float64) domain.PoolEntry {
	return domain.PoolEntry{
		ID: uuid.New(), CandidateID: uuid.New(), Skills: skills,
		Seniority: seniority, Location: location, RemoteOK: remoteOK, BestAssessmentScore: best,
	}
}

func TestPoolMatchScoresEachPart(t *testing.T) {
	job := domain.PoolJob{
		Skills: []string{"go", "postgres", "kubernetes"}, Seniority: "senior",
		Location: "Berlin", RemotePolicy: "onsite",
	}
	cases := []struct {
		name  string
		entry domain.PoolEntry
		want  domain.MatchBreakdown
		score float64
	}{
		{
			name:  "two of three skills, same seniority and city, strong assessment",
			entry: poolEntry([]string{"go", "postgres"}, "senior", "Berlin", false, 90),
			want:  domain.MatchBreakdown{Skills: 2.0 / 3.0, Seniority: 1, Location: 1, Assessment: 0.9},
			score: 0.6*(2.0/3.0) + 0.2 + 0.1 + 0.09,
		},
		{
			name:  "adjacent seniority is half credit",
			entry: poolEntry([]string{"go", "postgres", "kubernetes"}, "mid", "Berlin", false, 0),
			want:  domain.MatchBreakdown{Skills: 1, Seniority: 0.5, Location: 1, Assessment: 0},
			score: 0.6 + 0.1 + 0.1,
		},
		{
			name:  "two rungs apart scores nothing for seniority",
			entry: poolEntry([]string{"go"}, "junior", "Berlin", false, 0),
			want:  domain.MatchBreakdown{Skills: 1.0 / 3.0, Seniority: 0, Location: 1, Assessment: 0},
			score: 0.6*(1.0/3.0) + 0.1,
		},
		{
			name:  "remote-ok stands in for the city",
			entry: poolEntry([]string{"go", "postgres"}, "senior", "Lisbon", true, 50),
			want:  domain.MatchBreakdown{Skills: 2.0 / 3.0, Seniority: 1, Location: 1, Assessment: 0.5},
			score: 0.6*(2.0/3.0) + 0.2 + 0.1 + 0.05,
		},
		{
			name:  "another city, not remote, scores nothing for location",
			entry: poolEntry([]string{"go", "postgres"}, "senior", "Lisbon", false, 0),
			want:  domain.MatchBreakdown{Skills: 2.0 / 3.0, Seniority: 1, Location: 0, Assessment: 0},
			score: 0.6*(2.0/3.0) + 0.2,
		},
		{
			name:  "tags are matched case- and space-insensitively",
			entry: poolEntry([]string{" Go ", "POSTGRES", "kubernetes"}, "senior", "berlin", false, 100),
			want:  domain.MatchBreakdown{Skills: 1, Seniority: 1, Location: 1, Assessment: 1},
			score: 1,
		},
		{
			name:  "an unknown seniority scores nothing rather than matching itself",
			entry: poolEntry([]string{"go", "postgres", "kubernetes"}, "", "Berlin", false, 0),
			want:  domain.MatchBreakdown{Skills: 1, Seniority: 0, Location: 1, Assessment: 0},
			score: 0.6 + 0.1,
		},
		{
			name:  "a score above the scale is capped",
			entry: poolEntry(nil, "", "Berlin", false, 150),
			want:  domain.MatchBreakdown{Skills: 0, Seniority: 0, Location: 1, Assessment: 1},
			score: 0.2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			score, got := domain.ScorePoolMatch(job, tc.entry)
			if !near(got.Skills, tc.want.Skills) || !near(got.Seniority, tc.want.Seniority) ||
				!near(got.Location, tc.want.Location) || !near(got.Assessment, tc.want.Assessment) {
				t.Errorf("breakdown = %+v, want %+v", got, tc.want)
			}
			if !near(score, tc.score) {
				t.Errorf("score = %v, want %v", score, tc.score)
			}
		})
	}
}

func TestPoolMatchIgnoresAJobWithNoSkills(t *testing.T) {
	job := domain.PoolJob{Seniority: "senior", Location: "Berlin"}
	score, got := domain.ScorePoolMatch(job, poolEntry([]string{"go"}, "senior", "Berlin", false, 0))
	if !near(got.Skills, 0) {
		t.Errorf("skills = %v, want 0", got.Skills)
	}
	if !near(score, 0.3) {
		t.Errorf("score = %v, want 0.3", score)
	}
}

func TestRankPoolHidesEntriesBelowTheThreshold(t *testing.T) {
	job := domain.PoolJob{Skills: []string{"go"}, Seniority: "senior", Location: "Berlin"}
	// Location and a perfect assessment alone reach exactly the threshold.
	atThreshold := poolEntry([]string{"rust"}, "", "Berlin", false, 100)
	below := poolEntry([]string{"rust"}, "", "Berlin", false, 40)
	got := domain.RankPool(job, []domain.PoolEntry{atThreshold, below}, domain.PoolExclusions{}, time.Now())
	if len(got) != 1 {
		t.Fatalf("got %d suggestions, want 1", len(got))
	}
	if got[0].Entry.ID != atThreshold.ID {
		t.Errorf("kept %v, want the entry on the threshold", got[0].Entry.ID)
	}
	if !near(got[0].Score, domain.PoolThreshold) {
		t.Errorf("score = %v, want %v", got[0].Score, domain.PoolThreshold)
	}
}

func TestRankPoolKeepsTheBestTwenty(t *testing.T) {
	job := domain.PoolJob{Skills: []string{"go"}, Seniority: "senior", Location: "Berlin"}
	var entries []domain.PoolEntry
	for i := 0; i < 25; i++ {
		entries = append(entries, poolEntry([]string{"go"}, "senior", "Berlin", false, float64(i*4)))
	}
	got := domain.RankPool(job, entries, domain.PoolExclusions{}, time.Now())
	if len(got) != domain.PoolLimit {
		t.Fatalf("got %d suggestions, want %d", len(got), domain.PoolLimit)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Score < got[i].Score {
			t.Fatalf("suggestion %d scores %v, below %v at %d", i-1, got[i-1].Score, got[i].Score, i)
		}
	}
	if !near(got[0].Score, 0.6+0.2+0.1+0.096) {
		t.Errorf("best score = %v", got[0].Score)
	}
}

func TestRankPoolExcludesCandidatesAlreadyOnTheJob(t *testing.T) {
	job := domain.PoolJob{Skills: []string{"go"}, Seniority: "senior", Location: "Berlin"}
	in := poolEntry([]string{"go"}, "senior", "Berlin", false, 100)
	out := poolEntry([]string{"go"}, "senior", "Berlin", false, 100)
	got := domain.RankPool(job, []domain.PoolEntry{in, out},
		domain.PoolExclusions{InJob: map[uuid.UUID]bool{in.CandidateID: true}}, time.Now())
	if len(got) != 1 || got[0].Entry.CandidateID != out.CandidateID {
		t.Fatalf("got %d suggestions, want only the candidate not yet on the job", len(got))
	}
}

func TestRankPoolExcludesRecentRejectionsByTheSameClient(t *testing.T) {
	job := domain.PoolJob{Skills: []string{"go"}, Seniority: "senior", Location: "Berlin"}
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	cutoff := now.AddDate(0, -domain.PoolRejectionWindowMonths, 0)
	inside := poolEntry([]string{"go"}, "senior", "Berlin", false, 100)
	onEdge := poolEntry([]string{"go"}, "senior", "Berlin", false, 100)
	outside := poolEntry([]string{"go"}, "senior", "Berlin", false, 100)
	ex := domain.PoolExclusions{RejectedAt: map[uuid.UUID]time.Time{
		inside.CandidateID:  cutoff.Add(time.Second),
		onEdge.CandidateID:  cutoff,
		outside.CandidateID: cutoff.Add(-time.Second),
	}}
	got := domain.RankPool(job, []domain.PoolEntry{inside, onEdge, outside}, ex, now)
	kept := map[uuid.UUID]bool{}
	for _, s := range got {
		kept[s.Entry.CandidateID] = true
	}
	if kept[inside.CandidateID] {
		t.Error("a rejection inside the window was suggested back to the client")
	}
	if !kept[onEdge.CandidateID] || !kept[outside.CandidateID] {
		t.Errorf("rejections at or beyond the window edge were dropped: %v", kept)
	}
}

func near(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

func TestHybridJobsStillMatchOnTheCity(t *testing.T) {
	// A hybrid role is not a remote one: the candidate has to be able to come
	// in, so only their city — or their own remote working — earns the part.
	job := domain.PoolJob{Skills: []string{"go"}, Seniority: "senior", Location: "Berlin", RemotePolicy: "hybrid"}
	cases := []struct {
		name  string
		entry domain.PoolEntry
		want  float64
	}{
		{"same city", poolEntry([]string{"go"}, "senior", "Berlin", false, 0), 1},
		{"another city", poolEntry([]string{"go"}, "senior", "Lisbon", false, 0), 0},
		{"another city, works remotely", poolEntry([]string{"go"}, "senior", "Lisbon", true, 0), 1},
		{"no city on file", poolEntry([]string{"go"}, "senior", "", false, 0), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got := domain.ScorePoolMatch(job, tc.entry)
			if !near(got.Location, tc.want) {
				t.Errorf("location = %v, want %v", got.Location, tc.want)
			}
		})
	}
}
