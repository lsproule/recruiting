package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The ids the app may send; kept as a literal so a drift in either list is a
// failure here rather than an "unsupported language" at run time.
var codeLanguages = []string{
	"python", "javascript", "ruby", "php", "go", "java", "csharp", "cpp", "c", "rust",
}

func TestLangsCoverEveryCodeLanguage(t *testing.T) {
	for _, id := range codeLanguages {
		spec, ok := langs[id]
		if !ok {
			t.Errorf("no harness entry for %q", id)
			continue
		}
		if spec.File == "" {
			t.Errorf("%s has no source file name", id)
		}
		if len(spec.Run) == 0 {
			t.Errorf("%s has no run command", id)
		}
	}
	if len(langs) != len(codeLanguages) {
		t.Errorf("langs holds %d entries, want %d", len(langs), len(codeLanguages))
	}
}

// The four v1 languages keep the argv they shipped with; changing them
// changes how every stored submission is re-run.
func TestStageKeepsTheOriginalArgv(t *testing.T) {
	for _, tc := range []struct {
		lang, file      string
		compile, run    []string
		binIsWorkBinary bool
	}{
		{lang: "python", file: "main.py",
			compile: []string{"python3", "-I", "-m", "py_compile", "{src}"},
			run:     []string{"python3", "-I", "-u", "{src}"}},
		{lang: "javascript", file: "main.js",
			compile: []string{"node", "--check", "{src}"},
			run:     []string{"node", "--stack-size=2048", "{src}"}},
		{lang: "go", file: "main.go",
			compile: []string{"go", "build", "-o", "{bin}", "."},
			run:     []string{"{bin}"}},
		{lang: "java", file: "Main.java",
			compile: []string{"javac", "-d", "{work}", "Main.java"},
			run:     []string{"java", "-Xss8m", "-XX:+UseSerialGC", "-XX:TieredStopAtLevel=1", "-cp", "{work}", "Main"}},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			spec := langs[tc.lang]
			if spec.File != tc.file {
				t.Errorf("file = %q, want %q", spec.File, tc.file)
			}
			if !slices.Equal(spec.Compile, tc.compile) {
				t.Errorf("compile = %v, want %v", spec.Compile, tc.compile)
			}
			if !slices.Equal(spec.Run, tc.run) {
				t.Errorf("run = %v, want %v", spec.Run, tc.run)
			}
		})
	}
}

func TestStageWritesTheSourceAndSubstitutes(t *testing.T) {
	work := t.TempDir()
	compile, run, err := stage("rust", "fn main() {}", work)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	src := filepath.Join(work, "main.rs")
	if b, err := os.ReadFile(src); err != nil || string(b) != "fn main() {}" {
		t.Fatalf("main.rs = %q, %v", b, err)
	}
	if !slices.Contains(compile, src) {
		t.Errorf("compile %v does not name %q", compile, src)
	}
	bin := filepath.Join(work, "candidate")
	if !slices.Equal(run, []string{bin}) {
		t.Errorf("run = %v, want %v", run, []string{bin})
	}
	for _, arg := range append(append([]string{}, compile...), run...) {
		if strings.ContainsAny(arg, "{}") {
			t.Errorf("unsubstituted placeholder in %q", arg)
		}
	}
}

// Go needs a module file before it can build, so staging writes one.
func TestStageWritesGoModule(t *testing.T) {
	work := t.TempDir()
	if _, _, err := stage("go", "package main", work); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "go.mod")); err != nil {
		t.Fatalf("go.mod: %v", err)
	}
}

func TestStageRejectsAnUnknownLanguage(t *testing.T) {
	_, _, err := stage("cobol", "", t.TempDir())
	if err == nil || err.Error() != `unsupported language "cobol"` {
		t.Fatalf("err = %v, want unsupported language \"cobol\"", err)
	}
}

