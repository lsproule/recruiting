// Command harness runs inside a sandbox image. It reads a spec (language,
// source, tests, limits) as JSON on stdin, compiles when the language needs
// it, runs every test with its own wall-clock limit and output cap, and prints
// the result document as JSON on stdout. Isolation is the container's job;
// the harness only measures and reports.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type spec struct {
	Language string `json:"language"`
	Source   string `json:"source"`
	Tests    []test `json:"tests"`
	Limits   limits `json:"limits"`
}

type test struct {
	ID       string `json:"id"`
	Input    string `json:"input"`
	Expected string `json:"expected"`
}

type limits struct {
	CPUMs    int `json:"cpu_ms"`
	WallMs   int `json:"wall_ms"`
	MemMB    int `json:"mem_mb"`
	OutputKB int `json:"output_kb"`
	PIDs     int `json:"pids"`
}

type result struct {
	TestID     string `json:"test_id"`
	Status     string `json:"status"`
	StdoutHash string `json:"stdout_hash"`
	StderrTail string `json:"stderr_tail"`
	TimeMs     int64  `json:"time_ms"`
	MemKB      int64  `json:"mem_kb"`
}

type response struct {
	Status        string   `json:"status"`
	CompileOutput string   `json:"compile_output"`
	Results       []result `json:"results"`
}

func main() {
	var s spec
	if err := json.NewDecoder(os.Stdin).Decode(&s); err != nil {
		emit(response{Status: "error", CompileOutput: "bad spec: " + err.Error(), Results: []result{}})
		return
	}
	emit(run(s))
}

func emit(r response) {
	if r.Results == nil {
		r.Results = []result{}
	}
	_ = json.NewEncoder(os.Stdout).Encode(r)
}

func run(s spec) response {
	work, err := os.MkdirTemp("/tmp", "work-")
	if err != nil {
		return response{Status: "error", CompileOutput: err.Error()}
	}
	cmdline, compileOut, err := prepare(s.Language, s.Source, work)
	if err != nil {
		st := "compile_error"
		if compileOut == "" {
			st = "error"
			compileOut = err.Error()
		}
		return response{Status: st, CompileOutput: compileOut}
	}
	out := response{Status: "ok", CompileOutput: compileOut}
	for _, t := range s.Tests {
		out.Results = append(out.Results, runTest(cmdline, work, t, s.Limits))
	}
	return out
}

// prepare writes the source and compiles it, returning the command to run a test.
func prepare(lang, source, work string) (cmdline []string, compileOut string, err error) {
	// GOCACHE comes from the image: a pre-warmed, read-only standard library cache.
	env := append(os.Environ(), "HOME=/tmp", "GOPATH=/tmp/gopath", "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local", "GOPROXY=off")
	compile := func(name string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, name, args...)
		c.Dir, c.Env = work, env
		b, err := c.CombinedOutput()
		return string(b), err
	}
	switch lang {
	case "python":
		p := filepath.Join(work, "main.py")
		if err := os.WriteFile(p, []byte(source), 0o644); err != nil {
			return nil, "", err
		}
		// -I (isolated) ignores env vars, user site-packages, and script dir.
		if out, err := compile("python3", "-I", "-m", "py_compile", p); err != nil {
			return nil, out, err
		}
		return []string{"python3", "-I", "-u", p}, "", nil
	case "node":
		p := filepath.Join(work, "main.js")
		if err := os.WriteFile(p, []byte(source), 0o644); err != nil {
			return nil, "", err
		}
		if out, err := compile("node", "--check", p); err != nil {
			return nil, out, err
		}
		return []string{"node", "--stack-size=2048", p}, "", nil
	case "go":
		if err := os.WriteFile(filepath.Join(work, "main.go"), []byte(source), 0o644); err != nil {
			return nil, "", err
		}
		if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module candidate\n\ngo 1.22\n"), 0o644); err != nil {
			return nil, "", err
		}
		bin := filepath.Join(work, "candidate")
		if out, err := compile("go", "build", "-o", bin, "."); err != nil {
			return nil, out, err
		}
		return []string{bin}, "", nil
	case "java":
		if err := os.WriteFile(filepath.Join(work, "Main.java"), []byte(source), 0o644); err != nil {
			return nil, "", err
		}
		if out, err := compile("javac", "-d", work, "Main.java"); err != nil {
			return nil, out, err
		}
		return []string{"java", "-Xss8m", "-XX:+UseSerialGC", "-XX:TieredStopAtLevel=1", "-cp", work, "Main"}, "", nil
	}
	return nil, "", fmt.Errorf("unsupported language %q", lang)
}

func runTest(cmdline []string, work string, t test, l limits) result {
	res := result{TestID: t.ID, Status: "error"}
	wall := time.Duration(l.WallMs) * time.Millisecond
	if wall <= 0 {
		wall = 5 * time.Second
	}
	capBytes := l.OutputKB * 1024
	if capBytes <= 0 {
		capBytes = 64 * 1024
	}
	if l.MemMB > 0 {
		for i, a := range cmdline {
			if a == "java" {
				// leave headroom for the JVM itself under the cgroup limit
				cmdline = append(cmdline[:i+1:i+1], append([]string{fmt.Sprintf("-Xmx%dm", max(l.MemMB*3/4, 16))}, cmdline[i+1:]...)...)
				break
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), wall)
	defer cancel()
	cmd := exec.CommandContext(ctx, cmdline[0], cmdline[1:]...)
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "HOME=/tmp")
	cmd.Stdin = strings.NewReader(t.Input)
	// Own process group: a timeout kills every descendant, not only the leader.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	stdout := &cappedWriter{limit: capBytes}
	stderr := &cappedWriter{limit: 4096, keepTail: true}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	start := time.Now()
	err := cmd.Run()
	res.TimeMs = time.Since(start).Milliseconds()
	if ps := cmd.ProcessState; ps != nil {
		if ru, ok := ps.SysUsage().(*syscall.Rusage); ok {
			res.MemKB = ru.Maxrss
		}
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)

	res.StderrTail = stderr.String()
	trimmed := strings.TrimSpace(stdout.String())
	h := sha256.Sum256([]byte(trimmed))
	res.StdoutHash = hex.EncodeToString(h[:])

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.Status = "timeout"
	case err != nil:
		res.Status = "error"
		if res.StderrTail == "" {
			res.StderrTail = err.Error()
		}
	case trimmed == strings.TrimSpace(t.Expected):
		res.Status = "pass"
	default:
		res.Status = "fail"
	}
	return res
}

// cappedWriter keeps the first limit bytes (or last, with keepTail) and
// silently discards the rest so runaway output cannot exhaust memory.
type cappedWriter struct {
	limit    int
	keepTail bool
	buf      bytes.Buffer
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.keepTail {
		w.buf.Write(p)
		if w.buf.Len() > w.limit {
			b := w.buf.Bytes()
			w.buf = *bytes.NewBuffer(append([]byte(nil), b[len(b)-w.limit:]...))
		}
		return n, nil
	}
	if room := w.limit - w.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		w.buf.Write(p)
	}
	return n, nil
}

func (w *cappedWriter) String() string { return w.buf.String() }

var _ io.Writer = (*cappedWriter)(nil)
