// Package interviews serves the signed-in user's interviews: the booked
// slots they interview (or, for a recruiter, everyone's) and the sprints
// they are in, each with the way in. Handlers call internal/service only.
package interviews

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/service"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

// Prefix is where the interviews screen lives on the app surface.
const Prefix = "/app/interviews"

// Deps is what Mount needs.
type Deps struct {
	Interviews *service.InterviewService
	Sprints    *service.SprintService
	Org        *service.OrgService
	// Logger records the errors behind a 500; nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the screen on r. It expects the shared auth middleware to
// be installed already.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.With(middleware.RequireAuth("/app/login")).Get(Prefix, h.list)
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
		Title: title, Surface: layout.SurfaceApp, Nav: layout.AppNav(p, Prefix), UserRole: layout.RoleLabel(p), Menu: layout.AppMenu(p, Prefix),
		CSRF: middleware.CSRFToken(r), UserName: h.displayName(r, p),
	}
}

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

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, service.ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		status = http.StatusNotFound
	}
	if status == http.StatusInternalServerError && h.d.Logger != nil {
		h.d.Logger.ErrorContext(r.Context(), "interviews screen failed", "path", r.URL.Path, "error", err)
	}
	msg := "Something went wrong. Try again."
	if status != http.StatusInternalServerError {
		msg = strings.TrimPrefix(err.Error(), "service: ")
	}
	http.Error(w, msg, status)
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	slots, err := h.d.Interviews.Upcoming(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var sprints []service.SprintListItem
	if h.d.Sprints != nil {
		sprints, err = h.d.Sprints.ForInterviewer(r.Context(), p)
		if err != nil {
			h.fail(w, r, err)
			return
		}
	}
	render(w, r, http.StatusOK, listPage(h.page(r, "Interviews"), listView{
		Slots: slots, Sprints: sprints, Now: time.Now(), UserID: p.UserID,
		Everyone: p.HasRole(service.RoleRecruiter) || p.HasRole(service.RoleAdmin),
	}))
}

// listView is the interviews screen.
type listView struct {
	Slots    []service.InterviewRow
	Sprints  []service.SprintListItem
	Now      time.Time
	UserID   uuid.UUID
	Everyone bool
}

// Today and Later split the slots at the end of the day.
func (v listView) today() []service.InterviewRow { return v.split(true) }
func (v listView) later() []service.InterviewRow { return v.split(false) }

func (v listView) split(today bool) []service.InterviewRow {
	y, m, d := v.Now.UTC().Date()
	endOfDay := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	out := []service.InterviewRow{}
	for _, s := range v.Slots {
		if s.Start.Before(endOfDay) == today {
			out = append(out, s)
		}
	}
	return out
}
