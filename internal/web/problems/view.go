package problems

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

// problemForm is what the create and edit screens render and post. It mirrors
// the import shape so a hand-written problem and an imported one are held to
// the same rules.
type problemForm struct {
	ID               uuid.UUID
	New              bool
	Kind             string
	Title            string
	Statement        string
	Difficulty       string
	Tags             string
	AllowedLanguages string
	TimeLimitMs      int
	MemoryLimitKB    int
	SQLSchema        string
	SQLSeed          string
	References       []referenceRow
	TestCases        []testCaseRow
}

type referenceRow struct {
	Language string
	Source   string
}

type testCaseRow struct {
	Input      string
	Expected   string
	Visibility string
	Weight     string
	Unordered  bool
}

// action is where the form posts: a new problem to the collection, an edit to
// the problem itself.
func (f problemForm) action() string {
	if f.New {
		return Prefix
	}
	return Prefix + "/" + f.ID.String()
}

func (f problemForm) asImport() domain.ImportProblem {
	out := domain.ImportProblem{
		Kind: f.Kind, Title: f.Title, Statement: f.Statement, Difficulty: f.Difficulty,
		Tags: splitList(f.Tags), AllowedLanguages: splitList(f.AllowedLanguages),
		TimeLimitMs: f.TimeLimitMs, MemoryLimitKB: f.MemoryLimitKB,
		SQLSchema: f.SQLSchema, SQLSeed: f.SQLSeed,
	}
	for _, ref := range f.References {
		if strings.TrimSpace(ref.Source) == "" && strings.TrimSpace(ref.Language) == "" {
			continue
		}
		out.References = append(out.References, domain.ImportReference{Language: ref.Language, Source: ref.Source})
	}
	for _, tc := range f.TestCases {
		if strings.TrimSpace(tc.Expected) == "" && strings.TrimSpace(tc.Input) == "" {
			continue
		}
		imported := domain.ImportTestCase{
			Input: tc.Input, Expected: tc.Expected, Visibility: tc.Visibility,
			Unordered: tc.Unordered,
		}
		// A blank weight box means "leave it at the default"; anything typed
		// is passed through so a bad weight is refused rather than corrected.
		if typed := strings.TrimSpace(tc.Weight); typed != "" {
			weight, err := strconv.ParseFloat(typed, 64)
			if err != nil {
				weight = 0 // refused by validation, which names the field
			}
			imported.Weight = &weight
		}
		out.TestCases = append(out.TestCases, imported)
	}
	return out
}

// formOf fills the edit form from a stored problem, leaving blank rows to add
// another reference solution or test case.
func formOf(p service.Problem) problemForm {
	f := problemForm{
		ID: p.ID, Kind: p.Kind, Title: p.Title, Statement: p.Statement, Difficulty: p.Difficulty,
		Tags: strings.Join(p.Tags, ", "), AllowedLanguages: strings.Join(p.AllowedLanguages, ", "),
		TimeLimitMs: p.TimeLimitMs, MemoryLimitKB: p.MemoryLimitKB,
		SQLSchema: p.SQLSchema, SQLSeed: p.SQLSeed,
	}
	for _, r := range p.References {
		f.References = append(f.References, referenceRow{Language: r.Language, Source: r.Source})
	}
	for _, c := range p.TestCases {
		f.TestCases = append(f.TestCases, testCaseRow{
			Input: c.Input, Expected: c.Expected, Visibility: c.Visibility,
			Weight: strconv.FormatFloat(c.Weight, 'f', -1, 64), Unordered: c.Unordered,
		})
	}
	f.References = append(f.References, make([]referenceRow, formRows)...)
	f.TestCases = append(f.TestCases, make([]testCaseRow, formRows)...)
	return f
}

