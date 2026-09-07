// Package intake serves the recruiter's client-intake wizard: the five steps
// that turn a verbal agreement into a client company, its first portal user,
// a job with its pipeline, and the assessment that pipeline sends. Handlers
// call internal/service only.
package intake

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
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

// Prefix is where a draft's steps live on the app surface.
const Prefix = "/app/intake"

// StartPath opens the recruiter's intake, resuming the draft they already
// have. It is what the sidebar's intake button points at.
const StartPath = "/app/clients/new"

// ProblemParam names the problem an authoring detour hands back, so a
// question written mid-intake lands in the set that sent the recruiter away.
const ProblemParam = "problem"

// Deps is what Mount needs. Org supplies the signed-in user's display name.
type Deps struct {
	Intake *service.IntakeService
	// Problems is the bank a hand-assembled set is picked from.
	Problems *service.ProblemService
	Org      *service.OrgService
	// Logger records the errors behind a 500; the visitor only ever sees a
	// generic message. Nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the intake screens on r. It expects the shared auth
// middleware (CSRF and Authenticate) to be installed already.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		// An intake creates a client and a job, which is a recruiter's work.
		r.Use(requireRecruiter)
		r.Get(StartPath, h.start)
		r.Route(Prefix, func(r chi.Router) {
			r.Get("/{draft}", h.show)
			r.Post("/{draft}/create", h.create)
			r.Post("/{draft}/discard", h.discard)
			r.Post("/{draft}/{step}", h.step)
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

// fail answers with the error's status, logging the cause of a 500 rather
// than showing it.
func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(err)
	if status == http.StatusInternalServerError && h.d.Logger != nil {
		h.d.Logger.ErrorContext(r.Context(), "intake failed", "path", r.URL.Path, "error", err)
	}
	http.Error(w, userMessage(err), status)
}

func (h *handlers) page(r *http.Request, title string) layout.Page {
	p, _ := middleware.PrincipalFrom(r.Context())
	return layout.Page{
		Title: title, Surface: layout.SurfaceApp,
		Nav:      layout.AppNav(p, "/app/clients"),
		Menu:     layout.AppMenu(p, "/app/clients"),
		UserRole: layout.RoleLabel(p), CSRF: middleware.CSRFToken(r),
		UserName: h.displayName(r, p),
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

// statusFor maps a service error onto the response status: an answer the
// intake cannot use is 422, a missing draft 404, anything else a 500.
func statusFor(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, service.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrIntakeInvalid),
		errors.Is(err, service.ErrTemplateNoAssessment),
		errors.Is(err, service.ErrNoTemplate),
		errors.Is(err, service.ErrEmailTaken),
		errors.Is(err, service.ErrSlugTaken),
		errors.Is(err, service.ErrProblemQuality),
		errors.Is(err, service.ErrNoLanguage),
		errors.Is(err, service.ErrAssessmentInvalid),
		errors.Is(err, domain.ErrInvalidPipeline):
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

// start opens the recruiter's intake and sends them to it. A problem written
// during the intake comes back here, and is folded into the set on the way.
func (h *handlers) start(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	draft, err := h.d.Intake.Open(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if id, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get(ProblemParam))); err == nil {
		draft, err = h.adoptProblem(r, p, draft, id)
		if err != nil {
			h.fail(w, r, err)
			return
		}
	}
	http.Redirect(w, r, draftPath(draft.ID), http.StatusSeeOther)
}

// adoptProblem puts a freshly authored problem into the draft's set and
// leaves the recruiter on the step that sent them off to write it.
func (h *handlers) adoptProblem(r *http.Request, p service.Principal, draft service.IntakeDraft, problemID uuid.UUID) (service.IntakeDraft, error) {
	in := draft.Payload
	in.Assessment.Source = service.IntakeSourceAuthor
	for _, existing := range in.Assessment.ProblemIDs {
		if existing == problemID {
			return draft, nil
		}
	}
	in.Assessment.ProblemIDs = append(in.Assessment.ProblemIDs, problemID)
	return h.d.Intake.SaveStep(r.Context(), p, draft.ID, service.IntakeStepQuestion, in, service.IntakeStepQuestion)
}

func (h *handlers) show(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := draftID(w, r)
	if !ok {
		return
	}
	draft, err := h.d.Intake.Draft(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	view, err := h.view(r, p, draft, "")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, wizardPage(h.page(r, "New client intake"), view))
}

// step stores the panel that was posted and answers with the wizard on the
// step the recruiter asked for. htmx swaps the wizard in place; a plain post
// gets the whole page back.
func (h *handlers) step(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := draftID(w, r)
	if !ok {
		return
	}
	step, err := strconv.Atoi(chi.URLParam(r, "step"))
	if err != nil {
		http.Error(w, "bad step", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	goTo := step + 1
	if raw := strings.TrimSpace(r.PostFormValue("goto")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			goTo = n
		}
	}

	draft, saveErr := h.d.Intake.SaveStep(r.Context(), p, id, step, payloadFromForm(r, step), goTo)
	if saveErr != nil {
		if statusFor(saveErr) == http.StatusInternalServerError {
			h.fail(w, r, saveErr)
			return
		}
		// A refused step keeps the recruiter on it, with what they typed and
		// the reason it cannot be handed on.
		stored, err := h.d.Intake.Draft(r.Context(), p, id)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		stored.Step = step
		stored.Payload = mergeStep(stored.Payload, step, payloadFromForm(r, step))
		h.renderWizard(w, r, p, stored, userMessage(saveErr), statusFor(saveErr))
		return
	}
	h.renderWizard(w, r, p, draft, "", http.StatusOK)
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := draftID(w, r)
	if !ok {
		return
	}
	result, err := h.d.Intake.Create(r.Context(), p, id)
	if err != nil {
		if statusFor(err) == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		draft, loadErr := h.d.Intake.Draft(r.Context(), p, id)
		if loadErr != nil {
			h.fail(w, r, loadErr)
			return
		}
		h.renderWizard(w, r, p, draft, userMessage(err), statusFor(err))
		return
	}
	http.Redirect(w, r, "/app/jobs/"+result.JobID.String()+"/pipeline", http.StatusSeeOther)
}

func (h *handlers) discard(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := draftID(w, r)
	if !ok {
		return
	}
	if err := h.d.Intake.Discard(r.Context(), p, id); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/app/jobs", http.StatusSeeOther)
}

// renderWizard draws the wizard, as a fragment for htmx and as a whole page
// for a plain form post.
func (h *handlers) renderWizard(w http.ResponseWriter, r *http.Request, p service.Principal, draft service.IntakeDraft, message string, status int) {
	view, err := h.view(r, p, draft, message)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if r.Header.Get("HX-Request") != "" {
		render(w, r, status, wizard(view))
		return
	}
	render(w, r, status, wizardPage(h.page(r, "New client intake"), view))
}

// view loads what the draft's current step needs to draw itself. The step
// decides what is loaded: the bank is a listing nobody on step one wants.
func (h *handlers) view(r *http.Request, p service.Principal, draft service.IntakeDraft, message string) (wizardView, error) {
	out := wizardView{Draft: draft, Error: message, CSRF: middleware.CSRFToken(r)}
	switch draft.Step {
	case service.IntakeStepJob:
		templates, err := h.d.Intake.Templates(r.Context(), p)
		if err != nil {
			return out, err
		}
		out.Templates = templates
	case service.IntakeStepQuestion, service.IntakeStepReview:
		slots, err := h.d.Intake.Set(r.Context(), p, draft.ID)
		if err != nil {
			return out, err
		}
		out.Slots = slots
		bank, err := h.bank(r, p)
		if err != nil {
			return out, err
		}
		out.Bank = bank
	}
	return out, nil
}

// bank is what a hand-assembled set may draw on: the problems that cleared
// quality review, which are the only ones an assessment will take.
func (h *handlers) bank(r *http.Request, p service.Principal) ([]service.Problem, error) {
	if h.d.Problems == nil {
		return nil, nil
	}
	all, err := h.d.Problems.List(r.Context(), p, service.ProblemFilter{Kind: domain.ProblemKindCode})
	if err != nil {
		return nil, err
	}
	out := make([]service.Problem, 0, len(all))
	for _, pr := range all {
		if pr.Attachable() {
			out = append(out, pr)
		}
	}
	return out, nil
}

func draftID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "draft"))
	if err != nil {
		http.Error(w, "bad draft id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

// payloadFromForm reads the posted panel. Only the step's own fields are
// read, because only its panel was on the page.
func payloadFromForm(r *http.Request, step int) service.IntakePayload {
	var out service.IntakePayload
	switch step {
	case service.IntakeStepClient:
		out.Client = service.IntakeClient{
			Company:          r.PostFormValue("company"),
			Industry:         r.PostFormValue("industry"),
			ContactName:      r.PostFormValue("contact_name"),
			ContactEmail:     r.PostFormValue("contact_email"),
			ShortlistSLADays: atoiOr(r.PostFormValue("shortlist_sla_days"), 0),
			Brief:            r.PostFormValue("brief"),
		}
	case service.IntakeStepJob:
		out.Job = service.IntakeJob{
			Title:        r.PostFormValue("title"),
			Seniority:    r.PostFormValue("seniority"),
			Location:     r.PostFormValue("location"),
			RemotePolicy: r.PostFormValue("remote_policy"),
			SalaryMin:    atoiOr(r.PostFormValue("salary_min"), 0),
			SalaryMax:    atoiOr(r.PostFormValue("salary_max"), 0),
		}
		// An unparseable id is the org's default template, which is what the
		// select offers first anyway.
		out.Job.TemplateID, _ = uuid.Parse(strings.TrimSpace(r.PostFormValue("template_id")))
	case service.IntakeStepSkills:
		out.Skills = r.PostForm["skills"]
	case service.IntakeStepQuestion:
		out.Assessment = service.IntakeAssessment{
			Source:          r.PostFormValue("source"),
			DurationMinutes: atoiOr(r.PostFormValue("duration_minutes"), 0),
		}
		for _, raw := range r.PostForm["problem_id"] {
			id, err := uuid.Parse(strings.TrimSpace(raw))
			if err != nil {
				// The empty option means "leave this slot to the picker".
				id = uuid.Nil
			}
			out.Assessment.ProblemIDs = append(out.Assessment.ProblemIDs, id)
		}
	}
	return out
}

func atoiOr(raw string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return n
}
