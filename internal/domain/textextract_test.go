package domain_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"recruiting/internal/domain"
)

// docx builds the smallest file Word would still open: a zip whose
// word/document.xml holds one paragraph per line.
func docx(t *testing.T, paragraphs ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	write("[Content_Types].xml", `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`)
	var body strings.Builder
	body.WriteString(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paragraphs {
		fmt.Fprintf(&body, `<w:p><w:r><w:t>%s</w:t></w:r></w:p>`, p)
	}
	body.WriteString(`</w:body></w:document>`)
	write("word/document.xml", body.String())
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pdf builds a one-page PDF whose content stream draws line as literal text.
func pdf(t *testing.T, line string) []byte {
	t.Helper()
	content := fmt.Sprintf("BT /F1 24 Tf 72 700 Td (%s) Tj ET\n", line)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, obj := range objects {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, start)
	return buf.Bytes()
}

func TestSniffResumeAcceptsPDFAndDOCX(t *testing.T) {
	if got, err := domain.SniffResume(pdf(t, "hello")); err != nil || got != domain.ResumePDF {
		t.Errorf("pdf sniffed as %q, %v", got, err)
	}
	if got, err := domain.SniffResume(docx(t, "hello")); err != nil || got != domain.ResumeDOCX {
		t.Errorf("docx sniffed as %q, %v", got, err)
	}
}

func TestSniffResumeRejectsAnExecutableNamedLikeADocument(t *testing.T) {
	// An ELF binary renamed resume.pdf: the extension says nothing.
	elf := append([]byte{0x7f, 'E', 'L', 'F', 2, 1, 1}, bytes.Repeat([]byte{0}, 64)...)
	for name, data := range map[string][]byte{
		"executable": elf,
		"zip":        zipOf(t, "notes.txt", "just text"),
	} {
		if got, err := domain.SniffResume(data); !errors.Is(err, domain.ErrResumeType) {
			t.Errorf("%s sniffed as %q, %v; want ErrResumeType", name, got, err)
		}
	}
	if _, err := domain.SniffResume(nil); !errors.Is(err, domain.ErrResumeEmpty) {
		t.Errorf("empty upload returned %v, want ErrResumeEmpty", err)
	}
}

func zipOf(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractTextReadsDOCXParagraphs(t *testing.T) {
	got, err := domain.ExtractText(context.Background(), domain.ResumeDOCX, docx(t, "Ada Lovelace", "Go and Postgres"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Ada Lovelace", "Go and Postgres"} {
		if !strings.Contains(got, want) {
			t.Errorf("extracted %q, want it to contain %q", got, want)
		}
	}
}

func TestExtractTextReadsPDF(t *testing.T) {
	got, err := domain.ExtractText(context.Background(), domain.ResumePDF, pdf(t, "Ada Lovelace analytical engines"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "analytical") {
		t.Errorf("extracted %q, want it to contain %q", got, "analytical")
	}
}

func TestExtractTextFailsOnAFileThatIsNotWhatItClaims(t *testing.T) {
	if _, err := domain.ExtractText(context.Background(), domain.ResumePDF, []byte("%PDF-1.4 and then nonsense")); err == nil {
		t.Error("truncated PDF extracted without error")
	}
}

func TestExtractTextStopsWhenTheDeadlinePasses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := domain.ExtractText(ctx, domain.ResumeDOCX, docx(t, "Ada Lovelace")); !errors.Is(err, context.Canceled) {
		t.Errorf("extraction with a cancelled context returned %v, want context.Canceled", err)
	}
}

// bombDocx builds a .docx whose body decompresses to bodyBytes of text from a
// tiny archive — the shape of a zip bomb.
func bombDocx(t *testing.T, bodyBytes int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	write(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>`)
	chunk := strings.Repeat("a", 1<<16)
	for written := 0; written < bodyBytes; written += len(chunk) {
		write(chunk)
	}
	write(`</w:t></w:r></w:p></w:body></w:document>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractTextRefusesADOCXThatDecompressesTooFar(t *testing.T) {
	bomb := bombDocx(t, domain.MaxDecompressedBytes*3)
	if len(bomb) > domain.MaxResumeBytes {
		t.Fatalf("the bomb is %d bytes; it must pass the upload limit to be a bomb", len(bomb))
	}
	if _, err := domain.ExtractText(context.Background(), domain.ResumeDOCX, bomb); !errors.Is(err, domain.ErrExtractTooBig) {
		t.Errorf("zip bomb extraction returned %v, want ErrExtractTooBig", err)
	}
}

func TestExtractTextCapsTheTextItReturns(t *testing.T) {
	// Under the decompression limit but far past the output cap.
	long := bombDocx(t, domain.MaxExtractedTextBytes*4)
	got, err := domain.ExtractText(context.Background(), domain.ResumeDOCX, long)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > domain.MaxExtractedTextBytes {
		t.Errorf("extracted %d bytes, want at most %d", len(got), domain.MaxExtractedTextBytes)
	}
	if len(got) == 0 {
		t.Error("a long document extracted to nothing")
	}
}

func TestTruncateTextCutsOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("é", domain.MaxExtractedTextBytes)
	got := domain.TruncateText(long)
	if len(got) > domain.MaxExtractedTextBytes {
		t.Fatalf("truncated to %d bytes, want at most %d", len(got), domain.MaxExtractedTextBytes)
	}
	if !utf8.ValidString(got) {
		t.Error("truncation left an invalid UTF-8 string")
	}
}

// TestExtractTextStopsALongDOCXWhenCancelled proves the loop honours ctx
// rather than running to completion in an abandoned goroutine.
func TestExtractTextStopsALongDOCXWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	long := bombDocx(t, domain.MaxExtractedTextBytes)
	if _, err := domain.ExtractText(ctx, domain.ResumeDOCX, long); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled extraction returned %v, want context.Canceled", err)
	}
}

// TestSniffResumeAtReadsOnlyTheHeadOfAFileThatIsNeitherType proves a file
// that is not a PDF or a zip is refused after one small read, so an upload
// of the wrong kind is never pulled into memory to be told so.
func TestSniffResumeAtReadsOnlyTheHeadOfAFileThatIsNeitherType(t *testing.T) {
	junk := &countingReaderAt{r: bytes.NewReader(bytes.Repeat([]byte("MZ"), 1<<20))}
	if got, err := domain.SniffResumeAt(junk, 2<<20); !errors.Is(err, domain.ErrResumeType) {
		t.Fatalf("junk sniffed as %q, %v; want ErrResumeType", got, err)
	}
	if junk.reads.Load() != 1 || junk.bytes.Load() > 512 {
		t.Errorf("sniffing junk took %d reads of %d bytes; want one read of at most 512", junk.reads.Load(), junk.bytes.Load())
	}
	// A PDF is named from its head alone, too.
	doc := &countingReaderAt{r: bytes.NewReader(pdf(t, "hello"))}
	if got, err := domain.SniffResumeAt(doc, int64(len(pdf(t, "hello")))); err != nil || got != domain.ResumePDF {
		t.Fatalf("pdf sniffed as %q, %v", got, err)
	}
	if doc.reads.Load() != 1 {
		t.Errorf("sniffing a pdf took %d reads, want 1", doc.reads.Load())
	}
	// A Word file needs its part list, which is small and at the tail.
	word := docx(t, "hello")
	if got, err := domain.SniffResumeAt(bytes.NewReader(word), int64(len(word))); err != nil || got != domain.ResumeDOCX {
		t.Fatalf("docx sniffed as %q, %v", got, err)
	}
	if _, err := domain.SniffResumeAt(bytes.NewReader(nil), 0); !errors.Is(err, domain.ErrResumeEmpty) {
		t.Errorf("empty file returned %v, want ErrResumeEmpty", err)
	}
}

// TestExtractTextAtReadsFromAFileOnDisk is the upload path: the part sits in
// a temporary file and is parsed in place.
func TestExtractTextAtReadsFromAFileOnDisk(t *testing.T) {
	for name, data := range map[string][]byte{
		domain.ResumeDOCX: docx(t, "Ada Lovelace", "Go and Postgres"),
		domain.ResumePDF:  pdf(t, "Ada Lovelace analytical engines"),
	} {
		path := filepath.Join(t.TempDir(), "resume")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := domain.ExtractTextAt(context.Background(), name, f, int64(len(data)))
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(got, "Ada Lovelace") {
			t.Errorf("%s: extracted %q, want it to contain the name", name, got)
		}
	}
}

// TestExtractTextBoundsConcurrentParses fills every extraction slot with a
// parse that is stuck reading, then shows the next caller waits for a slot
// only as long as its deadline and never touches its file, and that the
// slots come back once the stuck parses fail.
func TestExtractTextBoundsConcurrentParses(t *testing.T) {
	release := make(chan struct{})
	stuck := &blockingReaderAt{release: release}
	var wg sync.WaitGroup
	for range domain.MaxConcurrentExtractions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = domain.ExtractTextAt(context.Background(), domain.ResumePDF, stuck, 4096)
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for stuck.reads.Load() < domain.MaxConcurrentExtractions {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d parses started", stuck.reads.Load(), domain.MaxConcurrentExtractions)
		}
		time.Sleep(5 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	waiting := &countingReaderAt{r: bytes.NewReader(pdf(t, "late"))}
	started := time.Now()
	_, err := domain.ExtractTextAt(ctx, domain.ResumePDF, waiting, int64(len(pdf(t, "late"))))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a caller past the slots returned %v, want DeadlineExceeded", err)
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Errorf("a caller past the slots waited %v, want about its 200ms deadline", took)
	}
	if waiting.reads.Load() != 0 {
		t.Errorf("a caller that got no slot still read its file %d times", waiting.reads.Load())
	}

	close(release)
	wg.Wait()
	// The slots are free again: a parse runs and finishes.
	got, err := domain.ExtractText(context.Background(), domain.ResumeDOCX, docx(t, "after the burst"))
	if err != nil || !strings.Contains(got, "after the burst") {
		t.Fatalf("extraction after the burst = %q, %v", got, err)
	}
}

// TestExtractTextAtAppliesItsOwnTimeout proves a parse is bounded even when
// the caller's context has no deadline: the stuck read is abandoned at the
// extraction timeout rather than held open for good.
func TestExtractTextAtAppliesItsOwnTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the extraction timeout")
	}
	release := make(chan struct{})
	stuck := &blockingReaderAt{release: release}
	defer close(release)
	started := time.Now()
	_, err := domain.ExtractTextAt(context.Background(), domain.ResumePDF, stuck, 4096)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stuck parse returned %v, want DeadlineExceeded", err)
	}
	if took := time.Since(started); took < domain.ExtractTimeout || took > domain.ExtractTimeout+2*time.Second {
		t.Errorf("stuck parse was abandoned after %v, want about %v", took, domain.ExtractTimeout)
	}
}

// countingReaderAt records how the parsers read a file.
type countingReaderAt struct {
	r     io.ReaderAt
	reads atomic.Int64
	bytes atomic.Int64
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	c.reads.Add(1)
	c.bytes.Add(int64(len(p)))
	return c.r.ReadAt(p, off)
}

// blockingReaderAt holds every read until release is closed, then fails it:
// a parse that is running for as long as the test wants.
type blockingReaderAt struct {
	release chan struct{}
	reads   atomic.Int64
}

func (b *blockingReaderAt) ReadAt([]byte, int64) (int, error) {
	b.reads.Add(1)
	<-b.release
	return 0, io.ErrUnexpectedEOF
}
