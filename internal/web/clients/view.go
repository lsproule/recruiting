package clients

import (
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/service"
)

// detailView is one client screen: the header numbers every tab shows, and
// whichever tab was asked for.
type detailView struct {
	Account service.ClientAccount
	Jobs    []service.ClientAccountJob
	Tab     string
	Packets []service.ClientPacket
	Board   service.ClientBoard
}

type tabLink struct {
	Label  string
	Href   string
	Active bool
}

// Tabs are the three ways into an account.
func (v detailView) Tabs() []tabLink {
	out := make([]tabLink, 0, 3)
	for _, t := range []struct{ key, label string }{
		{TabJobs, "Jobs"}, {TabBoard, "Board"}, {TabShortlists, "Shortlists"},
	} {
		out = append(out, tabLink{Label: t.label, Href: TabPath(v.Account.ID, t.key), Active: v.Tab == t.key})
	}
	return out
}

// AllJobs reports whether the board is showing every job of the client.
func (v detailView) AllJobs() bool { return v.Board.JobID == uuid.Nil }

// ScopeLabel names what the board is showing.
func (v detailView) ScopeLabel() string {
	for _, job := range v.Jobs {
		if job.ID == v.Board.JobID {
			return job.Title
		}
	}
	return "All jobs"
}

// ScopeAllHref is the board with every job merged back in.
func (v detailView) ScopeAllHref() string { return TabPath(v.Account.ID, TabBoard) }

// mark is the two-letter stand-in for a client's logo.
func mark(name string) string {
	out := ""
	for _, field := range strings.Fields(name) {
		out += strings.ToUpper(field[:1])
		if len(out) == 2 {
			break
		}
	}
	if out == "" {
		return "—"
	}
	return out
}

// days renders the median wait to one decimal; a client with no packet out
// yet has no median rather than a zero, which would read as instant.
func days(v *float64) string {
	if v == nil {
		return "—"
	}
	return strconv.FormatFloat(*v, 'f', 1, 64)
}

// sla is the contracted shortlist window, or says none was agreed.
func sla(n int) string {
	if n <= 0 {
		return "not set"
	}
	return strconv.Itoa(n) + "d"
}

func itoa(n int) string { return strconv.Itoa(n) }

func day(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("2 Jan 2006")
}

func sentAt(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return day(*t)
}

func applicationPath(id uuid.UUID) string { return "/app/applications/" + id.String() }

// text falls back to a dash so an empty cell still reads as a value.
func text(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
