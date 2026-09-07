package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/queue"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// MaxSnapshotBytes bounds one webcam frame. A JPEG of a webcam still fits
// comfortably; anything larger is not a frame this session took.
const MaxSnapshotBytes = 120 << 10

// SnapshotURLTTL is how long a snapshot download link stays valid: long
// enough for a reviewer to open the frame, short enough that a link copied
// out of the page is not a lasting handle on the candidate's likeness.
const SnapshotURLTTL = 5 * time.Minute

// SnapshotContentType is what the frames are stored and served as.
const SnapshotContentType = "image/jpeg"

// PurgeSnapshotBatch bounds one org's sweep so a long-neglected org does not
// hold a transaction open over every frame it ever took.
const PurgeSnapshotBatch = 500

var (
	ErrSnapshotTooLarge = errors.New("service: the snapshot is larger than 120 KiB")
	ErrSnapshotType     = errors.New("service: the snapshot is not a JPEG")
	ErrIdentityRecorded = errors.New("service: the identity frame has already been taken")
)

// AttemptSnapshot is one webcam frame of a sitting. URL is a signed link to
// the bytes, filled only for a reader that is allowed to see them.
type AttemptSnapshot struct {
	ID        uuid.UUID
	AttemptID uuid.UUID
	Seq       int
	TakenAt   time.Time
	BlobKey   string
	Bytes     int64
	URL       string
}

// SnapshotPurgePayload is the snapshot.purge payload. The sweep covers every
// org under each org's own retention, so it names none.
type SnapshotPurgePayload struct{}

// SnapshotBlobKey is where a sitting's frame is stored. The beat is in the
// key, so an upload of the same beat overwrites its own object.
func SnapshotBlobKey(attemptID uuid.UUID, seq int) string {
	return fmt.Sprintf("snapshots/%s/%d.jpg", attemptID, seq)
}

// IdentityBlobKey is where a sitting's photo of an ID is stored. It sits
// apart from the beats so the retention sweep, which walks the beats, cannot
// reach it.
func IdentityBlobKey(attemptID uuid.UUID) string {
	return fmt.Sprintf("identity/%s.jpg", attemptID)
}

// jpegMagic starts every JPEG. The part's declared media type is the
// candidate's word; the bytes are not.
var jpegMagic = []byte{0xff, 0xd8, 0xff}

func checkFrame(data []byte) error {
	if len(data) > MaxSnapshotBytes {
		return fmt.Errorf("%w: %d bytes", ErrSnapshotTooLarge, len(data))
	}
	if !bytes.HasPrefix(data, jpegMagic) {
		return ErrSnapshotType
	}
	return nil
}

// SaveSnapshot stores one webcam frame of the candidate's own sitting and
// appends the I4 snapshot event that puts the beat in the recording. The
// seq is the session's beat counter: uploading it twice replaces the frame
// rather than adding one.
func (s *AttemptService) SaveSnapshot(ctx context.Context, p Principal, attemptID uuid.UUID, seq int, data []byte) (AttemptSnapshot, error) {
	if err := requireCandidate(p, attemptID); err != nil {
		return AttemptSnapshot{}, err
	}
	if seq < 0 {
		return AttemptSnapshot{}, fmt.Errorf("%w: snapshot seq %d is negative", ErrEventInvalid, seq)
	}
	if err := checkFrame(data); err != nil {
		return AttemptSnapshot{}, err
	}
	if s.Blobs == nil {
		return AttemptSnapshot{}, ErrNoBlobStore
	}
	if err := s.expireIfDue(ctx, p, attemptID); err != nil {
		return AttemptSnapshot{}, wrapAttempt("expire attempt", err)
	}
	key := SnapshotBlobKey(attemptID, seq)
	now := s.Now()
	var out AttemptSnapshot
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		att, err := s.lockStarted(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		// The object goes first: a row pointing at nothing would send the
		// reviewer to a broken frame, while an object with no row is swept
		// with the attempt.
		if err := s.Blobs.Put(ctx, key, bytes.NewReader(data), int64(len(data)), SnapshotContentType); err != nil {
			return err
		}
		row, err := tx.Q.UpsertAttemptSnapshot(ctx, db.UpsertAttemptSnapshotParams{
			OrgID: att.OrgID, AttemptID: attemptID, Seq: int32(seq),
			TakenAt: ts(now), BlobKey: key, Bytes: int32(len(data)),
		})
		if err != nil {
			return err
		}
		out = toSnapshot(row)
		return s.appendSnapshotEvent(ctx, tx, att, seq, now)
	})
	if err != nil {
		return AttemptSnapshot{}, wrapAttempt("save snapshot", err)
	}
	return out, nil
}

