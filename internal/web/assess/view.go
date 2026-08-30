package assess

import (
	"strings"

	"github.com/a-h/templ"
)

// configID is the element the island reads its boot JSON from.
const configID = "assess-config"

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
	r := strings.NewReplacer("<", `<`, " ", ` `, " ", ` `)
	return r.Replace(s)
}
