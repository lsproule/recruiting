package domain_test

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"recruiting/internal/domain"
)

const goodCode = `[{
  "kind": "code",
  "title": "Sum two numbers",
  "statement": "Read two integers and print their sum.",
  "difficulty": "easy",
  "tags": ["math"],
  "allowed_languages": ["python"],
  "reference_solutions": [{"language": "python", "source": "print(sum(map(int, input().split())))"}],
  "test_cases": [{"input": "1 2", "expected": "3", "visibility": "public", "weight": 1}]
}]`

func parseOne(t *testing.T, doc string) domain.ImportProblem {
	t.Helper()
	got, err := domain.ParseProblemImport([]byte(doc))
	if err != nil {
		t.Fatalf("ParseProblemImport: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d problems, want 1", len(got))
	}
	return got[0]
}

func TestParseProblemImportAppliesDefaults(t *testing.T) {
	p := parseOne(t, goodCode)
	if p.TimeLimitMs != domain.DefaultProblemTimeLimitMs {
		t.Errorf("time limit = %d, want %d", p.TimeLimitMs, domain.DefaultProblemTimeLimitMs)
	}
	if p.MemoryLimitKB != domain.DefaultProblemMemoryLimitKB {
		t.Errorf("memory limit = %d, want %d", p.MemoryLimitKB, domain.DefaultProblemMemoryLimitKB)
	}
	if p.TestCases[0].WeightValue() != 1 {
		t.Errorf("weight = %v, want 1", p.TestCases[0].WeightValue())
	}
}

func TestParseProblemImportRejectsUnknownLanguage(t *testing.T) {
	doc := strings.ReplaceAll(goodCode, `"python"`, `"cobol"`)
	_, err := domain.ParseProblemImport([]byte(doc))
	var errs domain.ProblemImportErrors
	if !errors.As(err, &errs) {
		t.Fatalf("err = %v, want ProblemImportErrors", err)
	}
	if len(errs) != 1 || errs[0].Index != 0 {
		t.Fatalf("errs = %+v, want one error on problem 0", errs)
	}
	if !strings.Contains(strings.Join(errs[0].Errors, "; "), "cobol") {
		t.Errorf("errors %q do not name the unknown language", errs[0].Errors)
	}
}

func TestParseProblemImportRequiresAReferenceSolution(t *testing.T) {
	doc := strings.ReplaceAll(goodCode, `"reference_solutions"`, `"unused_solutions"`)
	_, err := domain.ParseProblemImport([]byte(doc))
	if err == nil {
		t.Fatal("a problem with no reference solution was accepted")
	}
}

func TestParseProblemImportRequiresSchemaForSQLProblems(t *testing.T) {
	doc := `[{
	  "kind": "sql",
	  "title": "Count rows",
	  "statement": "Count them.",
	  "allowed_languages": ["sql"],
	  "reference_solutions": [{"language": "sql", "source": "select count(*) from t"}],
	  "test_cases": [{"expected": "0", "visibility": "public"}]
	}]`
	_, err := domain.ParseProblemImport([]byte(doc))
	if err == nil {
		t.Fatal("a SQL problem without a schema was accepted")
	}
	doc = strings.Replace(doc, `"statement": "Count them.",`,
		`"statement": "Count them.", "sql_schema": "create table t (id int);",`, 1)
	if _, err := domain.ParseProblemImport([]byte(doc)); err != nil {
		t.Fatalf("SQL problem with a schema rejected: %v", err)
	}
}

func TestParseProblemImportRejectsReferenceOutsideAllowedLanguages(t *testing.T) {
	doc := strings.Replace(goodCode, `"allowed_languages": ["python"]`, `"allowed_languages": ["node"]`, 1)
	_, err := domain.ParseProblemImport([]byte(doc))
	if err == nil {
		t.Fatal("a reference solution in a language the problem disallows was accepted")
	}
}

func TestParseProblemImportRequiresAPublicTestCase(t *testing.T) {
	doc := strings.Replace(goodCode, `"visibility": "public"`, `"visibility": "hidden"`, 1)
	_, err := domain.ParseProblemImport([]byte(doc))
	if err == nil {
		t.Fatal("a problem with no public test case was accepted")
	}
}

func TestParseProblemImportReportsEveryBadProblem(t *testing.T) {
	doc := `[
	  {"kind": "code", "title": "", "statement": "s", "allowed_languages": ["python"],
	   "reference_solutions": [{"language": "python", "source": "x"}],
	   "test_cases": [{"expected": "1", "visibility": "public"}]},
	  {"kind": "banana", "title": "Second", "statement": "s", "allowed_languages": ["python"],
	   "reference_solutions": [{"language": "python", "source": "x"}],
	   "test_cases": [{"expected": "1", "visibility": "public"}]}
	]`
	_, err := domain.ParseProblemImport([]byte(doc))
	var errs domain.ProblemImportErrors
	if !errors.As(err, &errs) {
		t.Fatalf("err = %v, want ProblemImportErrors", err)
	}
	if len(errs) != 2 {
		t.Fatalf("got %d per-problem errors, want 2: %+v", len(errs), errs)
	}
	if errs[1].Title != "Second" {
		t.Errorf("second error title = %q, want %q", errs[1].Title, "Second")
	}
}

