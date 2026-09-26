// Package sprints serves screening sprints: the recruiter's setup and
// summary, the interviewer's console with its rotation clock and rating
// card, and the candidate's lobby behind their link. Handlers call
// internal/service only.
package sprints

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
	"recruiting/internal/web/room"
)

// Where the screens live: the recruiter's and interviewer's under the app
// surface, the candidate's behind their link.
const (
	Prefix      = "/app/sprints"
	LobbyPrefix = "/sprint"
)

// Path is a sprint's summary.
func Path(id uuid.UUID) string { return Prefix + "/" + id.String() }

// ConsolePath is the interviewer's console.
func ConsolePath(id uuid.UUID) string { return Path(id) + "/console" }

// NewPath opens the setup form for a stage.
func NewPath(jobID, stageID uuid.UUID) string {
	return Prefix + "/new?job=" + jobID.String() + "&stage=" + stageID.String()
}

// RoundLabel writes a round length in the unit it was set in.
func RoundLabel(seconds int) string {
	if seconds%60 == 0 {
		return strconv.Itoa(seconds/60) + " min"
	}
	if seconds < 60 {
		return strconv.Itoa(seconds) + " s"
	}
	return strconv.FormatFloat(float64(seconds)/60, 'f', 1, 64) + " min"
}

// Deps is what Mount needs.
type Deps struct {
	Sprints      *service.SprintService
	Applications *service.ApplicationService
	Jobs         *service.JobService
	Org          *service.OrgService
	Links        *service.MagicLinkService
	// Room mounts the candidate's room under their link; the org user's
	// rooms are mounted by room.Mount itself.
	Room room.Deps
	// Logger records the errors behind a 500; nil disables that logging.
	Logger *slog.Logger
}

// Mount registers the sprint screens on r. It expects the shared auth
// middleware to be installed already. The candidate's lobby sits outside
// any session: the link token is the credential.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Route(Prefix, func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"))
		r.Get("/", h.list)
		r.Get("/{id}", h.summary)
		r.Get("/{id}/console", h.console)
		r.Get("/{id}/state", h.state)
		r.Get("/{id}/rate/{pairing}", h.rateForm)
		r.Post("/{id}/rate/{pairing}", h.rate)
		r.Group(func(r chi.Router) {
			r.Use(requireRecruiter)
			r.Get("/new", h.setup)
			r.Post("/", h.create)
			r.Post("/{id}/schedule", h.schedule)
			r.Post("/{id}/start", h.start)
			r.Post("/{id}/cancel", h.cancel)
		})
	})
	r.Route(LobbyPrefix+"/{token}", func(r chi.Router) {
		r.Use(auth.MagicLink(d.Links, service.LinkSprint))
		r.Get("/", h.lobby)
		r.Get("/state", h.lobbyState)
		r.Route("/room/{roomID}", func(r chi.Router) {
			room.MountCandidate(r, service.RoomPairing, d.Room)
		})
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
		h.d.Logger.ErrorContext(r.Context(), "sprint screen failed", "path", r.URL.Path, "error", err)
	}
	http.Error(w, userMessage(err), status)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden), errors.Is(err, service.ErrNotInterviewer), errors.Is(err, service.ErrNotSprintMember):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrSprintNotDraft), errors.Is(err, service.ErrSprintNotScheduled),
		errors.Is(err, service.ErrSprintStarted), errors.Is(err, service.ErrRatingTooEarly):
		return http.StatusConflict
	case errors.Is(err, service.ErrSprintEmpty), errors.Is(err, service.ErrSprintStage),
		errors.Is(err, service.ErrSprintCandidate), errors.Is(err, service.ErrSprintInterviewer),
		errors.Is(err, service.ErrSprintName), errors.Is(err, service.ErrBadRating),
		errors.Is(err, domain.ErrInvalidPipeline):
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
	items, err := h.d.Sprints.List(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, listPage(h.page(r, "Screening sprints"), items, time.Now()))
}

