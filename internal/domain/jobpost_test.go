package domain_test

import (
	"strings"
	"testing"

	"recruiting/internal/domain"
)

func TestWritePostingReadsLikeAJobAd(t *testing.T) {
	p := domain.WritePosting(domain.PostingInput{
		Title: "Backend Engineer (Go)", Company: "Globex Logistics", Agency: "Northwind Talent",
		Description: "## About the role\n\nOwn the routing services.\n\n- Real **production** experience\n",
		Skills:      []string{"go", "postgres"}, Seniority: "senior", Location: "Berlin", RemotePolicy: "hybrid",
		SalaryMin: 85000, SalaryMax: 105000, ApplyURL: "https://app.example/apply/northwind/backend-go",
	})
	if p.Title != "Backend Engineer (Go) · Berlin, hybrid" {
		t.Errorf("title = %q", p.Title)
	}
	for _, want := range []string{
		"Globex Logistics is hiring a senior backend engineer (Go).",
		"About the role\n\nOwn the routing services.",
		"• Real production experience",
		"What you bring\n• go\n• postgres",
		"• Berlin, hybrid", "• Senior level", "• €85,000 to €105,000 a year",
		"How to apply\nSend your CV through https://app.example/apply/northwind/backend-go",
	} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, p.Body)
		}
	}
	if strings.Contains(p.Body, "##") || strings.Contains(p.Body, "**") {
		t.Errorf("markdown leaked into the plain body:\n%s", p.Body)
	}
}

func TestWritePostingSaysOnlyWhatTheJobSays(t *testing.T) {
	p := domain.WritePosting(domain.PostingInput{Title: "Data Engineer", Agency: "Northwind Talent", Blind: true, Company: "Secret Co"})
	if strings.Contains(p.Body, "Secret Co") {
		t.Errorf("a blind posting named the client:\n%s", p.Body)
	}
	if !strings.HasPrefix(p.Body, "Northwind Talent is hiring a data engineer.") {
		t.Errorf("opening = %q", strings.SplitN(p.Body, "\n", 2)[0])
	}
	for _, absent := range []string{"a year", "What you bring", "The details", "How to apply"} {
		if strings.Contains(p.Body, absent) {
			t.Errorf("a job without %s still got a %q section:\n%s", absent, absent, p.Body)
		}
	}
	if p.Title != "Data Engineer" {
		t.Errorf("title with no location = %q", p.Title)
	}
}

func TestBoardsResolve(t *testing.T) {
	if _, ok := domain.BoardByID("linkedin"); !ok {
		t.Fatal("linkedin is not a board")
	}
	if _, ok := domain.BoardByID("craigslist"); ok {
		t.Fatal("an unknown board resolved")
	}
}
