package domain

import (
	"fmt"
	"strings"
)

// A job board the platform can post to. Each has a browser adapter in
// tools/jobpost; the demo board is a local page the e2e suite runs, so the
// whole path is exercised without an account anywhere.
type Board struct {
	ID    string
	Label string
	// Plain marks a board whose posting form takes plain text rather than
	// Markdown; the copy is rendered accordingly.
	Plain bool
}

// Boards is every board, in the order the posting panel offers them.
var Boards = []Board{
	{ID: "linkedin", Label: "LinkedIn", Plain: true},
	{ID: "indeed", Label: "Indeed", Plain: true},
	{ID: "glassdoor", Label: "Glassdoor", Plain: true},
	{ID: "demo", Label: "Demo board (local)"},
}

// BoardByID resolves a board.
func BoardByID(id string) (Board, bool) {
	for _, b := range Boards {
		if b.ID == id {
			return b, true
		}
	}
	return Board{}, false
}

// PostingInput is what a posting is written from: the job as the recruiter
// filled it in, who is hiring, and where an applicant lands.
type PostingInput struct {
	Title, Company, Agency, Description string
	Skills                              []string
	Seniority, Location, RemotePolicy   string
	SalaryMin, SalaryMax                int
	ApplyURL                            string
	// Blind hides the client's name: the posting speaks for the agency.
	Blind bool
}

// Posting is the copy that goes on a board: a title a search engine and a
// person both read well, and a body that says what the role is, what it
// takes, and what is on offer, ending with how to apply. Every section
// comes from the job's own fields, so nothing is invented: a job with no
// salary gets no salary line.
type Posting struct {
	Title string
	Body  string
}

// WritePosting composes the copy. It is deterministic so a preview and the
// posting it becomes are the same text.
func WritePosting(in PostingInput) Posting {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "Software Engineer"
	}
	where := postingWhere(in)
	full := title
	if where != "" {
		full += " · " + where
	}
	who := strings.TrimSpace(in.Company)
	if in.Blind || who == "" {
		who = strings.TrimSpace(in.Agency)
	}
	if who == "" {
		who = "our client"
	}

	var b strings.Builder
	// The opening line is what shows in a feed before the fold.
	fmt.Fprintf(&b, "%s is hiring a %s%s.\n\n", who, postingLevel(in.Seniority), firstLower(title))
	if desc := strings.TrimSpace(in.Description); desc != "" {
		b.WriteString(plainText(desc))
		b.WriteString("\n\n")
	}
	if len(in.Skills) > 0 {
		b.WriteString("What you bring\n")
		for _, s := range in.Skills {
			if s = strings.TrimSpace(s); s != "" {
				fmt.Fprintf(&b, "• %s\n", s)
			}
		}
		b.WriteString("\n")
	}
	var details []string
	if where != "" {
		details = append(details, where)
	}
	if lvl := strings.TrimSpace(in.Seniority); lvl != "" {
		details = append(details, strings.Title(lvl)+" level")
	}
	if pay := postingPay(in.SalaryMin, in.SalaryMax); pay != "" {
		details = append(details, pay)
	}
	if len(details) > 0 {
		b.WriteString("The details\n")
		for _, d := range details {
			fmt.Fprintf(&b, "• %s\n", d)
		}
		b.WriteString("\n")
	}
	if in.Blind && strings.TrimSpace(in.Company) != "" {
		fmt.Fprintf(&b, "%s is recruiting on behalf of a client we will introduce you to once we have spoken.\n\n", strings.TrimSpace(in.Agency))
	}
	if in.ApplyURL != "" {
		fmt.Fprintf(&b, "How to apply\nSend your CV through %s — it takes two minutes and every application gets a reply.\n", in.ApplyURL)
	}
	return Posting{Title: full, Body: strings.TrimSpace(b.String()) + "\n"}
}

func postingWhere(in PostingInput) string {
	loc := strings.TrimSpace(in.Location)
	switch strings.ToLower(strings.TrimSpace(in.RemotePolicy)) {
	case "remote":
		if loc == "" {
			return "Remote"
		}
		return "Remote (" + loc + ")"
	case "hybrid":
		if loc == "" {
			return "Hybrid"
		}
		return loc + ", hybrid"
	case "onsite":
		if loc == "" {
			return "On site"
		}
		return loc + ", on site"
	}
	return loc
}

func postingLevel(seniority string) string {
	switch strings.ToLower(strings.TrimSpace(seniority)) {
	case "junior":
		return "junior "
	case "senior":
		return "senior "
	case "staff":
		return "staff-level "
	}
	return ""
}

func postingPay(lo, hi int) string {
	switch {
	case lo > 0 && hi > 0 && hi > lo:
		return fmt.Sprintf("%s to %s a year", money(lo), money(hi))
	case lo > 0:
		return "From " + money(lo) + " a year"
	case hi > 0:
		return "Up to " + money(hi) + " a year"
	}
	return ""
}

func money(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return "€" + s
}

// firstLower lower-cases a title's first word unless it is an acronym or a
// proper name (a capital inside the word), so "Backend Engineer (Go)" reads
// "a senior backend engineer (Go)" and "iOS Engineer" is left alone.
func firstLower(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if strings.ContainsAny(w, "()/") || (len(w) > 1 && strings.ToUpper(w[1:]) == w[1:] && strings.ToLower(w[1:]) != w[1:]) {
			continue
		}
		words[i] = strings.ToLower(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// plainText strips the Markdown a description is written in down to what a
// board's plain-text box shows well: headings become their text, list
// markers become bullets, emphasis is dropped.
func plainText(md string) string {
	var out []string
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "#"):
			t = strings.TrimSpace(strings.TrimLeft(t, "#"))
		case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* "):
			t = "• " + t[2:]
		}
		t = strings.NewReplacer("**", "", "__", "", "`", "").Replace(t)
		out = append(out, t)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
