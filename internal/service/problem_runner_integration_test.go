//go:build integration

package service_test

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	runnerclient "recruiting/internal/runner/client"
	"recruiting/internal/runner/server"
	"recruiting/internal/service"
)

// seedRunTimeout bounds the whole seed import: every reference solution of
// every seed problem is compiled and run, four at a time.
const seedRunTimeout = 15 * time.Minute

// TestPlatformSeedRunsOnTheRunner is the seed bank's real check: every
// reference solution the bank ships, in each of its languages, and every SQL
// problem's query, is executed by the sandboxed runner and must solve every
// test case.
func TestPlatformSeedRunsOnTheRunner(t *testing.T) {
	f := newProblemFixture(t)
	url := startRunner(t)
	svc := service.NewProblemService(f.st, runnerclient.New(url, runnerTestSecret))
	// Match the slots the in-process runner was started with, so the seed is
	// proven at the concurrency a deployment would use.
	svc.Concurrency = runnerTestConcurrency

	ctx, cancel := context.WithTimeout(context.Background(), seedRunTimeout)
	defer cancel()
	seeded, err := f.seedPlatform(t, ctx, svc)
	if err != nil {
		t.Fatalf("the seed bank does not pass its own reference solutions: %v", err)
	}
	bank, err := service.SeedProblems()
	if err != nil {
		t.Fatalf("the seed bank does not parse: %v", err)
	}
	if len(seeded) != len(bank) {
		t.Fatalf("seeded %d problems, want the whole bank of %d", len(seeded), len(bank))
	}
	if len(seeded) < 12 {
		t.Fatalf("seeded %d problems, want at least 12", len(seeded))
	}
	var sql int
	for _, p := range seeded {
		if p.Kind == "sql" {
			sql++
		}
	}
	if sql < 3 {
		t.Errorf("%d sql problems executed, want at least 3", sql)
	}
}

const (
	runnerTestSecret = "seed-import-secret"
	// runnerTestConcurrency is what the in-process runner is given and what
	// the import is run at.
	runnerTestConcurrency = 4
)

// startRunner serves the runner in-process and returns its base URL. The
// sandbox images are built on demand, and SQL problems get the Compose
// Postgres as their provisioning admin.
func startRunner(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not on PATH; the seed bank cannot be executed here")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("the docker daemon is unreachable; the seed bank cannot be executed here")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range seedReferenceLanguages(t) {
		if err := exec.Command("docker", "image", "inspect", "recruiting-runner-"+lang).Run(); err == nil {
			continue
		}
		t.Logf("building the missing recruiting-runner-%s image", lang)
		cmd := exec.Command(filepath.Join(root, "runner", "images", "build.sh"), lang)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("cannot build the %s sandbox image: %v\n%s", lang, err, out)
		}
	}

	addr := freeAddr(t)
	t.Setenv("RUNNER_LISTEN", addr)
	t.Setenv("RUNNER_ALLOW_INSECURE_RUNTIME", "1")
	t.Setenv("RUNNER_MAX_CONCURRENT", strconv.Itoa(runnerTestConcurrency))
	t.Setenv("RUNNER_SQL_URL", os.Getenv("DATABASE_URL"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		done <- server.Run(ctx, slog.New(slog.NewTextHandler(os.Stderr, nil)), server.ConfigFromEnv(runnerTestSecret))
	}()

	base := "http://" + addr
	deadline := time.Now().Add(30 * time.Second)
	for {
		res, err := http.Get(base + "/healthz")
		if err == nil {
			res.Body.Close()
			return base
		}
		select {
		case err := <-done:
			t.Skipf("the runner refused to start: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the runner did not become healthy")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// seedReferenceLanguages is every language the bank ships a reference solution
// in, so the images the seed actually needs are the images that get built. SQL
// is left out: it runs against Postgres rather than a sandbox image.
func seedReferenceLanguages(t *testing.T) []string {
	t.Helper()
	problems, err := service.SeedProblems()
	if err != nil {
		t.Fatalf("the seed bank does not parse: %v", err)
	}
	seen := map[string]bool{}
	var langs []string
	for _, p := range problems {
		for _, ref := range p.References {
			if ref.Language == "sql" || seen[ref.Language] {
				continue
			}
			seen[ref.Language] = true
			langs = append(langs, ref.Language)
		}
	}
	sort.Strings(langs)
	return langs
}

// freeAddr reserves a loopback port and hands it back, which is how the
// in-process runner is reached without a ready callback.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}
