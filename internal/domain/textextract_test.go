package domain_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
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
