// Package processes serves the org's hiring processes: the library of
// pipelines a job is built from, and the editor for each. Handlers call
// internal/service only.
package processes

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
	"recruiting/internal/web/stages"
)

// Prefix is where the processes live on the app surface.
const Prefix = "/app/processes"

// Path is one process's editor.
func Path(id uuid.UUID) string { return Prefix + "/" + id.String() }

// Deps is what Mount needs. Org supplies the signed-in user's display name.
type Deps struct {
	Processes *service.ProcessService
	Org       *service.OrgService
	// Logger records the errors behind a 500; nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the process screens on r. It expects the shared auth
// middleware to be installed already. Every org user may read the library;
// changing it is a recruiter's or an admin's.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Route(Prefix, func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		r.Get("/", h.list)
		r.Get("/{id}", h.show)
		r.Group(func(r chi.Router) {
			r.Use(requireRecruiter)
			r.Post("/", h.create)
			r.Post("/{id}", h.update)
			r.Post("/{id}/default", h.setDefault)
			r.Post("/{id}/delete", h.remove)
			r.Post("/{id}/stages", h.addStage)
			r.Post("/{id}/stages/order", h.reorderStages)
			r.Post("/{id}/stages/{stageID}", h.updateStage)
			r.Post("/{id}/stages/{stageID}/delete", h.deleteStage)
		})
	})
}

// requireRecruiter refuses anyone but a recruiter or an admin.
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

func (h *handlers) page(r *http.Request, title string, flashes ...layout.Flash) layout.Page {
	p, _ := middleware.PrincipalFrom(r.Context())
	return layout.Page{
		Title: title, Surface: layout.SurfaceApp, Nav: layout.AppNav(p, Prefix), UserRole: layout.RoleLabel(p), Menu: layout.AppMenu(p, Prefix),
		Flashes: flashes, CSRF: middleware.CSRFToken(r), UserName: h.displayName(r, p),
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
	status := statusFor(err)
	if status == http.StatusInternalServerError && h.d.Logger != nil {
		h.d.Logger.ErrorContext(r.Context(), "process screen failed", "path", r.URL.Path, "error", err)
	}
	http.Error(w, userMessage(err), status)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, domain.ErrInvalidPipeline),
		errors.Is(err, service.ErrProcessDefault),
		errors.Is(err, service.ErrProcessName),
		errors.Is(err, service.ErrProcessSource):
		return http.StatusUnprocessableEntity
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

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	list, err := h.d.Processes.List(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, listPage(h.page(r, "Hiring processes"), listView{
		Processes: list, Library: h.d.Processes.Library(), CanEdit: canEdit(p),
	}))
}

func canEdit(p service.Principal) bool {
	return p.HasRole(service.RoleRecruiter) || p.HasRole(service.RoleAdmin)
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	in := service.ProcessInput{Name: r.PostFormValue("name"), Description: r.PostFormValue("description")}
	from := service.ProcessSource{LibraryKey: strings.TrimSpace(r.PostFormValue("library_key"))}
	if raw := strings.TrimSpace(r.PostFormValue("copy_of")); raw != "" {
		if id, err := uuid.Parse(raw); err == nil {
			from.ProcessID = id
		}
	}
	// A copy takes the source's name when the form gave none, so one click
	// on a library card is enough.
	if strings.TrimSpace(in.Name) == "" && from.LibraryKey != "" {
		if spec, ok := domain.ProcessByKey(from.LibraryKey); ok {
			in.Name = spec.Name
		}
	}
	proc, err := h.d.Processes.Create(r.Context(), p, in, from)
	if err != nil {
		if statusFor(err) == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		list, lerr := h.d.Processes.List(r.Context(), p)
		if lerr != nil {
			h.fail(w, r, lerr)
			return
		}
		render(w, r, statusFor(err), listPage(h.page(r, "Hiring processes", layout.Flash{Kind: "error", Message: userMessage(err)}), listView{
			Processes: list, Library: h.d.Processes.Library(), CanEdit: true, Draft: in,
		}))
		return
	}
	http.Redirect(w, r, Path(proc.ID), http.StatusSeeOther)
}

