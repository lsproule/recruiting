package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/queue"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// PreviewCandidateName stands in for the candidate's name when a recruiter
// is sitting their own assessment.
const PreviewCandidateName = "Preview"

// PreviewRetention is how long a preview attempt lives. It is long enough to
// finish a sitting and come back to the results, short enough that a
// recruiter's own runs never accumulate.
const PreviewRetention = 24 * time.Hour

// AttemptPreview is a recruiter's sitting of their own assessment: the
// attempt and the link that opens it as a candidate would see it.
type AttemptPreview struct {
	Attempt Attempt
	URL     string
}

// AttemptPurgePreviewPayload is the attempt.purge_preview payload. The sweep
// covers every org, so it names none.
type AttemptPurgePreviewPayload struct{}

// Preview opens the assessment as a candidate sees it, without touching any
// pipeline: the attempt belongs to the calling org user rather than to an
// application, is marked preview, and is swept away with everything it
// produced a day later.
func (s *AttemptService) Preview(ctx context.Context, p Principal, assessmentID uuid.UUID) (AttemptPreview, error) {
	if err := requireRecruiter(p); err != nil {
		return AttemptPreview{}, err
	}
	var out AttemptPreview
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		// The read proves the assessment is the caller's org's to preview.
		if _, err := loadAssessment(ctx, tx, assessmentID); err != nil {
			return err
		}
		expires := s.Now().Add(PreviewRetention)
		row, err := tx.Q.CreatePreviewAttempt(ctx, db.CreatePreviewAttemptParams{
			OrgID: p.OrgID, AssessmentID: assessmentID,
			PreviewUserID: uuid.NullUUID{UUID: p.UserID, Valid: true}, InviteExpiresAt: ts(expires),
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
		out = AttemptPreview{Attempt: toAttempt(row), URL: s.baseURL + assessmentPath + token}
		return nil
	})
	if err != nil {
		return AttemptPreview{}, wrapAttempt("preview assessment", err)
	}
	return out, nil
}

// AttemptPurgePreviewHandler works attempt.purge_preview: it deletes every
// preview attempt past PreviewRetention. Wire it into the worker's handler
// table under queue.KindAttemptPurgePreview.
func AttemptPurgePreviewHandler(st *store.Store, b BlobStore, logger *slog.Logger) queue.Handler {
	s := NewAttemptService(st, nil, "")
	s.Blobs = b
	s.Logger = logger
	return func(ctx context.Context, _ queue.Job) error {
		_, err := s.PurgePreviews(ctx)
		return err
	}
}

// PurgePreviews deletes preview attempts older than PreviewRetention across
// every org, with the events, sources, submissions, and links that hang off
// them. Stored blobs go first: a row deleted before its blob would leave the
// key unrecoverable and the object behind forever. It returns how many
// attempts it deleted.
func (s *AttemptService) PurgePreviews(ctx context.Context) (int, error) {
	before := s.Now().Add(-PreviewRetention)
	var orgs []uuid.UUID
	err := s.st.WithTx(ctx, orgScoped(PlatformOrgID), func(ctx context.Context, tx *store.Tx) error {
		var err error
		orgs, err = tx.Q.ListStalePreviewOrgs(ctx, ts(before))
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("purge previews: %w", err)
	}
	n := 0
	for _, orgID := range orgs {
		err := s.st.WithTx(ctx, orgScoped(orgID), func(ctx context.Context, tx *store.Tx) error {
			stale, err := tx.Q.ListStalePreviewAttempts(ctx, ts(before))
			if err != nil {
				return err
			}
			for _, att := range stale {
				if err := s.dropBlobs(ctx, tx, att); err != nil {
					return err
				}
				if err := tx.Q.DeleteMagicLinksForSubject(ctx, db.DeleteMagicLinksForSubjectParams{
					Purpose: LinkAssessment, SubjectID: att.ID,
				}); err != nil {
					return err
				}
				if err := tx.Q.DeleteAttempt(ctx, att.ID); err != nil {
					return err
				}
				n++
			}
			return nil
		})
		if err != nil {
			return n, fmt.Errorf("purge previews: org %s: %w", orgID, err)
		}
	}
	return n, nil
}

// dropBlobs removes the recording, the identity frame, and the webcam frames
// a preview left in object storage. A key that is already gone is not an
// error; without a blob store the keys are noted and the rows still go.
func (s *AttemptService) dropBlobs(ctx context.Context, tx *store.Tx, att db.Attempt) error {
	var keys []string
	for _, key := range []*string{att.RecordingBlobKey, att.IdentityBlobKey} {
		if key != nil && *key != "" {
			keys = append(keys, *key)
		}
	}
	// The snapshot rows cascade with the attempt, so their keys have to be
	// read while they are still there.
	shots, err := tx.Q.ListAttemptSnapshots(ctx, att.ID)
	if err != nil {
		return err
	}
	for _, shot := range shots {
		keys = append(keys, shot.BlobKey)
	}
	if len(keys) == 0 {
		return nil
	}
	if s.Blobs == nil {
		if s.Logger != nil {
			s.Logger.Warn("no object storage configured; a preview's blobs stay", "attempt_id", att.ID, "keys", keys)
		}
		return nil
	}
	for _, key := range keys {
		if err := s.Blobs.Delete(ctx, key); err != nil {
			return errors.Join(fmt.Errorf("delete blob %s of preview %s", key, att.ID), err)
		}
	}
	return nil
}
