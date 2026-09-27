package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"recruiting/runner/wire"
)

// Problem kinds. A function problem asks for one function of a declared
// signature, called with typed arguments; a code problem runs a program
// against stdin/stdout; a SQL problem runs one query against a seeded
// database.
const (
	ProblemKindFunction = "function"
	ProblemKindCode     = "code"
	ProblemKindSQL      = "sql"
)

// ProblemKinds is every kind a problem may declare, in the order screens
// offer them.
var ProblemKinds = []string{ProblemKindFunction, ProblemKindCode, ProblemKindSQL}

// CodeKind reports whether a problem of this kind is answered in a
// programming language: a function or a program, as opposed to a query. The
// language registry files both under ProblemKindCode.
func CodeKind(kind string) bool {
	return kind == ProblemKindFunction || kind == ProblemKindCode
}

// Signature and Param are the function a function problem asks for; the
// harness reads the same type, so it lives in the wire package.
type (
	Signature = wire.Signature
	Param     = wire.Param
)

// SignatureTypes is every type a parameter or a return may declare.
var SignatureTypes = wire.SignatureTypes

// MaxSignatureParams bounds a signature, as the wire does.
const MaxSignatureParams = wire.MaxSignatureParams

// Test case visibility: a public case is shown to the candidate with its
// expected output, a hidden one only ever reports pass or fail.
const (
	VisibilityPublic = "public"
	VisibilityHidden = "hidden"
)

// Test case classes, in the order the authoring table offers them. The class
// says what a case is for — the worked example, a boundary, a load test, or
// the body of the problem — so a failing case names a weakness rather than a
// row number.
const (
	CaseClassSample = "sample"
	CaseClassEdge   = "edge"
	CaseClassPerf   = "perf"
	CaseClassCore   = "core"
)

// TestCaseClasses is every class a case may declare, in authoring order.
var TestCaseClasses = []string{CaseClassSample, CaseClassEdge, CaseClassPerf, CaseClassCore}

// Difficulties a problem may declare, in the order screens list them.
var ProblemDifficulties = []string{"easy", "medium", "hard"}

// ProblemLanguages is the language registry flattened to ids; the runner's
// wire protocol derives from the same list so the two cannot drift.
var ProblemLanguages = languageIDs()

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
	// MaxCaseNameLength keeps a case name to something a results table can
	// show in one column.
	MaxCaseNameLength = 80
)

// ImportProblem is one problem as it appears in an import document. The JSON
// names are the documented import format; see seed/problems/README.md.
type ImportProblem struct {
	Kind             string   `json:"kind"`
	Title            string   `json:"title"`
	Statement        string   `json:"statement"`
	Difficulty       string   `json:"difficulty"`
	Tags             []string `json:"tags"`
	AllowedLanguages []string `json:"allowed_languages"`
	TimeLimitMs      int      `json:"time_limit_ms"`
	MemoryLimitKB    int      `json:"memory_limit_kb"`
	SQLSchema        string   `json:"sql_schema"`
	SQLSeed          string   `json:"sql_seed"`
	// Signature is the function a function problem asks for; nil on the
	// other kinds.
	Signature *Signature `json:"signature"`
	// Guidelines are what an interviewer watches for. They are internal: no
	// candidate and no client ever sees them.
	Guidelines string            `json:"guidelines"`
	References []ImportReference `json:"reference_solutions"`
	TestCases  []ImportTestCase  `json:"test_cases"`
}

// ImportReference is a solution that must pass every test case; one is enough
// to prove a problem solvable, since 15 languages make one per allowed
// language unauthorable.
type ImportReference struct {
	Language string `json:"language"`
	Source   string `json:"source"`
}

// ImportTestCase is one case a solution is scored against. A function
// problem's case carries Args (a JSON array, one value per parameter) and
// Returns (the JSON value the function must return); a code or SQL case
// carries Input and Expected as text. Normalize renders Args and Returns
// into Input and Expected in their canonical spelling, which is what is
// stored and what the harness receives. Weight is a pointer so an omitted
// weight (which defaults to 1) is told apart from an explicit zero, which
// is refused.
type ImportTestCase struct {
	Name       string          `json:"name"`
	Class      string          `json:"class"`
	Input      string          `json:"input"`
	Expected   string          `json:"expected"`
	Args       json.RawMessage `json:"args"`
	Returns    json.RawMessage `json:"returns"`
	Visibility string          `json:"visibility"`
	Weight     *float64        `json:"weight"`
	// Unordered compares SQL result rows as a multiset; ignored for code.
	Unordered bool `json:"unordered"`

	// typed is the verdict of decoding the case against the signature,
	// recorded by Normalize so Validate does not decode a multi-megabyte
	// perf case a second time. Nil until Normalize has run.
	typed *typedCase
}

