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

// Wizard steps, in the order the authoring screen walks them.
const (
	stepDetails = 1
	stepLangs   = 2
	stepCases   = 3
	stepRefs    = 4
	lastStep    = stepRefs
)

// stepLabels names each step for the step nav.
var stepLabels = map[int]string{
	stepDetails: "Details",
	stepLangs:   "Languages",
	stepCases:   "Test cases",
	stepRefs:    "Reference solutions",
}

// problemForm is what the authoring wizard renders and posts. It mirrors the
// import shape so a hand-written problem and an imported one are held to the
// same rules. Every step's fields are on the page at once — only the active
// one is shown — so one post always carries the whole problem.
type problemForm struct {
	ID   uuid.UUID
	New  bool
	Step int

	Kind               string
	Title              string
	Statement          string
	Difficulty         string
	Tags               string
	AllowedLanguages   string
	RecommendedMinutes int
	Guidelines         string
	TimeLimitMs        int
	MemoryLimitKB      int
	SQLSchema          string
	SQLSeed            string
	References         []referenceRow
	TestCases          []testCaseRow

	// Proven are the languages the last verified save proved, so step 2 can
	// mark them without re-running the runner.
	Proven []string

	// Return names the screen that sent the author here, so a problem written
	// mid-flow lands back where it is wanted. Empty stays on the bank.
	Return string
}

// returnTargets are the screens a problem may be authored on behalf of. The
// list is closed so a crafted "return" cannot redirect anywhere it likes.
var returnTargets = map[string]string{"intake": "/app/clients/new"}

// returnPath is where a saved problem sends the author, with the new problem
// named so the screen that asked for it can pick it up.
func (f problemForm) returnPath(id uuid.UUID) string {
	target, ok := returnTargets[f.Return]
	if !ok {
		return Prefix + "/" + id.String()
	}
	return target + "?problem=" + id.String()
}

type referenceRow struct {
	Language string
	Source   string
}

type testCaseRow struct {
	Name       string
	Class      string
	Input      string
	Expected   string
	Visibility string
	Weight     string
	Unordered  bool
}

// action is where the form posts a save: a new problem to the collection, an
// edit to the problem itself. A draft that has already been stored posts to
// its own id even on the "new" path, so Next never creates a second row.
func (f problemForm) action() string {
	if f.ID == uuid.Nil {
		return Prefix + "/"
	}
	return Prefix + "/" + f.ID.String()
}

// steps is the wizard's step nav.
func (f problemForm) steps() []wizardStep {
	out := make([]wizardStep, 0, lastStep)
	for i := 1; i <= lastStep; i++ {
		out = append(out, wizardStep{Number: i, Label: stepLabels[i], Active: i == f.Step, Done: i < f.Step})
	}
	return out
}

type wizardStep struct {
	Number int
	Label  string
	Active bool
	Done   bool
}

// nextStep and prevStep are where the wizard's two navigation buttons go;
// zero means the button does not apply on this step.
func (f problemForm) nextStep() int {
	if f.Step >= lastStep {
		return 0
	}
	return f.Step + 1
}

func (f problemForm) prevStep() int {
	if f.Step <= stepDetails {
		return 0
	}
	return f.Step - 1
}

// languages are the checkboxes step 2 offers: the registry narrowed to the
// problem's kind, each marked as chosen and as already proven.
func (f problemForm) languages() []languageChoice {
	chosen := map[string]bool{}
	for _, id := range splitList(f.AllowedLanguages) {
		chosen[id] = true
	}
	proven := map[string]bool{}
	for _, id := range f.Proven {
		proven[id] = true
	}
	out := make([]languageChoice, 0, len(domain.Languages)+1)
	out = append(out, languageChoice{ID: domain.LanguageAny, Label: "Any", Chosen: chosen[domain.LanguageAny]})
	for _, l := range domain.Languages {
		if l.Kind != f.Kind {
			continue
		}
		out = append(out, languageChoice{ID: l.ID, Label: l.Label, Chosen: chosen[l.ID], Proven: proven[l.ID]})
	}
	return out
}

