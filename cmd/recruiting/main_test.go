package main

import (
	"strings"
	"testing"

	"recruiting/internal/config"
)

var requiredEnv = []string{
	"DATABASE_URL", "BLOB_ENDPOINT", "BLOB_BUCKET", "BLOB_KEY", "BLOB_SECRET",
	"SMTP_URL", "RUNNER_URL", "RUNNER_SECRET", "SESSION_SECRET", "BASE_URL",
}

func TestRunWithoutModeExplainsUsage(t *testing.T) {
	err := run(nil)
	if err == nil || !strings.Contains(err.Error(), "usage: recruiting <mode>") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestRunRejectsUnknownMode(t *testing.T) {
	err := run([]string{"nope"})
	if err == nil || !strings.Contains(err.Error(), `unknown mode "nope"`) {
		t.Fatalf("expected unknown-mode error, got %v", err)
	}
}

func TestModesFailOnEmptyConfig(t *testing.T) {
	for _, name := range requiredEnv {
		t.Setenv(name, "")
	}
	for name := range modes() {
		err := run([]string{name})
		if err == nil || !config.IsMissing(err) {
			t.Errorf("%s: expected a missing-configuration error, got %v", name, err)
		}
		if err != nil && !strings.Contains(err.Error(), "DATABASE_URL") {
			t.Errorf("%s: error does not name the missing variables: %v", name, err)
		}
	}
}

// Modes that need Postgres fail on the connection rather than on missing
// configuration; the rest still return cleanly.
func TestModesGetPastConfigurationWithCompleteConfig(t *testing.T) {
	for _, name := range requiredEnv {
		t.Setenv(name, "value-"+name)
	}
	stubs := map[string]bool{"runner": true}
	for name := range modes() {
		err := run([]string{name})
		if stubs[name] {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: reached the database with a nonsense DATABASE_URL", name)
		}
		if config.IsMissing(err) {
			t.Errorf("%s: still reports configuration as missing: %v", name, err)
		}
	}
}
