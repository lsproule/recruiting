package demo

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"strings"
)

// docx writes the smallest Word document that opens everywhere and that the
// platform's résumé extractor reads: a package with one body of paragraphs.
// A demo résumé is a real file a recruiter can download and open, not a
// placeholder.
func docx(paragraphs []string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	add("[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`)
	add("_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`)
	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paragraphs {
		var text bytes.Buffer
		_ = xml.EscapeText(&text, []byte(p))
		body.WriteString(`<w:p><w:r><w:t xml:space="preserve">`)
		body.Write(text.Bytes())
		body.WriteString(`</w:t></w:r></w:p>`)
	}
	body.WriteString(`</w:body></w:document>`)
	add("word/document.xml", body.String())
	_ = zw.Close()
	return buf.Bytes()
}
