package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/queue"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// InviteStatuses are the sitting states the Assessments screen filters by,
// in lifecycle order.
var InviteStatuses = []string{AttemptInvited, AttemptStarted, AttemptSubmitted, AttemptExpired, AttemptScored, AttemptReviewed}

// AttemptInvite is one assessment sitting as the recruiter's Assessments
// screen reads it: who was sent what, how far they have got, and how much
// the recording has flagged.
type AttemptInvite struct {
	AttemptID         uuid.UUID
	ApplicationID     uuid.UUID
	StageID           uuid.UUID
	CandidateName     string
	CandidateEmail    string
	JobTitle          string
	ClientCompanyName string
	AssessmentName    string
	Status            string
	ProblemsSubmitted int
	ProblemsTotal     int
	IntegrityFlags    int
	InvitedAt         time.Time
	InviteExpiresAt   time.Time
	SessionExpiresAt  time.Time
	FinishedAt        time.Time
	Score             *float64
	RiskScore         *float64
}

// Progress is the share of the set already submitted, 0–1. A set that lost a
// problem after the invite went out cannot read as more than finished, and
// an empty set divides by nothing.
func (i AttemptInvite) Progress() float64 {
	if i.ProblemsTotal <= 0 {
		return 0
	}
	if i.ProblemsSubmitted >= i.ProblemsTotal {
		return 1
	}
	return float64(i.ProblemsSubmitted) / float64(i.ProblemsTotal)
}

// ProgressLabel counts the same thing in words, unrounded, so a set that no
// longer matches the sitting is visible rather than clamped away.
func (i AttemptInvite) ProgressLabel() string {
	return strconv.Itoa(i.ProblemsSubmitted) + " of " + strconv.Itoa(i.ProblemsTotal)
}

// ExpiresAt is the deadline that still applies: the invite window before the
// candidate starts, the clock on the sitting afterwards, and none at all once
// the sitting is closed.
func (i AttemptInvite) ExpiresAt() time.Time {
	switch i.Status {
	case AttemptInvited:
		return i.InviteExpiresAt
	case AttemptStarted:
		return i.SessionExpiresAt
	}
	return time.Time{}
}

// Open reports whether the candidate can still work on the sitting, which is
// what makes a reminder or a revocation worth offering.
func (i AttemptInvite) Open() bool { return i.Status == AttemptInvited || i.Status == AttemptStarted }

// Invites lists the org's sittings, newest invite first. An empty status
// asks for all of them; anything else must be one of InviteStatuses.
func (s *AttemptService) Invites(ctx context.Context, p Principal, status string) ([]AttemptInvite, error) {
	if err := requireRecruiter(p); err != nil {
		return nil, err
	}
	var filter *string
	if status != "" {
		if !validInviteStatus(status) {
			return nil, fmt.Errorf("%w: %s is not an assessment status", ErrEventInvalid, status)
		}
		filter = &status
	}
	var out []AttemptInvite
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListAttemptInvites(ctx, filter)
		if err != nil {
			return err
		}
		out = make([]AttemptInvite, 0, len(rows))
		for _, r := range rows {
			out = append(out, AttemptInvite{
				AttemptID: r.AttemptID, ApplicationID: r.ApplicationID, StageID: r.StageID,
				CandidateName: r.CandidateName, CandidateEmail: r.CandidateEmail,
				JobTitle: r.JobTitle, ClientCompanyName: r.ClientCompanyName,
				AssessmentName: r.AssessmentName, Status: r.Status,
				ProblemsSubmitted: int(r.ProblemsSubmitted), ProblemsTotal: int(r.ProblemsTotal),
				IntegrityFlags: int(r.IntegrityFlags),
				InvitedAt:      r.InvitedAt.Time.UTC(), InviteExpiresAt: r.InviteExpiresAt.Time.UTC(),
				SessionExpiresAt: r.ExpiresAt.Time.UTC(), FinishedAt: r.FinishedAt.Time.UTC(),
				Score: numericPtr(r.Score), RiskScore: numericPtr(r.RiskScore),
			})
		}
		return nil
	})
	if err != nil {
		return nil, wrapAttempt("assessment invites", err)
	}
	return out, nil
}

