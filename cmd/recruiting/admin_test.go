package main

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"

	"recruiting/internal/config"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPasswordSetLink(t *testing.T) {
	got := passwordSetLink("https://hire.example.com/", "tok123")
	if want := "https://hire.example.com/app/reset/tok123"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAdminWithoutCommandListsCommands(t *testing.T) {
	err := runAdminCmd(io.Discard, discardLogger(), &config.Config{}, nil)
	if err == nil || !strings.Contains(err.Error(), "create-org") {
		t.Fatalf("expected a usage error naming create-org, got %v", err)
	}
}

func TestAdminRejectsUnknownCommand(t *testing.T) {
	err := runAdminCmd(io.Discard, discardLogger(), &config.Config{}, []string{"frobnicate"})
	if err == nil || !strings.Contains(err.Error(), "frobnicate") {
		t.Fatalf("expected an unknown-command error, got %v", err)
	}
}

func TestCreateOrgRequiresNameAndAdminEmail(t *testing.T) {
	for _, args := range [][]string{
		{"create-org"},
		{"create-org", "--name", "Acme"},
		{"create-org", "--admin-email", "a@example.com"},
	} {
		var out bytes.Buffer
		err := runAdminCmd(&out, discardLogger(), &config.Config{}, args)
		if err == nil || !strings.Contains(err.Error(), "required") {
			t.Errorf("%v: expected a required-flag error, got %v", args, err)
		}
	}
}

func TestOrgSlug(t *testing.T) {
	cases := map[string]string{
		"Acme Corp":       "acme-corp",
		"  Ünïcode  Ltd ": "unicode-ltd",
		"A/B  Testing!":   "a-b-testing",
	}
	for in, want := range cases {
		if got := orgSlug(in); got != want {
			t.Errorf("orgSlug(%q) = %q, want %q", in, got, want)
		}
	}
}
