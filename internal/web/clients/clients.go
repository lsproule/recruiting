// Package clients serves the recruiter's book of accounts: every client
// company with the numbers the desk is judged on, and one client's jobs,
// merged pipeline board, and shortlist packets. Handlers call
// internal/service only.
package clients

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/service"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

// Prefix is where the accounts live on the app surface.
const Prefix = "/app/clients"

// Tabs of the client detail screen.
const (
	TabJobs       = "jobs"
	TabBoard      = "board"
	TabShortlists = "shortlists"
)

// Query parameters of the detail screen: which tab, and which job the board
// is scoped to.
const (
	tabParam = "tab"
	jobParam = "job"
)

// Path is one client's detail screen.
func Path(id uuid.UUID) string { return Prefix + "/" + id.String() }

// TabPath is one tab of a client's detail screen.
func TabPath(id uuid.UUID, tab string) string { return Path(id) + "?" + tabParam + "=" + tab }

// BoardPath is the client's board scoped to one of its jobs.
func BoardPath(id, jobID uuid.UUID) string {
	return TabPath(id, TabBoard) + "&" + jobParam + "=" + jobID.String()
}

// Deps is what Mount needs. Org supplies the signed-in user's display name.
type Deps struct {
	Clients *service.ClientService
	Org     *service.OrgService
	// Logger records the errors behind a 500; the visitor only ever sees a
	// generic message. Nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the account screens on r. It expects the shared auth
// middleware to be installed already, and must be mounted after intake.Mount,
// which owns /app/clients/new.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		r.Get(Prefix, h.list)
		r.Get(Prefix+"/{id}", h.detail)
	})
}

type handlers struct{ d Deps }

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = c.Render(r.Context(), w)
}

func (h *handlers) page(r *http.Request, title string) layout.Page {
	p, _ := middleware.PrincipalFrom(r.Context())
	return layout.Page{
		Title: title, Surface: layout.SurfaceApp, Nav: layout.AppNav(p, Prefix),
		UserRole: layout.RoleLabel(p), Menu: layout.AppMenu(p, Prefix),
		CSRF: middleware.CSRFToken(r), UserName: h.displayName(r, p),
	}
}

// displayName is the signed-in user's name for the chrome. A lookup failure
// hides the name rather than failing the page it decorates.
func (h *handlers) displayName(r *http.Request, p service.Principal) string {
	if h.d.Org == nil {
		return ""
	}
	u, err := h.d.Org.User(r.Context(), p, p.UserID)
	if err != nil {
		return ""
	}
	if u.Name != "" {
		return u.Name
	}
	return u.Email
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	accounts, err := h.d.Clients.List(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, listPage(h.page(r, "Clients"), accounts))
}

// detail draws the tab that was asked for. The board is read per job or
// merged across the client's jobs, so it is loaded only when it is shown.
func (h *handlers) detail(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "bad client", http.StatusBadRequest)
		return
	}
	view := detailView{Tab: tab(r.URL.Query().Get(tabParam))}
	if view.Tab == TabBoard {
		jobID, err := jobScope(r)
		if err != nil {
			http.Error(w, "bad job", http.StatusBadRequest)
			return
		}
		board, err := h.d.Clients.Board(r.Context(), p, id, jobID)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		view.Account, view.Jobs, view.Board = board.Account, board.Jobs, board
		render(w, r, http.StatusOK, detailPage(h.page(r, board.Account.Name), view))
		return
	}
	detail, err := h.d.Clients.Detail(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	view.Account, view.Jobs, view.Packets = detail.Account, detail.Jobs, detail.Packets
	render(w, r, http.StatusOK, detailPage(h.page(r, detail.Account.Name), view))
}

// tab reads the requested tab, defaulting to the jobs the client has open.
func tab(raw string) string {
	switch strings.TrimSpace(raw) {
	case TabBoard:
		return TabBoard
	case TabShortlists:
		return TabShortlists
	}
	return TabJobs
}

// jobScope is the job the board is narrowed to; absent means every job.
func jobScope(r *http.Request) (uuid.UUID, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(jobParam))
	if raw == "" {
		return uuid.Nil, nil
	}
	return uuid.Parse(raw)
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, service.ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		status = http.StatusNotFound
	}
	if status == http.StatusInternalServerError {
		if h.d.Logger != nil {
			h.d.Logger.ErrorContext(r.Context(), "client screen failed", "path", r.URL.Path, "error", err)
		}
		http.Error(w, "Something went wrong. Try again.", status)
		return
	}
	http.Error(w, strings.TrimPrefix(err.Error(), "service: "), status)
}