func validInviteStatus(s string) bool {
	for _, v := range InviteStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// SendAssessment invites the application's candidate to the assessment its
// current stage carries. The invite is queued rather than written here, so a
// recruiter's nudge takes the same path as a stage entry's and lands once.
func (s *AttemptService) SendAssessment(ctx context.Context, p Principal, applicationID uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	if s.q == nil {
		return ErrNotActive
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		app, err := tx.Q.GetApplication(ctx, applicationID)
		if err != nil {
			return err
		}
		stage, err := tx.Q.GetStage(ctx, app.StageID)
		if err != nil {
			return err
		}
		if domain.StageKind(stage.Kind) != domain.StageAssessment || !stage.AssessmentID.Valid {
			return ErrStageNotAssessment
		}
		return enqueued(s.q.Enqueue(ctx, tx, queue.KindAssessmentInvite, AssessmentInvitePayload{
			ApplicationID: applicationID, StageID: stage.ID, OrgID: p.OrgID,
		}))
	})
	if err != nil {
		return wrapAttempt("send assessment", err)
	}
	return nil
}

// SendReminder nudges a candidate who has not started yet. The token behind
// the original link cannot be recovered from its hash, so the reminder
// carries a fresh link that runs to the same invite window.
func (s *AttemptService) SendReminder(ctx context.Context, p Principal, attemptID uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	if s.q == nil {
		return ErrNotActive
	}
	var payload AssessmentRemindPayload
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		att, err := tx.Q.GetAttemptForUpdate(ctx, attemptID)
		if err != nil {
			return err
		}
		if att.Preview || att.Status != AttemptInvited {
			return ErrAttemptClosed
		}
		if !att.InviteExpiresAt.Time.After(s.Now()) {
			return ErrInviteExpired
		}
		token, hash, err := newToken()
		if err != nil {
			return err
		}
		if _, err := tx.Q.CreateMagicLink(ctx, db.CreateMagicLinkParams{
			OrgID: p.OrgID, TokenHash: hash, Purpose: LinkAssessment, SubjectID: att.ID,
			ExpiresAt: att.InviteExpiresAt,
		}); err != nil {
			return err
		}
		payload = AssessmentRemindPayload{AttemptID: att.ID, OrgID: p.OrgID, AssessmentURL: s.baseURL + assessmentPath + token}
		return enqueued(s.q.Enqueue(ctx, tx, queue.KindAssessmentRemind, payload))
	})
	if err != nil {
		return wrapAttempt("remind", err)
	}
	return nil
}

// Revoke closes an open sitting and takes its links with it: the candidate
// cannot reach the assessment again, and whatever they had synced is
// submitted and scored rather than discarded.
func (s *AttemptService) Revoke(ctx context.Context, p Principal, attemptID uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		att, err := tx.Q.GetAttemptForUpdate(ctx, attemptID)
		if err != nil {
			return err
		}
		if att.Preview {
			return ErrNotFound
		}
		switch att.Status {
		case AttemptInvited:
			// Nothing was ever started, so there is no work to close over:
			// dropping the links and marking it expired is the whole of it.
			if _, err := tx.Q.ExpireInvitedAttempt(ctx, db.ExpireInvitedAttemptParams{ID: att.ID, FinishedAt: ts(s.Now())}); err != nil {
				return err
			}
		case AttemptStarted:
			if _, err := s.close(ctx, tx, att, AttemptExpired); err != nil {
				return err
			}
		default:
			return closedError(att)
		}
		return tx.Q.DeleteMagicLinksForSubject(ctx, db.DeleteMagicLinksForSubjectParams{
			Purpose: LinkAssessment, SubjectID: att.ID,
		})
	})
	if err != nil {
		return wrapAttempt("revoke assessment", err)
	}
	return nil
}