// setup draws the form for a stage: its job's candidates in the stage and
// the org's interviewers, with the stage's own round clock filled in.
func (h *handlers) setup(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	jobID, err1 := uuid.Parse(r.URL.Query().Get("job"))
	stageID, err2 := uuid.Parse(r.URL.Query().Get("stage"))
	if err1 != nil || err2 != nil {
		http.Error(w, "a sprint is set up from a job's sprint stage", http.StatusBadRequest)
		return
	}
	v, err := h.setupView(r, p, jobID, stageID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, setupPage(h.page(r, "New screening sprint"), v))
}

func (h *handlers) setupView(r *http.Request, p service.Principal, jobID, stageID uuid.UUID) (setupView, error) {
	job, err := h.d.Jobs.Job(r.Context(), p, jobID)
	if err != nil {
		return setupView{}, err
	}
	stages, err := h.d.Jobs.Stages(r.Context(), p, jobID)
	if err != nil {
		return setupView{}, err
	}
	var stage domain.Stage
	for _, s := range stages {
		if s.ID == stageID {
			stage = s
		}
	}
	if stage.ID == uuid.Nil || stage.Kind != domain.StageSprint {
		return setupView{}, service.ErrSprintStage
	}
	apps, err := h.d.Applications.List(r.Context(), p, jobID, service.ListFilter{StageID: stageID, Status: domain.StatusActive})
	if err != nil {
		return setupView{}, err
	}
	interviewers, err := h.d.Org.Interviewers(r.Context(), p)
	if err != nil {
		return setupView{}, err
	}
	v := setupView{
		Job: job, Stage: stage, Candidates: apps, Interviewers: interviewers,
		Form: sprintForm{
			Name:         job.Title + " screening sprint",
			RoundMinutes: strconv.FormatFloat(float64(stage.RoundSeconds)/60, 'f', -1, 64),
			BreakSeconds: strconv.Itoa(stage.BreakSeconds),
		},
	}
	for _, a := range apps {
		v.Form.Candidates = append(v.Form.Candidates, a.ID)
	}
	return v, nil
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	in, form, err := inputFromForm(r)
	if err == nil {
		var sp service.Sprint
		sp, err = h.d.Sprints.Create(r.Context(), p, in)
		if err == nil {
			http.Redirect(w, r, Path(sp.ID), http.StatusSeeOther)
			return
		}
	}
	if statusFor(err) == http.StatusInternalServerError {
		h.fail(w, r, err)
		return
	}
	v, verr := h.setupView(r, p, in.JobID, in.StageID)
	if verr != nil {
		h.fail(w, r, verr)
		return
	}
	v.Form = form
	v.Error = userMessage(err)
	render(w, r, statusFor(err), setupPage(h.page(r, "New screening sprint"), v))
}

// inputFromForm reads the setup form. The start is entered as a local time
// in the browser's zone, which the form carries beside it.
func inputFromForm(r *http.Request) (service.SprintInput, sprintForm, error) {
	form := sprintForm{
		Name: r.PostFormValue("name"), StartsLocal: r.PostFormValue("starts_local"), Timezone: r.PostFormValue("timezone"),
		RoundMinutes: r.PostFormValue("round_minutes"), BreakSeconds: r.PostFormValue("break_seconds"),
	}
	in := service.SprintInput{Name: form.Name}
	in.JobID, _ = uuid.Parse(r.PostFormValue("job_id"))
	in.StageID, _ = uuid.Parse(r.PostFormValue("stage_id"))
	for _, raw := range r.PostForm["candidate"] {
		if id, err := uuid.Parse(raw); err == nil {
			in.ApplicationIDs = append(in.ApplicationIDs, id)
			form.Candidates = append(form.Candidates, id)
		}
	}
	for _, raw := range r.PostForm["interviewer"] {
		if id, err := uuid.Parse(raw); err == nil {
			in.InterviewerIDs = append(in.InterviewerIDs, id)
			form.Interviewers = append(form.Interviewers, id)
		}
	}
	if minutes, err := strconv.ParseFloat(strings.TrimSpace(form.RoundMinutes), 64); err == nil {
		in.RoundSeconds = int(minutes*60 + 0.5)
	}
	in.BreakSeconds, _ = strconv.Atoi(strings.TrimSpace(form.BreakSeconds))
	loc, err := time.LoadLocation(strings.TrimSpace(form.Timezone))
	if err != nil || form.Timezone == "" {
		loc = time.UTC
	}
	start, err := time.ParseInLocation("2006-01-02T15:04", strings.TrimSpace(form.StartsLocal), loc)
	if err != nil {
		return in, form, errors.New("service: pick a start date and time")
	}
	in.StartsAt = start.UTC()
	return in, form, nil
}

