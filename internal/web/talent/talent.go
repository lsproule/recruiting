// Package talent serves the talent network's three faces: the public join
// page and a member's own profile page, the candidate's answer to an
// opportunity, and the recruiter's screens for requests and introductions.
// Handlers call internal/service only.
package talent

import (
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
)

// Where the pages live. The public join page names the org, as the apply
// page does; the member and opportunity pages are behind their links.
const (
	JoinPrefix        = "/talent"
	ProfilePrefix     = "/talent/profile"
	OpportunityPrefix = "/opportunity"
	AppPrefix         = "/app/talent"
	resumeField       = "resume"
)

// JoinPath is the public join page for one org.
func JoinPath(orgSlug string) string { return JoinPrefix + "/" + orgSlug }

// RequestPath is the recruiter's page for one request; ProfilePath one
// member's.
func RequestPath(id uuid.UUID) string { return AppPrefix + "/requests/" + id.String() }
func ProfilePath(id uuid.UUID) string { return AppPrefix + "/profiles/" + id.String() }

// Deps is what the mounts need.
type Deps struct {
	Talent *service.TalentService
	// Links resolves the profile and opportunity tokens.
	Links *service.MagicLinkService
	// Org supplies the signed-in user's display name on the recruiter screens.
	Org *service.OrgService
	// Logger records the errors behind a 500; nil disables that logging.
	Logger *slog.Logger
}

type handlers struct{ d Deps }

// Mount registers the public and candidate-facing pages. They sit outside
// any session; the shared CSRF middleware still applies, so Mount must come
// after auth.Mount, and after the apply body cap.
func Mount(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Get(JoinPrefix+"/{orgSlug}", h.joinForm)
	r.Post(JoinPrefix+"/{orgSlug}", h.join)
	r.Route(ProfilePrefix+"/{token}", func(r chi.Router) {
		r.Use(auth.MagicLink(d.Links, service.LinkProfile))
		r.Get("/", h.profile)
		r.Post("/", h.updateProfile)
		r.Post("/withdraw", h.withdraw)
		r.Post("/rejoin", h.rejoin)
	})
	r.Route(OpportunityPrefix+"/{token}", func(r chi.Router) {
		r.Use(auth.MagicLink(d.Links, service.LinkOpportunity))
		r.Get("/", h.opportunity)
		r.Post("/", h.answer)
	})
}

