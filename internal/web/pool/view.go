package pool

import (
	"strconv"
	"strings"

	"recruiting/internal/service"
)

func skillsText(e service.PoolEntry) string { return strings.Join(e.Skills, ", ") }

// panelNote is the line the suggestions panel shows above its list after an
// add: what happened, and whether it went wrong.
type panelNote struct {
	Message string
	Failed  bool
}

func (n panelNote) class() string {
	if n.Failed {
		return "flash flash-error"
	}
	return "flash flash-success"
}

// sourceLabel says why the candidate is in the pool.
func sourceLabel(source string) string {
	switch source {
	case service.PoolSourceScorecard:
		return "strong yes at interview"
	case service.PoolSourceReview:
		return "passed an assessment"
	case service.PoolSourceFlag:
		return "flagged by a recruiter"
	}
	return source
}

// locationText is where the candidate works, remote or otherwise.
func locationText(e service.PoolEntry) string {
	parts := make([]string, 0, 3)
	if e.Seniority != "" {
		parts = append(parts, e.Seniority)
	}
	if e.Location != "" {
		parts = append(parts, e.Location)
	}
	if e.RemoteOK {
		parts = append(parts, "remote ok")
	}
	return strings.Join(parts, " · ")
}

// percent renders one part of a match breakdown, which runs from 0 to 1.
func percent(v float64) string {
	return strconv.FormatFloat(v*100, 'f', 0, 64) + "%"
}

// scoreText is the overall match score, which runs from 0 to 1.
func scoreText(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
