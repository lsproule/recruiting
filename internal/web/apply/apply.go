// Package apply serves the public application page: the one screen a
// candidate sees without an account. Handlers call internal/service only.
package apply

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"recruiting/internal/domain"
	"recruiting/internal/service"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

// Prefix is where the public apply pages live.
const Prefix = "/apply"

// resumeField is the file input's name on the apply form.
const resumeField = "resume"

// Deps is what Mount needs.
type Deps struct {
	Candidates *service.CandidateService
	// Logger records the errors behind a 500; the applicant only ever sees a
	// generic message. Nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the apply pages on r. They are deliberately outside any
// authentication: the applicant has no account. The shared CSRF middleware
// still applies, so Mount must come after auth.Mount — and MaxBody must be
// installed before it, since CSRF reads the multipart form and would
// otherwise parse an unbounded upload.
//
// The URL names the org before the job because a job slug is unique only
// within an org.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Get(Prefix+"/{orgSlug}/{jobSlug}", h.form)
	r.Post(Prefix+"/{orgSlug}/{jobSlug}", h.submit)
}

// slugs reads the org and job halves of the apply URL.
func slugs(r *http.Request) (string, string) {
	return chi.URLParam(r, "orgSlug"), chi.URLParam(r, "jobSlug")
}

type handlers struct{ d Deps }

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = c.Render(r.Context(), w)
}

// page is the chrome for a visitor with no session: no nav, no user.
func page(r *http.Request, title string) layout.Page {
	return layout.Page{Title: title, CSRF: middleware.CSRFToken(r)}
}

func (h *handlers) form(w http.ResponseWriter, r *http.Request) {
	orgSlug, jobSlug := slugs(r)
	job, err := h.d.Candidates.PublicJob(r.Context(), orgSlug, jobSlug)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, applyPage(page(r, job.Title), job, applyForm{}))
}

func (h *handlers) submit(w http.ResponseWriter, r *http.Request) {
	orgSlug, jobSlug := slugs(r)
	job, err := h.d.Candidates.PublicJob(r.Context(), orgSlug, jobSlug)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	in, form, done := inputFromForm(r)
	defer done()
	if _, err := h.d.Candidates.Apply(r.Context(), orgSlug, jobSlug, in); err != nil {
		status := statusFor(err)
		if status == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		form.Error = userMessage(err)
		render(w, r, status, applyPage(page(r, job.Title), job, form))
		return
	}
	render(w, r, http.StatusOK, thanksPage(page(r, job.Title), job))
}

// inputFromForm reads the multipart form. The file is not copied: it stays
// where the parser parked the part — in memory when small, on disk past
// MaxBody's memory cap — and the service reads it there, sniffing its type
// from the head and refusing an oversized one by its declared size before a
// byte of it is loaded. The returned func closes the file and must run only
// once the service is done with the input.
func inputFromForm(r *http.Request) (service.ApplyInput, applyForm, func()) {
	in := service.ApplyInput{
		Name:  r.PostFormValue("name"),
		Email: r.PostFormValue("email"),
		Phone: r.PostFormValue("phone"),
		Links: []string{r.PostFormValue("links")},
	}
	form := applyForm{Name: in.Name, Email: in.Email, Phone: in.Phone, Links: r.PostFormValue("links")}
	file, header, err := r.FormFile(resumeField)
	if err != nil {
		return in, form, func() {}
	}
	in.Resume = service.ResumeUpload{Filename: header.Filename, File: file, Size: header.Size}
	return in, form, func() { _ = file.Close() }
}

// fail answers with the error's status, logging the cause of a 500 rather
// than showing it.
func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(err)
	if status == http.StatusInternalServerError && h.d.Logger != nil {
		h.d.Logger.ErrorContext(r.Context(), "apply page failed", "path", r.URL.Path, "error", err)
	}
	if status == http.StatusNotFound {
		render(w, r, status, notFoundPage(page(r, "Job not found")))
		return
	}
	http.Error(w, userMessage(err), status)
}

// statusFor maps a service error onto the response status. A job that is not
// open is a 404: the applicant must not learn it exists.
func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrAlreadyApplied),
		errors.Is(err, service.ErrNoStages),
		errors.Is(err, service.ErrNameRequired),
		errors.Is(err, service.ErrEmailRequired),
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
