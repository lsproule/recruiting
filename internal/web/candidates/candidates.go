// Package candidates serves the recruiter's candidate screens: the searchable
// list, one candidate's detail, manual entry, and resume downloads. Handlers
// call internal/service only.
package candidates

import (
	"errors"
	"io"
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

// Prefix is where the candidate screens live on the app surface.
const Prefix = "/app/candidates"

// resumeField is the file input's name on the manual-add form.
const resumeField = "resume"

// Deps is what Mount needs. Jobs supplies the roles a manually added
// candidate can be filed against; Org the signed-in user's display name.
type Deps struct {
	Candidates *service.CandidateService
	Jobs       *service.JobService
	Org        *service.OrgService
	// Logger records the errors behind a 500; the visitor only ever sees a
	// generic message. Nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the candidate screens on r. It expects the shared auth
// middleware (CSRF and Authenticate) to be installed already, and
// apply.MaxBody ahead of it: CSRF reads the multipart form the manual-add
// screen posts, and would otherwise parse an unbounded upload.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Route(Prefix, func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		// Every org user reads candidates — a vetter scores the people in the
		// list. Adding one is a recruiter's or an admin's.
		r.Get("/", h.list)
		r.Get("/{id}", h.detail)
		r.Get("/{id}/resumes/{resumeID}", h.resume)
		r.Group(func(r chi.Router) {
			r.Use(requireRecruiter)
			r.Get("/new", h.newCandidate)
			r.Post("/", h.create)
		})
	})
}

// requireRecruiter refuses anyone but a recruiter or an admin. Authentication
// has already happened, so a vetter is a 403 rather than a login redirect.
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
		Title: title, Surface: layout.SurfaceApp, Nav: layout.AppNav(p, Prefix),
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
		h.d.Logger.ErrorContext(r.Context(), "candidate screen failed", "path", r.URL.Path, "error", err)
	}
	http.Error(w, userMessage(err), status)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrAlreadyApplied),
		errors.Is(err, service.ErrNoStages),
		errors.Is(err, service.ErrNameRequired),
		errors.Is(err, service.ErrEmailRequired),
		errors.Is(err, service.ErrNoBlobStore),
		errors.Is(err, domain.ErrResumeEmpty),
		errors.Is(err, domain.ErrResumeType),
		errors.Is(err, domain.ErrResumeTooLarge):
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
	msg := err.Error()
	for _, prefix := range []string{"service: ", "domain: "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	return msg
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	found, err := h.d.Candidates.Search(r.Context(), p, query)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, listPage(h.page(r, "Candidates"), query, found))
}

func (h *handlers) detail(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := candidateID(w, r)
	if !ok {
		return
	}
	detail, err := h.d.Candidates.Detail(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, detailPage(h.page(r, detail.Candidate.Name), detail))
}

// resume redirects to a short-lived signed URL rather than proxying the file,
// so the bytes never pass through this process.
func (h *handlers) resume(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	resumeID, err := uuid.Parse(chi.URLParam(r, "resumeID"))
	if err != nil {
		http.Error(w, "bad resume id", http.StatusBadRequest)
		return
	}
	id, ok := candidateID(w, r)
	if !ok {
		return
	}
	url, err := h.d.Candidates.ResumeURL(r.Context(), p, id, resumeID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func (h *handlers) newCandidate(w http.ResponseWriter, r *http.Request) {
	h.renderForm(w, r, http.StatusOK, candidateForm{})
}

// renderForm draws the manual-add form, loading the jobs it can file against.
func (h *handlers) renderForm(w http.ResponseWriter, r *http.Request, status int, form candidateForm) {
	p, _ := middleware.PrincipalFrom(r.Context())
	jobs, err := h.d.Jobs.ListJobs(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	form.Jobs = openJobs(jobs)
	render(w, r, status, formPage(h.page(r, "Add candidate"), form))
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	in, form, err := candidateFromForm(r)
	var cand service.Candidate
	if err == nil {
		cand, err = h.d.Candidates.Add(r.Context(), p, in)
	}
	if err != nil {
		status := statusFor(err)
		if status == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		form.Error = userMessage(err)
		h.renderForm(w, r, status, form)
		return
	}
	http.Redirect(w, r, Prefix+"/"+cand.ID.String(), http.StatusSeeOther)
}

func candidateID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "bad candidate id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

// candidateFromForm reads the manual-add form. Both the job and the resume
// are optional: a recruiter may file a person before there is a role.
func candidateFromForm(r *http.Request) (service.NewCandidate, candidateForm, error) {
	in := service.NewCandidate{
		Name:  r.PostFormValue("name"),
		Email: r.PostFormValue("email"),
		Phone: r.PostFormValue("phone"),
		Links: []string{r.PostFormValue("links")},
	}
	form := candidateForm{Name: in.Name, Email: in.Email, Phone: in.Phone, Links: r.PostFormValue("links"), JobID: r.PostFormValue("job_id")}
	if raw := strings.TrimSpace(form.JobID); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return in, form, service.ErrNotFound
		}
		in.JobID = id
	}
	file, header, err := r.FormFile(resumeField)
	if err != nil {
		return in, form, nil
	}
	defer file.Close()
	// Read one byte past the limit: enough to reject the upload, and nothing
	// larger ever reaches memory.
	data, err := io.ReadAll(io.LimitReader(file, domain.MaxResumeBytes+1))
	if err != nil {
		return in, form, domain.ErrResumeEmpty
	}
	if len(data) > 0 {
		in.Resume = &service.ResumeUpload{Filename: header.Filename, Data: data}
	}
	return in, form, nil
}
