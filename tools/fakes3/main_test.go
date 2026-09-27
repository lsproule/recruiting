package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"recruiting/internal/blob"
)

const mib = 1 << 20

// newServer serves a store and counts, per request, how much of each body
// the store actually pulled from the connection; serverRead holds the last
// request's count.
func newServer(t *testing.T, maxResident, maxObject int64) (*store, *httptest.Server, *atomic.Int64) {
	t.Helper()
	s := newStore(maxResident, maxObject)
	var serverRead atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counted := &countingReader{r: r.Body}
		r.Body = struct {
			io.Reader
			io.Closer
		}{counted, r.Body}
		s.ServeHTTP(w, r)
		serverRead.Store(counted.read.Load())
	}))
	t.Cleanup(srv.Close)
	return s, srv, &serverRead
}

func do(t *testing.T, method, url string, body io.Reader, headers ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	_, _ = io.Copy(io.Discard, res.Body)
	return res
}

func makeBucket(t *testing.T, base, bucket string) {
	t.Helper()
	if res := do(t, http.MethodPut, base+"/"+bucket, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("create bucket = %d", res.StatusCode)
	}
}

// countingReader records how much was read through it.
type countingReader struct {
	r    io.Reader
	read atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read.Add(int64(n))
	return n, err
}

func TestPutChecksTheBucketBeforeReadingTheBody(t *testing.T) {
	_, srv, serverRead := newServer(t, 64*mib, 16*mib)
	res := do(t, http.MethodPut, srv.URL+"/nobucket/key", bytes.NewReader(bytes.Repeat([]byte("a"), mib)))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("put to a missing bucket = %d, want 404", res.StatusCode)
	}
	if serverRead.Load() != 0 {
		t.Errorf("the server read %d bytes of a body it had no bucket for", serverRead.Load())
	}
}

func TestPutRefusesAnOversizedObject(t *testing.T) {
	s, srv, serverRead := newServer(t, 64*mib, mib)
	makeBucket(t, srv.URL, "b")

	// Declared up front: refused without a read.
	res := do(t, http.MethodPut, srv.URL+"/b/big", bytes.NewReader(bytes.Repeat([]byte("a"), mib+1)))
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized put = %d, want 413", res.StatusCode)
	}
	if serverRead.Load() != 0 {
		t.Errorf("the server read %d bytes of an upload it had refused by length", serverRead.Load())
	}

	// Undeclared (chunked transfer): read only up to the cap, then refused.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, srv.URL+"/b/big", io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("a"), 8*mib))))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	res, err = http.DefaultClient.Do(req)
	if err == nil {
		res.Body.Close()
		if res.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("undeclared oversized put = %d, want 413", res.StatusCode)
		}
	}
	// The client may see the connection reset instead of the status when the
	// server answers before the body is sent; either way the read stopped.
	if got := serverRead.Load(); got != mib+1 {
		t.Errorf("the server pulled %d bytes of an undeclared body, want it to stop at %d", got, mib+1)
	}

	// The object at the limit itself is fine.
	res = do(t, http.MethodPut, srv.URL+"/b/ok", bytes.NewReader(bytes.Repeat([]byte("a"), mib)))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("put at the limit = %d", res.StatusCode)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.resident != mib || s.order.Len() != 1 {
		t.Errorf("resident = %d bytes in %d objects, want %d in 1", s.resident, s.order.Len(), mib)
	}
}

