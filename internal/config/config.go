// Package config loads process configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Config holds every setting the binary needs, regardless of mode.
type Config struct {
	DatabaseURL   string
	BlobEndpoint  string
	BlobBucket    string
	BlobKey       string
	BlobSecret    string
	SMTPURL       string
	RunnerURL     string
	RunnerSecret  string
	SessionSecret string
	BaseURL       string
}

// MissingError reports environment variables that are required but unset.
type MissingError struct {
	Names []string
}

func (e *MissingError) Error() string {
	return fmt.Sprintf("missing required configuration: %s (see .env.example)", strings.Join(e.Names, ", "))
}

// Load reads configuration from the environment. Every field is required; the
// returned error names all missing variables at once so a fresh checkout does
// not have to be fixed one variable per run.
func Load() (*Config, error) {
	var missing []string
	get := func(name string) string {
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}

	cfg := &Config{
		DatabaseURL:   get("DATABASE_URL"),
		BlobEndpoint:  get("BLOB_ENDPOINT"),
		BlobBucket:    get("BLOB_BUCKET"),
		BlobKey:       get("BLOB_KEY"),
		BlobSecret:    get("BLOB_SECRET"),
		SMTPURL:       get("SMTP_URL"),
		RunnerURL:     get("RUNNER_URL"),
		RunnerSecret:  get("RUNNER_SECRET"),
		SessionSecret: get("SESSION_SECRET"),
		BaseURL:       get("BASE_URL"),
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, &MissingError{Names: missing}
	}
	return cfg, nil
}

// IsMissing reports whether err was caused by unset configuration.
func IsMissing(err error) bool {
	var m *MissingError
	return errors.As(err, &m)
}
