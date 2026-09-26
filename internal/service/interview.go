package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// InterviewRow is one booked interview as the interviews screen lists it.
type InterviewRow struct {
	ID             uuid.UUID
	VetterID       uuid.UUID
	VetterName     string
	ApplicationID  uuid.UUID
	StageID        uuid.UUID
	StageName      string
	CandidateName  string
	CandidateEmail string
	JobTitle       string
	Start, End     time.Time
	Status         string
	Video          bool
	HasScorecard   bool
}

// RoomOpensAt is when the interview's room may be joined.
func (r InterviewRow) RoomOpensAt() time.Time { return r.Start.Add(-RoomOpensBefore) }

// RoomOpen reports whether the room may be joined at now.
func (r InterviewRow) RoomOpen(now time.Time) bool {
	closes := r.Start.Add(RoomStaysOpen)
	if r.End.After(closes) {
		closes = r.End
	}
	return r.Video && !now.Before(r.RoomOpensAt()) && now.Before(closes)
}

// InterviewService reads the interview calendar.
type InterviewService struct {
	st *store.Store
	// Now is the clock; tests move it.
	Now func() time.Time
}

func NewInterviewService(st *store.Store) *InterviewService {
	return &InterviewService{st: st, Now: time.Now}
}

// Upcoming is the booked interviews from two hours ago on: a vetter's own,
// or every one in the org for a recruiter or admin.
func (s *InterviewService) Upcoming(ctx context.Context, p Principal) ([]InterviewRow, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	vetter := uuid.NullUUID{UUID: p.UserID, Valid: true}
	if p.HasRole(RoleRecruiter) || p.HasRole(RoleAdmin) {
		vetter = uuid.NullUUID{}
	}
	var out []InterviewRow
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListInterviewSlotsForOrg(ctx, db.ListInterviewSlotsForOrgParams{
			EndsAfter: ts(s.Now().Add(-RoomStaysOpen)), VetterID: vetter,
		})
		if err != nil {
			return err
		}
		out = make([]InterviewRow, 0, len(rows))
		for _, r := range rows {
			out = append(out, InterviewRow{
				ID: r.ID, VetterID: r.VetterID, VetterName: r.VetterName,
				ApplicationID: r.ApplicationID.UUID, StageID: r.StageID.UUID, StageName: r.StageName,
				CandidateName: r.CandidateName, CandidateEmail: r.CandidateEmail, JobTitle: r.JobTitle,
				Start: r.StartsAt.Time.UTC(), End: r.EndsAt.Time.UTC(), Status: r.Status,
				Video: r.InterviewFormat == domain.FormatVideo, HasScorecard: r.HasScorecard,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("upcoming interviews: %w", err)
	}
	return out, nil
}

// Slot is one booked interview with its room, for the application page.
func (s *InterviewService) Slot(ctx context.Context, p Principal, id uuid.UUID) (InterviewRow, error) {
	rows, err := s.Upcoming(ctx, p)
	if err != nil {
		return InterviewRow{}, err
	}
	for _, r := range rows {
		if r.ID == id {
			return r, nil
		}
	}
	return InterviewRow{}, ErrNotFound
}
