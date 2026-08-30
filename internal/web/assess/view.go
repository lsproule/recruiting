package assess

import "strings"

// jsonForScript makes JSON safe to inline inside a <script> element: a
// "</script>" in a statement or source would otherwise end the element.
func jsonForScript(s string) string {
	r := strings.NewReplacer("</", `<\/`, "<!--", `<\!--`, " ", ` `, " ", ` `)
	return r.Replace(s)
}
