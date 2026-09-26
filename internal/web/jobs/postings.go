package jobs

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/service"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/middleware"
)

// postingsPath is a job's postings panel.
func postingsPath(jobID uuid.UUID) string { return Prefix + "/" + jobID.String() + "/postings" }

// postingsView is the panel: the job, the copy each board would carry, and
// every placement so far.
type postingsView struct {
	Job      service.Job
	Previews []service.BoardPreview
	Postings []service.JobPosting
	// Error is a refused post, shown above the boards.
	Error string
}

// postings shows the panel. A post, retry, or removal redirects back here
// with what it did in ?done, so a reload never repeats the action.
func (h *handlers) postings(w http.ResponseWriter, r *http.Request) {
	var flashes []layout.Flash
	if done := strings.TrimSpace(r.URL.Query().Get("done")); done != "" {
		flashes = append(flashes, layout.Flash{Kind: "success", Message: done})
	}
	h.renderPostings(w, r, http.StatusOK, "", flashes...)
}

// donePath is the panel with a success message to show once.
func donePath(jobID uuid.UUID, done string) string {
	return postingsPath(jobID) + "?done=" + url.QueryEscape(done)
}

func (h *handlers) renderPostings(w http.ResponseWriter, r *http.Request, status int, message string, flashes ...layout.Flash) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	if h.d.Postings == nil {
		http.NotFound(w, r)
		return
	}
	job, err := h.d.Jobs.Job(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	v := postingsView{Job: job, Error: message}
	// A closed job has no copy to show; the panel still lists what was
	// posted while it was open.
	if previews, err := h.d.Postings.Preview(r.Context(), p, id); err == nil {
		v.Previews = previews
	} else if v.Error == "" && job.Status != "open" {
		v.Error = "This job is " + job.Status + ", so it cannot be posted; reopen it first."
	}
	v.Postings, err = h.d.Postings.List(r.Context(), p, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render(w, r, status, postingsPage(h.page(r, job.Title+" · Postings", Prefix, flashes...), v))
}

// post queues one placement and comes back to the panel, where the row
// shows as queued until the worker's browser has done its work.
func (h *handlers) post(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	if h.d.Postings == nil {
		http.NotFound(w, r)
		return
	}
	board := strings.TrimSpace(r.PostFormValue("board"))
	posting, err := h.d.Postings.Post(r.Context(), p, id, board)
	if err != nil {
		if statusFor(err) == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		h.renderPostings(w, r, http.StatusUnprocessableEntity, strings.TrimPrefix(err.Error(), "service: "))
		return
	}
	http.Redirect(w, r, donePath(id, "Queued for "+posting.BoardLabel+". The worker's browser posts it in the background; this page shows where it landed."), http.StatusSeeOther)
}

func (h *handlers) retryPosting(w http.ResponseWriter, r *http.Request) {
	h.postingAction(w, r, func(p service.Principal, postingID uuid.UUID) error {
		return h.d.Postings.Retry(r.Context(), p, postingID)
	}, "Queued again.")
}

func (h *handlers) removePosting(w http.ResponseWriter, r *http.Request) {
	h.postingAction(w, r, func(p service.Principal, postingID uuid.UUID) error {
		return h.d.Postings.Remove(r.Context(), p, postingID)
	}, "Marked as taken down.")
}

func (h *handlers) postingAction(w http.ResponseWriter, r *http.Request, act func(service.Principal, uuid.UUID) error, done string) {
	p, _ := middleware.PrincipalFrom(r.Context())
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	if h.d.Postings == nil {
		http.NotFound(w, r)
		return
	}
	postingID, err := uuid.Parse(chi.URLParam(r, "postingID"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := act(p, postingID); err != nil {
		if statusFor(err) == http.StatusInternalServerError {
			h.fail(w, r, err)
			return
		}
		h.renderPostings(w, r, http.StatusUnprocessableEntity, strings.TrimPrefix(err.Error(), "service: "))
		return
	}
	http.Redirect(w, r, donePath(id, done), http.StatusSeeOther)
}

// statusLabel reads a posting's status as a person would.
func statusLabel(p service.JobPosting) string {
	switch p.Status {
	case service.PostingQueued:
		return "queued"
	case service.PostingPosting:
		return "posting…"
	case service.PostingPosted:
		return "live"
	case service.PostingFailed:
		return "failed"
	case service.PostingRemoved:
		return "taken down"
	}
	return p.Status
}
