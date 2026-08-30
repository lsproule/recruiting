// Package pipeline serves the recruiter's pipeline screens: a job's board and
// filterable application list, and the application page with its timeline
// and move form. Handlers call internal/service only.
package pipeline

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

// Where the screens live on the app surface: the board and list are per job,
// the application page per application.
const (
	BoardPrefix       = "/app/pipeline"
	ApplicationPrefix = "/app/applications"
)

// Deps is what Mount needs. Release is the client visibility switch, which
// also queues the client's notice; Schedule assigns an interviewer; Org
// supplies the signed-in user's display name and the vetters to assign.
type Deps struct {
	Applications *service.ApplicationService
	Release      *service.ReleaseService
	Schedule     *service.ScheduleService
	Org          *service.OrgService
	// Logger records the errors behind a 500; the visitor only ever sees a
	// generic message. Nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the pipeline screens on r. It expects the shared auth
// middleware (CSRF and Authenticate) to be installed already. Every org user
// may read the screens; who may move what is the service's decision.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Route(BoardPrefix+"/{jobID}", func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		r.Get("/", h.board)
		r.Get("/list", h.list)
	})
	r.Route(ApplicationPrefix+"/{id}", func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		r.Get("/", h.show)
		r.Post("/move", h.move)
		r.Post("/withdraw", h.withdraw)
		r.Post("/release", h.release)
		r.Post("/unrelease", h.unrelease)
		r.Post("/assign", h.assign)
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
		Title: title, Surface: layout.SurfaceApp, Nav: layout.AppNav(p, "/app/jobs"),
		Flashes: flashes, CSRF: middleware.CSRFToken(r), UserName: h.displayName(r, p),
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

// fail answers with the error's status, logging the cause of a 500 rather than
// showing it.
func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(err)
	if status == http.StatusInternalServerError && h.d.Logger != nil {
		h.d.Logger.ErrorContext(r.Context(), "pipeline screen failed", "path", r.URL.Path, "error", err)
	}
	http.Error(w, userMessage(err), status)
}

// statusFor maps a service error onto the response status: a refused move
// is 422 so the form re-renders with the reason, a missing row 404.
func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden), errors.Is(err, domain.ErrForbiddenMove):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrStale):
		return http.StatusConflict
	case errors.Is(err, domain.ErrPrereqMissing),
		errors.Is(err, domain.ErrReasonRequired),
		errors.Is(err, domain.ErrTerminal),
		errors.Is(err, service.ErrNotActive),
		errors.Is(err, service.ErrNotVetter):
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

func (h *handlers) board(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	jobID, ok := param(w, r, "jobID")
	if !ok {
		return
	}
	board, err := h.d.Applications.Board(r.Context(), p, jobID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, boardPage(h.page(r, board.Job.Title+" board"), board, middleware.CSRFToken(r), moveProblem{}))
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	jobID, ok := param(w, r, "jobID")
	if !ok {
		return
	}
	f := listFilter{
		StageID: r.URL.Query().Get("stage"),
		Status:  r.URL.Query().Get("status"),
		Query:   r.URL.Query().Get("q"),
	}
	filter := service.ListFilter{Status: domain.ApplicationStatus(f.Status), Query: f.Query}
	if f.StageID != "" {
		id, err := uuid.Parse(f.StageID)
		if err != nil {
			http.Error(w, "bad stage", http.StatusBadRequest)
			return
		}
		filter.StageID = id
	}
	board, err := h.d.Applications.Board(r.Context(), p, jobID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	rows, err := h.d.Applications.List(r.Context(), p, jobID, filter)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, listPage(h.page(r, board.Job.Title+" applications"), board, rows, f))
}

func (h *handlers) show(w http.ResponseWriter, r *http.Request) {
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	h.renderApplication(w, r, id, http.StatusOK, moveProblem{}, nil)
}

// renderApplication draws the application page, with the move form showing
// what a refused move asked for so the reason can be added.
func (h *handlers) renderApplication(w http.ResponseWriter, r *http.Request, id uuid.UUID, status int, problem moveProblem, flashes []layout.Flash) {
	p, _ := middleware.PrincipalFrom(r.Context())
	detail, err := h.d.Applications.Detail(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, status, applicationPage(h.page(r, detail.Application.CandidateName, flashes...), detail, h.vetters(r, p), middleware.CSRFToken(r), problem))
}

