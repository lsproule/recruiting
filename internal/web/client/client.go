// Package client serves the client portal: a client company's jobs, the
// applications released to them, and the actions a client may take. Every
// template here renders service.Client* view types only, so nothing a
// client must not see can reach a page by accident. Handlers call
// internal/service only.
package client

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

// Where the portal lives. Every route sits under /client so the session
// middleware keeps org users out.
const (
	Prefix            = "/client"
	JobsPath          = Prefix + "/jobs"
	ApplicationPrefix = Prefix + "/applications"
	loginPath         = Prefix + "/login"
)

// JobPath is a job's released applications.
func JobPath(jobID uuid.UUID) string { return JobsPath + "/" + jobID.String() }

// ApplicationPath is one released application.
func ApplicationPath(id uuid.UUID) string { return ApplicationPrefix + "/" + id.String() }

// ResumePath redirects to a short-lived resume download.
func ResumePath(id uuid.UUID) string { return ApplicationPath(id) + "/resume" }

// AdvancePath, RejectPath, and RequestInfoPath are the client's actions.
func AdvancePath(id uuid.UUID) string     { return ApplicationPath(id) + "/advance" }
func RejectPath(id uuid.UUID) string      { return ApplicationPath(id) + "/reject" }
func RequestInfoPath(id uuid.UUID) string { return ApplicationPath(id) + "/request-info" }

// Deps is what Mount needs.
type Deps struct {
	Portal *service.ClientPortalService
	// Logger records the errors behind a 500; nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the portal on r. It expects the shared auth middleware to
// be installed already; the service decides what the client may see.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(loginPath))
		r.Get(Prefix, h.home)
		r.Get(Prefix+"/", h.home)
		r.Get(JobsPath, h.jobs)
		r.Get(JobsPath+"/{jobID}", h.job)
		r.Get(ApplicationPrefix+"/{id}", h.application)
		r.Get(ApplicationPrefix+"/{id}/resume", h.resume)
		r.Post(ApplicationPrefix+"/{id}/advance", h.advance)
		r.Post(ApplicationPrefix+"/{id}/reject", h.reject)
		r.Post(ApplicationPrefix+"/{id}/request-info", h.requestInfo)
	})
}

type handlers struct{ d Deps }

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = c.Render(r.Context(), w)
}

func (h *handlers) page(r *http.Request, title string, flashes ...layout.Flash) layout.Page {
	p, _ := middleware.PrincipalFrom(r.Context())
	name := ""
	if me, err := h.d.Portal.Me(r.Context(), p); err == nil {
		name = me.Name
		if name == "" {
			name = me.Email
		}
	}
	return layout.Page{
		Title: title, Surface: layout.SurfaceClient, Nav: layout.ClientNav(JobsPath),
		Flashes: flashes, CSRF: middleware.CSRFToken(r), UserName: name,
	}
}

func (h *handlers) home(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, JobsPath, http.StatusSeeOther)
}

func (h *handlers) jobs(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	jobs, err := h.d.Portal.Jobs(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, jobsPage(h.page(r, "Jobs"), jobs))
}

func (h *handlers) job(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "jobID")
	if !ok {
		return
	}
	job, apps, err := h.d.Portal.Job(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, jobPage(h.page(r, job.Title), job, apps))
}

func (h *handlers) application(w http.ResponseWriter, r *http.Request) {
	var flashes []layout.Flash
	if r.URL.Query().Get("asked") == "1" {
		flashes = append(flashes, layout.Flash{Kind: "success", Message: "Your question was sent to the recruiter."})
	}
	h.renderApplication(w, r, http.StatusOK, flashes)
}

func (h *handlers) renderApplication(w http.ResponseWriter, r *http.Request, status int, flashes []layout.Flash) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	d, err := h.d.Portal.Application(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, status, applicationPage(h.page(r, d.Application.Candidate.Label, flashes...), d, middleware.CSRFToken(r)))
}

func (h *handlers) resume(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	url, err := h.d.Portal.ResumeURL(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func (h *handlers) advance(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	to, err := uuid.Parse(r.PostFormValue("to_stage_id"))
	if err != nil {
		http.Error(w, "bad to_stage_id", http.StatusBadRequest)
		return
	}
	h.afterAction(w, r, id, h.d.Portal.Advance(r.Context(), p, id, to))
}

func (h *handlers) reject(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	h.afterAction(w, r, id, h.d.Portal.Reject(r.Context(), p, id, r.PostFormValue("reason")))
}

func (h *handlers) requestInfo(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	err := h.d.Portal.RequestInfo(r.Context(), p, id, r.PostFormValue("message"))
	if err == nil {
		http.Redirect(w, r, ApplicationPath(id)+"?asked=1", http.StatusSeeOther)
		return
	}
	h.afterAction(w, r, id, err)
}

// afterAction redirects back to the application, or re-renders it with the
// refusal when the action was not allowed.
func (h *handlers) afterAction(w http.ResponseWriter, r *http.Request, id uuid.UUID, err error) {
	if err == nil {
		http.Redirect(w, r, ApplicationPath(id), http.StatusSeeOther)
		return
	}
	status := statusFor(err)
	if status == http.StatusInternalServerError {
		h.fail(w, r, err)
		return
	}
	h.renderApplication(w, r, status, []layout.Flash{{Kind: "error", Message: userMessage(err)}})
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(err)
	if status == http.StatusInternalServerError && h.d.Logger != nil {
		h.d.Logger.ErrorContext(r.Context(), "client portal failed", "path", r.URL.Path, "error", err)
	}
	http.Error(w, userMessage(err), status)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden), errors.Is(err, service.ErrBlind), errors.Is(err, domain.ErrForbiddenMove):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound), errors.Is(err, service.ErrNoResume):
		return http.StatusNotFound
	case errors.Is(err, domain.ErrReasonRequired), errors.Is(err, domain.ErrTerminal), errors.Is(err, domain.ErrPrereqMissing),
		errors.Is(err, service.ErrMessageRequired), errors.Is(err, service.ErrNotActive), errors.Is(err, service.ErrTooLong):
		return http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrStale):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func userMessage(err error) string {
	if statusFor(err) == http.StatusInternalServerError {
		return "Something went wrong. Try again."
	}
	msg := err.Error()
	for _, prefix := range []string{"service: ", "domain: "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	return msg
}

func param(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		http.Error(w, "bad "+name, http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}
