package domain_test

import (
	"math"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/domain"
)

func talentEntry(source string, skills []string, seniority, location, remote string, text float64) domain.TalentEntry {
	return domain.TalentEntry{
		ID: uuid.New(), CandidateID: uuid.New(), Source: source, Skills: skills,
		Seniority: seniority, Location: location, RemotePolicy: remote, TextRank: text,
	}
}

func TestTalentMatchScoresEachPart(t *testing.T) {
	req := domain.TalentRequest{Skills: []string{"go", "postgres"}, Seniority: "senior", Location: "Berlin", RemotePolicy: "onsite"}
	cases := []struct {
		name  string
		entry domain.TalentEntry
		want  domain.TalentBreakdown
	}{
		{"exact", talentEntry("network", []string{"go", "postgres"}, "senior", "Berlin", "", 2), domain.TalentBreakdown{Skills: 1, Seniority: 1, Location: 1, Text: 1}},
		{"half the skills, a rung off, elsewhere", talentEntry("network", []string{"go", "rust", "c"}, "staff", "Munich", "onsite", 1),
			domain.TalentBreakdown{Skills: 0.25, Seniority: 0.5, Location: 0, Text: 0.5}},
		{"remote worker for an onsite role elsewhere", talentEntry("pool", []string{"go", "postgres"}, "senior", "Lisbon", "remote", 0),
			domain.TalentBreakdown{Skills: 1, Seniority: 1, Location: 0, Text: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got := domain.ScoreTalentMatch(req, tc.entry, 2)
			if got != tc.want {
				t.Fatalf("breakdown = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestTalentLocationReadsBothSidesWishes(t *testing.T) {
	cases := []struct {
		role, roleCity, want, wantCity string
		fit                            float64
	}{
		{"remote", "", "remote", "Lisbon", 1},
		{"remote", "", "", "Lisbon", 1},
		{"remote", "Berlin", "onsite", "Lisbon", 0.5},
		{"remote", "Berlin", "onsite", "Berlin", 1},
		{"hybrid", "Berlin", "remote", "Lisbon", 0.5},
		{"hybrid", "Berlin", "hybrid", "berlin", 1},
		{"onsite", "Berlin", "remote", "Lisbon", 0},
		{"", "", "remote", "", 0.5},
		{"", "", "", "", 0},
	}
	for _, tc := range cases {
		req := domain.TalentRequest{RemotePolicy: tc.role, Location: tc.roleCity}
		e := domain.TalentEntry{RemotePolicy: tc.want, Location: tc.wantCity}
		_, b := domain.ScoreTalentMatch(req, e, 0)
		if b.Location != tc.fit {
			t.Errorf("role %q in %q, wants %q in %q: location = %v, want %v", tc.role, tc.roleCity, tc.want, tc.wantCity, b.Location, tc.fit)
		}
	}
}

func TestTextPartIsRelativeToTheBatch(t *testing.T) {
	req := domain.TalentRequest{Skills: []string{"go"}}
	entries := []domain.TalentEntry{
		talentEntry("network", []string{"go"}, "", "", "", 0.4),
		talentEntry("network", []string{"go"}, "", "", "", 0.1),
	}
	ranked := domain.RankTalent(req, entries, nil)
	if len(ranked) != 2 {
		t.Fatalf("ranked %d, want 2", len(ranked))
	}
	if ranked[0].Breakdown.Text != 1 || math.Abs(ranked[1].Breakdown.Text-0.25) > 1e-9 {
		t.Fatalf("text parts = %v, %v; want 1 and 0.25", ranked[0].Breakdown.Text, ranked[1].Breakdown.Text)
	}
	// With no text hits at all the part is simply absent, not a division by zero.
	entries[0].TextRank, entries[1].TextRank = 0, 0
	for _, m := range domain.RankTalent(req, entries, nil) {
		if m.Breakdown.Text != 0 || math.IsNaN(m.Score) {
			t.Fatalf("text = %v score = %v without any hits", m.Breakdown.Text, m.Score)
		}
	}
}

func TestRankTalentHidesWeakMatchesAndExclusions(t *testing.T) {
	req := domain.TalentRequest{Skills: []string{"go", "postgres", "kubernetes", "grpc"}, Seniority: "senior"}
	strong := talentEntry("network", []string{"go", "postgres", "kubernetes", "grpc"}, "senior", "", "", 0)
	weak := talentEntry("network", []string{"php"}, "junior", "", "", 0)
	excluded := talentEntry("pool", []string{"go", "postgres", "kubernetes", "grpc"}, "senior", "", "", 0)
	ranked := domain.RankTalent(req, []domain.TalentEntry{weak, strong, excluded}, map[uuid.UUID]bool{excluded.CandidateID: true})
	if len(ranked) != 1 || ranked[0].Entry.ID != strong.ID {
		t.Fatalf("ranked = %+v, want only the strong match", ranked)
	}
}

func TestRankTalentKeepsOneEntryPerPerson(t *testing.T) {
	req := domain.TalentRequest{Skills: []string{"go", "postgres"}}
	person := uuid.New()
	network := talentEntry("network", []string{"go"}, "", "", "", 0)
	network.CandidateID = person
	pool := talentEntry("pool", []string{"go", "postgres"}, "", "", "", 0)
	pool.CandidateID = person
	ranked := domain.RankTalent(req, []domain.TalentEntry{network, pool}, nil)
	if len(ranked) != 1 || ranked[0].Entry.Source != "pool" {
		t.Fatalf("ranked = %+v, want the pool entry alone", ranked)
	}
}

func TestRankTalentCapsTheList(t *testing.T) {
	req := domain.TalentRequest{Skills: []string{"go"}}
	entries := make([]domain.TalentEntry, 0, domain.TalentLimit+5)
	for i := 0; i < domain.TalentLimit+5; i++ {
		entries = append(entries, talentEntry("network", []string{"go"}, "", "", "", 0))
	}
	if got := len(domain.RankTalent(req, entries, nil)); got != domain.TalentLimit {
		t.Fatalf("ranked %d, want %d", got, domain.TalentLimit)
	}
}

func TestSharedSkillsKeepsTheRequestsSpelling(t *testing.T) {
	got := domain.SharedSkills([]string{"Go", "Postgres", "Rust"}, []string{"go ", "POSTGRES"})
	if len(got) != 2 || got[0] != "Go" || got[1] != "Postgres" {
		t.Fatalf("shared = %v", got)
	}
}
