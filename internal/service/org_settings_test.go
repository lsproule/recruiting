package service_test

import (
	"errors"
	"strings"
	"testing"

	"recruiting/internal/service"
)

func TestDefaultSettings(t *testing.T) {
	s := service.DefaultSettings()
	if s.PoolScoreThreshold != 80 {
		t.Errorf("pool_score_threshold = %d, want 80", s.PoolScoreThreshold)
	}
	if s.AssessmentInviteDays != 7 {
		t.Errorf("assessment_invite_days = %d, want 7", s.AssessmentInviteDays)
	}
	for _, name := range service.IntegritySignalNames {
		if _, ok := s.IntegrityWeights[name]; !ok {
			t.Errorf("no default weight for %q", name)
		}
	}
	if len(s.IntegrityWeights) != len(service.IntegritySignalNames) {
		t.Errorf("weights = %v, want one per signal", s.IntegrityWeights)
	}
	if err := s.Validate(); err != nil {
		t.Errorf("defaults must validate: %v", err)
	}
}

func TestSettingsValidateRejectsNegativeWeight(t *testing.T) {
	s := service.DefaultSettings()
	s.IntegrityWeights["paste_ratio"] = -0.1
	err := s.Validate()
	if !errors.Is(err, service.ErrInvalidSettings) {
		t.Fatalf("negative weight: got %v, want ErrInvalidSettings", err)
	}
	if got := err.Error(); !strings.Contains(got, "paste_ratio") {
		t.Errorf("error does not name the offending weight: %s", got)
	}
}

func TestSettingsValidateRejectsUnknownAndOutOfRange(t *testing.T) {
	cases := map[string]func(*service.Settings){
		"unknown weight":       func(s *service.Settings) { s.IntegrityWeights["made_up"] = 1 },
		"missing weight":       func(s *service.Settings) { delete(s.IntegrityWeights, "edit_ratio") },
		"threshold below zero": func(s *service.Settings) { s.PoolScoreThreshold = -1 },
		"threshold above 100":  func(s *service.Settings) { s.PoolScoreThreshold = 101 },
		"invite days zero":     func(s *service.Settings) { s.AssessmentInviteDays = 0 },
		"invite days negative": func(s *service.Settings) { s.AssessmentInviteDays = -3 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := service.DefaultSettings()
			mutate(&s)
			if err := s.Validate(); !errors.Is(err, service.ErrInvalidSettings) {
				t.Fatalf("got %v, want ErrInvalidSettings", err)
			}
		})
	}
}
