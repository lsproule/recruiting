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

	"recruiting/runner/wire"
)

// spec is what the app sends. Signature, when set, makes this a function
// run: the source is a function of that signature rather than a program,
// each test's input is a JSON array of arguments, and its expected value is
// JSON the function's result is compared against.
type spec struct {
	Language  string          `json:"language"`
	Source    string          `json:"source"`
	Signature *wire.Signature `json:"signature,omitempty"`
	Tests     []test          `json:"tests"`
	Limits    limits          `json:"limits"`
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
	var cmdline []string
	var compileOut string
	if s.Signature != nil {
		cmdline, compileOut, err = prepareFunction(s.Language, s.Source, *s.Signature, work)
	} else {
		cmdline, compileOut, err = prepare(s.Language, s.Source, work)
	}
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
		if s.Signature != nil {
			out.Results = append(out.Results, runFunctionTest(cmdline, work, *s.Signature, t, s.Limits))
		} else {
			out.Results = append(out.Results, runTest(cmdline, work, t, s.Limits))
		}
	}
	return out
}

// stageFunction writes the candidate's function and the generated driver
// under the language's function layout and resolves the argv pair.
func stageFunction(lang, source string, sig wire.Signature, work string) (compile, run []string, err error) {
	layout, ok := wire.Layouts[lang]
	if !ok {
		return nil, nil, fmt.Errorf("unsupported language %q", lang)
	}
	driver, err := wire.Driver(lang, sig)
	if err != nil {
		return nil, nil, err
	}
	if layout.Concat {
		if err := os.WriteFile(filepath.Join(work, layout.Candidate), []byte(source+"\n"+driver), 0o644); err != nil {
			return nil, nil, err
		}
	} else {
		if err := os.WriteFile(filepath.Join(work, layout.Candidate), []byte(source), 0o644); err != nil {
			return nil, nil, err
		}
		if err := os.WriteFile(filepath.Join(work, layout.Driver), []byte(driver), 0o644); err != nil {
			return nil, nil, err
		}
	}
	for name, body := range extraFiles[lang] {
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o644); err != nil {
			return nil, nil, err
		}
	}
	sub := strings.NewReplacer("{work}", work, "{bin}", filepath.Join(work, "candidate"))
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
	return expand(layout.Compile), expand(layout.Run), nil
}

// prepareFunction is prepare for a function run.
func prepareFunction(lang, source string, sig wire.Signature, work string) (cmdline []string, compileOut string, err error) {
	compileArgv, runArgv, err := stageFunction(lang, source, sig, work)
	if err != nil {
		return nil, "", err
	}
	return compileWith(compileArgv, runArgv, work)
}

// runFunctionTest runs one call: the arguments travel to the driver in the
// stream format, the result comes back after the marker and is compared
// against the expected value by type. What the candidate printed on their
// own is kept apart from the result and reported with stderr.
func runFunctionTest(cmdline []string, work string, sig wire.Signature, t test, l limits) result {
	args, err := sig.ArgsOf(json.RawMessage(t.Input))
	if err != nil {
		return result{TestID: t.ID, Status: "error", StderrTail: "bad test case arguments: " + err.Error()}
	}
	want, err := wire.DecodeTyped(sig.Returns, json.RawMessage(t.Expected))
	if err != nil {
		return result{TestID: t.ID, Status: "error", StderrTail: "bad expected value: " + err.Error()}
	}
	res, stdout := execute(cmdline, work, string(wire.Encode(sig, args)), t.ID, l)
	printed, raw, ok := wire.SplitResult(stdout)
	if p := strings.TrimSpace(string(printed)); p != "" {
		res.StderrTail = strings.TrimSpace(res.StderrTail + "\n[printed]\n" + clip(p, 1024))
	}
	if res.Status != "" {
		return res
	}
	if !ok {
		res.Status = "error"
		if res.StderrTail == "" {
			res.StderrTail = "the function returned nothing"
		}
		return res
	}
	got, err := wire.Decode(sig.Returns, raw)
	if err != nil {
		res.Status = "error"
		res.StderrTail = strings.TrimSpace(res.StderrTail + "\nthe result could not be read as " + sig.Returns + ": " + err.Error())
		return res
	}
	canon := wire.CanonicalJSON(got)
	h := sha256.Sum256([]byte(canon))
	res.StdoutHash = hex.EncodeToString(h[:])
	res.StdoutTail = clip(canon, outputTailBytes)
	if wire.ValuesEqual(sig.Returns, got, want) {
		res.Status = "pass"
	} else {
		res.Status = "fail"
	}
	return res
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
	return compileWith(compileArgv, runArgv, work)
}

