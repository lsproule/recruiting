// Package pool serves the recruiter's talent-pool screens: the searchable
// list of entries, the edit and remove actions on one entry, and the ranked
// suggestions panel a job embeds. Handlers call internal/service only.
package pool

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

// Prefix is where the talent-pool screens live on the app surface.
const Prefix = "/app/pool"

// jobsPrefix is where the job screens live. The suggestions partial hangs off
// a job rather than the pool, and naming it here keeps package jobs free to
// embed this package's fragment without an import cycle.
const jobsPrefix = "/app/jobs"

// SuggestionsPath is the partial a job's suggestions panel loads.
func SuggestionsPath(jobID uuid.UUID) string {
	return jobsPrefix + "/" + jobID.String() + "/suggestions"
}

// addPath is where the panel's one-click add posts.
func addPath(jobID, entryID uuid.UUID) string {
	return SuggestionsPath(jobID) + "/" + entryID.String()
}

func entryPath(e service.PoolEntry) string { return Prefix + "/" + e.ID.String() }

// FlagPath is where the "mark high quality" action posts. A pipeline screen
// embeds FlagButton, which posts here.
func FlagPath(applicationID uuid.UUID) string {
	return Prefix + "/flag/" + applicationID.String()
}

func removePath(e service.PoolEntry) string { return entryPath(e) + "/remove" }

// Deps is what Mount needs. Org supplies the signed-in user's display name.
type Deps struct {
	Pool *service.PoolService
	Org  *service.OrgService
	// Logger records the errors behind a 500; the visitor only ever sees a
	// generic message. Nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the pool screens and the per-job suggestions partial on r.
// It expects the shared auth middleware (CSRF and Authenticate) to be
// installed already, and must be mounted after jobs.Mount, whose subrouter
// owns the rest of the job screens.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Route(Prefix, func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		r.Use(requireRecruiter)
		r.Get("/", h.list)
		r.Post("/flag/{applicationID}", h.flag)
		r.Post("/{id}", h.update)
		r.Post("/{id}/remove", h.remove)
	})
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		r.Use(requireRecruiter)
		r.Get(jobsPrefix+"/{jobID}/suggestions", h.suggestions)
		r.Post(jobsPrefix+"/{jobID}/suggestions/{entryID}", h.add)
	})
}

// requireRecruiter refuses anyone but a recruiter or an admin: the pool is a
// hiring tool. Authentication has already happened, so a vetter is a 403
// rather than a login redirect.
func requireRecruiter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := middleware.PrincipalFrom(r.Context())
		if !ok || p.Kind != service.PrincipalOrgUser || (!p.HasRole(service.RoleRecruiter) && !p.HasRole(service.RoleAdmin)) {
			http.Error(w, "recruiter role required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
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
		Title: title, Surface: layout.SurfaceApp, Nav: layout.AppNav(p, Prefix), UserRole: layout.RoleLabel(p), Menu: layout.AppMenu(p, Prefix),
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

// fail answers with the error's status, logging the cause of a 500 rather
// than showing it.
func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(err)
	if status == http.StatusInternalServerError && h.d.Logger != nil {
		h.d.Logger.ErrorContext(r.Context(), "pool screen failed", "path", r.URL.Path, "error", err)
	}
	http.Error(w, userMessage(err), status)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrPoolAlreadyOnJob), errors.Is(err, service.ErrJobNotOpen),
		errors.Is(err, service.ErrNoStages):
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError
}

// userMessage strips the package prefix from an error meant for a person;
// unexpected errors are not shown at all.
func userMessage(err error) string {
	if statusFor(err) == http.StatusInternalServerError {
		return "Something went wrong. Try again."
	}
	return strings.TrimPrefix(err.Error(), "service: ")
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	entries, err := h.d.Pool.List(r.Context(), p, query)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, listPage(h.page(r, "Talent pool"), query, entries))
}

func (h *handlers) update(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	_, err := h.d.Pool.Update(r.Context(), p, id, service.PoolEdit{
		Skills: []string{r.PostFormValue("skills")}, Notes: r.PostFormValue("notes"),
		Location: r.PostFormValue("location"), RemoteOK: r.PostFormValue("remote_ok") != "",
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, Prefix, http.StatusSeeOther)
}

func (h *handlers) remove(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.d.Pool.Remove(r.Context(), p, id); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, Prefix, http.StatusSeeOther)
}

// suggestions renders the panel itself, which the job screen loads into the
// placeholder SuggestionsFragment leaves.
func (h *handlers) suggestions(w http.ResponseWriter, r *http.Request) {
	jobID, ok := idParam(w, r, "jobID")
	if !ok {
		return
	}
	h.renderPanel(w, r, jobID, panelNote{})
}

// flag marks an application high quality from wherever it is shown, and
// answers with the fragment that replaces the button.
func (h *handlers) flag(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	applicationID, ok := idParam(w, r, "applicationID")
	if !ok {
		return
	}
	entry, err := h.d.Pool.Flag(r.Context(), p, applicationID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, flagged(entry))
}

// add files the entry's candidate on the job and redraws the panel, so the
// recruiter sees the candidate leave the suggestions they came from.
func (h *handlers) add(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	jobID, ok := idParam(w, r, "jobID")
	if !ok {
		return
	}
	entryID, ok := idParam(w, r, "entryID")
	if !ok {
		return
	}
	note := panelNote{Message: "Added to the job."}
	if _, err := h.d.Pool.AddToJob(r.Context(), p, jobID, entryID); err != nil {
		if statusFor(err) == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		note = panelNote{Message: userMessage(err), Failed: true}
	}
	h.renderPanel(w, r, jobID, note)
}

func (h *handlers) renderPanel(w http.ResponseWriter, r *http.Request, jobID uuid.UUID, note panelNote) {
	p, _ := middleware.PrincipalFrom(r.Context())
	found, err := h.d.Pool.Suggestions(r.Context(), p, jobID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, suggestionsPanel(jobID, found, note))
}

func idParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		http.Error(w, "bad "+name, http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}
