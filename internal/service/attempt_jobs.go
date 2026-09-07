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

// AssessmentInviteHandler works assessment.invite: it creates the attempt
// for the application and stage the payload names, issues the assessment
// link, and queues the invite email. A redelivered job finds the attempt
// already there and does nothing, so the candidate is invited once. Wire it
// into the worker's handler table under queue.KindAssessmentInvite.
func AssessmentInviteHandler(st *store.Store, q *queue.Client, baseURL string) queue.Handler {
	s := NewAttemptService(st, q, baseURL)
	return func(ctx context.Context, job queue.Job) error {
		var p AssessmentInvitePayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("assessment.invite payload: %w", err)
		}
		if p.OrgID == uuid.Nil || (p.AttemptID == uuid.Nil && (p.ApplicationID == uuid.Nil || p.StageID == uuid.Nil)) {
			return errors.New("assessment.invite payload names no org, and no attempt or application and stage")
		}
		_, err := s.Invite(ctx, p)
		return err
	}
}

// AssessmentReminderLead is how long before the invite window closes the
// reminder goes out to a candidate who has not started.
const AssessmentReminderLead = 24 * time.Hour

// AssessmentRemindPayload is the assessment.remind payload. The link carries
// the URL because a token cannot be recovered from its hash later.
type AssessmentRemindPayload struct {
	AttemptID     uuid.UUID `json:"attempt_id"`
	OrgID         uuid.UUID `json:"org_id"`
	AssessmentURL string    `json:"assessment_url"`
}

// AssessmentRemindHandler works assessment.remind. The reminder is queued at
// invite time for AssessmentReminderLead before the window closes and never
// removed: an attempt that has been started, closed, or whose invite has
// already lapsed finds nothing to do and succeeds silently. Wire it into the
// worker's handler table under queue.KindAssessmentRemind.
func AssessmentRemindHandler(st *store.Store, q *queue.Client, baseURL string) queue.Handler {
	s := NewAttemptService(st, q, baseURL)
	return func(ctx context.Context, job queue.Job) error {
		var p AssessmentRemindPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("assessment.remind payload: %w", err)
		}
		if p.AttemptID == uuid.Nil || p.OrgID == uuid.Nil {
			return errors.New("assessment.remind payload names no attempt or org")
		}
		return s.Remind(ctx, p)
	}
}

// Remind sends the reminder an assessment.remind payload asks for, when the
// attempt is still waiting to be started and its invite is still open.
func (s *AttemptService) Remind(ctx context.Context, p AssessmentRemindPayload) error {
	err := s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		att, err := tx.Q.GetAttempt(ctx, p.AttemptID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if att.Status != AttemptInvited || !att.InviteExpiresAt.Time.After(s.Now()) || s.q == nil {
			return nil
		}
		card, err := tx.Q.GetApplicationCard(ctx, att.ApplicationID.UUID)
		if err != nil {
			return err
		}
		org, err := tx.Q.GetOrg(ctx, p.OrgID)
		if err != nil {
			return err
		}
		return enqueued(s.q.Enqueue(ctx, tx, queue.KindEmailSend, queue.EmailPayload{
			Template: mail.TemplateAssessmentReminder, To: card.CandidateEmail, OrgID: p.OrgID,
			Data: map[string]any{
				"CandidateName": card.CandidateName,
				"JobTitle":      card.JobTitle,
				"OrgName":       org.Name,
				"AssessmentURL": p.AssessmentURL,
				"ExpiresAt":     att.InviteExpiresAt.Time.UTC().Format("2006-01-02 15:04 UTC"),
			},
		}))
	})
	if err != nil {
		return fmt.Errorf("remind: %w", err)
	}
	return nil
}

// Invite creates the attempt an assessment.invite payload asks for and sends
// the link. It returns the existing attempt, unchanged, when one already
// covers the application and stage.
func (s *AttemptService) Invite(ctx context.Context, p AssessmentInvitePayload) (Attempt, error) {
	var out Attempt
	err := s.st.WithTx(ctx, orgScoped(p.OrgID), func(ctx context.Context, tx *store.Tx) error {
		existing, err := s.existingAttempt(ctx, tx, p)
		if err != nil {
			return err
		}
		if existing != nil {
			out = toAttempt(*existing)
			return nil
		}
		stage, err := tx.Q.GetStage(ctx, p.StageID)
		if err != nil {
			return err
		}
		if !stage.AssessmentID.Valid {
			// Nothing to invite to; the stage carries no assessment yet.
			return nil
		}
		a, err := tx.Q.GetAssessment(ctx, stage.AssessmentID.UUID)
		if err != nil {
			return err
		}
		card, err := tx.Q.GetApplicationCard(ctx, p.ApplicationID)
		if err != nil {
			return err
		}
		org, err := tx.Q.GetOrg(ctx, p.OrgID)
		if err != nil {
			return err
		}
		expires := s.Now().Add(time.Duration(a.InviteWindowDays) * 24 * time.Hour)
		row, err := tx.Q.CreateAttempt(ctx, db.CreateAttemptParams{
			OrgID: p.OrgID, ApplicationID: p.ApplicationID, AssessmentID: a.ID, StageID: p.StageID, InviteExpiresAt: ts(expires),
		})
		if err != nil {
			return err
		}
		token, hash, err := newToken()
		if err != nil {
			return err
		}
		if _, err := tx.Q.CreateMagicLink(ctx, db.CreateMagicLinkParams{
			OrgID: p.OrgID, TokenHash: hash, Purpose: LinkAssessment, SubjectID: row.ID, ExpiresAt: ts(expires),
		}); err != nil {
			return err
		}
		if s.q != nil {
			link := s.baseURL + assessmentPath + token
			if _, err := s.q.Enqueue(ctx, tx, queue.KindEmailSend, queue.EmailPayload{
				Template: mail.TemplateAssessmentInvite, To: card.CandidateEmail, OrgID: p.OrgID,
				Data: map[string]any{
					"CandidateName": card.CandidateName,
					"JobTitle":      card.JobTitle,
					"OrgName":       org.Name,
					"AssessmentURL": link,
					"ExpiresAt":     expires.UTC().Format("2006-01-02 15:04 UTC"),
				},
			}); err != nil {
				return err
			}
			// A window shorter than the lead gets no reminder: the invite
			// itself is the last word.
			if remindAt := expires.Add(-AssessmentReminderLead); remindAt.After(s.Now()) {
				if _, err := s.q.EnqueueAt(ctx, tx, queue.KindAssessmentRemind, AssessmentRemindPayload{
					AttemptID: row.ID, OrgID: p.OrgID, AssessmentURL: link,
				}, remindAt); err != nil {
					return err
				}
			}
		}
		out = toAttempt(row)
		return nil
	})
	if err != nil {
		return Attempt{}, fmt.Errorf("invite: %w", err)
	}
	return out, nil
}

func (s *AttemptService) existingAttempt(ctx context.Context, tx *store.Tx, p AssessmentInvitePayload) (*db.Attempt, error) {
	var row db.Attempt
	var err error
	if p.AttemptID != uuid.Nil {
		row, err = tx.Q.GetAttempt(ctx, p.AttemptID)
	} else {
		row, err = tx.Q.GetAttemptForApplicationStage(ctx, db.GetAttemptForApplicationStageParams{ApplicationID: p.ApplicationID, StageID: p.StageID})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}