// MountApp registers the recruiter's screens.
func MountApp(r chi.Router, d Deps) {
	h := &handlers{d: d}
	r.Route(AppPrefix, func(r chi.Router) {
		r.Use(middleware.RequireAuth("/app/login"), requireRecruiter)
		r.Get("/", h.list)
		r.Get("/requests/{id}", h.request)
		r.Post("/requests/{id}/introductions/{introID}/send", h.send)
		r.Post("/requests/{id}/introductions/{introID}/dismiss", h.dismiss)
		r.Get("/profiles/{id}", h.member)
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

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = c.Render(r.Context(), w)
}

// publicPage is the chrome for a visitor with no session.
func publicPage(r *http.Request, title string, flashes ...layout.Flash) layout.Page {
	return layout.Page{Title: title, Flashes: flashes, CSRF: middleware.CSRFToken(r)}
}

func (h *handlers) appPage(r *http.Request, title string, flashes ...layout.Flash) layout.Page {
	p, _ := middleware.PrincipalFrom(r.Context())
	return layout.Page{
		Title: title, Surface: layout.SurfaceApp, Nav: layout.AppNav(p, AppPrefix), UserRole: layout.RoleLabel(p), Menu: layout.AppMenu(p, AppPrefix),
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

// joinForm is the public form as it was filled in, so a refusal comes back
// with what was typed.
type joinForm struct {
	Name, Email, Phone, Links, Headline, Skills, Seniority, Roles, Location, RemotePolicy, SalaryMin, AvailableFrom string
	Consent                                                                                                         bool
	Error                                                                                                           string
}

func (h *handlers) joinForm(w http.ResponseWriter, r *http.Request) {
	org, err := h.d.Talent.Org(r.Context(), chi.URLParam(r, "orgSlug"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, joinPage(publicPage(r, org.Name+" talent network"), org, joinForm{}))
}

func (h *handlers) join(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "orgSlug")
	org, err := h.d.Talent.Org(r.Context(), slug)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	in, form, done, err := joinFromForm(r)
	defer done()
	if err == nil {
		_, err = h.d.Talent.Join(r.Context(), slug, in)
	}
	if err != nil {
		status := statusFor(err)
		if status == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		form.Error = userMessage(err)
		render(w, r, status, joinPage(publicPage(r, org.Name+" talent network"), org, form))
		return
	}
	render(w, r, http.StatusOK, joinedPage(publicPage(r, org.Name+" talent network"), org, in.Email))
}

// joinFromForm reads the multipart form. The résumé is optional here: a
// member may join with a profile alone and send one later.
func joinFromForm(r *http.Request) (service.TalentProfileInput, joinForm, func(), error) {
	form := joinForm{
		Name: r.PostFormValue("name"), Email: r.PostFormValue("email"), Phone: r.PostFormValue("phone"),
		Links: r.PostFormValue("links"), Headline: r.PostFormValue("headline"), Skills: r.PostFormValue("skills"),
		Seniority: r.PostFormValue("seniority"), Roles: r.PostFormValue("roles"), Location: r.PostFormValue("location"),
		RemotePolicy: r.PostFormValue("remote_policy"), SalaryMin: r.PostFormValue("salary_min"),
		AvailableFrom: r.PostFormValue("available_from"), Consent: r.PostFormValue("consent") != "",
	}
	in := service.TalentProfileInput{
		Name: form.Name, Email: form.Email, Phone: form.Phone, Links: []string{form.Links},
		Headline: form.Headline, Skills: []string{form.Skills}, Seniority: form.Seniority, Roles: []string{form.Roles},
		Location: form.Location, RemotePolicy: form.RemotePolicy, Consent: form.Consent,
	}
	if raw := strings.TrimSpace(form.SalaryMin); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return in, form, func() {}, errBadSalary
		}
		in.SalaryMin = n
	}
	if raw := strings.TrimSpace(form.AvailableFrom); raw != "" {
		d, err := time.Parse("2006-01-02", raw)
		if err != nil {
			return in, form, func() {}, errBadDate
		}
		in.AvailableFrom = &d
	}
	up, done, err := readResume(r)
	if err != nil {
		return in, form, nil, err
	}
	in.Resume = up
	return in, form, done, nil
}

var (
	errBadSalary = errors.New("the salary floor must be a whole number")
	errBadDate   = errors.New("the start date must be a date")
)

// readResume opens the optional upload without loading it: the part sits on
// disk past MaxBody's memory threshold and the service streams it from
// there. Its declared size and its head are checked first, so an oversized
// file or one of the wrong kind is refused without being read. done closes
// the part; the caller runs it once the service has finished with the input.
func readResume(r *http.Request) (*service.ResumeUpload, func(), error) {
	file, header, err := r.FormFile(resumeField)
	if err != nil {
		return nil, func() {}, nil
	}
	if header.Size == 0 {
		_ = file.Close()
		return nil, func() {}, nil
	}
	if header.Size > domain.MaxResumeBytes {
		_ = file.Close()
		return nil, func() {}, domain.ErrResumeTooLarge
	}
	if _, err := domain.SniffResumeAt(file, header.Size); err != nil {
		_ = file.Close()
		return nil, func() {}, err
	}
	return &service.ResumeUpload{Filename: header.Filename, File: file, Size: header.Size}, func() { _ = file.Close() }, nil
}

// profileView is the member's page: their profile and the form to change it.
type profileView struct {
	Profile service.TalentProfile
	Action  string
	Error   string
}

func (h *handlers) profile(w http.ResponseWriter, r *http.Request) {
	h.renderProfile(w, r, http.StatusOK, "", nil)
}

func (h *handlers) renderProfile(w http.ResponseWriter, r *http.Request, status int, errMsg string, flashes []layout.Flash) {
	p, _ := middleware.PrincipalFrom(r.Context())
	prof, err := h.d.Talent.Profile(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, status, profilePage(publicPage(r, "Your profile", flashes...), profileView{Profile: prof, Action: tokenPath(r), Error: errMsg}))
}

func tokenPath(r *http.Request) string {
	return ProfilePrefix + "/" + chi.URLParam(r, "token")
}

func (h *handlers) updateProfile(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	in, _, done, err := joinFromForm(r)
	defer done()
	if err == nil {
		_, err = h.d.Talent.UpdateProfile(r.Context(), p, service.TalentProfileEdit{
			Phone: in.Phone, Links: in.Links, Headline: in.Headline, Skills: in.Skills, Seniority: in.Seniority,
			Roles: in.Roles, Location: in.Location, RemotePolicy: in.RemotePolicy, SalaryMin: in.SalaryMin,
			AvailableFrom: in.AvailableFrom, Resume: in.Resume,
		})
	}
	if err != nil {
		status := statusFor(err)
		if status == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		h.renderProfile(w, r, status, userMessage(err), nil)
		return
	}
	h.renderProfile(w, r, http.StatusOK, "", []layout.Flash{{Kind: "success", Message: "Saved."}})
}

func (h *handlers) withdraw(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	if err := h.d.Talent.Withdraw(r.Context(), p); err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderProfile(w, r, http.StatusOK, "", []layout.Flash{{Kind: "success", Message: "You have left the network. Nobody will be approached about you."}})
}

func (h *handlers) rejoin(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	if err := h.d.Talent.Rejoin(r.Context(), p); err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderProfile(w, r, http.StatusOK, "", []layout.Flash{{Kind: "success", Message: "Welcome back. You are in the network again."}})
}

func (h *handlers) opportunity(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	op, err := h.d.Talent.Opportunity(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, opportunityPage(publicPage(r, op.JobTitle+" at "+op.ClientCompanyName), op, OpportunityPrefix+"/"+chi.URLParam(r, "token")))
}

func (h *handlers) answer(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	op, err := h.d.Talent.Answer(r.Context(), p, r.PostFormValue("answer") == "yes")
	if err != nil {
		status := statusFor(err)
		if status == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		op, err = h.d.Talent.Opportunity(r.Context(), p)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		render(w, r, status, opportunityPage(publicPage(r, op.JobTitle, layout.Flash{Kind: "error", Message: userMessage(err)}), op, OpportunityPrefix+"/"+chi.URLParam(r, "token")))
		return
	}
	render(w, r, http.StatusOK, opportunityPage(publicPage(r, op.JobTitle+" at "+op.ClientCompanyName), op, OpportunityPrefix+"/"+chi.URLParam(r, "token")))
}

// listView is the recruiter's overview: every company's requests, and the
// network itself.
type listView struct {
	Requests []service.TalentRequest
	Profiles []service.TalentProfile
	Query    string
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	reqs, err := h.d.Talent.AllRequests(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	profiles, err := h.d.Talent.Profiles(r.Context(), p, query, false)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, listPage(h.appPage(r, "Talent network"), listView{Requests: reqs, Profiles: profiles, Query: query}))
}

func (h *handlers) request(w http.ResponseWriter, r *http.Request) {
	var flashes []layout.Flash
	switch r.URL.Query().Get("did") {
	case "send":
		flashes = append(flashes, layout.Flash{Kind: "success", Message: "The opportunity is on its way to them."})
	case "dismiss":
		flashes = append(flashes, layout.Flash{Kind: "success", Message: "The introduction will not be made."})
	}
	h.renderRequest(w, r, http.StatusOK, flashes)
}

func (h *handlers) renderRequest(w http.ResponseWriter, r *http.Request, status int, flashes []layout.Flash) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	d, err := h.d.Talent.RequestForRecruiter(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, status, requestPage(h.appPage(r, d.Request.Title, flashes...), d))
}

func (h *handlers) send(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	introID, ok := param(w, r, "introID")
	if !ok {
		return
	}
	var jobID uuid.UUID
	if raw := strings.TrimSpace(r.PostFormValue("job_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, "bad job id", http.StatusBadRequest)
			return
		}
		jobID = parsed
	}
	_, err := h.d.Talent.SendOpportunity(r.Context(), p, introID, jobID)
	h.afterRequest(w, r, id, "send", err)
}

func (h *handlers) dismiss(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	introID, ok := param(w, r, "introID")
	if !ok {
		return
	}
	h.afterRequest(w, r, id, "dismiss", h.d.Talent.DismissIntro(r.Context(), p, introID))
}

func (h *handlers) afterRequest(w http.ResponseWriter, r *http.Request, id uuid.UUID, did string, err error) {
	if err == nil {
		http.Redirect(w, r, RequestPath(id)+"?did="+did, http.StatusSeeOther)
		return
	}
	status := statusFor(err)
	if status == http.StatusInternalServerError {
		h.fail(w, r, err)
		return
	}
	h.renderRequest(w, r, status, []layout.Flash{{Kind: "error", Message: userMessage(err)}})
}

func (h *handlers) member(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	prof, err := h.d.Talent.ProfileByID(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, http.StatusOK, memberPage(h.appPage(r, prof.Name), prof))
}

func param(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		http.Error(w, "bad "+name, http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(err)
	if status == http.StatusInternalServerError && h.d.Logger != nil {
		h.d.Logger.ErrorContext(r.Context(), "talent page failed", "path", r.URL.Path, "error", err)
	}
	if status == http.StatusNotFound && strings.HasPrefix(r.URL.Path, JoinPrefix+"/") {
		render(w, r, status, notFoundPage(publicPage(r, "Not found")))
		return
	}
	http.Error(w, userMessage(err), status)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrTalentRequestClosed), errors.Is(err, service.ErrIntroRequested),
		errors.Is(err, service.ErrIntroNotWaiting), errors.Is(err, service.ErrIntroNotSent), errors.Is(err, service.ErrAlreadyApplied):
		return http.StatusConflict
	case errors.Is(err, service.ErrTalentConsent), errors.Is(err, service.ErrTalentSkills), errors.Is(err, service.ErrTalentTitle),
		errors.Is(err, service.ErrIntroJob), errors.Is(err, service.ErrTalentJobRequired), errors.Is(err, service.ErrInvalidJob),
		errors.Is(err, service.ErrTooLong), errors.Is(err, service.ErrNameRequired), errors.Is(err, service.ErrEmailRequired),
		errors.Is(err, service.ErrNoStages), errors.Is(err, service.ErrJobNotOpen), errors.Is(err, domain.ErrResumeEmpty),
		errors.Is(err, errBadSalary), errors.Is(err, errBadDate):
		return http.StatusUnprocessableEntity
	case errors.Is(err, domain.ErrResumeTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, domain.ErrResumeType):
		return http.StatusUnsupportedMediaType
	case errors.Is(err, service.ErrNoBlobStore):
		return http.StatusServiceUnavailable
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
