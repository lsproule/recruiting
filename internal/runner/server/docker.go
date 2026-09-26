package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"recruiting/runner/wire"
	"strings"
	"time"
)

// DockerExecutor runs the language harness in a fresh, hardened container per
// request via the docker CLI (kept dependency-free on purpose).
type DockerExecutor struct {
	Runtime     string
	ImagePrefix string
	Logger      *slog.Logger
	// Docker overrides the CLI binary (tests).
	Docker string
}

const (
	sandboxUID = "65534:65534"
	// compileBudget bounds toolchain start-up and compilation, on top of the
	// per-test wall limits the harness enforces itself.
	compileBudget = 90 * time.Second
	tmpfsSizeMB   = 256
)

func availableRuntimes(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{range $k,$v := .Runtimes}}{{$k}} {{end}}").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("docker info: %s", bytes.TrimSpace(ee.Stderr))
		}
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

// harnessSpec is what the in-image harness reads from stdin.
type harnessSpec struct {
	Language  string          `json:"language"`
	Source    string          `json:"source"`
	Signature *wire.Signature `json:"signature,omitempty"`
	Tests     []Test          `json:"tests"`
	Limits    Limits          `json:"limits"`
}

func (d *DockerExecutor) runArgs(name, language string, l Limits) []string {
	cpus := float64(l.CPUMs) / float64(l.WallMs)
	if cpus < 0.5 {
		cpus = 0.5
	}
	if cpus > 4 {
		cpus = 4
	}
	// noexec is deliberately absent: go/javac emit binaries into /tmp.
	return []string{
		"run", "--rm", "-i", "--init",
		"--name=" + name,
		"--runtime=" + d.Runtime,
		"--network=none",
		"--read-only",
		fmt.Sprintf("--tmpfs=/tmp:rw,exec,nosuid,nodev,size=%dm,mode=1777", tmpfsSizeMB),
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		fmt.Sprintf("--pids-limit=%d", l.PIDs),
		fmt.Sprintf("--memory=%dm", l.MemMB),
		fmt.Sprintf("--memory-swap=%dm", l.MemMB),
		fmt.Sprintf("--cpus=%.2f", cpus),
		"--user=" + sandboxUID,
		"--env=HOME=/tmp",
		"--label=recruiting.runner=1",
		d.ImagePrefix + language,
	}
}

func (d *DockerExecutor) Execute(ctx context.Context, req *Request) *Response {
	docker := d.Docker
	if docker == "" {
		docker = "docker"
	}
	limits := req.Limits.normalized()
	spec, err := json.Marshal(harnessSpec{Language: req.Language, Source: req.Source, Signature: req.Signature, Tests: req.Tests, Limits: limits})
	if err != nil {
		return &Response{ID: req.ID, Status: StatusError, CompileOutput: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, executionBudget(len(req.Tests), limits))
	defer cancel()

	name := containerName(req.ID)
	cmd := exec.CommandContext(ctx, docker, d.runArgs(name, req.Language, limits)...)
	cmd.Stdin = bytes.NewReader(spec)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Cancel kills only the CLI; the container must be removed explicitly.
	cmd.Cancel = func() error {
		_ = exec.Command(docker, "rm", "-f", name).Run()
		return cmd.Process.Kill()
	}
	runErr := cmd.Run()
	_ = exec.Command(docker, "rm", "-f", name).Run()

	if ctx.Err() != nil {
		return &Response{ID: req.ID, Status: StatusTimeout, CompileOutput: "execution exceeded its wall-clock budget", Results: timedOut(req.Tests)}
	}
	var out Response
	if jsonErr := json.Unmarshal(stdout.Bytes(), &out); jsonErr == nil && out.Status != "" {
		out.ID = req.ID
		return &out
	}
	tail := strings.TrimSpace(stderr.String())
	if len(tail) > 4096 {
		tail = tail[len(tail)-4096:]
	}
	if d.Logger != nil {
		d.Logger.Error("harness produced no result", "id", req.ID, "err", runErr, "stderr", tail)
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) && ee.ExitCode() == 137 {
		return &Response{ID: req.ID, Status: StatusRuntimeError, CompileOutput: "sandbox killed: out of memory", Results: errored(req.Tests, "killed: out of memory")}
	}
	msg := "harness produced no result"
	if runErr != nil {
		msg += ": " + runErr.Error()
	}
	if tail != "" {
		msg += "\n" + tail
	}
	return &Response{ID: req.ID, Status: StatusError, CompileOutput: msg, Results: errored(req.Tests, "runner error")}
}

// executionBudget is the host-side wall clock for one run: compile slack plus
// per-test wall limits with overhead, never beyond MaxBudget.
func executionBudget(tests int, l Limits) time.Duration {
	b := compileBudget + time.Duration(tests)*(time.Duration(l.WallMs)*time.Millisecond+2*time.Second)
	if b > MaxBudget {
		return MaxBudget
	}
	return b
}

// containerName hashes the raw id so distinct ids never share a container
// name whatever characters they contain.
func containerName(id string) string {
	h := sha256.Sum256([]byte(id))
	return "runner-" + hex.EncodeToString(h[:16])
}

func timedOut(tests []Test) []TestResult {
	rs := make([]TestResult, len(tests))
	for i, t := range tests {
		rs[i] = TestResult{TestID: t.ID, Status: TestTimeout}
	}
	return rs
}

func errored(tests []Test, msg string) []TestResult {
	rs := make([]TestResult, len(tests))
	for i, t := range tests {
		rs[i] = TestResult{TestID: t.ID, Status: TestError, StderrTail: msg}
	}
	return rs
}
