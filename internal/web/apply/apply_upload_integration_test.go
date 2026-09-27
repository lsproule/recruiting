//go:build integration

package apply_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"strings"
	"testing"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

// paddedDocx is a Word file of exactly size bytes: the usual document part
// plus an incompressible media part that brings the archive to the length
// asked for. Word would still open it, and the text still extracts.
func paddedDocx(t *testing.T, size int, paragraphs ...string) []byte {
	t.Helper()
	build := func(pad int) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, err := zw.CreateHeader(&zip.FileHeader{Name: "word/document.xml", Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		var body strings.Builder
		body.WriteString(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
		for _, p := range paragraphs {
			body.WriteString(`<w:p><w:r><w:t>` + p + `</w:t></w:r></w:p>`)
		}
		body.WriteString(`</w:body></w:document>`)
		if _, err := w.Write([]byte(body.String())); err != nil {
			t.Fatal(err)
		}
		// Stored, not deflated, so the padding's size in the archive is its own.
		w, err = zw.CreateHeader(&zip.FileHeader{Name: "word/media/image1.bin", Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		noise := make([]byte, pad)
		if _, err := rand.Read(noise); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(noise); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	// The archive's overhead is fixed, so one unpadded build says how much
	// padding reaches the size asked for.
	base := len(build(0))
	if base > size {
		t.Fatalf("a bare document is already %d bytes, past %d", base, size)
	}
	out := build(size - base)
	if len(out) != size {
		t.Fatalf("padded document is %d bytes, want %d", len(out), size)
	}
	return out
}

// TestApplyPageTakesAResumeAtTheSizeLimit uploads exactly the 10 MB the form
// allows: the part is spooled past the parser's memory cap, read in place
// by the object store and the extractor, and both see the whole file.
func TestApplyPageTakesAResumeAtTheSizeLimit(t *testing.T) {
	f := newFixture(t)
	v := f.visitor(t)
	v.get(f.applyURL(f.orgSlug, f.openSlug))

	resume := paddedDocx(t, domain.MaxResumeBytes, "Grace Hopper", "COBOL and compilers")
	res, body := v.submit(f.applyURL(f.orgSlug, f.openSlug), applyFields("grace@example.com"), "grace.docx", resume)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("submit at the limit = %d, body %q", res.StatusCode, body)
	}
	if !strings.Contains(body, "Thank you") {
		t.Errorf("body after applying does not thank the applicant: %q", body)
	}

	// The stored object is the whole file, and its text was read.
	if len(f.blob.objects) != 1 {
		t.Fatalf("%d objects stored, want 1", len(f.blob.objects))
	}
	for key, stored := range f.blob.objects {
		if !bytes.Equal(stored, resume) {
			t.Errorf("object %s holds %d bytes, want the %d uploaded", key, len(stored), len(resume))
		}
	}
	recruiter := service.Principal{Kind: service.PrincipalOrgUser, OrgID: f.orgID, Roles: []string{service.RoleRecruiter}}
	found, err := f.candidates.Search(context.Background(), recruiter, "compilers")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Email != "grace@example.com" {
		t.Fatalf("search for the resume's text found %+v", found)
	}
	detail, err := f.candidates.Detail(context.Background(), recruiter, found[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Resumes) != 1 || detail.Resumes[0].SizeBytes != int64(len(resume)) || detail.Resumes[0].TextStatus != service.ResumeExtracted {
		t.Errorf("resumes = %+v, want one of %d bytes with its text extracted", detail.Resumes, len(resume))
	}

	// One byte past the limit is still refused, by its declared size, before
	// any of it is read.
	over := append(paddedDocx(t, domain.MaxResumeBytes, "Too Big"), 0)
	res, body = v.submit(f.applyURL(f.orgSlug, f.openSlug), applyFields("big@example.com"), "big.docx", over)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "10 MB") {
		t.Fatalf("one byte over = %d, body %q", res.StatusCode, body)
	}
}
