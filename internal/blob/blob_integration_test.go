//go:build integration

package blob_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/blob"
)

// newClient builds a client against the Compose MinIO. The blob settings are
// separate from DATABASE_URL, so a database-only run skips rather than fails.
func newClient(t *testing.T) *blob.Client {
	t.Helper()
	cfg := blob.Config{
		Endpoint: os.Getenv("BLOB_ENDPOINT"),
		Bucket:   os.Getenv("BLOB_BUCKET"),
		Key:      os.Getenv("BLOB_KEY"),
		Secret:   os.Getenv("BLOB_SECRET"),
	}
	if cfg.Endpoint == "" {
		t.Skip("BLOB_ENDPOINT is not set; run `make dev-up` and export the BLOB_* settings from .env.example to exercise object storage")
	}
	c, err := blob.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureBucket(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestObjectRoundTripAndSignedURL(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	key := "test/" + uuid.NewString() + ".pdf"
	body := []byte("%PDF-1.4 pretend resume")

	if err := c.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), key) })

	rc, err := c.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("read back %q, want %q", got, body)
	}

	// A signed URL is fetchable without credentials and names the download.
	url, err := c.SignedGetURL(ctx, key, "Ada Lovelace.pdf", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.Get(url) //nolint:noctx // the signed URL is the subject under test
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	signed, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !bytes.Equal(signed, body) {
		t.Fatalf("signed URL returned %d %q", res.StatusCode, signed)
	}
	if cd := res.Header.Get("Content-Disposition"); !strings.Contains(cd, "Ada Lovelace.pdf") {
		t.Errorf("Content-Disposition %q does not name the file", cd)
	}

	if err := c.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, key); err == nil {
		t.Error("deleted object is still readable")
	}
}

func TestSignedURLExpires(t *testing.T) {
	c := newClient(t)
	if _, err := c.SignedGetURL(context.Background(), "test/whatever.pdf", "cv.pdf", 0); err == nil {
		t.Error("a zero lifetime produced a URL; want an error")
	}
}
