package layout

import (
	"encoding/json"

	"recruiting/internal/service"
	"recruiting/internal/web/middleware"
)

// Surface is the HTML surface a page belongs to; it decides the nav and the
// logout target.
type Surface string

const (
	SurfaceApp    Surface = "/app"
	SurfaceClient Surface = "/client"
)

// NavItem is one entry in the primary navigation.
type NavItem struct {
	Label  string
	Href   string
	Active bool
}

// Flash is a one-off message shown above the page content.
type Flash struct {
	Kind    string // "success" or "error"
	Message string
}

// Page is the chrome around every rendered view.
type Page struct {
	Title    string
	Surface  Surface
	Nav      []NavItem
	Flashes  []Flash
	CSRF     string
	UserName string
}

// LogoutPath is the surface's logout endpoint.
func (p Page) LogoutPath() string {
	if p.Surface == "" {
		return string(SurfaceApp) + "/logout"
	}
	return string(p.Surface) + "/logout"
}

func (p Page) flashClass(f Flash) string {
	if f.Kind == "error" {
		return "flash flash-error"
	}
	return "flash flash-success"
}

// AppNav is the org-user navigation, with the entry matching current marked
// active. Admin-only entries are omitted for principals without the role.
func AppNav(p service.Principal, current string) []NavItem {
	items := []NavItem{
		{Label: "Jobs", Href: "/app/jobs"},
		{Label: "Candidates", Href: "/app/candidates"},
	}
	// The pool is a hiring tool; the screens behind it refuse anyone else.
	if p.HasRole(service.RoleRecruiter) || p.HasRole(service.RoleAdmin) {
		items = append(items,
			NavItem{Label: "Talent pool", Href: "/app/pool"},
			NavItem{Label: "Problems", Href: "/app/problems"},
			NavItem{Label: "Assessments", Href: "/app/assessments"},
		)
	}
	if p.HasRole(service.RoleVetter) {
		items = append(items,
			NavItem{Label: "Availability", Href: "/app/availability"},
			NavItem{Label: "Scorecards", Href: "/app/scorecards"},
		)
	}
	if p.HasRole(service.RoleAdmin) {
		items = append(items,
			NavItem{Label: "Users", Href: "/app/admin/users"},
			NavItem{Label: "Clients", Href: "/app/admin/clients"},
			NavItem{Label: "Settings", Href: "/app/admin/settings"},
		)
	}
	for i := range items {
		items[i].Active = items[i].Href == current
	}
	return items
}

// csrfHeader is the hx-headers value that puts the CSRF token on every htmx
// request; forms use CSRFField instead.
func csrfHeader(token string) string {
	b, _ := json.Marshal(map[string]string{middleware.CSRFHeader: token})
	return string(b)
}

// ClientNav is the client-portal navigation, with the entry matching current
// marked active.
func ClientNav(current string) []NavItem {
	items := []NavItem{{Label: "Jobs", Href: "/client/jobs"}}
	for i := range items {
		items[i].Active = items[i].Href == current
	}
	return items
}
