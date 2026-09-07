package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/service"
)

// ShortlistPick is one candidate of a packet at their rank.
type ShortlistPick struct {
	ApplicationID uuid.UUID `json:"application_id"`
	Rank          int       `json:"rank"`
	CandidateName string    `json:"candidate_name"`
	Score         *float64  `json:"score,omitempty" doc:"Best assessment score, absent when they never sat one"`
}

// Shortlist is the recruiter's ranked recommendation for one job. It is a
// draft until it is sent; sending releases every pick and freezes it.
type Shortlist struct {
	ID     uuid.UUID       `json:"id"`
	JobID  uuid.UUID       `json:"job_id"`
	Status string          `json:"status" doc:"draft or sent"`
	Note   string          `json:"note,omitempty"`
	Picks  []ShortlistPick `json:"picks" doc:"In rank order"`
	SentAt *time.Time      `json:"sent_at,omitempty" doc:"Absent while the packet is a draft"`
	SentBy *uuid.UUID      `json:"sent_by,omitempty"`
}

func shortlistView(p service.ShortlistPacket) Shortlist {
	out := Shortlist{
		ID: p.ID, JobID: p.JobID, Status: p.Status, Note: p.Note,
		Picks: make([]ShortlistPick, 0, len(p.Picks)), SentAt: p.SentAt,
	}
	if p.SentBy != uuid.Nil {
		by := p.SentBy
		out.SentBy = &by
	}
	for _, pick := range p.Picks {
		out.Picks = append(out.Picks, ShortlistPick{
			ApplicationID: pick.ApplicationID, Rank: pick.Rank,
			CandidateName: pick.CandidateName, Score: pick.Score,
		})
	}
	return out
}

type shortlistHandlers struct{ d Deps }

// mountShortlists registers the packet operations. A client reads their own
// shortlist through the portal surface, never here: these are the
// recruiter's, and accessOrg is what says so.
func (m *mounter) mountShortlists() {
	h := shortlistHandlers{d: m.d}
	register(m, accessOrg, huma.Operation{
		OperationID: "create-shortlist", Method: http.MethodPost, Path: "/shortlists",
		Summary: "Save a shortlist packet", Tags: []string{"shortlists"}, DefaultStatus: http.StatusCreated,
	}, h.create)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-shortlist", Method: http.MethodGet, Path: "/shortlists/{shortlist_id}",
		Summary: "One shortlist packet", Tags: []string{"shortlists"},
	}, h.get)
	register(m, accessOrg, huma.Operation{
		OperationID: "send-shortlist", Method: http.MethodPost, Path: "/shortlists/{shortlist_id}/send",
		Summary: "Send the packet to the client", Tags: []string{"shortlists"},
	}, h.send)
}

type shortlistOutput struct{ Body Shortlist }

type shortlistInput struct {
	ShortlistID uuid.UUID `path:"shortlist_id"`
}

type createShortlistInput struct {
	Body struct {
		ID             *uuid.UUID  `json:"id,omitempty" doc:"Amend this draft instead of starting a new packet"`
		JobID          uuid.UUID   `json:"job_id"`
		Note           string      `json:"note,omitempty"`
		ApplicationIDs []uuid.UUID `json:"application_ids" doc:"Their order is the ranking the client reads"`
	}
}

func (h shortlistHandlers) create(ctx context.Context, in *createShortlistInput) (*shortlistOutput, error) {
	packet, err := h.d.Shortlists.Save(ctx, principal(ctx), in.Body.ID, service.ShortlistInput{
		JobID: in.Body.JobID, Note: in.Body.Note, ApplicationIDs: in.Body.ApplicationIDs,
	})
	if err != nil {
		return nil, problemDetail(err)
	}
	return &shortlistOutput{Body: shortlistView(packet)}, nil
}

func (h shortlistHandlers) get(ctx context.Context, in *shortlistInput) (*shortlistOutput, error) {
	packet, err := h.d.Shortlists.Get(ctx, principal(ctx), in.ShortlistID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &shortlistOutput{Body: shortlistView(packet)}, nil
}

func (h shortlistHandlers) send(ctx context.Context, in *shortlistInput) (*shortlistOutput, error) {
	packet, err := h.d.Shortlists.Send(ctx, principal(ctx), in.ShortlistID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &shortlistOutput{Body: shortlistView(packet)}, nil
}
