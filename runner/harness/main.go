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
	// StdoutTail is the head of what the program printed, kept short. It is
	// what a run is debugged from; the hash alone says only that two runs
	// differ, never how.
	StdoutTail string `json:"stdout_tail,omitempty"`
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

// langSpec is how one language is built and run. {work} is the work dir,
// {src} the written source file, {bin} the binary a compile produces.
type langSpec struct {
	File    string
	Compile []string
	Run     []string
}

// langs is the harness half of the platform's language registry
// (internal/domain/languages.go); every code language has one entry.
var langs = map[string]langSpec{
	// -I (isolated) ignores env vars, user site-packages, and script dir.
	"python": {File: "main.py",
		Compile: []string{"python3", "-I", "-m", "py_compile", "{src}"},
		Run:     []string{"python3", "-I", "-u", "{src}"}},
	"javascript": {File: "main.js",
		Compile: []string{"node", "--check", "{src}"},
		Run:     []string{"node", "--stack-size=2048", "{src}"}},
	// No compile step: node's --check parses .ts as JavaScript, so a type
	// annotation is reported as a syntax error before stripping ever runs.
	"typescript": {File: "main.ts",
		Run: []string{"node", "--experimental-strip-types", "--stack-size=2048", "{src}"}},
	"go": {File: "main.go",
		Compile: []string{"go", "build", "-o", "{bin}", "."},
		Run:     []string{"{bin}"}},
	"java": {File: "Main.java",
		Compile: []string{"javac", "-d", "{work}", "Main.java"},
		Run:     []string{"java", "-Xss8m", "-XX:+UseSerialGC", "-XX:TieredStopAtLevel=1", "-cp", "{work}", "Main"}},
	"c": {File: "main.c",
		Compile: []string{"gcc", "-O2", "-std=c17", "-o", "{bin}", "{src}", "-lm"},
		Run:     []string{"{bin}"}},
	"cpp": {File: "main.cpp",
		Compile: []string{"g++", "-O2", "-std=c++20", "-o", "{bin}", "{src}"},
		Run:     []string{"{bin}"}},
	"rust": {File: "main.rs",
		Compile: []string{"rustc", "-O", "-o", "{bin}", "{src}"},
		Run:     []string{"{bin}"}},
	"php": {File: "main.php",
		Compile: []string{"php", "-l", "{src}"},
		Run:     []string{"php", "-d", "error_reporting=E_ALL", "{src}"}},
	"ruby": {File: "main.rb",
		Compile: []string{"ruby", "-c", "{src}"},
		Run:     []string{"ruby", "{src}"}},
	"haskell": {File: "main.hs",
		Compile: []string{"ghc", "-O0", "-o", "{bin}", "{src}"},
		Run:     []string{"{bin}"}},
	"lua": {File: "main.lua",
		Run: []string{"lua", "{src}"}},
	// -jar keeps "java" first in the argv, so the memory flag still applies.
	"kotlin": {File: "main.kt",
		Compile: []string{"kotlinc", "{src}", "-include-runtime", "-d", "{work}/candidate.jar"},
		Run:     []string{"java", "-Xss8m", "-XX:+UseSerialGC", "-XX:TieredStopAtLevel=1", "-jar", "{work}/candidate.jar"}},
	"csharp": {File: "Program.cs",
		Compile: []string{"dotnet", "build", "--nologo", "-c", "Release", "-o", "{work}/out"},
		Run:     []string{"dotnet", "{work}/out/candidate.dll"}},
}

// extraFiles are written alongside the source because a toolchain refuses to
// build without them; the language's own argv never names them.
var extraFiles = map[string]map[string]string{
	"go": {"go.mod": "module candidate\n\ngo 1.22\n"},
	"csharp": {"candidate.csproj": `<Project Sdk="Microsoft.NET.Sdk">` +
		`<PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net9.0</TargetFramework>` +
		`<AssemblyName>candidate</AssemblyName><Nullable>disable</Nullable>` +
		`<ImplicitUsings>enable</ImplicitUsings></PropertyGroup></Project>` + "\n"},
}

// stage writes the work dir and resolves the language's argv. It runs
// nothing, so the argv a language produces is testable without its toolchain.
func stage(lang, source, work string) (compile, run []string, err error) {
	spec, ok := langs[lang]
	if !ok {
		return nil, nil, fmt.Errorf("unsupported language %q", lang)
	}
	src := filepath.Join(work, spec.File)
	if err := os.WriteFile(src, []byte(source), 0o644); err != nil {
		return nil, nil, err
	}
	for name, body := range extraFiles[lang] {
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o644); err != nil {
			return nil, nil, err
		}
	}
	sub := strings.NewReplacer("{work}", work, "{src}", src, "{bin}", filepath.Join(work, "candidate"))
	expand := func(argv []string) []string {
		if len(argv) == 0 {
			return nil
		}
		out := make([]string, len(argv))
		for i, a := range argv {
			out[i] = sub.Replace(a)
		}
		return out
	}
	return expand(spec.Compile), expand(spec.Run), nil
}

// prepare writes the source and compiles it, returning the command to run a test.
func prepare(lang, source, work string) (cmdline []string, compileOut string, err error) {
	compileArgv, runArgv, err := stage(lang, source, work)
	if err != nil {
		return nil, "", err
	}
	if len(compileArgv) == 0 {
		return runArgv, "", nil
	}
	// GOCACHE comes from the image: a pre-warmed, read-only standard library cache.
	env := append(os.Environ(), "HOME=/tmp", "GOPATH=/tmp/gopath", "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local", "GOPROXY=off")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, compileArgv[0], compileArgv[1:]...)
	c.Dir, c.Env = work, env
	out, err := c.CombinedOutput()
	if err != nil {
		return nil, string(out), err
	}
	return runArgv, "", nil
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
	res.StdoutTail = clip(trimmed, outputTailBytes)

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

// outputTailBytes bounds what travels back as readable output. A program that
// prints a megabyte is still hashed in full; only what a person would read is
// carried.
const outputTailBytes = 4096

// clip cuts s to at most n bytes and says so when it had to.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… truncated"
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