// readForm reads the posted problem. Reference solutions and test cases are
// indexed rows: the form renders however many the problem has plus a couple of
// blanks, and empty rows are dropped rather than validated.
func readForm(r *http.Request) problemForm {
	f := problemForm{
		Kind:             strings.TrimSpace(r.PostFormValue("kind")),
		Title:            strings.TrimSpace(r.PostFormValue("title")),
		Statement:        r.PostFormValue("statement"),
		Difficulty:       strings.TrimSpace(r.PostFormValue("difficulty")),
		Tags:             r.PostFormValue("tags"),
		AllowedLanguages: r.PostFormValue("allowed_languages"),
		TimeLimitMs:      atoiOr(r.PostFormValue("time_limit_ms"), domain.DefaultProblemTimeLimitMs),
		MemoryLimitKB:    atoiOr(r.PostFormValue("memory_limit_kb"), domain.DefaultProblemMemoryLimitKB),
		SQLSchema:        r.PostFormValue("sql_schema"),
		SQLSeed:          r.PostFormValue("sql_seed"),
	}
	for i := range maxFormRows {
		suffix := "_" + strconv.Itoa(i)
		lang, source := r.PostFormValue("ref_language"+suffix), r.PostFormValue("ref_source"+suffix)
		if _, present := r.PostForm["ref_source"+suffix]; !present {
			break
		}
		f.References = append(f.References, referenceRow{Language: strings.TrimSpace(lang), Source: source})
	}
	for i := range maxFormRows {
		suffix := "_" + strconv.Itoa(i)
		if _, present := r.PostForm["tc_expected"+suffix]; !present {
			break
		}
		f.TestCases = append(f.TestCases, testCaseRow{
			Input:      r.PostFormValue("tc_input" + suffix),
			Expected:   r.PostFormValue("tc_expected" + suffix),
			Visibility: strings.TrimSpace(r.PostFormValue("tc_visibility" + suffix)),
			Weight:     r.PostFormValue("tc_weight" + suffix),
			Unordered:  r.PostFormValue("tc_unordered"+suffix) != "",
		})
	}
	// Always leave somewhere to add another row after a refused submission.
	f.References = append(f.References, make([]referenceRow, formRows)...)
	f.TestCases = append(f.TestCases, make([]testCaseRow, formRows)...)
	return f
}

// problemMessages flattens a validation report into the lines the form shows.
func problemMessages(err error) []string {
	var report domain.ProblemImportErrors
	if errors.As(err, &report) {
		var out []string
		for _, p := range report {
			out = append(out, p.Errors...)
		}
		return out
	}
	return []string{userMessage(err)}
}

func splitList(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func atoiOr(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// rowIndex names one indexed form row.
func rowIndex(prefix string, i int) string { return prefix + "_" + strconv.Itoa(i) }

func itoa(n int) string { return strconv.Itoa(n) }

// problemPath is where one problem's screens live.
func problemPath(p service.Problem) string { return Prefix + "/" + p.ID.String() }

func editPath(p service.Problem) string { return problemPath(p) + "/edit" }

func deletePath(p service.Problem) string { return problemPath(p) + "/delete" }

// ownerLabel says whether a problem is the org's own or part of the shared
// platform bank, which is read-only.
func ownerLabel(p service.Problem) string {
	if p.Platform() {
		return "platform"
	}
	return "yours"
}

func languagesText(p service.Problem) string { return strings.Join(p.AllowedLanguages, ", ") }

func tagsText(p service.Problem) string { return strings.Join(p.Tags, ", ") }

// selected marks the option matching the current value.
func selected(current, value string) bool { return current == value }

// weightText renders a test case's weight without a trailing ".0".
func weightText(w float64) string { return strconv.FormatFloat(w, 'f', -1, 64) }

// languageList names the languages a problem may allow.
func languageList() string { return strings.Join(domain.ProblemLanguages, ", ") }

// reportPosition numbers a rejected problem in the report; a fault of the
// document itself belongs to no problem and is left unnumbered.
func reportPosition(p domain.ProblemImportError) string {
	if p.Index < 0 {
		return "—"
	}
	return strconv.Itoa(p.Index + 1)
}