// appendSnapshotEvent puts the beat in the recording under the server's own
// seq. The island resyncs its counter from the attempt's last seq, so a
// server-appended event does not strand the batch it is recording.
func (s *AttemptService) appendSnapshotEvent(ctx context.Context, tx *store.Tx, att db.Attempt, seq int, now time.Time) error {
	payload, err := json.Marshal(map[string]any{"seq": seq, "ok": true})
	if err != nil {
		return err
	}
	next := att.LastEventSeq + 1
	if err := tx.Q.AppendAttemptEvent(ctx, db.AppendAttemptEventParams{
		OrgID: att.OrgID, AttemptID: att.ID, Seq: next, Kind: "snapshot", Payload: payload,
		ClientTs: ts(now), ServerTs: ts(now),
	}); err != nil {
		return err
	}
	return tx.Q.SetAttemptLastEventSeq(ctx, db.SetAttemptLastEventSeqParams{ID: att.ID, LastEventSeq: next})
}

// SaveIdentityFrame stores the photo of an ID the candidate consented to,
// once. A sitting that already has one is refused rather than replacing the
// frame a reviewer may already have seen.
func (s *AttemptService) SaveIdentityFrame(ctx context.Context, p Principal, attemptID uuid.UUID, data []byte) error {
	if err := requireCandidate(p, attemptID); err != nil {
		return err
	}
	if err := checkFrame(data); err != nil {
		return err
	}
	if s.Blobs == nil {
		return ErrNoBlobStore
	}
	if err := s.expireIfDue(ctx, p, attemptID); err != nil {
		return wrapAttempt("expire attempt", err)
	}
	key := IdentityBlobKey(attemptID)
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		att, err := s.lockStarted(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if att.IdentityBlobKey != nil && *att.IdentityBlobKey != "" {
			return ErrIdentityRecorded
		}
		if err := s.Blobs.Put(ctx, key, bytes.NewReader(data), int64(len(data)), SnapshotContentType); err != nil {
			return err
		}
		if _, err := tx.Q.SetAttemptIdentityBlobKey(ctx, db.SetAttemptIdentityBlobKeyParams{
			ID: attemptID, IdentityBlobKey: &key,
		}); errors.Is(err, pgx.ErrNoRows) {
			return ErrIdentityRecorded
		} else if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return wrapAttempt("save identity frame", err)
	}
	return nil
}

// Snapshots lists a sitting's frames with links to them. They are the
// candidate's likeness: only the org's own users read them, and a client
// principal never does.
func (s *AttemptService) Snapshots(ctx context.Context, p Principal, attemptID uuid.UUID) ([]AttemptSnapshot, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	var out []AttemptSnapshot
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		// The read proves the attempt is the caller's org's; RLS hides
		// another org's row entirely.
		if _, err := tx.Q.GetAttempt(ctx, attemptID); err != nil {
			return err
		}
		rows, err := tx.Q.ListAttemptSnapshots(ctx, attemptID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			shot := toSnapshot(row)
			if s.Blobs != nil {
				url, err := s.Blobs.SignedGetURL(ctx, shot.BlobKey, fmt.Sprintf("snapshot-%d.jpg", shot.Seq), SnapshotURLTTL)
				if err != nil {
					return err
				}
				shot.URL = url
			}
			out = append(out, shot)
		}
		return nil
	})
	if err != nil {
		return nil, wrapAttempt("list snapshots", err)
	}
	return out, nil
}

