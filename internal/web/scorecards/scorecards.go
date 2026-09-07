// Package scorecards serves the interview scorecards: the vetter's own list
// of interviews and the form they fill in, the recruiter's rubric editor for
// an interview stage, and the summary the application page embeds. Handlers
// call internal/service only.
package scorecards

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/service"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

// Where the screens live. The form and the summary hang off the application
// the pipeline surface already owns, and the rubric editor off its stage.
const (
	Prefix            = "/app/scorecards"
	ApplicationPrefix = "/app/applications"
	JobPrefix         = "/app/jobs"
)

// FormPath is the vetter's scorecard form for one application and stage.
func FormPath(applicationID, stageID uuid.UUID) string {
	return ApplicationPrefix + "/" + applicationID.String() + "/scorecard/" + stageID.String()
}

// SummaryPath is the summary partial the application page loads.
func SummaryPath(applicationID uuid.UUID) string {
	return ApplicationPrefix + "/" + applicationID.String() + "/scorecards"
}

// RubricPath is the recruiter's rubric editor for an interview stage.
func RubricPath(jobID, stageID uuid.UUID) string {
	return JobPrefix + "/" + jobID.String() + "/stages/" + stageID.String() + "/rubric"
}

// Deps is what Mount needs. Org supplies the signed-in user's display name.
type Deps struct {
	Scorecards *service.ScorecardService
	Org        *service.OrgService
	// Logger records the errors behind a 500; nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the scorecard screens on r. It expects the shared auth
// middleware to be installed already; the service decides who may read or
// write what. The routes are registered individually rather than as a
// subtree, so they sit alongside the pipeline surface's own application
// routes on the same router.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		r.Get(Prefix, h.assignments)
		r.Get(ApplicationPrefix+"/{id}/scorecard/{stageID}", h.form)
		r.Post(ApplicationPrefix+"/{id}/scorecard/{stageID}", h.save)
		r.Get(ApplicationPrefix+"/{id}/scorecards", h.summary)
		r.Get(JobPrefix+"/{jobID}/stages/{stageID}/rubric", h.rubric)
		r.Post(JobPrefix+"/{jobID}/stages/{stageID}/rubric", h.saveRubric)
	})
}

type handlers struct{ d Deps }

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = c.Render(r.Context(), w)
}

func (h *handlers) page(r *http.Request, title, nav string, flashes ...layout.Flash) layout.Page {
	p, _ := middleware.PrincipalFrom(r.Context())
	return layout.Page{
		Title: title, Surface: layout.SurfaceApp, Nav: layout.AppNav(p, nav), UserRole: layout.RoleLabel(p), Menu: layout.AppMenu(p, nav),
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

func (h *handlers) assignments(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	rows, err := h.d.Scorecards.Assignments(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, assignmentsPage(h.page(r, "Scorecards", Prefix), rows))
}

func (h *handlers) form(w http.ResponseWriter, r *http.Request) {
	h.renderForm(w, r, http.StatusOK, nil, nil)
}

// renderForm draws the form. A refused submission comes back carrying what
// the interviewer typed, so nothing has to be entered twice.
func (h *handlers) renderForm(w http.ResponseWriter, r *http.Request, status int, flashes []layout.Flash, submitted *service.ScorecardInput) {
	p, _ := middleware.PrincipalFrom(r.Context())
	appID, stageID, ok := ids(w, r)
	if !ok {
		return
	}
	form, err := h.d.Scorecards.Form(r.Context(), p, appID, stageID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	filed := form.Card != nil
	if submitted != nil {
		card := service.Scorecard{Scores: submitted.Scores, Overall: submitted.Overall, Notes: submitted.Notes}
		if form.Card != nil {
			card.ID = form.Card.ID
		}
		form.Card = &card
	}
	render(w, r, status, scorecardPage(h.page(r, "Scorecard", Prefix, flashes...), form, filed, middleware.CSRFToken(r)))
}

func (h *handlers) save(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	appID, stageID, ok := ids(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	in := service.ScorecardInput{
		ApplicationID: appID, StageID: stageID,
		Overall: strings.TrimSpace(r.PostFormValue("overall")),
		Notes:   r.PostFormValue("notes"),
	}
	if id, err := uuid.Parse(r.PostFormValue("scorecard_id")); err == nil {
		in.ID = id
	}
	names, scores, notes := r.PostForm["criterion"], r.PostForm["score"], r.PostForm["criterion_notes"]
	for i, name := range names {
		cs := service.CriterionScore{Name: name}
		if i < len(scores) {
			cs.Score, _ = strconv.Atoi(strings.TrimSpace(scores[i]))
		}
		if i < len(notes) {
			cs.Notes = notes[i]
		}
		in.Scores = append(in.Scores, cs)
	}
	if _, err := h.d.Scorecards.Save(r.Context(), p, in); err != nil {
		status := statusFor(err)
		if status == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		h.renderForm(w, r, status, []layout.Flash{{Kind: "error", Message: userMessage(err)}}, &in)
		return
	}
	http.Redirect(w, r, Prefix, http.StatusSeeOther)
}

// summary is the partial the application page pulls in: the cards filed on
// this application, with no chrome of its own.
func (h *handlers) summary(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	cards, err := h.d.Scorecards.List(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, SummaryFragment(cards))
}

func (h *handlers) rubric(w http.ResponseWriter, r *http.Request) {
	h.renderRubric(w, r, http.StatusOK, nil)
}

func (h *handlers) renderRubric(w http.ResponseWriter, r *http.Request, status int, flashes []layout.Flash) {
	p, _ := middleware.PrincipalFrom(r.Context())
	jobID, stageID, ok := rubricIDs(w, r)
	if !ok {
		return
	}
	rubric, err := h.d.Scorecards.Rubric(r.Context(), p, jobID, stageID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, status, rubricPage(h.page(r, "Rubric", "/app/jobs", flashes...), jobID, rubric, middleware.CSRFToken(r)))
}

func (h *handlers) saveRubric(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	jobID, stageID, ok := rubricIDs(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	in := service.RubricInput{Name: r.PostFormValue("name")}
	descriptions := r.PostForm["description"]
	for i, name := range r.PostForm["name_of"] {
		c := service.Criterion{Name: name}
		if i < len(descriptions) {
			c.Description = descriptions[i]
		}
		in.Criteria = append(in.Criteria, c)
	}
	if _, err := h.d.Scorecards.SetRubric(r.Context(), p, jobID, stageID, in); err != nil {
		status := statusFor(err)
		if status == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		h.renderRubric(w, r, status, []layout.Flash{{Kind: "error", Message: userMessage(err)}})
		return
	}
	http.Redirect(w, r, RubricPath(jobID, stageID), http.StatusSeeOther)
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(err)
	if status == http.StatusInternalServerError && h.d.Logger != nil {
		h.d.Logger.ErrorContext(r.Context(), "scorecard screen failed", "path", r.URL.Path, "error", err)
	}
	http.Error(w, userMessage(err), status)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden), errors.Is(err, service.ErrNotAuthor):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrBadScore), errors.Is(err, service.ErrBadOverall), errors.Is(err, service.ErrNotInterview):
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

func ids(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	appID, ok := param(w, r, "id")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	stageID, ok := param(w, r, "stageID")
	return appID, stageID, ok
}

func rubricIDs(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	jobID, ok := param(w, r, "jobID")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	stageID, ok := param(w, r, "stageID")
	return jobID, stageID, ok
}
