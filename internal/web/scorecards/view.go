package scorecards

import (
	"strconv"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/service"
)

// overallLabels reads a verdict back to a human.
var overallLabels = map[string]string{
	service.OverallStrongYes: "Strong yes",
	service.OverallYes:       "Yes",
	service.OverallNo:        "No",
	service.OverallStrongNo:  "Strong no",
}

func overallLabel(v string) string {
	if label, ok := overallLabels[v]; ok {
		return label
	}
	return "Not filed"
}

// scoreValues are the points on the scale, in order.
var scoreValues = []int{1, 2, 3, 4, 5}

// blankRows are the empty criterion rows the rubric editor always offers, so
// adding one needs no JavaScript.
const blankRows = 3

func blanks() []int {
	out := make([]int, blankRows)
	for i := range out {
		out[i] = i
	}
	return out
}

// scoreOf is the score already recorded for a criterion, or zero.
func scoreOf(card *service.Scorecard, name string) int {
	if card == nil {
		return 0
	}
	for _, s := range card.Scores {
		if s.Name == name {
			return s.Score
		}
	}
	return 0
}

func criterionNotes(card *service.Scorecard, name string) string {
	if card == nil {
		return ""
	}
	for _, s := range card.Scores {
		if s.Name == name {
			return s.Notes
		}
	}
	return ""
}

// cardID names the card an edit rewrites; a card not yet filed has none.
func cardID(card *service.Scorecard) string {
	if card == nil || card.ID == uuid.Nil {
		return ""
	}
	return card.ID.String()
}

func cardOverall(card *service.Scorecard) string {
	if card == nil {
		return ""
	}
	return card.Overall
}

func cardNotes(card *service.Scorecard) string {
	if card == nil {
		return ""
	}
	return card.Notes
}

func itoa(n int) string { return strconv.Itoa(n) }

func filedAt(t time.Time) string { return t.UTC().Format("2 Jan 2006 15:04 UTC") }
