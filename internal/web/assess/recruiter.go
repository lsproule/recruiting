package assess

import (
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

// Prefix is where the recruiter's assessment screens live.
const Prefix = "/app/assessments"

// AttachPath is the screen that attaches an assessment to a job's stage;
// the jobs stage editor links to it with ?job=&stage=.
const AttachPath = Prefix + "/attach"

// RecruiterDeps is what MountRecruiter needs.
type RecruiterDeps struct {
	Assessments *service.AssessmentService
	Problems    *service.ProblemService
	Jobs        *service.JobService
	Org         *service.OrgService
	Logger      *slog.Logger
}

// MountRecruiter registers the assessment CRUD and stage attachment screens
// on r. It expects the shared auth middleware to be installed already.
func MountRecruiter(r chi.Router, d RecruiterDeps) {
	h := &recruiter{d: d}
	r.Route(Prefix, func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		r.Use(requireRecruiter)
		r.Get("/", h.list)
		r.Get("/new", h.form)
		r.Post("/", h.create)
		r.Get("/attach", h.attachForm)
		r.Post("/attach", h.attach)
		r.Get("/{id}", h.detail)
		r.Get("/{id}/edit", h.form)
		r.Post("/{id}", h.update)
		r.Post("/{id}/delete", h.remove)
	})
}

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

type recruiter struct{ d RecruiterDeps }

func (h *recruiter) page(r *http.Request, title string) layout.Page {
	p, _ := middleware.PrincipalFrom(r.Context())
	return layout.Page{Title: title, Surface: layout.SurfaceApp, Nav: layout.AppNav(p, Prefix), CSRF: middleware.CSRFToken(r), UserName: h.displayName(r, p)}
}

func (h *recruiter) displayName(r *http.Request, p service.Principal) string {
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

func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrAssessmentInvalid), errors.Is(err, service.ErrAssessmentInUse), errors.Is(err, service.ErrStageNotAssessment):
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError
}

func (h *recruiter) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(err)
	if status == http.StatusInternalServerError {
		if h.d.Logger != nil {
			h.d.Logger.ErrorContext(r.Context(), "assessment screen failed", "path", r.URL.Path, "error", err)
		}
		http.Error(w, "Something went wrong. Try again.", status)
		return
	}
	http.Error(w, userMessage(err), status)
}

// assessmentForm is what the create and edit screens render and post. The
// problem picker filters the bank by tag and difficulty; picked problems
// stay picked across filter changes because they are posted back as hidden
// order fields.
type assessmentForm struct {
	ID               uuid.UUID
	New              bool
	Name             string
	DurationMinutes  int
	LanguageOverride string
	InviteWindowDays int
	// Picked is the chosen problems in order.
	Picked []service.Problem
	// Filter narrows the bank listing below the picked set.
	Filter service.ProblemFilter
	Bank   []service.Problem
}

func (f assessmentForm) action() string {
	if f.New {
		return Prefix
	}
	return Prefix + "/" + f.ID.String()
}

func (f assessmentForm) picked(id uuid.UUID) bool {
	for _, p := range f.Picked {
		if p.ID == id {
			return true
		}
	}
	return false
}

func (f assessmentForm) input() service.AssessmentInput {
	in := service.AssessmentInput{Name: f.Name, DurationMinutes: f.DurationMinutes, LanguageOverride: f.LanguageOverride, InviteWindowDays: f.InviteWindowDays}
	for _, p := range f.Picked {
		in.ProblemIDs = append(in.ProblemIDs, p.ID)
	}
	return in
}

func (h *recruiter) list(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	found, err := h.d.Assessments.List(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, listPage(h.page(r, "Assessments"), found))
}

func (h *recruiter) detail(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	a, err := h.d.Assessments.Get(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, detailPage(h.page(r, a.Name), a))
}

// form serves both the new and the edit screen, and re-renders on a filter
// change (GET with the current field values carried in the query).
func (h *recruiter) form(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	f := assessmentForm{New: true, DurationMinutes: 60, InviteWindowDays: service.DefaultSettings().AssessmentInviteDays}
	if raw := chi.URLParam(r, "id"); raw != "" {
		id, ok := idParam(w, r, "id")
		if !ok {
			return
		}
		a, err := h.d.Assessments.Get(r.Context(), p, id)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		f = assessmentForm{ID: a.ID, Name: a.Name, DurationMinutes: a.DurationMinutes, LanguageOverride: a.LanguageOverride, InviteWindowDays: a.InviteWindowDays, Picked: a.Problems}
	}
	if r.URL.Query().Has("name") {
		// A filter change re-posts the form as a query so nothing typed is lost.
		f = h.readForm(r, f.ID, f.New)
	}
	f.Filter = service.ProblemFilter{Tag: r.URL.Query().Get("tag"), Difficulty: r.URL.Query().Get("difficulty"), Query: r.URL.Query().Get("q")}
	h.renderForm(w, r, f, nil)
}

func (h *recruiter) renderForm(w http.ResponseWriter, r *http.Request, f assessmentForm, problems []string) {
	p, _ := middleware.PrincipalFrom(r.Context())
	bank, err := h.d.Problems.List(r.Context(), p, f.Filter)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	f.Bank = bank
	title := "New assessment"
	if !f.New {
		title = "Edit " + f.Name
	}
	status := http.StatusOK
	if len(problems) > 0 {
		status = http.StatusUnprocessableEntity
	}
	render(w, r, status, formPage(h.page(r, title), f, problems))
}

