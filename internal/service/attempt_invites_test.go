package service

import (
	"testing"
	"time"
)

func TestInviteProgressIsSubmittedProblemsOverTheSet(t *testing.T) {
	cases := []struct {
		name      string
		submitted int
		total     int
		want      float64
		label     string
	}{
		{"nothing sent yet", 0, 3, 0, "0 of 3"},
		{"part way", 1, 4, 0.25, "1 of 4"},
		{"every problem", 3, 3, 1, "3 of 3"},
		// A set that lost a problem after the invite went out must not
		// read as more than finished, nor divide by nothing.
		{"more submitted than the set holds", 4, 3, 1, "4 of 3"},
		{"an empty set", 1, 0, 0, "1 of 0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := AttemptInvite{ProblemsSubmitted: c.submitted, ProblemsTotal: c.total}
			if got := in.Progress(); got != c.want {
				t.Errorf("Progress() = %v, want %v", got, c.want)
			}
			if got := in.ProgressLabel(); got != c.label {
				t.Errorf("ProgressLabel() = %q, want %q", got, c.label)
			}
		})
	}
}

func TestInviteExpiryIsTheWindowThatStillApplies(t *testing.T) {
	invited := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	deadline := invited.Add(2 * time.Hour)
	// Before the start it is the invite window that closes; afterwards the
	// clock on the sitting itself.
	waiting := AttemptInvite{Status: AttemptInvited, InviteExpiresAt: deadline}
	if got := waiting.ExpiresAt(); !got.Equal(deadline) {
		t.Errorf("invited expiry = %s, want %s", got, deadline)
	}
	sitting := AttemptInvite{Status: AttemptStarted, InviteExpiresAt: invited, SessionExpiresAt: deadline}
	if got := sitting.ExpiresAt(); !got.Equal(deadline) {
		t.Errorf("started expiry = %s, want %s", got, deadline)
	}
	done := AttemptInvite{Status: AttemptScored, InviteExpiresAt: deadline, SessionExpiresAt: deadline}
	if got := done.ExpiresAt(); !got.IsZero() {
		t.Errorf("closed expiry = %s, want none", got)
	}
}
