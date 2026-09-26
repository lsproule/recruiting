package client

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/service"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

// talentRequestForm is the new-request form as it was filled in, so a
// refused submission comes back with what was typed.
type talentRequestForm struct {
	Title, Skills, Seniority, Location, RemotePolicy, Note, JobID string
	Error                                                         string
}

// talentView is the company's talent screen: its requests, the form for a
// new one, and the open jobs a request may name.
type talentView struct {
	Requests []service.TalentRequest
	Jobs     []service.ClientJob
	Form     talentRequestForm
}

// talentRequestView is one request's page: the anonymised matches and the
// introductions asked for so far.
type talentRequestView struct {
	Request service.TalentRequest
	Matches []service.TalentMatch
	Intros  []service.ClientIntro
}

func (h *handlers) talent(w http.ResponseWriter, r *http.Request) {
	h.renderTalent(w, r, http.StatusOK, talentRequestForm{})
}

func (h *handlers) renderTalent(w http.ResponseWriter, r *http.Request, status int, form talentRequestForm) {
	p, _ := middleware.PrincipalFrom(r.Context())
	reqs, err := h.d.Talent.Requests(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	jobs, err := h.d.Portal.Jobs(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	open := make([]service.ClientJob, 0, len(jobs))
	for _, j := range jobs {
		if j.Status == service.JobOpen {
			open = append(open, j)
		}
	}
	render(w, r, status, talentPage(h.page(r, "Talent requests"), talentView{Requests: reqs, Jobs: open, Form: form}))
}

func (h *handlers) createTalentRequest(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	form := talentRequestForm{
		Title: r.PostFormValue("title"), Skills: r.PostFormValue("skills"), Seniority: r.PostFormValue("seniority"),
		Location: r.PostFormValue("location"), RemotePolicy: r.PostFormValue("remote_policy"), Note: r.PostFormValue("note"),
		JobID: r.PostFormValue("job_id"),
	}
	in := service.TalentRequestInput{
		Title: form.Title, Skills: []string{form.Skills}, Seniority: form.Seniority, Location: form.Location,
		RemotePolicy: form.RemotePolicy, Note: form.Note,
	}
	if raw := strings.TrimSpace(form.JobID); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			form.Error = "Pick one of your open jobs."
			h.renderTalent(w, r, http.StatusUnprocessableEntity, form)
			return
		}
		in.JobID = id
	}
	req, err := h.d.Talent.CreateRequest(r.Context(), p, in)
	if err != nil {
		status := statusFor(err)
		if status == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		form.Error = userMessage(err)
		h.renderTalent(w, r, status, form)
		return
	}
	http.Redirect(w, r, TalentRequestPath(req.ID), http.StatusSeeOther)
}

func (h *handlers) talentRequest(w http.ResponseWriter, r *http.Request) {
	var flashes []layout.Flash
	switch r.URL.Query().Get("did") {
	case "introduce":
		flashes = append(flashes, layout.Flash{Kind: "success", Message: "Your recruiter will approach them and let you know what they say."})
	case "close":
		flashes = append(flashes, layout.Flash{Kind: "success", Message: "The request is closed."})
	}
	h.renderTalentRequest(w, r, http.StatusOK, flashes)
}

func (h *handlers) renderTalentRequest(w http.ResponseWriter, r *http.Request, status int, flashes []layout.Flash) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	req, err := h.d.Talent.Request(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	v := talentRequestView{Request: req}
	if v.Intros, err = h.d.Talent.Intros(r.Context(), p, id); err != nil {
		h.fail(w, r, err)
		return
	}
	if req.Open() {
		if v.Matches, err = h.d.Talent.Matches(r.Context(), p, id); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	render(w, r, status, talentRequestPage(h.page(r, req.Title, flashes...), v))
}

func (h *handlers) closeTalentRequest(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	h.afterTalent(w, r, id, "close", h.d.Talent.CloseRequest(r.Context(), p, id))
}

func (h *handlers) introduce(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	matchID, ok := param(w, r, "matchID")
	if !ok {
		return
	}
	_, err := h.d.Talent.Introduce(r.Context(), p, id, matchID)
	h.afterTalent(w, r, id, "introduce", err)
}

// afterTalent redirects back to the request on success and re-renders it
// with the refusal otherwise.
func (h *handlers) afterTalent(w http.ResponseWriter, r *http.Request, id uuid.UUID, did string, err error) {
	if err == nil {
		http.Redirect(w, r, TalentRequestPath(id)+"?did="+did, http.StatusSeeOther)
		return
	}
	status := statusFor(err)
	if status == http.StatusInternalServerError {
		h.fail(w, r, err)
		return
	}
	h.renderTalentRequest(w, r, status, []layout.Flash{{Kind: "error", Message: userMessage(err)}})
}

// developerView is the API page: the user's tokens, the secret of one just
// issued, and where the documentation lives.
type developerView struct {
	Tokens  []service.APIToken
	Secret  string
	BaseURL string
	Error   string
}

func (h *handlers) developer(w http.ResponseWriter, r *http.Request) {
	h.renderDeveloper(w, r, http.StatusOK, "", "")
}

func (h *handlers) renderDeveloper(w http.ResponseWriter, r *http.Request, status int, secret, errMsg string) {
	p, _ := middleware.PrincipalFrom(r.Context())
	tokens, err := h.d.Tokens.ListOwn(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	base := strings.TrimRight(h.d.BaseURL, "/")
	if base == "" {
		base = "https://" + r.Host
	}
	render(w, r, status, developerPage(h.page(r, "Developer"), developerView{Tokens: tokens, Secret: secret, BaseURL: base, Error: errMsg}))
}

func (h *handlers) issueToken(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	var expires *time.Time
	if raw := strings.TrimSpace(r.PostFormValue("expires_on")); raw != "" {
		d, err := time.Parse("2006-01-02", raw)
		if err != nil {
			h.renderDeveloper(w, r, http.StatusUnprocessableEntity, "", "The expiry must be a date.")
			return
		}
		// The token lasts through the whole of the day named.
		at := d.Add(24*time.Hour - time.Second)
		expires = &at
	}
	_, secret, err := h.d.Tokens.IssueOwn(r.Context(), p, r.PostFormValue("name"), expires)
	if err != nil {
		status := statusFor(err)
		if status == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		h.renderDeveloper(w, r, status, "", userMessage(err))
		return
	}
	h.renderDeveloper(w, r, http.StatusOK, secret, "")
}

func (h *handlers) revokeToken(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := param(w, r, "id")
	if !ok {
		return
	}
	if err := h.d.Tokens.RevokeOwn(r.Context(), p, id); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, DeveloperPath, http.StatusSeeOther)
}
