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

// Every mode reaches past configuration and fails on something downstream of
// it: the modes that open Postgres fail on the connection, and runner fails
// on the OCI runtime it needs — a dev host with Docker but no gVisor names
// exactly the failure production's own startup check exists to catch.
func TestModesGetPastConfigurationWithCompleteConfig(t *testing.T) {
	for _, name := range requiredEnv {
		t.Setenv(name, "value-"+name)
	}
	t.Setenv("METRICS_ADDR", "127.0.0.1:0")
	for name := range modes() {
		err := run([]string{name})
		if err == nil {
			t.Errorf("%s: reached the database (or, for runner, the runtime) with nonsense configuration", name)
			continue
		}
		if config.IsMissing(err) {
			t.Errorf("%s: still reports configuration as missing: %v", name, err)
		}
	}
}
