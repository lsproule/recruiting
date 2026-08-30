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
			if err := s.q.Enqueue(ctx, tx, queue.KindEmailSend, queue.EmailPayload{
				Template: mail.TemplateAssessmentInvite, To: card.CandidateEmail, OrgID: p.OrgID,
				Data: map[string]any{
					"CandidateName": card.CandidateName,
					"JobTitle":      card.JobTitle,
					"OrgName":       org.Name,
					"AssessmentURL": s.baseURL + assessmentPath + token,
					"ExpiresAt":     expires.UTC().Format("2006-01-02 15:04 UTC"),
				},
			}); err != nil {
				return err
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
