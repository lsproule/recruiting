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

func TestLoadDerivesDatabaseURLAppFromDatabaseURL(t *testing.T) {
	for _, name := range required {
		t.Setenv(name, "value-"+name)
	}
	t.Setenv("DATABASE_URL", "postgres://recruiting:recruiting@localhost:5433/recruiting?sslmode=disable")
	t.Setenv("DATABASE_URL_APP", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := "postgres://app_rw:app_rw@localhost:5433/recruiting?sslmode=disable"
	if cfg.DatabaseURLApp != want {
		t.Fatalf("DatabaseURLApp = %q, want %q", cfg.DatabaseURLApp, want)
	}
}

func TestLoadHonorsExplicitDatabaseURLApp(t *testing.T) {
	for _, name := range required {
		t.Setenv(name, "value-"+name)
	}
	t.Setenv("DATABASE_URL", "postgres://recruiting:recruiting@localhost:5433/recruiting?sslmode=disable")
	t.Setenv("DATABASE_URL_APP", "postgres://app_rw:app_rw@otherhost:5433/recruiting?sslmode=disable")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DatabaseURLApp != "postgres://app_rw:app_rw@otherhost:5433/recruiting?sslmode=disable" {
		t.Fatalf("explicit DATABASE_URL_APP not honored: %q", cfg.DatabaseURLApp)
	}
}

func TestLoadReadsWorkerRunnerConcurrency(t *testing.T) {
	for _, name := range required {
		t.Setenv(name, "value-"+name)
	}
	t.Setenv("WORKER_RUNNER_CONCURRENCY", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WorkerRunnerConcurrency != config.DefaultWorkerRunnerConcurrency {
		t.Errorf("WorkerRunnerConcurrency = %d, want the default %d", cfg.WorkerRunnerConcurrency, config.DefaultWorkerRunnerConcurrency)
	}
	t.Setenv("WORKER_RUNNER_CONCURRENCY", "7")
	if cfg, err = config.Load(); err != nil || cfg.WorkerRunnerConcurrency != 7 {
		t.Errorf("WorkerRunnerConcurrency = %d, %v; want 7", cfg.WorkerRunnerConcurrency, err)
	}
	for _, bad := range []string{"0", "-1", "many"} {
		t.Setenv("WORKER_RUNNER_CONCURRENCY", bad)
		if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "WORKER_RUNNER_CONCURRENCY") {
			t.Errorf("WORKER_RUNNER_CONCURRENCY=%q: err = %v, want it named", bad, err)
		}
	}
}
