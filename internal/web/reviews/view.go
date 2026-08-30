package reviews

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"recruiting/internal/service"
)

// configScript is an island's boot JSON as a whole script element. templ
// treats the content of a script element as raw text, so the element is
// written here rather than interpolated inside one in the template.
func configScript(id, body string) templ.Component {
	return templ.Raw(`<script id="` + id + `" type="application/json">` + jsonForScript(body) + `</script>`)
}

// jsonForScript makes a JSON document safe to inline in a script element.
// Escaping every "<" is what makes that true whatever follows it — a closing
// tag, a comment, anything a future parser reads specially — and the escape
// is JSON's own, so the document still parses. The line separators are
// escaped because they end a statement in JavaScript but not in JSON.
func jsonForScript(s string) string {
	r := strings.NewReplacer("<", `\u003c`, "\u2028", `\u2028`, "\u2029", `\u2029`)
	return r.Replace(s)
}

// num renders a plain number without trailing zeroes.
func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// filed reports whether a verdict is already stored, as against one the form
// is carrying back after a refusal.
func filed(r *service.Review) bool { return r != nil && r.ID != uuid.Nil }

// verdictLabels read a verdict back to a human.
var verdictLabels = map[string]string{
	service.VerdictPass:       "Pass",
	service.VerdictBorderline: "Borderline",
	service.VerdictFail:       "Fail",
}

func verdictLabel(v string) string {
	if label, ok := verdictLabels[v]; ok {
		return label
	}
	return "Not reviewed"
}

// signalLabels name the integrity signals in the reviewer's words.
var signalLabels = map[string]string{
	"paste_ratio":          "Pasted text",
	"paste_then_pass":      "Paste then pass",
	"burst_typing":         "Burst typing",
	"edit_ratio":           "Edit ratio",
	"blur_then_solution":   "Left the page, then solved",
	"speed_vs_difficulty":  "Speed against difficulty",
	"reference_similarity": "Similar to known code",
}

func signalLabel(name string) string {
	if label, ok := signalLabels[name]; ok {
		return label
	}
	return name
}

// percent renders a 0–1 signal value as whole percent.
func percent(v float64) string { return strconv.Itoa(int(v*100+0.5)) + "%" }

// score renders a stored score, or a dash when there is none.
func score(v *float64) string {
	if v == nil {
		return "—"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

// shareOf renders a per-problem score, 0–1, as whole percent.
func shareOf(s service.ProblemScore) string { return percent(s.Score) }

func itoa(n int) string { return strconv.Itoa(n) }

func i64toa(n int64) string { return strconv.FormatInt(n, 10) }

func at(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2 Jan 2006 15:04 UTC")
}

// verdictOf is the verdict already filed, for the form's selection.
func verdictOf(r *service.Review) string {
	if r == nil {
		return ""
	}
	return r.Verdict
}

func notesOf(r *service.Review) string {
	if r == nil {
		return ""
	}
	return r.Notes
}

// evidenceValues renders a signal's numbers in a stable order, so two
// readings of the same evidence read the same.
func evidenceValues(values map[string]float64) string {
	if len(values) == 0 {
		return ""
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+" "+strconv.FormatFloat(values[name], 'f', -1, 64))
	}
	return strings.Join(parts, ", ")
}
