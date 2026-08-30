package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Problem kinds. A code problem runs a program against stdin/stdout; a SQL
// problem runs one query against a seeded database.
const (
	ProblemKindCode = "code"
	ProblemKindSQL  = "sql"
)

// Test case visibility: a public case is shown to the candidate with its
// expected output, a hidden one only ever reports pass or fail.
const (
	VisibilityPublic = "public"
	VisibilityHidden = "hidden"
)

// Difficulties and languages a problem may declare, in the order screens list
// them. Languages match the runner's wire protocol.
var (
	ProblemDifficulties = []string{"easy", "medium", "hard"}
	ProblemLanguages    = []string{"python", "node", "go", "java", "sql"}
)

// Limits a problem falls back to and the bounds an import may ask for.
const (
	DefaultProblemTimeLimitMs   = 2000
	DefaultProblemMemoryLimitKB = 262144
	MaxProblemTimeLimitMs       = 60000
	MaxProblemMemoryLimitKB     = 2097152
	// MaxProblemTestCases matches the runner's per-request test cap, so a
	// stored problem can always be executed in one call.
	MaxProblemTestCases = 100
	// MaxImportProblems bounds one batch. Every reference solution in it is
	// executed before anything is stored, so the batch is also a bound on how
	// long an import holds the runner.
	MaxImportProblems = 50
)

// ImportProblem is one problem as it appears in an import document. The JSON
// names are the documented import format; see seed/problems/README.md.
type ImportProblem struct {
	Kind             string            `json:"kind"`
	Title            string            `json:"title"`
	Statement        string            `json:"statement"`
	Difficulty       string            `json:"difficulty"`
	Tags             []string          `json:"tags"`
	AllowedLanguages []string          `json:"allowed_languages"`
	TimeLimitMs      int               `json:"time_limit_ms"`
	MemoryLimitKB    int               `json:"memory_limit_kb"`
	SQLSchema        string            `json:"sql_schema"`
	SQLSeed          string            `json:"sql_seed"`
	References       []ImportReference `json:"reference_solutions"`
	TestCases        []ImportTestCase  `json:"test_cases"`
}

// ImportReference is a solution that must pass every test case; the import
// proves the problem is solvable in each language it offers.
type ImportReference struct {
	Language string `json:"language"`
	Source   string `json:"source"`
}

// ImportTestCase is one case a solution is scored against. Weight is a
// pointer so an omitted weight (which defaults to 1) is told apart from an
// explicit zero, which is refused.
type ImportTestCase struct {
	Input      string   `json:"input"`
	Expected   string   `json:"expected"`
	Visibility string   `json:"visibility"`
	Weight     *float64 `json:"weight"`
	// Unordered compares SQL result rows as a multiset; ignored for code.
	Unordered bool `json:"unordered"`
}

// WeightValue is the case's weight, 1 when the document omitted one.
func (t ImportTestCase) WeightValue() float64 {
	if t.Weight == nil {
		return 1
	}
	return *t.Weight
}

// ProblemImportError collects everything wrong with one problem of a batch,
// identified by its position and title so the report survives a nameless
// problem. A negative Index means the fault is the document's, not any one
// problem's — malformed JSON, say.
type ProblemImportError struct {
	Index  int
	Title  string
	Errors []string
}

// DocumentIndex marks an error the whole document is at fault for.
const DocumentIndex = -1

// Label names what the error is about: the problem's title, its position, or
// the document itself.
func (e ProblemImportError) Label() string {
	switch {
	case e.Index < 0:
		return "the document"
	case e.Title != "":
		return e.Title
	default:
		return fmt.Sprintf("problem %d", e.Index+1)
	}
}

// ProblemImportErrors is the whole batch's report. A batch is all-or-nothing,
// so one bad problem rejects every problem in the document.
type ProblemImportErrors []ProblemImportError

func (e ProblemImportErrors) Error() string {
	parts := make([]string, 0, len(e))
	for _, p := range e {
		parts = append(parts, p.Label()+": "+strings.Join(p.Errors, "; "))
	}
	return "problem import rejected: " + strings.Join(parts, " | ")
}