// readForm parses the posted (or query-carried) form. Picked problems come
// as problem_id[]=<uuid> with order[]=<n> alongside; unknown ids are dropped.
func (h *recruiter) readForm(r *http.Request, id uuid.UUID, isNew bool) assessmentForm {
	p, _ := middleware.PrincipalFrom(r.Context())
	values := r.URL.Query()
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		values = r.PostForm
	}
	f := assessmentForm{ID: id, New: isNew, Name: values.Get("name"), LanguageOverride: values.Get("language_override")}
	f.DurationMinutes, _ = strconv.Atoi(values.Get("duration_minutes"))
	f.InviteWindowDays, _ = strconv.Atoi(values.Get("invite_window_days"))
	type pick struct {
		id    uuid.UUID
		order int
	}
	var picks []pick
	ids := values["problem_id"]
	orders := values["order"]
	for i, raw := range ids {
		pid, err := uuid.Parse(raw)
		if err != nil {
			continue
		}
		order := i + 1
		if i < len(orders) {
			if n, err := strconv.Atoi(orders[i]); err == nil && n > 0 {
				order = n
			}
		}
		picks = append(picks, pick{pid, order})
	}
	sort.SliceStable(picks, func(i, j int) bool { return picks[i].order < picks[j].order })
	for _, pk := range picks {
		problem, err := h.d.Problems.Get(r.Context(), p, pk.id)
		if err != nil {
			continue
		}
		f.Picked = append(f.Picked, problem)
	}
	return f
}

func (h *recruiter) create(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	f := h.readForm(r, uuid.Nil, true)
	created, err := h.d.Assessments.Create(r.Context(), p, f.input())
	if err != nil {
		h.formError(w, r, f, err)
		return
	}
	http.Redirect(w, r, Prefix+"/"+created.ID.String(), http.StatusSeeOther)
}

func (h *recruiter) update(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	f := h.readForm(r, id, false)
	if _, err := h.d.Assessments.Update(r.Context(), p, id, f.input()); err != nil {
		h.formError(w, r, f, err)
		return
	}
	http.Redirect(w, r, Prefix+"/"+id.String(), http.StatusSeeOther)
}

func (h *recruiter) formError(w http.ResponseWriter, r *http.Request, f assessmentForm, err error) {
	if statusFor(err) == http.StatusInternalServerError {
		h.fail(w, r, err)
		return
	}
	msg := strings.TrimPrefix(userMessage(err), "invalid assessment: ")
	h.renderForm(w, r, f, strings.Split(msg, "; "))
}

func (h *recruiter) remove(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.d.Assessments.Delete(r.Context(), p, id); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, Prefix, http.StatusSeeOther)
}

// attachView is the stage attachment screen: the job, its assessment
// stages, and the assessments to choose from.
type attachView struct {
	Job         service.Job
	Stages      []domain.Stage
	StageID     uuid.UUID
	Current     *service.Assessment
	Assessments []service.Assessment
}

func (h *recruiter) attachForm(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	jobID, err := uuid.Parse(r.URL.Query().Get("job"))
	if err != nil {
		http.Error(w, "bad job", http.StatusBadRequest)
		return
	}
	stageID, _ := uuid.Parse(r.URL.Query().Get("stage"))
	v, err := h.loadAttach(r, p, jobID, stageID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, attachPage(h.page(r, "Attach assessment"), v))
}

func (h *recruiter) loadAttach(r *http.Request, p service.Principal, jobID, stageID uuid.UUID) (attachView, error) {
	job, err := h.d.Jobs.Job(r.Context(), p, jobID)
	if err != nil {
		return attachView{}, err
	}
	stages, err := h.d.Jobs.Stages(r.Context(), p, jobID)
	if err != nil {
		return attachView{}, err
	}
	v := attachView{Job: job, StageID: stageID}
	for _, st := range stages {
		if st.Kind == domain.StageAssessment {
			v.Stages = append(v.Stages, st)
		}
	}
	if v.StageID == uuid.Nil && len(v.Stages) > 0 {
		v.StageID = v.Stages[0].ID
	}
	if v.StageID != uuid.Nil {
		if v.Current, err = h.d.Assessments.StageAssessment(r.Context(), p, v.StageID); err != nil {
			return attachView{}, err
		}
	}
	v.Assessments, err = h.d.Assessments.List(r.Context(), p)
	return v, err
}

func (h *recruiter) attach(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	jobID, err := uuid.Parse(r.PostFormValue("job_id"))
	if err != nil {
		http.Error(w, "bad job", http.StatusBadRequest)
		return
	}
	stageID, err := uuid.Parse(r.PostFormValue("stage_id"))
	if err != nil {
		http.Error(w, "bad stage", http.StatusBadRequest)
		return
	}
	// An empty assessment detaches.
	assessmentID, _ := uuid.Parse(r.PostFormValue("assessment_id"))
	if err := h.d.Assessments.AttachToStage(r.Context(), p, jobID, stageID, assessmentID); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, AttachPath+"?job="+jobID.String()+"&stage="+stageID.String(), http.StatusSeeOther)
}

func idParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		http.Error(w, "bad "+name, http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}
