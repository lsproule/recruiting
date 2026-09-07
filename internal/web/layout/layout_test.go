package layout_test

import (
	"context"
	"strings"
	"testing"

	"recruiting/internal/web/layout"
)

func render(t *testing.T, p layout.Page) string {
	t.Helper()
	var b strings.Builder
	if err := layout.Base(p).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func appPage() layout.Page {
	return layout.Page{
		Title:    "Work queue",
		Surface:  layout.SurfaceApp,
		UserName: "Lucas Ruiz",
		Nav: []layout.NavItem{
			{Key: "queue", Label: "Work queue", Href: "/app/queue", Active: true},
			{Key: "clients", Label: "Clients", Href: "/app/clients"},
		},
	}
}

func TestBaseLinksTheDesignSystemStylesheets(t *testing.T) {
	html := render(t, appPage())
	for _, want := range []string{layout.NocturnePath, layout.AppCSSPath} {
		if !strings.Contains(html, want) {
			t.Errorf("Base does not link %s", want)
		}
	}
	if strings.Contains(html, "<style>") {
		t.Error("Base still carries an inline stylesheet")
	}
}

func TestBaseMarksTheActiveSidebarEntry(t *testing.T) {
	html := render(t, appPage())
	for _, want := range []string{
		`href="/app/queue" aria-current="page"`,
		`href="/app/clients"`,
		"Work queue",
		"Clients",
		"New client intake",
		"Lucas Ruiz",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("sidebar is missing %q", want)
		}
	}
	if strings.Contains(html, `href="/app/clients" aria-current`) {
		t.Error("an inactive entry is marked current")
	}
}

func TestBaseRendersNavCountsOnlyWhenNonZero(t *testing.T) {
	p := appPage()
	p.NavCounts = map[string]int{"queue": 7, "clients": 0}
	html := render(t, p)
	if !strings.Contains(html, `<span class="nav-count">7</span>`) {
		t.Error("a supplied count is not rendered")
	}
	if strings.Contains(html, `<span class="nav-count">0</span>`) {
		t.Error("a zero count is rendered instead of hidden")
	}
	if n := strings.Count(render(t, appPage()), `class="nav-count"`); n != 0 {
		t.Errorf("counts rendered without NavCounts: %d", n)
	}
}

// The client portal keeps the tokens but not the recruiter sidebar.
func TestClientSurfaceUsesTheSlimTopBar(t *testing.T) {
	html := render(t, layout.Page{
		Title:   "Jobs",
		Surface: layout.SurfaceClient,
		Nav:     []layout.NavItem{{Key: "jobs", Label: "Jobs", Href: "/client/jobs", Active: true}},
	})
	if strings.Contains(html, `class="sidebar"`) {
		t.Error("the client portal renders the recruiter sidebar")
	}
	if !strings.Contains(html, `class="topbar"`) {
		t.Error("the client portal has no top bar")
	}
}