// ParseProblemImport decodes an import document and validates every problem in
// it. Structural problems the runner cannot help with — an unknown language, a
// missing reference solution, a SQL problem without a schema — are caught
// here; whether a reference actually solves the problem is the caller's job.
func ParseProblemImport(data []byte) ([]ImportProblem, error) {
	var problems []ImportProblem
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&problems); err != nil {
		return nil, documentError("the document is not a valid problem batch: %v", err)
	}
	if dec.More() {
		return nil, documentError("the document carries more than one JSON value; a batch is a single array of problems")
	}
	switch {
	case len(problems) == 0:
		return nil, documentError("the document contains no problems")
	case len(problems) > MaxImportProblems:
		return nil, documentError("the document holds %d problems, more than the %d one import accepts", len(problems), MaxImportProblems)
	}
	var report ProblemImportErrors
	seen := map[string]int{}
	for i := range problems {
		problems[i].Normalize()
		errs := problems[i].Validate()
		if key := strings.ToLower(problems[i].Title); key != "" {
			if first, dup := seen[key]; dup {
				errs = append(errs, fmt.Sprintf("duplicate title, already used by problem %d", first+1))
			} else {
				seen[key] = i
			}
		}
		if len(errs) > 0 {
			report = append(report, ProblemImportError{Index: i, Title: problems[i].Title, Errors: errs})
		}
	}
	if len(report) > 0 {
		return nil, report
	}
	return problems, nil
}

// documentError reports a fault of the document itself rather than of any one
// problem in it, in the same shape so one report renders both.
func documentError(format string, args ...any) ProblemImportErrors {
	return ProblemImportErrors{{Index: DocumentIndex, Errors: []string{fmt.Sprintf(format, args...)}}}
}

// Normalize trims and lower-cases the enumerated fields and fills defaults, so
// validation and storage see one shape whatever the document wrote.
func (p *ImportProblem) Normalize() {
	p.Kind = strings.ToLower(strings.TrimSpace(p.Kind))
	p.Title = strings.TrimSpace(p.Title)
	p.Statement = strings.TrimSpace(p.Statement)
	p.Difficulty = strings.ToLower(strings.TrimSpace(p.Difficulty))
	if p.Difficulty == "" {
		p.Difficulty = "medium"
	}
	p.Tags = normalizeList(p.Tags)
	p.AllowedLanguages = normalizeList(p.AllowedLanguages)
	if p.Kind == ProblemKindSQL && len(p.AllowedLanguages) == 0 {
		p.AllowedLanguages = []string{"sql"}
	}
	if p.TimeLimitMs <= 0 {
		p.TimeLimitMs = DefaultProblemTimeLimitMs
	}
	if p.MemoryLimitKB <= 0 {
		p.MemoryLimitKB = DefaultProblemMemoryLimitKB
	}
	p.SQLSchema = strings.TrimSpace(p.SQLSchema)
	p.SQLSeed = strings.TrimSpace(p.SQLSeed)
	for i := range p.References {
		p.References[i].Language = strings.ToLower(strings.TrimSpace(p.References[i].Language))
	}
	for i := range p.TestCases {
		tc := &p.TestCases[i]
		tc.Visibility = strings.ToLower(strings.TrimSpace(tc.Visibility))
		if tc.Visibility == "" {
			tc.Visibility = VisibilityPublic
		}
		if tc.Weight == nil {
			one := 1.0
			tc.Weight = &one
		}
	}
}

