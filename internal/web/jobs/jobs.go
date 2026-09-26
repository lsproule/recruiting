// Package jobs serves the recruiter's job screens: the job list, the job
// form, and the per-job pipeline editor. Handlers call internal/service only.
package jobs

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
	"recruiting/internal/web/assess"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
	"recruiting/internal/web/scorecards"
	"recruiting/internal/web/stages"
)

// Prefix is where the job screens live on the app surface.
const Prefix = "/app/jobs"

// Deps is what Mount needs. Org supplies the client companies a job is
// created for and the signed-in user's display name.
type Deps struct {
	Jobs *service.JobService
	Org  *service.OrgService
	// Logger records the errors behind a 500; the visitor only ever sees a
	// generic message. Nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the job screens on r, plus the app surface's landing
// redirect. It expects the shared auth middleware (CSRF and Authenticate) to
// be installed already.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.With(middleware.RequireAuth("/app/login")).Get("/app/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, Prefix, http.StatusSeeOther)
	})
	r.Route(Prefix, func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		// Every org user reads jobs and pipelines — a vetter needs to see the
		// stage they are scoring in. Changing one is a recruiter's or an
		// admin's, which is where the service draws the line too.
		r.Get("/", h.list)
		r.Get("/{id}", h.edit)
		r.Get("/{id}/pipeline", h.pipeline)
		r.Group(func(r chi.Router) {
			r.Use(requireRecruiter)
			r.Get("/new", h.newJob)
			r.Post("/", h.create)
			r.Post("/{id}", h.update)
			r.Post("/{id}/stages", h.addStage)
			r.Post("/{id}/stages/order", h.reorderStages)
			r.Post("/{id}/stages/{stageID}", h.updateStage)
			r.Post("/{id}/stages/{stageID}/delete", h.deleteStage)
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

// fail answers with the error's status, logging the cause of a 500 rather than
// showing it: the visitor gets a generic message either way.
func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(err)
	if status == http.StatusInternalServerError && h.d.Logger != nil {
		h.d.Logger.ErrorContext(r.Context(), "job screen failed", "path", r.URL.Path, "error", err)
	}
	http.Error(w, userMessage(err), status)
}

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = c.Render(r.Context(), w)
}

func (h *handlers) page(r *http.Request, title, current string, flashes ...layout.Flash) layout.Page {
	p, _ := middleware.PrincipalFrom(r.Context())
	return layout.Page{
		Title: title, Surface: layout.SurfaceApp, Nav: layout.AppNav(p, current), UserRole: layout.RoleLabel(p), Menu: layout.AppMenu(p, current),
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

// statusFor maps a service error onto the response status: rejected input is
// 422, a missing row 404, and anything else a 500.
func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, domain.ErrInvalidPipeline),
		errors.Is(err, service.ErrStageOccupied),
		errors.Is(err, service.ErrTitleRequired),
		errors.Is(err, service.ErrSlugTaken),
		errors.Is(err, service.ErrInvalidJob),
		errors.Is(err, service.ErrCompanyRequired),
		errors.Is(err, service.ErrNoTemplate):
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
	list, err := h.d.Jobs.ListJobs(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, jobsPage(h.page(r, "Jobs", Prefix), list))
}

func (h *handlers) newJob(w http.ResponseWriter, r *http.Request) {
	h.renderForm(w, r, http.StatusOK, jobForm{Action: Prefix, Submit: "Create job", Job: service.Job{Status: service.JobDraft}})
}

// renderForm draws the job form, loading the client companies it needs.
func (h *handlers) renderForm(w http.ResponseWriter, r *http.Request, status int, form jobForm) {
	p, _ := middleware.PrincipalFrom(r.Context())
	// A reader without the recruiter role sees the job's own values with no
	// client list to choose from; the form's posts are refused anyway.
	companies, err := h.d.Jobs.ClientCompanies(r.Context(), p)
	if err != nil && !errors.Is(err, service.ErrForbidden) {
		h.fail(w, r, err)
		return
	}
	form.Companies = companies
	title := "New job"
	if form.Job.ID != uuid.Nil {
		title = form.Job.Title
	}
	render(w, r, status, jobFormPage(h.page(r, title, Prefix), form))
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	in, err := jobFromForm(r)
	var job service.Job
	if err == nil {
		job, err = h.d.Jobs.CreateJob(r.Context(), p, in)
	}
	if err != nil {
		h.renderForm(w, r, statusFor(err), jobForm{
			Action: Prefix, Submit: "Create job", Job: jobFromInput(uuid.Nil, in), Error: userMessage(err),
		})
		return
	}
	http.Redirect(w, r, Prefix+"/"+job.ID.String()+"/pipeline", http.StatusSeeOther)
}

func (h *handlers) edit(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	job, err := h.d.Jobs.Job(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderForm(w, r, http.StatusOK, jobForm{Action: Prefix + "/" + id.String(), Submit: "Save job", Job: job})
}

func (h *handlers) update(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	in, err := jobFromForm(r)
	var job service.Job
	if err == nil {
		job, err = h.d.Jobs.UpdateJob(r.Context(), p, id, in)
	}
	if err != nil {
		h.renderForm(w, r, statusFor(err), jobForm{
			Action: Prefix + "/" + id.String(), Submit: "Save job",
			Job: jobFromInput(id, in), Error: userMessage(err),
		})
		return
	}
	h.renderForm(w, r, http.StatusOK, jobForm{
		Action: Prefix + "/" + id.String(), Submit: "Save job", Job: job, Saved: true,
	})
}

func (h *handlers) pipeline(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	job, err := h.d.Jobs.Job(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	list, err := h.d.Jobs.Stages(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, pipelinePage(h.page(r, job.Title+" pipeline", Prefix), job, h.editor(r, p, job, list, "")))
}

// editor is the shared stage editor bound to this job's routes.
func (h *handlers) editor(r *http.Request, p service.Principal, job service.Job, list []domain.Stage, message string) stages.Editor {
	jobID := job.ID
	recruiter := p.HasRole(service.RoleRecruiter) || p.HasRole(service.RoleAdmin)
	return stages.Editor{
		ID: "pipeline", Stages: list, Vetters: h.vetters(r, p), Error: message, CSRF: middleware.CSRFToken(r),
		ReadOnly:    !recruiter,
		OrderAction: Prefix + "/" + jobID.String() + "/stages/order",
		AddAction:   Prefix + "/" + jobID.String() + "/stages",
		StageAction: func(stageID uuid.UUID) string { return stagePath(jobID.String(), stageID.String()) },
		DeleteAction: func(stageID uuid.UUID) string {
			return stagePath(jobID.String(), stageID.String()) + "/delete"
		},
		SetupLink: func(s domain.Stage) (string, string) {
			switch s.Kind {
			case domain.StageInterview:
				return "Rubric", scorecards.RubricPath(jobID, s.ID)
			case domain.StageAssessment:
				return "Assessment", assess.AttachStagePath(jobID, s.ID)
			}
			return "", ""
		},
	}
}

// vetters is who the editor offers as a stage's default interviewer. A
// reader who may not list them (a vetter looking at the pipeline) gets an
// empty list rather than a failed page; the editor is not theirs to use.
func (h *handlers) vetters(r *http.Request, p service.Principal) []service.OrgUser {
	if h.d.Org == nil {
		return nil
	}
	vetters, err := h.d.Org.Vetters(r.Context(), p)
	if err != nil {
		return nil
	}
	return vetters
}

// afterStageChange re-renders the editor. htmx swaps the fragment in place;
// a plain form post gets the whole page back.
func (h *handlers) afterStageChange(w http.ResponseWriter, r *http.Request, jobID uuid.UUID, opErr error) {
	p, _ := middleware.PrincipalFrom(r.Context())
	status := http.StatusOK
	message := ""
	if opErr != nil {
		status, message = statusFor(opErr), userMessage(opErr)
		if status == http.StatusInternalServerError {
			h.fail(w, r, opErr)
			return
		}
	}
	job, err := h.d.Jobs.Job(r.Context(), p, jobID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	list, err := h.d.Jobs.Stages(r.Context(), p, jobID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	editor := h.editor(r, p, job, list, message)
	if r.Header.Get("HX-Request") != "" {
		render(w, r, status, stages.View(editor))
		return
	}
	render(w, r, status, pipelinePage(h.page(r, job.Title+" pipeline", Prefix), job, editor))
}

func (h *handlers) addStage(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	_, err := h.d.Jobs.AddStage(r.Context(), p, id, stages.FromForm(r))
	h.afterStageChange(w, r, id, err)
}

func (h *handlers) updateStage(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	stageID, err := uuid.Parse(chi.URLParam(r, "stageID"))
	if err != nil {
		http.Error(w, "bad stage id", http.StatusBadRequest)
		return
	}
	_, err = h.d.Jobs.UpdateStage(r.Context(), p, id, stageID, stages.FromForm(r))
	h.afterStageChange(w, r, id, err)
}

func (h *handlers) deleteStage(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	stageID, err := uuid.Parse(chi.URLParam(r, "stageID"))
	if err != nil {
		http.Error(w, "bad stage id", http.StatusBadRequest)
		return
	}
	h.afterStageChange(w, r, id, h.d.Jobs.DeleteStage(r.Context(), p, id, stageID))
}

func (h *handlers) reorderStages(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := jobID(w, r)
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
	h.afterStageChange(w, r, id, h.d.Jobs.ReorderStages(r.Context(), p, id, order))
}

func jobID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "bad job id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func jobFromForm(r *http.Request) (service.NewJob, error) {
	if err := r.ParseForm(); err != nil {
		return service.NewJob{}, service.ErrInvalidJob
	}
	in := service.NewJob{
		Title:        r.PostFormValue("title"),
		Slug:         r.PostFormValue("slug"),
		Description:  r.PostFormValue("description"),
		Skills:       []string{r.PostFormValue("skills")},
		Seniority:    r.PostFormValue("seniority"),
		Location:     r.PostFormValue("location"),
		RemotePolicy: r.PostFormValue("remote_policy"),
		BlindMode:    r.PostFormValue("blind_mode") != "",
		Status:       r.PostFormValue("status"),
	}
	if raw := strings.TrimSpace(r.PostFormValue("client_company_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return in, service.ErrCompanyRequired
		}
		in.ClientCompanyID = id
	}
	var err error
	if in.SalaryMin, err = salary(r.PostFormValue("salary_min")); err != nil {
		return in, err
	}
	if in.SalaryMax, err = salary(r.PostFormValue("salary_max")); err != nil {
		return in, err
	}
	return in, nil
}

func salary(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, service.ErrInvalidJob
	}
	return n, nil
}

// jobFromInput redraws a rejected form with what the recruiter typed rather
// than what the database holds.
func jobFromInput(id uuid.UUID, in service.NewJob) service.Job {
	return service.Job{
		ID: id, ClientCompanyID: in.ClientCompanyID, Title: in.Title, Slug: in.Slug,
		Description: in.Description, Skills: in.Skills, Seniority: in.Seniority,
		Location: in.Location, RemotePolicy: in.RemotePolicy,
		SalaryMin: in.SalaryMin, SalaryMax: in.SalaryMax,
		BlindMode: in.BlindMode, Status: in.Status,
	}
}
