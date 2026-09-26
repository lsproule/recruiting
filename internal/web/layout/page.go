package layout

import (
	"encoding/json"
	"strings"

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

// Nav keys. They name the sidebar entry, key Page.NavCounts, and select the
// entry's mark in NavIcon.
const (
	NavQueue       = "queue"
	NavClients     = "clients"
	NavCandidates  = "candidates"
	NavProblems    = "problems"
	NavAssessments = "assessments"
)

// NavItem is one entry in the primary navigation.
type NavItem struct {
	Key    string
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
	Title   string
	Surface Surface
	Nav     []NavItem
	// Menu holds the entries behind the sidebar footer disclosure: the
	// role-gated screens that are not one of the five primary destinations.
	Menu    []NavItem
	Flashes []Flash
	CSRF    string
	// NavCounts is keyed by NavItem.Key. A key that is absent or zero renders
	// no count, so a screen supplies only the numbers it actually knows.
	NavCounts map[string]int
	UserName  string
	UserRole  string
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
		{Key: NavQueue, Label: "Work queue", Href: "/app/queue"},
		{Key: NavClients, Label: "Clients", Href: "/app/clients"},
		{Key: NavCandidates, Label: "Candidates", Href: "/app/candidates"},
	}
	// The problem bank and assessments are hiring tools; the screens behind
	// them refuse anyone else.
	if p.HasRole(service.RoleRecruiter) || p.HasRole(service.RoleAdmin) {
		items = append(items,
			NavItem{Key: NavProblems, Label: "Problem bank", Href: "/app/problems"},
			NavItem{Key: NavAssessments, Label: "Assessments", Href: "/app/assessments"},
		)
	}
	return markActive(items, current)
}

// AppMenu is the sidebar footer menu: the role-gated screens that sit outside
// the five primary destinations.
func AppMenu(p service.Principal, current string) []NavItem {
	items := []NavItem{
		{Key: "jobs", Label: "Jobs", Href: "/app/jobs"},
		{Key: "interviews", Label: "Interviews", Href: "/app/interviews"},
	}
	if p.HasRole(service.RoleRecruiter) || p.HasRole(service.RoleAdmin) {
		items = append(items,
			NavItem{Key: "processes", Label: "Hiring processes", Href: "/app/processes"},
			NavItem{Key: "sprints", Label: "Sprints", Href: "/app/sprints"},
			NavItem{Key: "pool", Label: "Talent pool", Href: "/app/pool"},
			NavItem{Key: "talent", Label: "Talent network", Href: "/app/talent"},
		)
	}
	if p.HasRole(service.RoleVetter) {
		items = append(items,
			NavItem{Key: "availability", Label: "Availability", Href: "/app/availability"},
			NavItem{Key: "scorecards", Label: "Scorecards", Href: "/app/scorecards"},
			NavItem{Key: "reviews", Label: "Reviews", Href: "/app/reviews"},
		)
	}
	if p.HasRole(service.RoleAdmin) {
		items = append(items,
			NavItem{Key: "users", Label: "Users", Href: "/app/admin/users"},
			NavItem{Key: "admin-clients", Label: "Client accounts", Href: "/app/admin/clients"},
			NavItem{Key: "settings", Label: "Settings", Href: "/app/admin/settings"},
		)
	}
	return markActive(items, current)
}

// RoleLabel is the second line of the sidebar user footer: the principal's
// roles, title-cased and joined. Empty for a principal with no role.
func RoleLabel(p service.Principal) string {
	labels := make([]string, 0, len(p.Roles))
	for _, role := range p.Roles {
		if role == "" {
			continue
		}
		labels = append(labels, strings.ToUpper(role[:1])+role[1:])
	}
	return strings.Join(labels, " · ")
}

func markActive(items []NavItem, current string) []NavItem {
	for i := range items {
		items[i].Active = items[i].Href == current
	}
	return items
}

// initials seeds the user footer mark; empty when there is no signed-in user.
func (p Page) initials() string {
	out := ""
	for _, field := range strings.Fields(p.UserName) {
		out += strings.ToUpper(field[:1])
		if len(out) == 2 {
			break
		}
	}
	return out
}

// csrfHeader is the hx-headers value that puts the CSRF token on every htmx
// request; forms use CSRFField instead.
func csrfHeader(token string) string {
	b, _ := json.Marshal(map[string]string{middleware.CSRFHeader: token})
	return string(b)
}

// htmxConfig is read by htmx at boot. It swaps a 422 like a 2xx: every
// refused form here answers with the fragment re-rendered around its reason,
// and htmx's default of leaving a 4xx unswapped would hide that reason.
const htmxConfig = `{"responseHandling":[{"code":"204","swap":false},{"code":"[23]..","swap":true},{"code":"422","swap":true},{"code":"[45]..","swap":false,"error":true}]}`

// ClientNav is the client-portal navigation, with the entry matching current
// marked active.
func ClientNav(current string) []NavItem {
	return markActive([]NavItem{
		{Key: "jobs", Label: "Jobs", Href: "/client/jobs"},
		{Key: "talent", Label: "Talent requests", Href: "/client/talent"},
		{Key: "developer", Label: "Developer", Href: "/client/developer"},
	}, current)
}