// typedCase is what decoding a function case against its signature found:
// nil errors when the arguments and the return value fit it.
type typedCase struct {
	args, returns error
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
	p.AllowedLanguages = normalizeList(mapList(p.AllowedLanguages, NormalizeLanguageID))
	p.AllowedLanguages = normalizeList(expandLanguageAny(p.Kind, p.AllowedLanguages))
	if p.Kind == ProblemKindSQL && len(p.AllowedLanguages) == 0 {
		p.AllowedLanguages = []string{"sql"}
	}
	if p.TimeLimitMs <= 0 {
		p.TimeLimitMs = DefaultProblemTimeLimitMs
	}
	if p.MemoryLimitKB <= 0 {
		p.MemoryLimitKB = DefaultProblemMemoryLimitKB
	}
	if p.Signature != nil {
		p.Signature.Normalize()
		if p.Kind == "" {
			p.Kind = ProblemKindFunction
		}
	}
	p.Guidelines = strings.TrimSpace(p.Guidelines)
	p.SQLSchema = strings.TrimSpace(p.SQLSchema)
	p.SQLSeed = strings.TrimSpace(p.SQLSeed)
	for i := range p.References {
		p.References[i].Language = NormalizeLanguageID(p.References[i].Language)
	}
	for i := range p.TestCases {
		tc := &p.TestCases[i]
		tc.Visibility = strings.ToLower(strings.TrimSpace(tc.Visibility))
		if tc.Visibility == "" {
			tc.Visibility = VisibilityPublic
		}
		tc.Name = strings.TrimSpace(tc.Name)
		tc.Class = strings.ToLower(strings.TrimSpace(tc.Class))
		if tc.Class == "" {
			// A case the author did not class is a sample when the candidate
			// can see it and part of the body of the problem otherwise.
			tc.Class = CaseClassCore
			if tc.Visibility == VisibilityPublic {
				tc.Class = CaseClassSample
			}
		}
		if tc.Weight == nil {
			one := 1.0
			tc.Weight = &one
		}
		if p.Kind == ProblemKindFunction && p.Signature != nil {
			p.normalizeFunctionCase(tc)
		}
	}
}

// normalizeFunctionCase renders a function case's typed arguments and
// result into the stored text: Input is the canonical JSON array of
// arguments, Expected the canonical JSON of the result. A case written with
// Input and Expected already in that shape is left alone; one whose values
// do not fit the signature keeps what it was given so Validate can say why.
//
// Each half is decoded exactly once, here, and the verdict kept on the
// case: a perf case carries a hundred thousand values, and decoding it
// again to validate it would cost as much as reading it did.
func (p ImportProblem) normalizeFunctionCase(tc *ImportTestCase) {
	verdict := &typedCase{}
	if len(tc.Args) > 0 {
		args, err := p.Signature.ArgsOf(tc.Args)
		if err == nil {
			tc.Input = wire.CanonicalJSON(args)
		} else {
			tc.Input = string(tc.Args)
		}
		verdict.args = err
	} else {
		_, verdict.args = p.Signature.ArgsOf(json.RawMessage(tc.Input))
	}
	if len(tc.Returns) > 0 {
		v, err := wire.DecodeTyped(p.Signature.Returns, tc.Returns)
		if err == nil {
			tc.Expected = wire.CanonicalJSON(v)
		} else {
			tc.Expected = string(tc.Returns)
		}
		verdict.returns = err
	} else {
		_, verdict.returns = wire.DecodeTyped(p.Signature.Returns, json.RawMessage(tc.Expected))
	}
	tc.Args, tc.Returns = nil, nil
	tc.typed = verdict
}

// typedVerdict is the case's fit against the signature: what Normalize
// recorded, or a decode now for a case that was built after it ran.
func (p ImportProblem) typedVerdict(tc ImportTestCase) typedCase {
	if tc.typed != nil {
		return *tc.typed
	}
	var v typedCase
	_, v.args = p.Signature.ArgsOf(json.RawMessage(tc.Input))
	_, v.returns = wire.DecodeTyped(p.Signature.Returns, json.RawMessage(tc.Expected))
	return v
}

