//go:build integration

package main

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"recruiting/internal/config"
	"recruiting/internal/store"
)

// TestServeMountsEverySurface starts the serve mode on a real listener and
// checks that the health endpoint, the local assets, and the HTML surface all
// answer — the wiring no unit test of a single package can prove.
func TestServeMountsEverySurface(t *testing.T) {
	ownerURL := os.Getenv("DATABASE_URL")
	if ownerURL == "" {
		t.Fatal("DATABASE_URL is not set; run `make dev-up` and use `make test-integration`")
	}
	lockSchema(t, ownerURL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	if err := store.MigrateUp(ctx, ownerURL); err != nil {
		t.Fatal(err)
	}
	appURL, _ := url.Parse(ownerURL)
	appURL.User = url.UserPassword("app_rw", "app_rw")

	cfg := &config.Config{
		DatabaseURL:    ownerURL,
		DatabaseURLApp: appURL.String(),
		BaseURL:        "http://127.0.0.1",
		SessionSecret:  "serve-test-secret",
	}
	addrs := make(chan net.Addr, 1)
	done := make(chan error, 1)
	serveCtx, stop := context.WithCancel(ctx)
	go func() { done <- serve(serveCtx, discardLogger(), cfg, "127.0.0.1:0", func(a net.Addr) { addrs <- a }) }()

	var addr net.Addr
	select {
	case addr = <-addrs:
	case err := <-done:
		t.Fatalf("serve returned before listening: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("serve never reported a listening address")
	}
	base := "http://" + addr.String()

	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/healthz", http.StatusOK},
		{"/static/htmx.min.js", http.StatusOK},
		{"/static/alpine.min.js", http.StatusOK},
		{"/app/login", http.StatusOK},
		{"/client/login", http.StatusOK},
		{"/api/v1/openapi.json", http.StatusOK},
		// Mounted behind the auth middleware, so an anonymous visitor is sent
		// to the login page rather than the screen.
		{"/app/jobs", http.StatusSeeOther},
		// The board route always carries a job id; an anonymous visitor is
		// redirected before the id is ever looked up.
		{"/app/pipeline/00000000-0000-0000-0000-000000000000", http.StatusSeeOther},
		{"/app/admin/users", http.StatusSeeOther},
		{"/app/candidates", http.StatusSeeOther},
		{"/app/reviews", http.StatusSeeOther},
		{"/app/problems", http.StatusSeeOther},
		{"/app/admin/api-tokens", http.StatusSeeOther},
		{"/client/jobs", http.StatusSeeOther},
		// The public apply page needs no session; an org or job that does
		// not exist is a 404 rather than a redirect.
		{"/apply/x/y", http.StatusNotFound},
		// The metrics endpoint is unauthenticated and always answers.
		{"/metrics", http.StatusOK},
	} {
		res, err := client.Get(base + tc.path)
		if err != nil {
			t.Errorf("GET %s: %v", tc.path, err)
			continue
		}
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Errorf("GET %s = %d, want %d", tc.path, res.StatusCode, tc.want)
		}
	}

	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not stop when its context was cancelled")
	}
}
