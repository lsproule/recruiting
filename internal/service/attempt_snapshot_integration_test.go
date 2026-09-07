//go:build integration

package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"recruiting/internal/queue"
	"recruiting/internal/service"
)

// jpeg is a frame of n bytes that starts with the JPEG magic number, which
// is what the upload sniffs rather than trusting the part's media type.
func jpegBytes(n int) []byte {
	out := make([]byte, n)
	copy(out, []byte{0xff, 0xd8, 0xff, 0xe0})
	return out
}

// snapshotFixture is a started attempt with object storage behind it.
type snapshotFixture struct {
	*attemptFixture
	blob    *fakeBlob
	attempt service.Attempt
	cand    service.Principal
}

func newSnapshotFixture(t *testing.T) *snapshotFixture {
	t.Helper()
	f := newAttemptFixture(t)
	blob := newFakeBlob()
	f.attempts.Blobs = blob
	att := f.invite(t)
	cand := f.candidate(att.ID)
	if _, err := f.attempts.Start(context.Background(), cand); err != nil {
		t.Fatalf("start: %v", err)
	}
	return &snapshotFixture{attemptFixture: f, blob: blob, attempt: att, cand: cand}
}

func (f *snapshotFixture) snapshotRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.sys.QueryRow(context.Background(),
		`select count(*) from attempt_snapshot where attempt_id = $1`, f.attempt.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The upload writes the frame to object storage under the attempt, records
// the row the retention sweep works from, and leaves the recording with a
// snapshot event the reviewer's replay can see.
func TestSnapshotUploadStoresTheBlobTheRowAndTheEvent(t *testing.T) {
	f := newSnapshotFixture(t)
	ctx := context.Background()

	shot, err := f.attempts.SaveSnapshot(ctx, f.cand, f.attempt.ID, 1, jpegBytes(2048))
	if err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	wantKey := fmt.Sprintf("snapshots/%s/1.jpg", f.attempt.ID)
	if shot.BlobKey != wantKey || shot.Seq != 1 || shot.Bytes != 2048 {
		t.Errorf("snapshot = %+v, want %s at seq 1 of 2048 bytes", shot, wantKey)
	}
	if !f.blob.has(wantKey) {
		t.Errorf("no object at %s", wantKey)
	}
	if n := f.snapshotRows(t); n != 1 {
		t.Errorf("%d snapshot rows, want 1", n)
	}
	var kind string
	var payload []byte
	if err := f.sys.QueryRow(ctx, `select kind, payload from attempt_event
		where attempt_id = $1 and kind = 'snapshot' order by seq desc limit 1`, f.attempt.ID).Scan(&kind, &payload); err != nil {
		t.Fatalf("snapshot event: %v", err)
	}
	var beat struct {
		Seq int  `json:"seq"`
		OK  bool `json:"ok"`
	}
	if err := json.Unmarshal(payload, &beat); err != nil || beat.Seq != 1 || !beat.OK {
		t.Errorf("snapshot event payload = %s, want the I4 shape", payload)
	}

	// The same beat uploaded twice is one frame, not two rows.
	if _, err := f.attempts.SaveSnapshot(ctx, f.cand, f.attempt.ID, 1, jpegBytes(2048)); err != nil {
		t.Fatalf("re-upload of the same seq: %v", err)
	}
	if n := f.snapshotRows(t); n != 1 {
		t.Errorf("%d snapshot rows after a re-upload, want 1", n)
	}
}

// A frame that is too big, or that is not a JPEG whatever the part claimed,
// is refused before anything is stored.
func TestSnapshotUploadRefusesOversizedAndNonJPEGFrames(t *testing.T) {
	f := newSnapshotFixture(t)
	ctx := context.Background()

	if _, err := f.attempts.SaveSnapshot(ctx, f.cand, f.attempt.ID, 1, jpegBytes(service.MaxSnapshotBytes+1)); !errors.Is(err, service.ErrSnapshotTooLarge) {
		t.Errorf("oversized frame = %v, want ErrSnapshotTooLarge", err)
	}
	if _, err := f.attempts.SaveSnapshot(ctx, f.cand, f.attempt.ID, 2, []byte("GIF89a not a jpeg")); !errors.Is(err, service.ErrSnapshotType) {
		t.Errorf("non-JPEG frame = %v, want ErrSnapshotType", err)
	}
	if f.blob.count() != 0 || f.snapshotRows(t) != 0 {
		t.Errorf("a refused frame left %d objects and %d rows behind", f.blob.count(), f.snapshotRows(t))
	}
}

// The identity frame is taken once, at consent; a second one is refused
// rather than overwriting the frame the reviewer has already been shown.
func TestIdentityFrameIsStoredOnce(t *testing.T) {
	f := newSnapshotFixture(t)
	ctx := context.Background()

	if err := f.attempts.SaveIdentityFrame(ctx, f.cand, f.attempt.ID, jpegBytes(1024)); err != nil {
		t.Fatalf("identity frame: %v", err)
	}
	var key string
	if err := f.sys.QueryRow(ctx, `select identity_blob_key from attempt where id = $1`, f.attempt.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key == "" || !f.blob.has(key) {
		t.Fatalf("identity_blob_key = %q, object stored = %v", key, f.blob.has(key))
	}
	if err := f.attempts.SaveIdentityFrame(ctx, f.cand, f.attempt.ID, jpegBytes(1024)); !errors.Is(err, service.ErrIdentityRecorded) {
		t.Errorf("second identity frame = %v, want ErrIdentityRecorded", err)
	}
}

// Snapshots are the candidate's own likeness: the org that assessed them
// reads them, another org's rows are invisible, and a client user is refused
// outright.
func TestSnapshotsAreReadableOnlyByTheOrgsUsers(t *testing.T) {
	f := newSnapshotFixture(t)
	ctx := context.Background()
	if _, err := f.attempts.SaveSnapshot(ctx, f.cand, f.attempt.ID, 1, jpegBytes(512)); err != nil {
		t.Fatal(err)
	}

	shots, err := f.attempts.Snapshots(ctx, f.principal(service.RoleVetter), f.attempt.ID)
	if err != nil || len(shots) != 1 {
		t.Fatalf("vetter read %d snapshots, err = %v", len(shots), err)
	}
	if shots[0].URL == "" {
		t.Error("the snapshot carries no download URL")
	}

	stranger := service.Principal{
		Kind: service.PrincipalOrgUser, OrgID: uuid.New(), UserID: uuid.New(),
		Roles: []string{service.RoleAdmin, service.RoleRecruiter, service.RoleVetter},
	}
	if shots, err := f.attempts.Snapshots(ctx, stranger, f.attempt.ID); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("another org read %d snapshots, err = %v; want ErrNotFound", len(shots), err)
	}
	client := service.Principal{Kind: service.PrincipalClientUser, OrgID: f.orgID, UserID: uuid.New(), ClientCompanyID: uuid.New()}
	if _, err := f.attempts.Snapshots(ctx, client, f.attempt.ID); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("client read = %v, want ErrForbidden", err)
	}
}