type languageChoice struct {
	ID     string
	Label  string
	Chosen bool
	Proven bool
}

// quality is the review panel's verdict for what is currently typed. A draft
// has proven nothing, so the panel shows the languages the last verified save
// proved rather than the ones merely chosen.
func (f problemForm) quality() []domain.QualityCheck {
	return domain.QualityChecks(f.qualityInput())
}

func (f problemForm) qualityScore() int { return domain.ProblemQuality(f.qualityInput()) }

func (f problemForm) qualityInput() domain.QualityInput {
	in := f.asImport()
	in.Normalize()
	return in.Quality(f.Proven)
}

// totalPoints is the weight of every case together, which is what a candidate
// is scored out of.
func (f problemForm) totalPoints() string {
	total := 0.0
	for _, tc := range f.asImport().TestCases {
		total += tc.WeightValue()
	}
	return weightText(total)
}

func (f problemForm) asImport() domain.ImportProblem {
	out := domain.ImportProblem{
		Kind: f.Kind, Title: f.Title, Statement: f.Statement, Difficulty: f.Difficulty,
		Tags: splitList(f.Tags), AllowedLanguages: splitList(f.AllowedLanguages),
		TimeLimitMs: f.TimeLimitMs, MemoryLimitKB: f.MemoryLimitKB,
		RecommendedMinutes: f.RecommendedMinutes, Guidelines: f.Guidelines,
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
			Name: tc.Name, Class: tc.Class,
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

// newForm is a blank wizard on step 1.
func newForm() problemForm {
	return problemForm{
		New: true, Step: stepDetails, Difficulty: "medium", Kind: domain.ProblemKindCode,
		RecommendedMinutes: domain.DefaultRecommendedMinutes,
		TimeLimitMs:        domain.DefaultProblemTimeLimitMs,
		MemoryLimitKB:      domain.DefaultProblemMemoryLimitKB,
		References:         make([]referenceRow, formRows),
		TestCases:          make([]testCaseRow, formRows),
	}
}

// formOf fills the wizard from a stored problem, leaving blank rows to add
// another reference solution or test case.
func formOf(p service.Problem) problemForm {
	f := problemForm{
		ID: p.ID, Step: stepDetails, Kind: p.Kind, Title: p.Title, Statement: p.Statement,
		Difficulty: p.Difficulty,
		Tags:       strings.Join(p.Tags, ", "), AllowedLanguages: strings.Join(p.AllowedLanguages, ", "),
		RecommendedMinutes: p.RecommendedMinutes, Guidelines: p.Guidelines,
		TimeLimitMs: p.TimeLimitMs, MemoryLimitKB: p.MemoryLimitKB,
		SQLSchema: p.SQLSchema, SQLSeed: p.SQLSeed,
		Proven: p.ProvenLanguages,
	}
	for _, r := range p.References {
		f.References = append(f.References, referenceRow{Language: r.Language, Source: r.Source})
	}
	for _, c := range p.TestCases {
		f.TestCases = append(f.TestCases, testCaseRow{
			Name: c.Name, Class: c.Class,
			Input: c.Input, Expected: c.Expected, Visibility: c.Visibility,
			Weight: strconv.FormatFloat(c.Weight, 'f', -1, 64), Unordered: c.Unordered,
		})
	}
	f.References = append(f.References, make([]referenceRow, formRows)...)
	f.TestCases = append(f.TestCases, make([]testCaseRow, formRows)...)
	return f
}

// langChoiceField marks a post as coming from the wizard's language
// checkboxes, so an empty set is read as "none chosen" rather than as a form
// that never offered the choice.
const langChoiceField = "lang_choice"

// readForm reads the posted problem. Reference solutions and test cases are
// indexed rows: the form renders however many the problem has plus a couple of
// blanks, and empty rows are dropped rather than validated.
func readForm(r *http.Request) problemForm {
	f := problemForm{
		Kind:               strings.TrimSpace(r.PostFormValue("kind")),
		Title:              strings.TrimSpace(r.PostFormValue("title")),
		Statement:          r.PostFormValue("statement"),
		Difficulty:         strings.TrimSpace(r.PostFormValue("difficulty")),
		Tags:               r.PostFormValue("tags"),
		AllowedLanguages:   readLanguages(r),
		RecommendedMinutes: atoiOr(r.PostFormValue("recommended_minutes"), domain.DefaultRecommendedMinutes),
		Guidelines:         r.PostFormValue("guidelines"),
		TimeLimitMs:        atoiOr(r.PostFormValue("time_limit_ms"), domain.DefaultProblemTimeLimitMs),
		MemoryLimitKB:      atoiOr(r.PostFormValue("memory_limit_kb"), domain.DefaultProblemMemoryLimitKB),
		SQLSchema:          r.PostFormValue("sql_schema"),
		SQLSeed:            r.PostFormValue("sql_seed"),
		Step:               clampStep(atoiOr(r.PostFormValue("step"), stepDetails)),
		Return:             strings.TrimSpace(r.PostFormValue("return")),
	}
	if id, err := uuid.Parse(r.PostFormValue("id")); err == nil {
		f.ID = id
	}
	f.New = f.ID == uuid.Nil
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
			Name:       strings.TrimSpace(r.PostFormValue("tc_name" + suffix)),
			Class:      strings.TrimSpace(r.PostFormValue("tc_class" + suffix)),
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

// readLanguages takes the wizard's checkboxes when the post carries them and
// the plain comma-separated field otherwise, which is what the API-shaped
// form and older clients send.
func readLanguages(r *http.Request) string {
	if _, chose := r.PostForm[langChoiceField]; chose {
		return strings.Join(r.PostForm["lang"], ", ")
	}
	return r.PostFormValue("allowed_languages")
}

func clampStep(n int) int {
	switch {
	case n < stepDetails:
		return stepDetails
	case n > lastStep:
		return lastStep
	}
	return n
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

func tryPath(p service.Problem) string { return problemPath(p) + "/try" }

func clonePath(p service.Problem) string { return problemPath(p) + "/clone" }

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

// caseCount summarises a bank row's size: how many cases it scores on and how
// long it is meant to take.
func caseCount(p service.Problem) string {
	if p.RecommendedMinutes <= 0 {
		return itoa(p.CaseCount) + " cases"
	}
	return itoa(p.CaseCount) + " cases · " + itoa(p.RecommendedMinutes) + " min"
}

// qualityLabel is how the bank reports a problem's score, naming the one
// consequence a low score has.
func qualityLabel(p service.Problem) string {
	if p.Attachable() {
		return itoa(p.Quality)
	}
	return itoa(p.Quality) + " · not attachable"
}

// selected marks the option matching the current value.
func selected(current, value string) bool { return current == value }

// weightText renders a test case's weight without a trailing ".0".
func weightText(w float64) string { return strconv.FormatFloat(w, 'f', -1, 64) }

// languageList names the languages a problem may allow.
func languageList() string { return strings.Join(domain.ProblemLanguages, ", ") }

// languageLabel is a language id as a person reads it.
func languageLabel(id string) string {
	if l, ok := domain.LanguageByID(id); ok {
		return l.Label
	}
	return id
}

// reportPosition numbers a rejected problem in the report; a fault of the
// document itself belongs to no problem and is left unnumbered.
func reportPosition(p domain.ProblemImportError) string {
	if p.Index < 0 {
		return "—"
	}
	return strconv.Itoa(p.Index + 1)
}

// millis renders a case's run time.
func millis(ms int64) string { return strconv.FormatInt(ms, 10) + " ms" }

// verdictLabel says whether a reference solution solved the problem.
func verdictLabel(v service.ReferenceVerdict) string {
	switch {
	case v.Err != "":
		return "the runner could not be reached"
	case v.Passed():
		return "solves every case"
	default:
		return "does not solve the problem"
	}
}
