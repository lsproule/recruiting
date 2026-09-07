package domain

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Quality thresholds. They are the bar a problem has to clear before its
// score is a fair measure of a candidate: too few cases and one lucky guess
// passes, too few hidden cases and the candidate hard-codes the samples, too
// short a statement and every candidate answers a different question.
const (
	QualityMinCases     = 6
	QualityMinPublic    = 1
	QualityMinHidden    = 3
	QualityMinTags      = 1
	QualityMinStatement = 200
	QualityMinProven    = 2
)

// ProblemQualityFloor is the score an assessment refuses below: under it a
// problem still stores, but it cannot be attached, because the scores it
// would produce say nothing about the candidate.
const ProblemQualityFloor = 60

// QualityInput is everything the score is derived from. It is a projection of
// a problem rather than the problem itself, so a half-typed authoring form
// scores the same way a stored problem does.
type QualityInput struct {
	Statement string
	Tags      []string
	TestCases []ImportTestCase
	// ProvenLanguages are the languages whose reference solution passed every
	// case. An unverified draft has none.
	ProvenLanguages []string
}

// QualityCheck is one rule with the points it carries and how the problem
// stands against it.
type QualityCheck struct {
	Label  string
	Note   string
	Points int
	OK     bool
}

// QualityChecks scores every rule in the order the review panel lists them.
// The points sum to 100.
func QualityChecks(p QualityInput) []QualityCheck {
	var public, hidden int
	for _, tc := range p.TestCases {
		if tc.Visibility == VisibilityHidden {
			hidden++
		} else {
			public++
		}
	}
	statement := utf8.RuneCountInString(strings.TrimSpace(p.Statement))
	return []QualityCheck{
		check("Six or more test cases", 20, len(p.TestCases), QualityMinCases, "case"),
		check("A public case to work from", 15, public, QualityMinPublic, "public case"),
		check("Three or more hidden cases", 20, hidden, QualityMinHidden, "hidden case"),
		check("At least one skill tag", 10, len(p.Tags), QualityMinTags, "tag"),
		check("A statement of 200 characters", 15, statement, QualityMinStatement, "character"),
		check("Two proven languages", 20, len(p.ProvenLanguages), QualityMinProven, "proven language"),
	}
}

// ProblemQuality is the problem's score out of 100.
func ProblemQuality(p QualityInput) int {
	total := 0
	for _, c := range QualityChecks(p) {
		if c.OK {
			total += c.Points
		}
	}
	return total
}

// check builds one rule's verdict; the note is the count against the bar, so
// the panel says how far off the problem is rather than only that it is.
func check(label string, points, have, want int, noun string) QualityCheck {
	return QualityCheck{
		Label: label, Points: points, OK: have >= want,
		Note: fmt.Sprintf("%d of %d %s", have, want, plural(noun, want)),
	}
}

func plural(noun string, n int) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}
