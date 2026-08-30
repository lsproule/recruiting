package config_test

import (
	"strings"
	"testing"

	"recruiting/internal/config"
)

var required = []string{
	"DATABASE_URL", "BLOB_ENDPOINT", "BLOB_BUCKET", "BLOB_KEY", "BLOB_SECRET",
	"SMTP_URL", "RUNNER_URL", "RUNNER_SECRET", "SESSION_SECRET", "BASE_URL",
}

func TestLoadReportsEveryMissingVariable(t *testing.T) {
	for _, name := range required {
		t.Setenv(name, "")
	}

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected an error for empty configuration")
	}
	if !config.IsMissing(err) {
		t.Fatalf("expected a missing-configuration error, got %v", err)
	}
	for _, name := range required {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not mention %s: %v", name, err)
		}
	}
}

func TestLoadReturnsConfigWhenComplete(t *testing.T) {
	for _, name := range required {
		t.Setenv(name, "value-"+name)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DatabaseURL != "value-DATABASE_URL" || cfg.BaseURL != "value-BASE_URL" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}
