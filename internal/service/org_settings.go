package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Setting keys stored one row per key in org_setting.
const (
	SettingPoolScoreThreshold   = "pool_score_threshold"
	SettingIntegrityWeights     = "integrity_weights"
	SettingAssessmentInviteDays = "assessment_invite_days"
)

// IntegritySignalNames is the closed set of signals the worker computes; an
// org may reweight them but not add or remove one.
var IntegritySignalNames = []string{
	"paste_ratio",
	"paste_then_pass",
	"burst_typing",
	"edit_ratio",
	"blur_then_solution",
	"speed_vs_difficulty",
	"reference_similarity",
}

// Starting weights; calibration against recorded sessions will move them.
var defaultIntegrityWeights = map[string]float64{
	"paste_ratio":          25,
	"paste_then_pass":      25,
	"burst_typing":         10,
	"edit_ratio":           10,
	"blur_then_solution":   15,
	"speed_vs_difficulty":  5,
	"reference_similarity": 10,
}

var ErrInvalidSettings = errors.New("service: invalid org settings")

// Settings is an org's tunable configuration.
type Settings struct {
	// PoolScoreThreshold is the assessment score at or above which a passing
	// verdict adds the candidate to the talent pool.
	PoolScoreThreshold int
	// AssessmentInviteDays is how long an assessment invite stays usable.
	AssessmentInviteDays int
	// IntegrityWeights weights each signal in the risk score; one entry per
	// IntegritySignalNames.
	IntegrityWeights map[string]float64
}

func DefaultSettings() Settings {
	w := make(map[string]float64, len(defaultIntegrityWeights))
	for k, v := range defaultIntegrityWeights {
		w[k] = v
	}
	return Settings{PoolScoreThreshold: 80, AssessmentInviteDays: 7, IntegrityWeights: w}
}

// Validate reports every problem at once so a settings form can show them all.
func (s Settings) Validate() error {
	var problems []string
	if s.PoolScoreThreshold < 0 || s.PoolScoreThreshold > 100 {
		problems = append(problems, fmt.Sprintf("%s must be between 0 and 100", SettingPoolScoreThreshold))
	}
	if s.AssessmentInviteDays < 1 {
		problems = append(problems, fmt.Sprintf("%s must be at least 1", SettingAssessmentInviteDays))
	}
	known := make(map[string]bool, len(IntegritySignalNames))
	for _, name := range IntegritySignalNames {
		known[name] = true
		w, ok := s.IntegrityWeights[name]
		switch {
		case !ok:
			problems = append(problems, "missing weight for "+name)
		case w < 0:
			problems = append(problems, "weight for "+name+" must not be negative")
		}
	}
	var unknown []string
	for name := range s.IntegrityWeights {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		problems = append(problems, "unknown integrity signal "+name)
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrInvalidSettings, strings.Join(problems, "; "))
}
