package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"recruiting/internal/mail"
	"recruiting/internal/queue"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// ErrConsentRequired is a session whose measures the candidate has not
// agreed to yet. Nothing is recorded before that answer.
var ErrConsentRequired = errors.New("service: this assessment records you, and cannot start before you agree")

// ConsentRequired reports whether the settings need the candidate's answer
// before the timer starts. With every measure off there is nothing to agree
// to and the session runs as it always has.
func ConsentRequired(i IntegritySettings) bool {
	return i.Fullscreen || i.BlockPaste || i.Webcam || i.PhotoID
}

// Consent records that the candidate agreed to the session's measures. It is
// the step before Start, and only an invited attempt has it to give.
func (s *AttemptService) Consent(ctx context.Context, p Principal) error {
	id := p.SubjectID
	if err := requireCandidate(p, id); err != nil {
		return err
	}
	if err := s.expireIfDue(ctx, p, id); err != nil {
		return wrapAttempt("expire attempt", err)
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		att, err := s.lock(ctx, tx, id)
		if err != nil {
			return err
		}
		if att.Status != AttemptInvited {
			return closedError(att)
		}
		_, err = tx.Q.SetAttemptConsentAt(ctx, db.SetAttemptConsentAtParams{ID: id, ConsentAt: ts(s.Now())})
		return err
	})
	if err != nil {
		return wrapAttempt("record consent", err)
	}
	return nil
}

// Decline ends the invitation without starting anything: no consent, no
// timer, no frames, and the attempt stays invited so a recruiter can waive a
// measure and invite again. The recruiters are told, because an invite that
// stalls here looks exactly like one the candidate ignored.
func (s *AttemptService) Decline(ctx context.Context, p Principal) error {
	id := p.SubjectID
	if err := requireCandidate(p, id); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		att, err := s.lock(ctx, tx, id)
		if err != nil {
			return err
		}
		if att.Status != AttemptInvited {
			return closedError(att)
		}
		if s.q == nil || !att.ApplicationID.Valid {
			// A preview sitting has no application and no one to tell.
			return nil
		}
		card, err := tx.Q.GetApplicationCard(ctx, att.ApplicationID.UUID)
		if err != nil {
			return err
		}
		recruiters, err := tx.Q.ListOrgUsersWithRole(ctx, db.ListOrgUsersWithRoleParams{OrgID: att.OrgID, Role: RoleRecruiter})
		if err != nil {
			return err
		}
		for _, r := range recruiters {
			_, err := s.q.Enqueue(ctx, tx, queue.KindEmailSend, queue.EmailPayload{
				Template: mail.TemplateAssessmentDeclined, To: r.Email, OrgID: att.OrgID,
				Data: map[string]any{
					"RecruiterName":  r.Name,
					"CandidateName":  card.CandidateName,
					"JobTitle":       card.JobTitle,
					"ApplicationURL": s.baseURL + appPath + att.ApplicationID.UUID.String(),
				},
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return wrapAttempt("decline assessment", err)
	}
	return nil
}

// appendConsentEvent opens the recording with what the candidate agreed to,
// under the server's own seq like the snapshot beats.
func (s *AttemptService) appendConsentEvent(ctx context.Context, tx *store.Tx, att db.Attempt, i IntegritySettings) error {
	payload, err := json.Marshal(map[string]any{"webcam": i.Webcam, "photo_id": i.PhotoID})
	if err != nil {
		return err
	}
	now := s.Now()
	next := att.LastEventSeq + 1
	if err := tx.Q.AppendAttemptEvent(ctx, db.AppendAttemptEventParams{
		OrgID: att.OrgID, AttemptID: att.ID, Seq: next, Kind: "consent", Payload: payload,
		ClientTs: ts(now), ServerTs: ts(now),
	}); err != nil {
		return fmt.Errorf("append consent event: %w", err)
	}
	return tx.Q.SetAttemptLastEventSeq(ctx, db.SetAttemptLastEventSeqParams{ID: att.ID, LastEventSeq: next})
}