// Validate returns every reason the problem cannot be stored, in reading
// order, so an import report tells the author about all of them at once.
// Normalize must have run first.
func (p ImportProblem) Validate() []string {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	switch p.Kind {
	case ProblemKindFunction, ProblemKindCode, ProblemKindSQL:
	case "":
		add("kind is required (%s)", strings.Join(ProblemKinds, ", "))
	default:
		add("unknown kind %q", p.Kind)
	}
	if p.Title == "" {
		add("title is required")
	}
	if p.Statement == "" {
		add("statement is required")
	}
	switch {
	case p.Kind == ProblemKindFunction && p.Signature == nil:
		add("a function problem needs a signature: the function's name, its parameters, and what it returns")
	case p.Kind == ProblemKindFunction:
		errs = append(errs, ValidateSignature(*p.Signature)...)
	case p.Signature != nil:
		add("a signature belongs to a function problem")
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
		if p.Kind != ProblemKindSQL && lang == "sql" {
			add("sql is not a language for a %s problem", p.Kind)
		}
		if p.Kind == ProblemKindFunction && p.Signature != nil && lang != "sql" && !wire.Supports(lang, *p.Signature) {
			add("%s cannot express this signature: it has no way to return a %s", lang, p.Signature.Returns)
		}
	}

	errs = append(errs, p.validateReferences()...)
	errs = append(errs, p.validateTestCases()...)

	switch p.Kind {
	case ProblemKindSQL:
		if p.SQLSchema == "" {
			add("a sql problem needs sql_schema to seed its database")
		}
	case ProblemKindCode, ProblemKindFunction:
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
		if !contains(TestCaseClasses, tc.Class) {
			errs = append(errs, fmt.Sprintf("test_cases[%d] has unknown class %q (want %s)", i, tc.Class, strings.Join(TestCaseClasses, ", ")))
		}
		if len(tc.Name) > MaxCaseNameLength {
			errs = append(errs, fmt.Sprintf("test_cases[%d] has a name longer than %d characters", i, MaxCaseNameLength))
		}
		if tc.WeightValue() <= 0 {
			errs = append(errs, fmt.Sprintf("test_cases[%d] has weight %v; a weight must be positive", i, tc.WeightValue()))
		}
		if p.Kind == ProblemKindFunction && p.Signature != nil {
			verdict := p.typedVerdict(tc)
			if verdict.args != nil {
				errs = append(errs, fmt.Sprintf("test_cases[%d] args: %v", i, verdict.args))
			}
			if verdict.returns != nil {
				errs = append(errs, fmt.Sprintf("test_cases[%d] returns: %v", i, verdict.returns))
			}
		}
	}
	if public == 0 {
		errs = append(errs, "at least one test case must be public so the candidate sees an example")
	}
	return errs
}

// mapList applies f to every entry, leaving de-duplication to normalizeList.
func mapList(in []string, f func(string) string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
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

// ValidateDraft is what a half-finished problem is held to. An author moving
// between wizard steps has not written the cases or the solutions yet, so
// only the fields that are already typed are checked; a draft is never
// runnable and never attachable, so nothing downstream depends on the rest.
// Normalize must have run first.
func (p ImportProblem) ValidateDraft() []string {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	switch p.Kind {
	case ProblemKindFunction, ProblemKindCode, ProblemKindSQL:
	case "":
		add("kind is required (%s)", strings.Join(ProblemKinds, ", "))
	default:
		add("unknown kind %q", p.Kind)
	}
	if p.Title == "" {
		add("title is required")
	}
	if p.Signature != nil {
		errs = append(errs, ValidateSignature(*p.Signature)...)
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
	for _, lang := range p.AllowedLanguages {
		if !contains(ProblemLanguages, lang) {
			add("unknown language %q in allowed_languages (want %s)", lang, strings.Join(ProblemLanguages, ", "))
		}
	}
	for i, tc := range p.TestCases {
		if !contains(TestCaseClasses, tc.Class) {
			add("test_cases[%d] has unknown class %q (want %s)", i, tc.Class, strings.Join(TestCaseClasses, ", "))
		}
	}
	return errs
}

// Quality is the problem as the quality review scores it, given the languages
// a reference solution has been proven in.
func (p ImportProblem) Quality(proven []string) QualityInput {
	return QualityInput{Statement: p.Statement, Tags: p.Tags, TestCases: p.TestCases, ProvenLanguages: proven}
}

// Stub is the empty function a candidate starts from for a function
// problem, in one language; empty for the other kinds.
func (p ImportProblem) Stub(lang string) string {
	if p.Kind != ProblemKindFunction || p.Signature == nil {
		return ""
	}
	return wire.Stub(lang, *p.Signature)
}
