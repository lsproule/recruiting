// Package problems serves the recruiter's problem-bank screens: the filtered
// list, one problem's detail, the create and edit form, and the JSON import
// with its per-problem error report. Handlers call internal/service only.
package problems

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

// Prefix is where the problem-bank screens live on the app surface.
const Prefix = "/app/problems"

// ImportPath is the upload screen that takes a JSON batch.
const ImportPath = Prefix + "/import"

// MaxImportBytes bounds an uploaded batch. A hundred problems with their test
// cases sit far inside it; anything larger is a mistake, not a bank.
const MaxImportBytes = 4 << 20

// formRows is how many blank reference and test-case rows the form offers on
// top of what a problem already has.
const formRows = 2

// maxFormRows bounds how far the form parser looks for indexed rows, so a
// crafted post cannot make it walk forever.
const maxFormRows = 100

// Deps is what Mount needs. Org supplies the signed-in user's display name.
type Deps struct {
	Problems *service.ProblemService
	Org      *service.OrgService
	// Logger records the errors behind a 500; the visitor only ever sees a
	// generic message. Nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the problem-bank screens on r. It expects the shared auth
// middleware (CSRF and Authenticate) to be installed already.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Route(Prefix, func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		// The bank is a hiring tool; a vetter scores attempts, not problems.
		r.Use(requireRecruiter)
		r.Get("/", h.list)
		r.Get("/new", h.newProblem)
		r.Post("/", h.create)
		r.Get("/import", h.importForm)
		r.Post("/import", h.importBatch)
		r.Get("/{id}", h.detail)
		r.Get("/{id}/edit", h.edit)
		r.Post("/{id}", h.update)
		r.Post("/{id}/delete", h.remove)
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
		h.d.Logger.ErrorContext(r.Context(), "problem screen failed", "path", r.URL.Path, "error", err)
	}
	http.Error(w, userMessage(err), status)
}

func statusFor(err error) int {
	var report domain.ProblemImportErrors
	switch {
	case errors.As(err, &report):
		return http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrPlatformProblem), errors.Is(err, service.ErrProblemTitleTaken),
		errors.Is(err, service.ErrNoExecutor):
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
	f := service.ProblemFilter{
		Kind:       r.URL.Query().Get("kind"),
		Difficulty: r.URL.Query().Get("difficulty"),
		Tag:        r.URL.Query().Get("tag"),
		Query:      r.URL.Query().Get("q"),
	}
	found, err := h.d.Problems.List(r.Context(), p, f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, listPage(h.page(r, "Problem bank"), f, found))
}

func (h *handlers) detail(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	problem, err := h.d.Problems.Get(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, detailPage(h.page(r, problem.Title), problem))
}

func (h *handlers) newProblem(w http.ResponseWriter, r *http.Request) {
	render(w, r, http.StatusOK, formPage(h.page(r, "New problem"), problemForm{
		New: true, Difficulty: "medium", Kind: domain.ProblemKindCode,
		TimeLimitMs: domain.DefaultProblemTimeLimitMs, MemoryLimitKB: domain.DefaultProblemMemoryLimitKB,
		References: make([]referenceRow, formRows), TestCases: make([]testCaseRow, formRows),
	}, nil))
}

func (h *handlers) edit(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	problem, err := h.d.Problems.Get(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if problem.Platform() {
		h.fail(w, r, service.ErrPlatformProblem)
		return
	}
	render(w, r, http.StatusOK, formPage(h.page(r, "Edit "+problem.Title), formOf(problem), nil))
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	form := readForm(r)
	form.New = true
	created, err := h.d.Problems.Create(r.Context(), p, form.asImport())
	if err != nil {
		h.renderFormError(w, r, "New problem", form, err)
		return
	}
	http.Redirect(w, r, Prefix+"/"+created.ID.String(), http.StatusSeeOther)
}

func (h *handlers) update(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	form := readForm(r)
	form.ID = id
	if _, err := h.d.Problems.Update(r.Context(), p, id, form.asImport()); err != nil {
		h.renderFormError(w, r, "Edit "+form.Title, form, err)
		return
	}
	http.Redirect(w, r, Prefix+"/"+id.String(), http.StatusSeeOther)
}

func (h *handlers) remove(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.d.Problems.Delete(r.Context(), p, id); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, Prefix, http.StatusSeeOther)
}

// renderFormError redraws the form with what the author typed and why it was
// refused. A validation report is shown per field group; anything unexpected
// is still a 500 with nothing leaked.
func (h *handlers) renderFormError(w http.ResponseWriter, r *http.Request, title string, form problemForm, err error) {
	if statusFor(err) == http.StatusInternalServerError {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusUnprocessableEntity, formPage(h.page(r, title), form, problemMessages(err)))
}

func (h *handlers) importForm(w http.ResponseWriter, r *http.Request) {
	render(w, r, http.StatusOK, importPage(h.page(r, "Import problems"), nil, nil))
}

// importBatch takes a JSON batch as an upload or pasted text, and answers with
// the per-problem report when the runner refuses any reference solution.
func (h *handlers) importBatch(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	doc, err := importDocument(r)
	if err != nil {
		render(w, r, http.StatusUnprocessableEntity, importPage(h.page(r, "Import problems"), nil, []string{err.Error()}))
		return
	}
	imported, err := h.d.Problems.Import(r.Context(), p, doc)
	if err != nil {
		var report domain.ProblemImportErrors
		if errors.As(err, &report) {
			render(w, r, http.StatusUnprocessableEntity, importPage(h.page(r, "Import problems"), report, nil))
			return
		}
		if statusFor(err) == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		render(w, r, http.StatusUnprocessableEntity, importPage(h.page(r, "Import problems"), nil, []string{userMessage(err)}))
		return
	}
	render(w, r, http.StatusOK, importedPage(h.page(r, "Import problems"), imported))
}

// importDocument reads the batch from the file field when one was uploaded and
// from the pasted text otherwise.
func importDocument(r *http.Request) ([]byte, error) {
	if file, _, err := r.FormFile("document"); err == nil {
		defer file.Close()
		// One byte past the limit is enough to refuse the upload, and nothing
		// larger ever reaches memory.
		data, err := io.ReadAll(io.LimitReader(file, MaxImportBytes+1))
		if err != nil {
			return nil, errors.New("the upload could not be read")
		}
		if len(data) > MaxImportBytes {
			return nil, errors.New("that file is larger than the " + strconv.Itoa(MaxImportBytes>>20) + " MB import limit")
		}
		if len(strings.TrimSpace(string(data))) > 0 {
			return data, nil
		}
	}
	pasted := strings.TrimSpace(r.PostFormValue("document_text"))
	if pasted == "" {
		return nil, errors.New("choose a JSON file or paste the batch")
	}
	if len(pasted) > MaxImportBytes {
		return nil, errors.New("that batch is larger than the import limit")
	}
	return []byte(pasted), nil
}

func idParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		http.Error(w, "bad "+name, http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}
