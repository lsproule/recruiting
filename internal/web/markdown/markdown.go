// Package markdown turns a recruiter-authored Markdown statement into the HTML
// a page can show. Rendering happens here, in Go, so every surface that shows a
// statement — the recruiter's problem page, the candidate's session, the
// author's Try it screen — reads the same document the same way, and no page
// has to ship a Markdown parser to the browser.
package markdown

import (
	"bytes"
	"sync"

	"github.com/a-h/templ"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// md is configured once and is safe for concurrent use. Raw HTML passthrough
// is left off — goldmark's default — so a statement can only produce the
// elements Markdown itself names. Statements come from recruiters rather than
// from the public, but a problem bank is imported and cloned across orgs, and
// the cheapest place to be sure is the renderer.
var md = sync.OnceValue(func() goldmark.Markdown {
	return goldmark.New(goldmark.WithExtensions(extension.GFM))
})

// ToHTML renders one statement. A document that will not parse is impossible —
// any byte string is valid Markdown — so a render error can only be the writer
// failing, and the empty string is the honest answer for that.
func ToHTML(src string) string {
	if src == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := md().Convert([]byte(src), &buf); err != nil {
		return ""
	}
	return buf.String()
}

// Render is ToHTML as a component, for templates. The HTML is the renderer's
// own output, never the author's markup, which is what makes it safe to write
// unescaped.
func Render(src string) templ.Component {
	return templ.Raw(ToHTML(src))
}
