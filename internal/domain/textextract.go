package domain

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
)

// Resume file limits and the only two content types an intake accepts.
const (
	// MaxResumeBytes is the largest resume an apply form takes.
	MaxResumeBytes = 10 << 20

	ResumePDF  = "application/pdf"
	ResumeDOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
)

// ExtractTimeout bounds extraction so a hostile file cannot hold a request
// open; a resume that runs past it is stored unindexed.
const ExtractTimeout = 10 * time.Second

// Limits that keep a small archive from becoming a large allocation. A .docx
// is a zip, so its parts decompress to whatever the author chose: a few MB of
// upload can claim gigabytes of heap unless both the decompressed part and
// the text built from it are capped.
const (
	// MaxDecompressedBytes is the most any single archive part may expand to.
	MaxDecompressedBytes = 20 << 20
	// MaxExtractedTextBytes caps the text handed back; longer documents are
	// truncated rather than rejected, since the head of a resume is the part
	// worth searching.
	MaxExtractedTextBytes = 2 << 20
)

// ctxCheckInterval is how many XML tokens pass between cancellation checks;
// often enough to stop promptly, rarely enough to stay cheap.
const ctxCheckInterval = 4096

var (
	ErrResumeType     = errors.New("domain: a resume must be a PDF or a Word (.docx) file")
	ErrResumeTooLarge = errors.New("domain: that resume is larger than 10 MB")
	ErrResumeEmpty    = errors.New("domain: no resume file was uploaded")
	ErrExtractTooBig  = errors.New("domain: the resume expands to more text than can be read")
)

// docxEntry is the part of a .docx holding the document body.
const docxEntry = "word/document.xml"

// SniffResume identifies data by its bytes. The uploaded filename is never
// consulted: an executable renamed resume.pdf must not pass.
func SniffResume(data []byte) (string, error) {
	switch {
	case len(data) == 0:
		return "", ErrResumeEmpty
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return ResumePDF, nil
	case isDOCX(data):
		return ResumeDOCX, nil
	}
	return "", ErrResumeType
}

// isDOCX reports whether data is a zip carrying a Word document part. A plain
// zip shares the magic number, so the part list decides.
func isDOCX(data []byte) bool {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return false
	}
	for _, f := range zr.File {
		if f.Name == docxEntry {
			return true
		}
	}
	return false
}

// ExtractText reads the plain text of a sniffed resume, bounded by ctx and by
// the decompression and output limits above. Callers treat any error as "no
// searchable text" rather than a failed upload.
func ExtractText(ctx context.Context, contentType string, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	type result struct {
		text string
		err  error
	}
	// Buffered so the worker never blocks on a caller that has given up.
	done := make(chan result, 1)
	go func() {
		// Third-party parsers panic on malformed input; a resume must never
		// take the process down.
		defer func() {
			if r := recover(); r != nil {
				done <- result{err: fmt.Errorf("domain: extract %s: %v", contentType, r)}
			}
		}()
		text, err := extract(ctx, contentType, data)
		done <- result{text: text, err: err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-done:
		return r.text, r.err
	}
}

func extract(ctx context.Context, contentType string, data []byte) (string, error) {
	switch contentType {
	case ResumePDF:
		return extractPDF(ctx, data)
	case ResumeDOCX:
		return extractDOCX(ctx, data)
	}
	return "", ErrResumeType
}

func extractPDF(ctx context.Context, data []byte) (string, error) {
	// The reader is what the parser pulls bytes through, so cancelling ctx
	// stops the parse rather than only abandoning it.
	r, err := pdf.NewReader(&ctxReaderAt{ctx: ctx, r: bytes.NewReader(data)}, int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("domain: read pdf: %w", err)
	}
	body, err := r.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("domain: read pdf text: %w", err)
	}
	text, err := io.ReadAll(io.LimitReader(body, MaxExtractedTextBytes))
	if err != nil {
		return "", fmt.Errorf("domain: read pdf text: %w", err)
	}
	return normalizeText(string(text)), nil
}

// ctxReaderAt fails every read once ctx is done, which is how a parse that
// takes no cancellation of its own is stopped.
type ctxReaderAt struct {
	ctx context.Context
	r   io.ReaderAt
}

func (c *ctxReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.ReadAt(p, off)
}

func extractDOCX(ctx context.Context, data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("domain: read docx: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != docxEntry {
			continue
		}
		// The declared size is the archive's own claim, so it is checked
		// first and then enforced again while reading.
		if f.UncompressedSize64 > MaxDecompressedBytes {
			return "", ErrExtractTooBig
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("domain: read docx: %w", err)
		}
		defer rc.Close()
		return docxText(ctx, io.LimitReader(rc, MaxDecompressedBytes+1))
	}
	return "", fmt.Errorf("domain: read docx: no %s part", docxEntry)
}

// docxText streams the WordprocessingML body, keeping <w:t> runs and turning
// paragraph and line breaks into newlines. It stops at the output cap and at
// ctx cancellation, so a hostile document cannot outlive its request.
func docxText(ctx context.Context, r io.Reader) (string, error) {
	dec := xml.NewDecoder(io.LimitReader(r, MaxDecompressedBytes+1))
	var out strings.Builder
	inText := false
	for n := 0; ; n++ {
		if n%ctxCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		if out.Len() >= MaxExtractedTextBytes {
			break
		}
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("domain: read docx text: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "br", "tab":
				out.WriteString(" ")
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				out.WriteString("\n")
			}
		case xml.CharData:
			if inText {
				out.Write(t)
			}
		}
	}
	return normalizeText(TruncateText(out.String())), nil
}

// TruncateText cuts s to the output cap on a rune boundary. It is exported so
// callers that persist extracted text enforce the same ceiling.
func TruncateText(s string) string {
	if len(s) <= MaxExtractedTextBytes {
		return s
	}
	cut := MaxExtractedTextBytes
	// Back off to the last byte that starts a rune, so the cut never leaves a
	// half-encoded character behind.
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

// normalizeText collapses the whitespace an extractor leaves behind so the
// stored text indexes and displays cleanly.
func normalizeText(s string) string {
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