// vetters is who a recruiter may assign as the interviewer. Anyone who may
// not list them (a vetter reading the page) gets none, and no assign form.
func (h *handlers) vetters(r *http.Request, p service.Principal) []service.OrgUser {
	if h.d.Org == nil || h.d.Schedule == nil {
		return nil
	}
	vetters, err := h.d.Org.Vetters(r.Context(), p)
	if err != nil {
		return nil
	}
	return vetters
}

// move handles both the board's drag (an htmx post that gets the board back)
// and the application page's form. A refused move comes back as 422 with the
// same screen re-rendered around the reason and override fields.
func (h *handlers) move(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	toStage, err := uuid.Parse(strings.TrimSpace(r.PostFormValue("to_stage_id")))
	if err != nil {
		http.Error(w, "bad stage id", http.StatusBadRequest)
		return
	}
	req := service.MoveRequest{
		ApplicationID:  id,
		ToStageID:      toStage,
		Reason:         r.PostFormValue("reason"),
		OverridePrereq: r.PostFormValue("override") != "",
	}
	app, err := h.d.Applications.Move(r.Context(), p, req)
	problem := moveProblem{}
	status := http.StatusOK
	if err != nil {
		status = statusFor(err)
		if status == http.StatusInternalServerError || status == http.StatusNotFound {
			h.fail(w, r, err)
			return
		}
		// htmx swaps a 422 back into the board (see boardScript); a refused
		// drag has to show its message the same way, so it goes out as 422.
		if r.PostFormValue("view") == "board" && (status == http.StatusForbidden || status == http.StatusConflict) {
			status = http.StatusUnprocessableEntity
		}
		problem = moveProblem{
			ApplicationID: id, ToStageID: toStage, Reason: req.Reason, Message: userMessage(err),
			NeedsOverride: errors.Is(err, domain.ErrPrereqMissing) || (req.OverridePrereq && errors.Is(err, domain.ErrReasonRequired)),
			NeedsReason:   errors.Is(err, domain.ErrReasonRequired),
		}
	}
	if r.PostFormValue("view") == "board" {
		jobID := app.JobID
		if err != nil {
			current, aErr := h.d.Applications.Application(r.Context(), p, id)
			if aErr != nil {
				h.fail(w, r, aErr)
				return
			}
			jobID = current.JobID
		}
		board, bErr := h.d.Applications.Board(r.Context(), p, jobID)
		if bErr != nil {
			h.fail(w, r, bErr)
			return
		}
		csrf := middleware.CSRFToken(r)
		if r.Header.Get("HX-Request") != "" {
			render(w, r, status, boardView(board, csrf, problem))
			return
		}
		render(w, r, status, boardPage(h.page(r, board.Job.Title+" board"), board, csrf, problem))
		return
	}
	if err != nil {
		h.renderApplication(w, r, id, status, problem, nil)
		return
	}
	http.Redirect(w, r, ApplicationPrefix+"/"+id.String(), http.StatusSeeOther)
}

func (h *handlers) withdraw(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	_, err := h.d.Applications.Withdraw(r.Context(), p, id, r.PostFormValue("reason"))
	h.afterAction(w, r, id, err)
}

func (h *handlers) release(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	_, err := h.d.Release.Release(r.Context(), p, id)
	h.afterAction(w, r, id, err)
}

func (h *handlers) unrelease(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	_, err := h.d.Release.Unrelease(r.Context(), p, id)
	h.afterAction(w, r, id, err)
}

// assign names the interviewer for the application.
func (h *handlers) assign(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	vetterID, err := uuid.Parse(strings.TrimSpace(r.PostFormValue("vetter_id")))
	if err != nil {
		http.Error(w, "bad vetter id", http.StatusBadRequest)
		return
	}
	h.afterAction(w, r, id, h.d.Schedule.Assign(r.Context(), p, id, vetterID))
}

// afterAction redirects back to the application page, or re-renders it with
// the refusal when the action was not allowed.
func (h *handlers) afterAction(w http.ResponseWriter, r *http.Request, id uuid.UUID, err error) {
	if err == nil {
		http.Redirect(w, r, ApplicationPrefix+"/"+id.String(), http.StatusSeeOther)
		return
	}
	status := statusFor(err)
	if status != http.StatusUnprocessableEntity && status != http.StatusConflict {
		h.fail(w, r, err)
		return
	}
	h.renderApplication(w, r, id, status, moveProblem{}, []layout.Flash{{Kind: "error", Message: userMessage(err)}})
}
