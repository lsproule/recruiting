package markdown_test

import (
	"strings"
	"testing"

	"recruiting/internal/web/markdown"
)

func TestToHTMLRendersTheElementsStatementsUse(t *testing.T) {
	src := "# Two Sum\n\n**Input**: an array.\n\n" +
		"Return the *indices*, not the `values`.\n\n" +
		"- one\n- two\n\n```python\nreturn 1\n```\n"
	got := markdown.ToHTML(src)
	for _, want := range []string{
		"<h1>Two Sum</h1>",
		"<strong>Input</strong>",
		"<em>indices</em>",
		"<code>values</code>",
		"<li>one</li>",
		"<pre><code class=\"language-python\">",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered statement is missing %q:\n%s", want, got)
		}
	}
}

func TestToHTMLDropsRawHTML(t *testing.T) {
	got := markdown.ToHTML("<script>alert(1)</script>\n\nfine <b onclick=\"x\">here</b>\n")
	if strings.Contains(got, "<script") || strings.Contains(got, "onclick") {
		t.Fatalf("raw HTML survived the render:\n%s", got)
	}
}

func TestToHTMLEmpty(t *testing.T) {
	if got := markdown.ToHTML(""); got != "" {
		t.Fatalf("empty statement rendered %q", got)
	}
}