// compileWith runs the compile step, when there is one, and hands back the
// argv that runs a test.
func compileWith(compileArgv, runArgv []string, work string) (cmdline []string, compileOut string, err error) {
	if len(compileArgv) == 0 {
		return runArgv, "", nil
	}
	// GOCACHE comes from the image: a pre-warmed, read-only standard library cache.
	env := append(os.Environ(), "HOME=/tmp", "GOPATH=/tmp/gopath", "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local", "GOPROXY=off")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, compileArgv[0], compileArgv[1:]...)
	c.Dir, c.Env = work, env
	// A toolchain's diagnostics are unbounded (a template error in C++ or a
	// borrow-checker trace in Rust runs to megabytes), so only the tail
	// travels back: the last lines are where the actual error is.
	out := &cappedWriter{limit: outputTailBytes, keepTail: true}
	c.Stdout, c.Stderr = out, out
	if err := c.Run(); err != nil {
		compileOut = out.String()
		if out.dropped {
			compileOut = "[earlier output truncated]\n" + compileOut
		}
		return nil, compileOut, err
	}
	return runArgv, "", nil
}

func runTest(cmdline []string, work string, t test, l limits) result {
	res, stdout := execute(cmdline, work, t.Input, t.ID, l)
	trimmed := strings.TrimSpace(string(stdout))
	h := sha256.Sum256([]byte(trimmed))
	res.StdoutHash = hex.EncodeToString(h[:])
	res.StdoutTail = clip(trimmed, outputTailBytes)
	if res.Status != "" {
		return res
	}
	if trimmed == strings.TrimSpace(t.Expected) {
		res.Status = "pass"
	} else {
		res.Status = "fail"
	}
	return res
}

// execute runs the program once with input on stdin under the limits. The
// result's Status is set only when the run itself failed (timeout or a
// crash); a clean run leaves it empty for the caller to judge the output.
func execute(cmdline []string, work, input, testID string, l limits) (result, []byte) {
	res := result{TestID: testID}
	wall := time.Duration(l.WallMs) * time.Millisecond
	if wall <= 0 {
		wall = 5 * time.Second
	}
	capBytes := l.OutputKB * 1024
	if capBytes <= 0 {
		capBytes = 64 * 1024
	}
	cmdline, memEnv := applyMemoryLimit(cmdline, l.MemMB)

	ctx, cancel := context.WithTimeout(context.Background(), wall)
	defer cancel()
	cmd := exec.CommandContext(ctx, cmdline[0], cmdline[1:]...)
	cmd.Dir = work
	cmd.Env = append(append(os.Environ(), "HOME=/tmp"), memEnv...)
	cmd.Stdin = strings.NewReader(input)
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
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.Status = "timeout"
	case err != nil:
		res.Status = "error"
		if res.StderrTail == "" {
			res.StderrTail = err.Error()
		}
	}
	return res, stdout.buf.Bytes()
}

// applyMemoryLimit sizes a managed runtime's heap from the candidate's
// memory limit, the mem_mb the spec carries, rather than letting the runtime
// read the container's cgroup. The container is given that limit plus a
// fixed headroom for the harness, the tmpfs and the runtime's own overhead
// (see the runner's docker executor); a JVM or .NET GC that sized itself
// from the cgroup would claim that headroom too and get the whole sandbox
// OOM-killed, blaming the candidate for the harness's memory. The heap gets
// three quarters of the limit: the rest is the runtime's metaspace, code
// cache and thread stacks. Unmanaged languages are bounded by the cgroup
// alone.
func applyMemoryLimit(cmdline []string, memMB int) (argv, env []string) {
	if memMB <= 0 || len(cmdline) == 0 {
		return cmdline, nil
	}
	heapMB := max(memMB*3/4, 16)
	switch filepath.Base(cmdline[0]) {
	case "java":
		argv = append(append(append([]string{}, cmdline[0]), fmt.Sprintf("-Xmx%dm", heapMB)), cmdline[1:]...)
		return argv, nil
	case "dotnet":
		// The GC's hard limit is read as a hexadecimal byte count.
		return cmdline, []string{fmt.Sprintf("DOTNET_GCHeapHardLimit=0x%X", heapMB<<20)}
	}
	return cmdline, nil
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
	// dropped reports that something was discarded, so a reader can be
	// told the text is incomplete.
	dropped bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.keepTail {
		if len(p) > w.limit {
			// Only the tail of a single huge write can matter; never buffer
			// the whole of it first.
			p = p[len(p)-w.limit:]
			w.dropped = true
		}
		w.buf.Write(p)
		if w.buf.Len() > w.limit {
			b := w.buf.Bytes()
			w.buf = *bytes.NewBuffer(append([]byte(nil), b[len(b)-w.limit:]...))
			w.dropped = true
		}
		return n, nil
	}
	room := w.limit - w.buf.Len()
	if len(p) > room {
		w.dropped = true
	}
	if room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		w.buf.Write(p)
	}
	return n, nil
}

func (w *cappedWriter) String() string { return w.buf.String() }

var _ io.Writer = (*cappedWriter)(nil)