// The sweep clears frames past the org's retention and keeps the rest. A
// blob the store would not delete keeps its row, so the next sweep tries
// again rather than orphaning the object.
func TestSnapshotPurgeDropsFramesPastRetention(t *testing.T) {
	f := newSnapshotFixture(t)
	ctx := context.Background()
	settings := service.DefaultSettings()
	settings.SnapshotRetentionDays = 1
	if err := service.NewOrgService(f.st).UpdateSettings(ctx, f.principal(service.RoleAdmin), settings); err != nil {
		t.Fatalf("settings: %v", err)
	}

	var old, kept service.AttemptSnapshot
	for seq, into := range map[int]*service.AttemptSnapshot{1: &old, 2: &kept} {
		shot, err := f.attempts.SaveSnapshot(ctx, f.cand, f.attempt.ID, seq, jpegBytes(512))
		if err != nil {
			t.Fatal(err)
		}
		*into = shot
	}
	if _, err := f.sys.Exec(ctx, `update attempt_snapshot set taken_at = now() - interval '2 days' where id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}

	// A store that cannot delete leaves the row for the next sweep.
	f.blob.delErr = errors.New("object store is down")
	h := service.SnapshotPurgeHandler(f.st, f.blob, nil)
	if err := h(ctx, queue.Job{Kind: queue.KindSnapshotPurge, Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("purge with a failing store: %v", err)
	}
	if n := f.snapshotRows(t); n != 2 {
		t.Errorf("%d rows after a failed blob delete, want both kept for retry", n)
	}

	f.blob.delErr = nil
	if err := h(ctx, queue.Job{Kind: queue.KindSnapshotPurge, Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n := f.snapshotRows(t); n != 1 {
		t.Fatalf("%d rows after the purge, want only the fresh frame", n)
	}
	if f.blob.has(old.BlobKey) {
		t.Error("the expired frame is still in object storage")
	}
	if !f.blob.has(kept.BlobKey) {
		t.Error("the fresh frame was purged with the expired one")
	}
	var left uuid.UUID
	if err := f.sys.QueryRow(ctx, `select id from attempt_snapshot where attempt_id = $1`, f.attempt.ID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != kept.ID {
		t.Errorf("the row left is %s, want the fresh frame %s", left, kept.ID)
	}
}