func (h *handlers) summary(w http.ResponseWriter, r *http.Request) {
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	h.renderSummary(w, r, id, http.StatusOK, nil)
}

func (h *handlers) renderSummary(w http.ResponseWriter, r *http.Request, id uuid.UUID, status int, flashes []layout.Flash) {
	p, _ := middleware.PrincipalFrom(r.Context())
	sum, err := h.d.Sprints.Summary(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	v := summaryView{Summary: sum, Now: time.Now(), CanManage: p.HasRole(service.RoleRecruiter) || p.HasRole(service.RoleAdmin), UserID: p.UserID}
	if stages, err := h.d.Jobs.Stages(r.Context(), p, sum.Sprint.JobID); err == nil {
		v.Next, v.Reject = nextAndReject(stages, sum.Sprint.StageID)
	}
	render(w, r, status, summaryPage(h.page(r, sum.Sprint.Name, flashes...), v))
}

// nextAndReject are the stages the summary's Advance and Reject lead to.
func nextAndReject(stages []domain.Stage, stageID uuid.UUID) (next, reject domain.Stage) {
	for i, s := range stages {
		if s.Kind == domain.StageTerminal && s.Terminal == domain.StatusRejected {
			reject = s
		}
		if s.ID == stageID {
			for _, later := range stages[i+1:] {
				if later.Kind != domain.StageTerminal {
					next = later
					break
				}
			}
		}
	}
	return next, reject
}

func (h *handlers) schedule(w http.ResponseWriter, r *http.Request) {
	h.manage(w, r, func(p service.Principal, id uuid.UUID) error {
		_, err := h.d.Sprints.Schedule(r.Context(), p, id)
		return err
	}, "Scheduled. Every candidate has been sent their lobby link.")
}

func (h *handlers) start(w http.ResponseWriter, r *http.Request) {
	h.manage(w, r, func(p service.Principal, id uuid.UUID) error {
		_, err := h.d.Sprints.StartNow(r.Context(), p, id)
		return err
	}, "Started. The first round is running.")
}

func (h *handlers) cancel(w http.ResponseWriter, r *http.Request) {
	h.manage(w, r, func(p service.Principal, id uuid.UUID) error {
		return h.d.Sprints.Cancel(r.Context(), p, id)
	}, "Cancelled. The candidates' links no longer open.")
}

func (h *handlers) manage(w http.ResponseWriter, r *http.Request, op func(service.Principal, uuid.UUID) error, done string) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	if err := op(p, id); err != nil {
		if statusFor(err) == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		h.renderSummary(w, r, id, statusFor(err), []layout.Flash{{Kind: "error", Message: userMessage(err)}})
		return
	}
	h.renderSummary(w, r, id, http.StatusOK, []layout.Flash{{Kind: "success", Message: done}})
}