func TestStoreEvictsTheOldestObjectsPastItsBudget(t *testing.T) {
	s, srv, _ := newServer(t, 3*mib, mib)
	makeBucket(t, srv.URL, "b")
	put := func(key string, size int) {
		t.Helper()
		if res := do(t, http.MethodPut, srv.URL+"/b/"+key, bytes.NewReader(bytes.Repeat([]byte(key[:1]), size))); res.StatusCode != http.StatusOK {
			t.Fatalf("put %s = %d", key, res.StatusCode)
		}
	}
	status := func(key string) int {
		t.Helper()
		return do(t, http.MethodHead, srv.URL+"/b/"+key, nil).StatusCode
	}
	put("a", mib)
	put("b", mib)
	put("c", mib)
	put("d", mib)
	if status("a") != http.StatusNotFound {
		t.Error("the oldest object survived a put past the budget")
	}
	for _, key := range []string{"b", "c", "d"} {
		if status(key) != http.StatusOK {
			t.Errorf("object %s was evicted, want only the oldest gone", key)
		}
	}
	// Overwriting a key frees what it held, so the budget is not charged twice.
	put("b", mib/2)
	s.mu.RLock()
	resident, n := s.resident, s.order.Len()
	s.mu.RUnlock()
	if resident != 2*mib+mib/2 || n != 3 {
		t.Errorf("resident = %d bytes in %d objects after an overwrite, want %d in 3", resident, n, 2*mib+mib/2)
	}
	// Deleting frees at once.
	if res := do(t, http.MethodDelete, srv.URL+"/b/c", nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", res.StatusCode)
	}
	s.mu.RLock()
	resident, n = s.resident, s.order.Len()
	s.mu.RUnlock()
	if resident != mib+mib/2 || n != 2 {
		t.Errorf("resident = %d bytes in %d objects after a delete, want %d in 2", resident, n, mib+mib/2)
	}
	if status("c") != http.StatusNotFound {
		t.Error("a deleted object is still served")
	}
}

func TestPutUnframesAnAWSChunkedBody(t *testing.T) {
	_, srv, _ := newServer(t, 64*mib, 16*mib)
	makeBucket(t, srv.URL, "b")
	payload := bytes.Repeat([]byte("resume"), 20_000)
	var framed bytes.Buffer
	for chunk := payload; len(chunk) > 0; {
		n := min(65536, len(chunk))
		fmt.Fprintf(&framed, "%x;chunk-signature=%s\r\n", n, strings.Repeat("0", 64))
		framed.Write(chunk[:n])
		framed.WriteString("\r\n")
		chunk = chunk[n:]
	}
	fmt.Fprintf(&framed, "0;chunk-signature=%s\r\n\r\n", strings.Repeat("0", 64))
	res := do(t, http.MethodPut, srv.URL+"/b/chunked", &framed,
		"Content-Encoding", "aws-chunked",
		"X-Amz-Content-Sha256", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD",
		"X-Amz-Decoded-Content-Length", fmt.Sprint(len(payload)))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("chunked put = %d", res.StatusCode)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/b/chunked", nil)
	got, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	body, _ := io.ReadAll(got.Body)
	if !bytes.Equal(body, payload) {
		t.Errorf("read back %d bytes, want the %d-byte payload without framing", len(body), len(payload))
	}
}

// TestWorksWithTheBlobClient drives the store through internal/blob, the
// client the platform uses, so its streaming uploads, signed URLs and
// deletes all round-trip.
func TestWorksWithTheBlobClient(t *testing.T) {
	s, srv, _ := newServer(t, 64*mib, 16*mib)
	c, err := blob.New(blob.Config{Endpoint: srv.URL, Bucket: "resumes", Key: "k", Secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.EnsureBucket(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureBucket(ctx); err != nil {
		t.Fatalf("second EnsureBucket: %v", err)
	}
	body := bytes.Repeat([]byte("%PDF-1.4 pretend resume\n"), 100_000)
	if err := c.Put(ctx, "a/b.pdf", bytes.NewReader(body), int64(len(body)), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	rc, err := c.Get(ctx, "a/b.pdf")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("read back %d bytes, %v; want the %d put", len(got), err, len(body))
	}
	url, err := c.SignedGetURL(ctx, "a/b.pdf", "cv.pdf", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	res := do(t, http.MethodGet, url, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Disposition"), "cv.pdf") {
		t.Errorf("signed URL = %d, disposition %q", res.StatusCode, res.Header.Get("Content-Disposition"))
	}
	if err := c.Delete(ctx, "a/b.pdf"); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "a/b.pdf"); err != nil {
		t.Errorf("deleting a missing key: %v", err)
	}
	if _, err := c.Get(ctx, "a/b.pdf"); err == nil {
		t.Error("a deleted object is still readable")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.resident != 0 || s.order.Len() != 0 {
		t.Errorf("resident = %d bytes in %d objects after the delete, want nothing", s.resident, s.order.Len())
	}
	// An object past the per-object cap is refused as the client would see
	// it from S3.
	if err := c.Put(ctx, "a/huge.pdf", bytes.NewReader(make([]byte, 17*mib)), 17*mib, "application/pdf"); err == nil {
		t.Error("an object past the cap was accepted")
	}
}
