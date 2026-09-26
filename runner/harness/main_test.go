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