func (h *handlers) show(w http.ResponseWriter, r *http.Request) {
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	h.renderProcess(w, r, id, http.StatusOK, "", nil)
}

// renderProcess draws the editor page, or just the editor for an htmx
// request, with a refused change's message above it.
func (h *handlers) renderProcess(w http.ResponseWriter, r *http.Request, id uuid.UUID, status int, message string, flashes []layout.Flash) {
	p, _ := middleware.PrincipalFrom(r.Context())
	proc, err := h.d.Processes.Get(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	editor := h.editor(r, p, proc, message)
	if r.Header.Get("HX-Request") != "" {
		render(w, r, status, stages.View(editor))
		return
	}
	render(w, r, status, processPage(h.page(r, proc.Name, flashes...), proc, editor, canEdit(p)))
}

func (h *handlers) editor(r *http.Request, p service.Principal, proc service.Process, message string) stages.Editor {
	id := proc.ID
	return stages.Editor{
		ID: "process-stages", Stages: proc.Stages, Error: message, CSRF: middleware.CSRFToken(r),
		ReadOnly:    !canEdit(p),
		OrderAction: Path(id) + "/stages/order",
		AddAction:   Path(id) + "/stages",
		StageAction: func(stageID uuid.UUID) string { return Path(id) + "/stages/" + stageID.String() },
		DeleteAction: func(stageID uuid.UUID) string {
			return Path(id) + "/stages/" + stageID.String() + "/delete"
		},
	}
}

// afterChange re-renders the editor after a stage change, htmx or not.
func (h *handlers) afterChange(w http.ResponseWriter, r *http.Request, id uuid.UUID, opErr error) {
	status, message := http.StatusOK, ""
	if opErr != nil {
		status, message = statusFor(opErr), userMessage(opErr)
		if status == http.StatusInternalServerError {
			h.fail(w, r, opErr)
			return
		}
	}
	h.renderProcess(w, r, id, status, message, nil)
}

func (h *handlers) update(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	in := service.ProcessInput{Name: r.PostFormValue("name"), Description: r.PostFormValue("description")}
	if _, err := h.d.Processes.Update(r.Context(), p, id, in); err != nil {
		if statusFor(err) == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		h.renderProcess(w, r, id, statusFor(err), "", []layout.Flash{{Kind: "error", Message: userMessage(err)}})
		return
	}
	h.renderProcess(w, r, id, http.StatusOK, "", []layout.Flash{{Kind: "success", Message: "Saved."}})
}

func (h *handlers) setDefault(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	if err := h.d.Processes.SetDefault(r.Context(), p, id); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, Prefix, http.StatusSeeOther)
}

func (h *handlers) remove(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	if err := h.d.Processes.Delete(r.Context(), p, id); err != nil {
		if errors.Is(err, service.ErrProcessDefault) {
			h.renderProcess(w, r, id, http.StatusUnprocessableEntity, "", []layout.Flash{{Kind: "error", Message: userMessage(err)}})
			return
		}
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, Prefix, http.StatusSeeOther)
}

func (h *handlers) addStage(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	_, err := h.d.Processes.AddStage(r.Context(), p, id, stages.FromForm(r))
	h.afterChange(w, r, id, err)
}

func (h *handlers) updateStage(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	stageID, ok := param(w, r, "stageID")
	if !ok {
		return
	}
	_, err := h.d.Processes.UpdateStage(r.Context(), p, id, stageID, stages.FromForm(r))
	h.afterChange(w, r, id, err)
}

func (h *handlers) deleteStage(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	stageID, ok := param(w, r, "stageID")
	if !ok {
		return
	}
	h.afterChange(w, r, id, h.d.Processes.DeleteStage(r.Context(), p, id, stageID))
}

func (h *handlers) reorderStages(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	order := make([]uuid.UUID, 0, len(r.PostForm["stage"]))
	for _, raw := range r.PostForm["stage"] {
		stageID, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, "bad stage id", http.StatusBadRequest)
			return
		}
		order = append(order, stageID)
	}
	h.afterChange(w, r, id, h.d.Processes.ReorderStages(r.Context(), p, id, order))
}
