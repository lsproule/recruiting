package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// RemindPayload is the interview.remind payload. slot_id and offset name the
// reminder; org_id scopes the job's transaction and booking_url gives the
// candidate their way back, since a link token cannot be recovered from its
// hash later. starts_at is the time the reminder was queued for; a slot
// found at another time is not the booking this reminder belongs to.
type RemindPayload struct {
	SlotID     uuid.UUID `json:"slot_id"`
	Offset     string    `json:"offset"`
	OrgID      uuid.UUID `json:"org_id"`
	StartsAt   time.Time `json:"starts_at"`
	BookingURL string    `json:"booking_url"`
}

// RemindHandler works interview.remind. Reminders are queued at booking time
// and never removed: a reschedule cancels the old slot row and books a new
// one, so a reminder whose slot is no longer booked, or already started,
// finds nothing to do and succeeds silently. That check is the cancellation.
// Wire it into the worker's handler table under queue.KindInterviewRemind.
func RemindHandler(st *store.Store, q *queue.Client, baseURL string) queue.Handler {
	s := NewScheduleService(st, q, baseURL)
	return func(ctx context.Context, job queue.Job) error {
		var p RemindPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("interview.remind payload: %w", err)
		}
		if p.SlotID == uuid.Nil || p.OrgID == uuid.Nil {
			return errors.New("interview.remind payload names no slot or org")
		}
		return st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
			slot, err := tx.Q.GetInterviewSlot(ctx, p.SlotID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			// A slot that is no longer booked, has moved, or has started is
			// not reminded about.
			if slot.Status != SlotBooked || !slot.StartsAt.Time.Equal(p.StartsAt) || !slot.StartsAt.Time.After(s.Now()) || !slot.ApplicationID.Valid {
				return nil
			}
			card, err := tx.Q.GetApplicationCard(ctx, slot.ApplicationID.UUID)
			if err != nil {
				return err
			}
			vetter, err := tx.Q.GetOrgUser(ctx, db.GetOrgUserParams{ID: slot.VetterID, OrgID: p.OrgID})
			if err != nil {
				return err
			}
			candTZ := deref(slot.CandidateTimezone)
			start := slot.StartsAt.Time
			if err := s.email(ctx, tx, p.OrgID, mail.TemplateReminder, card.CandidateEmail, map[string]any{
				"CandidateName": card.CandidateName, "JobTitle": card.JobTitle,
				"StartsAt": bothTimes(start, candTZ, vetter.Timezone), "Timezone": candTZ, "BookingURL": p.BookingURL,
			}); err != nil {
				return err
			}
			return s.email(ctx, tx, p.OrgID, mail.TemplateReminder, vetter.Email, map[string]any{
				"CandidateName": vetter.Name, "JobTitle": card.JobTitle + " with " + card.CandidateName,
				"StartsAt": bothTimes(start, vetter.Timezone, candTZ), "Timezone": vetter.Timezone,
				"BookingURL": s.baseURL + applicationPath + slot.ApplicationID.UUID.String(),
			})
		})
	}
}
