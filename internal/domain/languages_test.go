package domain_test

import (
	"slices"
	"testing"

	"recruiting/internal/domain"
)

func TestLanguagesAreTheDocumentedSetInOrder(t *testing.T) {
	want := []string{
		"python", "javascript", "typescript", "go", "java", "c", "cpp",
		"rust", "php", "ruby", "haskell", "lua", "kotlin", "csharp", "sql",
	}
	got := make([]string, 0, len(domain.Languages))
	for _, l := range domain.Languages {
		got = append(got, l.ID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("language ids = %v, want %v", got, want)
	}
	for _, l := range domain.Languages {
		if l.Label == "" {
			t.Errorf("%s has no label", l.ID)
		}
		if l.Kind != domain.ProblemKindCode && l.Kind != domain.ProblemKindSQL {
			t.Errorf("%s has kind %q", l.ID, l.Kind)
		}
	}
}

func TestProblemLanguagesDerivesFromTheRegistry(t *testing.T) {
	if len(domain.ProblemLanguages) != len(domain.Languages) {
		t.Fatalf("ProblemLanguages = %v, want one id per registry entry", domain.ProblemLanguages)
	}
	for i, l := range domain.Languages {
		if domain.ProblemLanguages[i] != l.ID {
			t.Fatalf("ProblemLanguages[%d] = %q, want %q", i, domain.ProblemLanguages[i], l.ID)
		}
	}
}

func TestCodeLanguageIDsExcludesSQL(t *testing.T) {
	got := domain.CodeLanguageIDs()
	if len(got) != 14 {
		t.Fatalf("got %d code languages, want 14: %v", len(got), got)
	}
	if slices.Contains(got, "sql") {
		t.Error("sql is listed as a code language")
	}
	if got[0] != "python" || got[len(got)-1] != "csharp" {
		t.Errorf("code ids = %v, want registry order", got)
	}
}

func TestLanguageByID(t *testing.T) {
	l, ok := domain.LanguageByID("cpp")
	if !ok || l.ID != "cpp" || !l.Compiled {
		t.Fatalf("LanguageByID(cpp) = %+v, %v", l, ok)
	}
	// The v1 id for JavaScript is still accepted on input.
	if l, ok := domain.LanguageByID("node"); !ok || l.ID != "javascript" {
		t.Fatalf("LanguageByID(node) = %+v, %v; want javascript", l, ok)
	}
	if l, ok := domain.LanguageByID(" Python "); !ok || l.ID != "python" {
		t.Fatalf("LanguageByID with padding = %+v, %v", l, ok)
	}
	if _, ok := domain.LanguageByID("cobol"); ok {
		t.Error("cobol resolved")
	}
	if _, ok := domain.LanguageByID(domain.LanguageAny); ok {
		t.Error("any resolved to a language")
	}
}