func TestParseProblemImportRejectsEmptyBatch(t *testing.T) {
	if _, err := domain.ParseProblemImport([]byte(`[]`)); err == nil {
		t.Fatal("an empty batch was accepted")
	}
}

func TestParseProblemImportRejectsDuplicateTitles(t *testing.T) {
	doc := "[" + strings.TrimSuffix(strings.TrimPrefix(goodCode, "["), "]") + "," +
		strings.TrimSuffix(strings.TrimPrefix(goodCode, "["), "]") + "]"
	if _, err := domain.ParseProblemImport([]byte(doc)); err == nil {
		t.Fatal("a batch with two problems of the same title was accepted")
	}
}

// A document that never parses has no problem to blame, so the report names
// the document itself — the caller shows it like any other refusal rather
// than turning it into a server error.
func TestParseProblemImportReportsDocumentFaults(t *testing.T) {
	for _, tc := range []struct {
		name, doc, want string
	}{
		{"unknown field", strings.Replace(goodCode, `"kind":`, `"knid":`, 1), "knid"},
		{"malformed json", `[{"kind": "code",]`, "not a valid problem batch"},
		{"not an array", `{"kind": "code"}`, "not a valid problem batch"},
		{"trailing content", goodCode + " []", "more than one JSON value"},
		{"empty array", `[]`, "contains no problems"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.ParseProblemImport([]byte(tc.doc))
			var errs domain.ProblemImportErrors
			if !errors.As(err, &errs) {
				t.Fatalf("err = %#v, want ProblemImportErrors so the caller can report it", err)
			}
			if len(errs) != 1 || errs[0].Index != domain.DocumentIndex {
				t.Fatalf("errs = %+v, want one document-level error", errs)
			}
			if errs[0].Label() != "the document" {
				t.Errorf("label = %q, want %q", errs[0].Label(), "the document")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestParseProblemImportCapsBatchSize(t *testing.T) {
	one := strings.TrimSuffix(strings.TrimPrefix(goodCode, "["), "]")
	parts := make([]string, 0, domain.MaxImportProblems+1)
	for i := range domain.MaxImportProblems + 1 {
		parts = append(parts, strings.Replace(one, `"Sum two numbers"`, `"Problem `+strconv.Itoa(i)+`"`, 1))
	}
	_, err := domain.ParseProblemImport([]byte("[" + strings.Join(parts, ",") + "]"))
	var errs domain.ProblemImportErrors
	if !errors.As(err, &errs) || len(errs) != 1 || errs[0].Index != domain.DocumentIndex {
		t.Fatalf("err = %v, want one document-level error", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(domain.MaxImportProblems)) {
		t.Errorf("error %q does not name the cap", err)
	}
}

// Fifteen languages make one reference per allowed language unauthorable, so
// a problem only has to be proven solvable once.
func TestParseProblemImportAcceptsOneReferenceForManyAllowedLanguages(t *testing.T) {
	doc := strings.Replace(goodCode, `"allowed_languages": ["python"]`, `"allowed_languages": ["python", "go"]`, 1)
	if _, err := domain.ParseProblemImport([]byte(doc)); err != nil {
		t.Fatalf("a problem proven in one of its languages was rejected: %v", err)
	}
}

func TestParseProblemImportNormalizesTheNodeLanguageID(t *testing.T) {
	doc := strings.ReplaceAll(goodCode, `"python"`, `"node"`)
	doc = strings.Replace(doc, "print(sum(map(int, input().split())))", "x", 1)
	p := parseOne(t, doc)
	if len(p.AllowedLanguages) != 1 || p.AllowedLanguages[0] != "javascript" {
		t.Errorf("allowed_languages = %v, want [javascript]", p.AllowedLanguages)
	}
	if p.References[0].Language != "javascript" {
		t.Errorf("reference language = %q, want javascript", p.References[0].Language)
	}
}

func TestParseProblemImportExpandsAnyLanguage(t *testing.T) {
	doc := strings.Replace(goodCode, `"allowed_languages": ["python"]`, `"allowed_languages": ["any"]`, 1)
	p := parseOne(t, doc)
	if len(p.AllowedLanguages) != len(domain.CodeLanguageIDs()) {
		t.Fatalf("allowed_languages = %v, want the %d code languages", p.AllowedLanguages, len(domain.CodeLanguageIDs()))
	}
	for _, id := range domain.CodeLanguageIDs() {
		if !slices.Contains(p.AllowedLanguages, id) {
			t.Errorf("allowed_languages %v is missing %q", p.AllowedLanguages, id)
		}
	}

	sqlDoc := `[{
	  "kind": "sql",
	  "title": "Count rows",
	  "statement": "Count them.",
	  "sql_schema": "create table t (id int);",
	  "allowed_languages": ["any"],
	  "reference_solutions": [{"language": "sql", "source": "select count(*) from t"}],
	  "test_cases": [{"expected": "0", "visibility": "public"}]
	}]`
	sp := parseOne(t, sqlDoc)
	if !slices.Equal(sp.AllowedLanguages, []string{"sql"}) {
		t.Errorf("sql allowed_languages = %v, want [sql]", sp.AllowedLanguages)
	}
}

func TestParseProblemImportAcceptsEveryRegisteredLanguage(t *testing.T) {
	for _, id := range domain.CodeLanguageIDs() {
		doc := strings.ReplaceAll(goodCode, `"python"`, `"`+id+`"`)
		if _, err := domain.ParseProblemImport([]byte(doc)); err != nil {
			t.Errorf("%s rejected: %v", id, err)
		}
	}
}

func TestParseProblemImportRejectsANonPositiveWeight(t *testing.T) {
	for _, weight := range []string{"0", "-1"} {
		doc := strings.Replace(goodCode, `"weight": 1`, `"weight": `+weight, 1)
		if _, err := domain.ParseProblemImport([]byte(doc)); err == nil {
			t.Errorf("weight %s was accepted", weight)
		}
	}
}

// An empty expected output is a real answer: a query that returns no rows, or
// a program that prints nothing.
func TestParseProblemImportAcceptsEmptyExpectedOutput(t *testing.T) {
	doc := strings.Replace(goodCode, `"expected": "3"`, `"expected": ""`, 1)
	if _, err := domain.ParseProblemImport([]byte(doc)); err != nil {
		t.Fatalf("an empty expected output was rejected: %v", err)
	}
}

// functionProblem is a function problem with one typed case, the way the
// import document and the API both write it.
func functionProblem() domain.ImportProblem {
	return domain.ImportProblem{
		Kind: "function", Title: "Add", Statement: "Add them.", AllowedLanguages: []string{"python"},
		Signature:  &domain.Signature{Name: "add", Params: []domain.Param{{Name: "a", Type: "int"}, {Name: "b", Type: "int"}}, Returns: "int"},
		References: []domain.ImportReference{{Language: "python", Source: "def add(a, b):\n    return a + b\n"}},
		TestCases:  []domain.ImportTestCase{{Args: []byte(`[1, 2]`), Returns: []byte(`3`), Visibility: "public"}},
	}
}

// A function case is decoded against the signature once, by Normalize, and
// Validate reads that verdict rather than decoding the payload again: after
// Normalize, the stored text can be changed under Validate's feet without
// changing its answer.
func TestNormalizeDecodesAFunctionCaseOnceAndValidateReusesTheVerdict(t *testing.T) {
	p := functionProblem()
	p.Normalize()
	if p.TestCases[0].Input != "[1,2]" || p.TestCases[0].Expected != "3" {
		t.Fatalf("canonical case = %q / %q", p.TestCases[0].Input, p.TestCases[0].Expected)
	}
	if errs := p.Validate(); len(errs) > 0 {
		t.Fatalf("a good case was refused: %v", errs)
	}
	p.TestCases[0].Input, p.TestCases[0].Expected = "not json", "not json"
	if errs := p.Validate(); len(errs) > 0 {
		t.Errorf("Validate decoded the payload again: %v", errs)
	}

	bad := functionProblem()
	bad.TestCases[0].Args = []byte(`["one", 2]`)
	bad.Normalize()
	errs := bad.Validate()
	if len(errs) != 1 || !strings.Contains(errs[0], "test_cases[0] args") {
		t.Errorf("a mistyped argument was not reported once from Normalize's verdict: %v", errs)
	}

	// A case built after Normalize ran — the form's rows, say — is still
	// checked, from its stored text.
	late := functionProblem()
	late.Normalize()
	late.TestCases = append(late.TestCases, domain.ImportTestCase{Input: `[1]`, Expected: `"x"`, Visibility: "hidden", Class: "core"})
	errs = late.Validate()
	if len(errs) != 2 || !strings.Contains(errs[0], "test_cases[1] args") || !strings.Contains(errs[1], "test_cases[1] returns") {
		t.Errorf("a case added after Normalize was not decoded: %v", errs)
	}
}