// console is the interviewer's page: the clock, their rounds, the room of
// the running one, and the rating card.
func (h *handlers) console(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	sp, err := h.d.Sprints.Console(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	v := consoleView{Sprint: sp, UserID: p.UserID, Now: time.Now(), Interviewer: sp.IsInterviewer(p.UserID)}
	v.Config = consoleConfig(sp, p.UserID, Path(id)+"/state", middleware.CSRFToken(r), h.d.Room.ICEServers)
	render(w, r, http.StatusOK, consolePage(h.page(r, sp.Name+" · console"), v))
}

// state is the clock as JSON, for the console to poll.
func (h *handlers) state(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	sp, err := h.d.Sprints.Console(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, stateJSON(sp, sp.PairingsOf(p.UserID), time.Now(), func(pr service.SprintPairing) string {
		return room.PairingPath(pr.ID)
	}, func(pr service.SprintPairing) string { return pr.CandidateName }, Path(id)+"/rate/"))
}

func (h *handlers) rateForm(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	pairingID, ok := param(w, r, "pairing")
	if !ok {
		return
	}
	sp, err := h.d.Sprints.Console(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	pr, found := sp.Pairing(pairingID)
	if !found {
		http.NotFound(w, r)
		return
	}
	render(w, r, http.StatusOK, ratingCard(id, pr, middleware.CSRFToken(r), "", false))
}

func (h *handlers) rate(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	pairingID, ok := param(w, r, "pairing")
	if !ok {
		return
	}
	in := service.RatingInput{Recommendation: r.PostFormValue("recommendation"), Note: r.PostFormValue("note")}
	in.Score, _ = strconv.Atoi(r.PostFormValue("score"))
	pr, err := h.d.Sprints.Rate(r.Context(), p, pairingID, in)
	if err != nil {
		if statusFor(err) == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		sp, gerr := h.d.Sprints.Console(r.Context(), p, id)
		if gerr != nil {
			h.fail(w, r, gerr)
			return
		}
		pr, _ = sp.Pairing(pairingID)
		render(w, r, statusFor(err), ratingCard(id, pr, middleware.CSRFToken(r), userMessage(err), false))
		return
	}
	if r.Header.Get("HX-Request") == "" {
		http.Redirect(w, r, Path(id), http.StatusSeeOther)
		return
	}
	render(w, r, http.StatusOK, ratingCard(id, pr, middleware.CSRFToken(r), "", true))
}

// lobby is the candidate's page.
func (h *handlers) lobby(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	lobby, err := h.d.Sprints.Lobby(r.Context(), p)
	if err != nil {
		if errors.Is(err, service.ErrSprintNotScheduled) {
			render(w, r, http.StatusGone, lobbyClosedPage("This sprint is not running", "It has been cancelled or has not been scheduled. Your recruiter will be in touch."))
			return
		}
		h.fail(w, r, err)
		return
	}
	base := LobbyPrefix + "/" + chi.URLParam(r, "token")
	v := lobbyView{Lobby: lobby, Now: time.Now(), Base: base}
	v.Config = lobbyConfig(lobby, base, middleware.CSRFToken(r), h.d.Room.ICEServers)
	render(w, r, http.StatusOK, lobbyPage(v))
}

func (h *handlers) lobbyState(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	lobby, err := h.d.Sprints.Lobby(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	base := LobbyPrefix + "/" + chi.URLParam(r, "token")
	writeJSON(w, stateJSON(lobby.Sprint, lobby.Pairings, time.Now(), func(pr service.SprintPairing) string {
		return base + "/room/" + pr.ID.String()
	}, func(pr service.SprintPairing) string { return pr.InterviewerName }, ""))
}

// stateJSON is the clock and one person's rounds as the pages poll them.
func stateJSON(sp service.Sprint, mine []service.SprintPairing, now time.Time, roomPath func(service.SprintPairing) string, with func(service.SprintPairing) string, ratePrefix string) map[string]any {
	clock := sp.Clock()
	state := clock.At(now)
	rounds := make([]map[string]any, 0, len(mine))
	var current map[string]any
	for _, pr := range mine {
		entry := map[string]any{
			"pairing": pr.ID.String(), "round": pr.Round, "with": with(pr),
			"starts_at": clock.RoundStart(pr.Round).UnixMilli(), "ends_at": clock.RoundEnd(pr.Round).UnixMilli(),
			"room": roomPath(pr), "rated": pr.Rating != nil,
		}
		if ratePrefix != "" {
			entry["rate"] = ratePrefix + pr.ID.String()
		}
		rounds = append(rounds, entry)
		if state.Phase == domain.PhaseRound && pr.Round == state.Round {
			current = entry
		}
	}
	return map[string]any{
		"now": now.UnixMilli(), "status": sp.Status, "phase": string(state.Phase), "round": state.Round,
		"rounds": sp.Rounds(), "next": state.Next.UnixMilli(),
		"starts_at": sp.StartsAt.UnixMilli(), "ends_at": clock.EndsAt().UnixMilli(),
		"round_seconds": sp.RoundSeconds, "break_seconds": sp.BreakSeconds,
		"mine": rounds, "current": current,
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