// A toolchain's diagnostics can run to megabytes; only a bounded tail may
// travel back as the compile output, or one submission's error text could
// exhaust the harness and the runner that reads it.
func TestCompileOutputIsCappedToATail(t *testing.T) {
	work := t.TempDir()
	// Print ~1 MiB of numbered lines, then fail, the way a compiler that
	// cannot stop complaining does.
	script := `i=0; while [ $i -lt 40000 ]; do echo "error line $i: something is wrong"; i=$((i+1)); done; echo LAST; exit 1`
	_, out, err := compileWith([]string{"sh", "-c", script}, []string{"noop"}, work)
	if err == nil {
		t.Fatal("a failing compile must report an error")
	}
	if len(out) > outputTailBytes+64 {
		t.Fatalf("compile output is %d bytes, want at most the %d-byte tail plus a note", len(out), outputTailBytes)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "LAST") {
		t.Errorf("the tail must keep the last line, where the actual error is; got %q", out[len(out)-80:])
	}
	if !strings.HasPrefix(out, "[earlier output truncated]") {
		t.Errorf("truncated output must say so; got prefix %q", out[:40])
	}
	if strings.Contains(out, "error line 0:") {
		t.Error("the head of the output should have been dropped")
	}
}

func TestCompileOutputShortIsKeptWhole(t *testing.T) {
	_, out, err := compileWith([]string{"sh", "-c", "echo boom >&2; exit 2"}, []string{"noop"}, t.TempDir())
	if err == nil || out != "boom\n" {
		t.Fatalf("out = %q, err = %v; want the whole short output and an error", out, err)
	}
}

func TestCappedWriterKeepsTailOrHead(t *testing.T) {
	tail := &cappedWriter{limit: 4, keepTail: true}
	for _, chunk := range []string{"ab", "cd", "ef"} {
		_, _ = tail.Write([]byte(chunk))
	}
	if tail.String() != "cdef" || !tail.dropped {
		t.Errorf("tail writer = %q dropped=%v, want cdef dropped", tail.String(), tail.dropped)
	}
	big := &cappedWriter{limit: 4, keepTail: true}
	_, _ = big.Write([]byte(strings.Repeat("x", 100) + "yz"))
	if big.String() != "xxyz" || !big.dropped {
		t.Errorf("one huge write = %q, want its last 4 bytes", big.String())
	}
	head := &cappedWriter{limit: 4}
	_, _ = head.Write([]byte("abcdef"))
	if head.String() != "abcd" || !head.dropped {
		t.Errorf("head writer = %q dropped=%v, want abcd dropped", head.String(), head.dropped)
	}
	exact := &cappedWriter{limit: 4}
	_, _ = exact.Write([]byte("abcd"))
	if exact.dropped {
		t.Error("a write that fits exactly drops nothing")
	}
}

// A managed runtime must size its heap from the spec's limit, not from the
// container's cgroup, which is deliberately larger (see the runner's docker
// executor for the headroom the container carries).
func TestApplyMemoryLimitSizesManagedHeaps(t *testing.T) {
	argv, env := applyMemoryLimit([]string{"java", "-Xss8m", "-cp", "/tmp/w", "Main"}, 256)
	if !slices.Equal(argv, []string{"java", "-Xmx192m", "-Xss8m", "-cp", "/tmp/w", "Main"}) || env != nil {
		t.Errorf("java: argv = %v env = %v", argv, env)
	}
	argv, env = applyMemoryLimit([]string{"dotnet", "/tmp/w/out/candidate.dll"}, 256)
	if !slices.Equal(argv, []string{"dotnet", "/tmp/w/out/candidate.dll"}) || !slices.Equal(env, []string{"DOTNET_GCHeapHardLimit=0xC000000"}) {
		t.Errorf("dotnet: argv = %v env = %v", argv, env)
	}
	argv, env = applyMemoryLimit([]string{"/tmp/w/candidate"}, 256)
	if !slices.Equal(argv, []string{"/tmp/w/candidate"}) || env != nil {
		t.Errorf("native: argv = %v env = %v", argv, env)
	}
	if argv, _ := applyMemoryLimit([]string{"java", "Main"}, 8); argv[1] != "-Xmx16m" {
		t.Errorf("tiny limit: argv = %v, want a 16m floor", argv)
	}
	if argv, env := applyMemoryLimit([]string{"java", "Main"}, 0); len(argv) != 2 || env != nil {
		t.Errorf("no limit: argv = %v env = %v, want untouched", argv, env)
	}
}