// Validate returns every reason the problem cannot be stored, in reading
// order, so an import report tells the author about all of them at once.
// Normalize must have run first.
func (p ImportProblem) Validate() []string {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	switch p.Kind {
	case ProblemKindCode, ProblemKindSQL:
	case "":
		add("kind is required (%s or %s)", ProblemKindCode, ProblemKindSQL)
	default:
		add("unknown kind %q", p.Kind)
	}
	if p.Title == "" {
		add("title is required")
	}
	if p.Statement == "" {
		add("statement is required")
	}
	if !contains(ProblemDifficulties, p.Difficulty) {
		add("unknown difficulty %q (want %s)", p.Difficulty, strings.Join(ProblemDifficulties, ", "))
	}
	if p.TimeLimitMs > MaxProblemTimeLimitMs {
		add("time_limit_ms %d exceeds the %d ms cap", p.TimeLimitMs, MaxProblemTimeLimitMs)
	}
	if p.MemoryLimitKB > MaxProblemMemoryLimitKB {
		add("memory_limit_kb %d exceeds the %d kB cap", p.MemoryLimitKB, MaxProblemMemoryLimitKB)
	}

	if len(p.AllowedLanguages) == 0 {
		add("allowed_languages must name at least one language")
	}
	for _, lang := range p.AllowedLanguages {
		if !contains(ProblemLanguages, lang) {
			add("unknown language %q in allowed_languages (want %s)", lang, strings.Join(ProblemLanguages, ", "))
			continue
		}
		if p.Kind == ProblemKindSQL && lang != "sql" {
			add("a sql problem cannot offer %q", lang)
		}
		if p.Kind == ProblemKindCode && lang == "sql" {
			add("sql is not a language for a code problem")
		}
	}

	errs = append(errs, p.validateReferences()...)
	errs = append(errs, p.validateTestCases()...)

	switch p.Kind {
	case ProblemKindSQL:
		if p.SQLSchema == "" {
			add("a sql problem needs sql_schema to seed its database")
		}
	case ProblemKindCode:
		if p.SQLSchema != "" || p.SQLSeed != "" {
			add("sql_schema and sql_seed belong to a sql problem")
		}
	}
	return errs
}

func (p ImportProblem) validateReferences() []string {
	var errs []string
	if len(p.References) == 0 {
		return []string{"reference_solutions must carry at least one solution"}
	}
	seen := map[string]bool{}
	for i, ref := range p.References {
		switch {
		case ref.Language == "":
			errs = append(errs, fmt.Sprintf("reference_solutions[%d] has no language", i))
		case !contains(p.AllowedLanguages, ref.Language):
			errs = append(errs, fmt.Sprintf("reference_solutions[%d] is in %q, which the problem does not allow", i, ref.Language))
		case seen[ref.Language]:
			errs = append(errs, fmt.Sprintf("reference_solutions[%d] is a second solution in %q", i, ref.Language))
		}
		seen[ref.Language] = true
		if strings.TrimSpace(ref.Source) == "" {
			errs = append(errs, fmt.Sprintf("reference_solutions[%d] has no source", i))
		}
	}
	// A candidate may pick any allowed language, so every one of them must be
	// proven solvable before the problem enters the bank.
	for _, lang := range p.AllowedLanguages {
		if !seen[lang] {
			errs = append(errs, fmt.Sprintf("%s is allowed but has no reference solution", lang))
		}
	}
	return errs
}

func (p ImportProblem) validateTestCases() []string {
	var errs []string
	if len(p.TestCases) == 0 {
		return []string{"test_cases must carry at least one case"}
	}
	if len(p.TestCases) > MaxProblemTestCases {
		errs = append(errs, fmt.Sprintf("test_cases holds %d cases, more than the %d the runner accepts in one call", len(p.TestCases), MaxProblemTestCases))
	}
	public := 0
	for i, tc := range p.TestCases {
		switch tc.Visibility {
		case VisibilityPublic:
			public++
		case VisibilityHidden:
		default:
			errs = append(errs, fmt.Sprintf("test_cases[%d] has unknown visibility %q", i, tc.Visibility))
		}
		if tc.WeightValue() <= 0 {
			errs = append(errs, fmt.Sprintf("test_cases[%d] has weight %v; a weight must be positive", i, tc.WeightValue()))
		}
	}
	if public == 0 {
		errs = append(errs, "at least one test case must be public so the candidate sees an example")
	}
	return errs
}

// normalizeList trims, lower-cases, and de-duplicates a tag or language list,
// keeping a stable order so two imports of the same document store the same
// rows.
func normalizeList(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
