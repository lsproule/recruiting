package problems

import (
	"encoding/json"
	"strings"

	"github.com/a-h/templ"

	"recruiting/internal/service"
	"recruiting/internal/web/markdown"
)

// configID is the element the editor island reads its boot JSON from; it is
// the same id the candidate session uses, because it is the same island.
const configID = "assess-config"

// tryAPIBase is where the island posts in try mode: the JSON API, which is
// where the try endpoint lives. There is no attempt to scope the call to.
const tryAPIBase = "/api/v1"

// tryConfig is the island's boot config in try mode. The attempt fields are
// the shape the island expects; in try mode it reads none of them, because
// there is no attempt, no timer, and no recorder.
type tryConfig struct {
	Mode      string       `json:"mode"`
	AttemptID string       `json:"attempt_id"`
	APIBase   string       `json:"api_base"`
	BeaconURL string       `json:"beacon_url"`
	CSRF      string       `json:"csrf"`
	Status    string       `json:"status"`
	ExpiresAt int64        `json:"expires_at"`
	Problems  []tryProblem `json:"problems"`
}

type tryProblem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	Statement string `json:"statement"`
	// StatementHTML is the statement rendered from Markdown here rather than
	// in the browser: the island has no parser, and this way every surface
	// shows the same document.
	StatementHTML string   `json:"statement_html"`
	Languages     []string `json:"languages"`
	Language      string   `json:"language"`
	Source        string   `json:"source"`
	SQLSchema     string   `json:"sql_schema"`
	// Signature is a function problem's entrypoint as a person reads it, and
	// Stubs the starting source per language; both empty on the other kinds.
	Signature   string            `json:"signature,omitempty"`
	Stubs       map[string]string `json:"stubs,omitempty"`
	PublicTests []tryTest         `json:"public_tests"`
}

type tryTest struct {
	Input    string `json:"input"`
	Expected string `json:"expected"`
}

// tryIslandConfig is the boot JSON for one problem's Try it screen. Only the
// public cases travel: the author stands where the candidate stands, so a
// hidden case's expected output is not on the page.
func tryIslandConfig(p service.Problem, csrf string) (string, error) {
	tp := tryProblem{
		ID: p.ID.String(), Title: p.Title, Kind: p.Kind, Statement: p.Statement,
		StatementHTML: markdown.ToHTML(p.Statement),
		Languages:     p.AllowedLanguages, SQLSchema: p.SQLSchema,
		Stubs:       p.Stubs(),
		PublicTests: []tryTest{},
	}
	if p.Signature != nil {
		tp.Signature = p.Signature.Describe()
	}
	if len(tp.Languages) > 0 {
		tp.Language = tp.Languages[0]
		tp.Source = tp.Stubs[tp.Language]
	}
	for _, c := range p.TestCases {
		if c.Visibility == "public" {
			tp.PublicTests = append(tp.PublicTests, tryTest{Input: c.Input, Expected: c.Expected})
		}
	}
	cfg := tryConfig{
		Mode: "try", APIBase: tryAPIBase, CSRF: csrf, Status: "started",
		Problems: []tryProblem{tp},
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// configScript is the island's boot JSON as a whole script element. templ
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
	r := strings.NewReplacer("<", "\\u003c", "\u2028", "\\u2028", "\u2029", "\\u2029")
	return r.Replace(s)
}

// stepVals is the htmx payload that moves the wizard to one step.
func stepVals(step int) string {
	b, _ := json.Marshal(map[string]string{"goto": itoa(step)})
	return string(b)
}
