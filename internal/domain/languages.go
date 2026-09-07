package domain

import "strings"

// Language is one language a problem may be offered in. Kind tells a code
// language (a program read from stdin) from the SQL one (a single query), and
// Compiled marks the languages the runner builds once before the tests.
type Language struct {
	ID       string
	Label    string
	Kind     string
	Compiled bool
}

// LanguageAny is the wildcard an import or a screen may write instead of
// listing languages; it expands to every code language, or to sql alone on a
// sql problem.
const LanguageAny = "any"

// Languages is the one place language ids are listed: the problem bank, the
// runner's wire protocol, and the sandbox harness all follow this order.
var Languages = []Language{
	{ID: "python", Label: "Python", Kind: ProblemKindCode},
	{ID: "javascript", Label: "JavaScript", Kind: ProblemKindCode},
	{ID: "typescript", Label: "TypeScript", Kind: ProblemKindCode},
	{ID: "go", Label: "Go", Kind: ProblemKindCode, Compiled: true},
	{ID: "java", Label: "Java", Kind: ProblemKindCode, Compiled: true},
	{ID: "c", Label: "C", Kind: ProblemKindCode, Compiled: true},
	{ID: "cpp", Label: "C++", Kind: ProblemKindCode, Compiled: true},
	{ID: "rust", Label: "Rust", Kind: ProblemKindCode, Compiled: true},
	{ID: "php", Label: "PHP", Kind: ProblemKindCode},
	{ID: "ruby", Label: "Ruby", Kind: ProblemKindCode},
	{ID: "haskell", Label: "Haskell", Kind: ProblemKindCode, Compiled: true},
	{ID: "lua", Label: "Lua", Kind: ProblemKindCode},
	{ID: "kotlin", Label: "Kotlin", Kind: ProblemKindCode, Compiled: true},
	{ID: "csharp", Label: "C#", Kind: ProblemKindCode, Compiled: true},
	{ID: "sql", Label: "SQL", Kind: ProblemKindSQL},
}

// languageAliases maps retired ids to their current one so a document, an API
// caller, or a stored row written before a rename still resolves.
var languageAliases = map[string]string{"node": "javascript"}

// NormalizeLanguageID trims, lower-cases, and resolves aliases. It does not
// check that the result is a known language.
func NormalizeLanguageID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if current, ok := languageAliases[id]; ok {
		return current
	}
	return id
}

// LanguageByID resolves an id, accepting padding, case, and retired ids.
func LanguageByID(id string) (Language, bool) {
	id = NormalizeLanguageID(id)
	for _, l := range Languages {
		if l.ID == id {
			return l, true
		}
	}
	return Language{}, false
}

// CodeLanguageIDs lists the languages a code problem may offer, in registry
// order.
func CodeLanguageIDs() []string {
	out := make([]string, 0, len(Languages))
	for _, l := range Languages {
		if l.Kind == ProblemKindCode {
			out = append(out, l.ID)
		}
	}
	return out
}

// languageIDs lists every id in registry order.
func languageIDs() []string {
	out := make([]string, 0, len(Languages))
	for _, l := range Languages {
		out = append(out, l.ID)
	}
	return out
}

// expandLanguageAny replaces the wildcard, wherever it appears, with every
// language the kind allows. A kind other than sql expands to the code
// languages so an unknown kind is reported as itself, not as a pile of
// language errors.
func expandLanguageAny(kind string, ids []string) []string {
	if !contains(ids, LanguageAny) {
		return ids
	}
	if kind == ProblemKindSQL {
		return []string{"sql"}
	}
	return CodeLanguageIDs()
}
