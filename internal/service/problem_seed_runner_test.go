//go:build integration

package service

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"recruiting/internal/runner/server"
)

// TestSeedReferencesSolveTheirCases runs every reference solution the seed
// bank ships straight through the sandbox executor, with no database and no
// HTTP in between: the quickest proof that each language's driver, each
// solution, and each case agree. SEED_TITLES narrows it to a comma-separated
// list of titles while a problem is being written.
func TestSeedReferencesSolveTheirCases(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not on PATH")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon unreachable")
	}
	runtime := os.Getenv("RUNNER_RUNTIME")
	if runtime == "" {
		runtime = "runsc"
		if exec.Command("docker", "info", "--format", "{{.Runtimes.runsc}}").Run() != nil || os.Getenv("RUNNER_ALLOW_INSECURE_RUNTIME") == "1" {
			runtime = "runc"
		}
	}
	problems, err := SeedProblems()
	if err != nil {
		t.Fatalf("the seed bank does not parse: %v", err)
	}
	if only := os.Getenv("SEED_TITLES"); only != "" {
		want := map[string]bool{}
		for _, title := range strings.Split(only, ",") {
			want[strings.TrimSpace(title)] = true
		}
		kept := problems[:0]
		for _, p := range problems {
			if want[p.Title] {
				kept = append(kept, p)
			}
		}
		problems = kept
	}
	// SQL problems need Postgres; TestPlatformSeedRunsOnTheRunner covers them.
	code := problems[:0]
	for _, p := range problems {
		if p.Kind != "sql" {
			code = append(code, p)
		}
	}
	if len(code) == 0 {
		t.Skip("no code problems selected")
	}
	docker := &server.DockerExecutor{Runtime: runtime, ImagePrefix: "recruiting-runner-"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := validateProblemReferences(ctx, directExecutor{docker}, 4, code); err != nil {
		t.Fatalf("the seed bank does not pass its own reference solutions:\n%v", err)
	}
	t.Logf("%d problems proven in every language they ship", len(code))
}

// directExecutor adapts the sandbox executor to the service's Executor, which
// is otherwise the HTTP client: the runner's own failure is an error, a verdict
// on the code is a response.
type directExecutor struct{ d *server.DockerExecutor }

func (e directExecutor) Execute(ctx context.Context, req server.Request) (server.Response, error) {
	res := e.d.Execute(ctx, &req)
	if res == nil {
		return server.Response{}, context.Canceled
	}
	return *res, nil
}
