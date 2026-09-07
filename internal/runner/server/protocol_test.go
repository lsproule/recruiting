package server

import (
	"testing"

	"recruiting/internal/domain"
)

// The wire protocol and the problem bank drifted apart when each kept its own
// list, so the accepted set is asserted against the one registry.
func TestLanguagesMatchTheRegistry(t *testing.T) {
	if len(Languages) != len(domain.Languages) {
		t.Fatalf("Languages = %v, want one entry per registered language", Languages)
	}
	for _, l := range domain.Languages {
		if !Languages[l.ID] {
			t.Errorf("the runner does not accept %q", l.ID)
		}
	}
	if Languages["node"] {
		t.Error("the runner still accepts the retired node id")
	}
}