// SnapshotPurgeHandler works snapshot.purge. Wire it into the worker's
// handler table under queue.KindSnapshotPurge.
func SnapshotPurgeHandler(st *store.Store, b BlobStore, logger *slog.Logger) queue.Handler {
	s := NewAttemptService(st, nil, "")
	s.Blobs = b
	s.Logger = logger
	return func(ctx context.Context, _ queue.Job) error {
		_, err := s.PurgeSnapshots(ctx)
		return err
	}
}

// PurgeSnapshots deletes every stored frame past its org's retention, blob
// first so a row is never the last handle on an object. A frame whose object
// the store would not delete keeps its row and is tried again next sweep. It
// returns how many frames it deleted.
func (s *AttemptService) PurgeSnapshots(ctx context.Context) (int, error) {
	if s.Blobs == nil {
		if s.Logger != nil {
			s.Logger.Warn("no object storage configured; snapshots are not purged")
		}
		return 0, nil
	}
	now := s.Now()
	// Nothing can be past retention sooner than the shortest one an org may
	// set, so the widest sweep still starts there.
	var orgs []uuid.UUID
	err := s.st.WithTx(ctx, orgScoped(PlatformOrgID), func(ctx context.Context, tx *store.Tx) error {
		var err error
		orgs, err = tx.Q.ListSnapshotOrgs(ctx, ts(now.AddDate(0, 0, -MinSnapshotRetentionDays)))
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("purge snapshots: %w", err)
	}
	n := 0
	for _, orgID := range orgs {
		deleted, err := s.purgeOrgSnapshots(ctx, orgID, now)
		n += deleted
		if err != nil {
			return n, fmt.Errorf("purge snapshots: org %s: %w", orgID, err)
		}
	}
	return n, nil
}

func (s *AttemptService) purgeOrgSnapshots(ctx context.Context, orgID uuid.UUID, now time.Time) (int, error) {
	n := 0
	err := s.st.WithTx(ctx, orgScoped(orgID), func(ctx context.Context, tx *store.Tx) error {
		days, err := snapshotRetentionDays(ctx, tx, orgID)
		if err != nil {
			return err
		}
		rows, err := tx.Q.ListSnapshotsTakenBefore(ctx, ts(now.AddDate(0, 0, -days)))
		if err != nil {
			return err
		}
		for i, row := range rows {
			if i >= PurgeSnapshotBatch {
				break
			}
			if err := s.Blobs.Delete(ctx, row.BlobKey); err != nil {
				// The row is the only record of the key: leaving it is what
				// makes the next sweep able to try the object again.
				if s.Logger != nil {
					s.Logger.Warn("a snapshot's object could not be deleted; its row stays for the next sweep",
						"snapshot_id", row.ID, "blob_key", row.BlobKey, "error", err)
				}
				continue
			}
			if err := tx.Q.DeleteAttemptSnapshot(ctx, row.ID); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// snapshotRetentionDays is the org's retention, the default filling in for
// an org that never set one.
func snapshotRetentionDays(ctx context.Context, tx *store.Tx, orgID uuid.UUID) (int, error) {
	row, err := tx.Q.GetOrgSetting(ctx, db.GetOrgSettingParams{OrgID: orgID, Key: SettingSnapshotRetentionDays})
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultSnapshotRetentionDays, nil
	}
	if err != nil {
		return 0, err
	}
	var days int
	if err := json.Unmarshal(row, &days); err != nil || days < MinSnapshotRetentionDays {
		// A setting that no longer parses must not delete frames early.
		return DefaultSnapshotRetentionDays, nil
	}
	return days, nil
}

func toSnapshot(r db.AttemptSnapshot) AttemptSnapshot {
	return AttemptSnapshot{
		ID: r.ID, AttemptID: r.AttemptID, Seq: int(r.Seq),
		TakenAt: r.TakenAt.Time.UTC(), BlobKey: r.BlobKey, Bytes: int64(r.Bytes),
	}
}
