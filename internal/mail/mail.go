package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io/fs"
	"mime"
	"sort"
	"strings"
	texttemplate "text/template"
	"time"

	templates "recruiting/templates/email"
)

// The templates the specification lists. Callers name one of these when they
// enqueue an email.send job.
const (
	TemplateApplyReceived       = "apply_received"
	TemplateBookingInvite       = "booking_invite"
	TemplateBookingConfirmation = "booking_confirmation"
	TemplateReminder            = "reminder"
	TemplateRescheduleCancel    = "reschedule_cancel"
	TemplateAssessmentInvite    = "assessment_invite"
	TemplateAssessmentReminder  = "assessment_reminder"
	TemplateAssessmentDeclined  = "assessment_declined"
	TemplateClientReleaseNotice = "client_release_notice"
	TemplateClientRequestInfo   = "client_request_info"
	TemplateClientShortlist     = "client_shortlist"
	TemplatePasswordReset       = "password_reset"
	TemplateSprintInvite        = "sprint_invite"
	TemplateTalentWelcome       = "talent_welcome"
	TemplateOpportunity         = "opportunity"
	TemplateApplicationRejected = "application_rejected"
)

// ErrUnknownTemplate is returned for a name no template file answers to.
var ErrUnknownTemplate = errors.New("mail: unknown template")

// Message is one rendered email. Both bodies are always present; the wire
// format sends them as alternatives. Attachments, when there are any, ride
// alongside in a multipart/mixed envelope.
type Message struct {
	From        string
	To          string
	Subject     string
	Text        string
	HTML        string
	Attachments []Attachment
}

// Attachment is one file on a message.
type Attachment struct {
	Filename    string
	ContentType string
	Body        []byte
}

// tripleSuffixes are the three files that make up one template.
const (
	subjectSuffix = ".subject.tmpl"
	textSuffix    = ".txt.tmpl"
	htmlSuffix    = ".html.tmpl"
)

type templateSet struct {
	subject *texttemplate.Template
	text    *texttemplate.Template
	html    *htmltemplate.Template
}

// Renderer renders the embedded templates. It is safe for concurrent use.
type Renderer struct {
	sets map[string]templateSet
}

// NewRenderer parses every embedded template. A template referring to a
// variable the caller does not supply fails at render time rather than
// emitting "<no value>", so a mis-built payload never reaches a recipient.
func NewRenderer() (*Renderer, error) {
	names, err := fs.Glob(templates.FS, "*"+subjectSuffix)
	if err != nil {
		return nil, fmt.Errorf("mail: list templates: %w", err)
	}
	r := &Renderer{sets: make(map[string]templateSet, len(names))}
	for _, file := range names {
		name := strings.TrimSuffix(file, subjectSuffix)
		set, err := parseSet(name)
		if err != nil {
			return nil, err
		}
		r.sets[name] = set
	}
	if len(r.sets) == 0 {
		return nil, errors.New("mail: no templates embedded")
	}
	return r, nil
}

func parseSet(name string) (templateSet, error) {
	read := func(suffix string) (string, error) {
		b, err := templates.FS.ReadFile(name + suffix)
		if err != nil {
			return "", fmt.Errorf("mail: template %s: %w", name, err)
		}
		return string(b), nil
	}
	var set templateSet
	for _, part := range []struct {
		suffix string
		assign func(string) error
	}{
		{subjectSuffix, func(s string) (err error) {
			set.subject, err = texttemplate.New(name + subjectSuffix).Option("missingkey=error").Parse(s)
			return err
		}},
		{textSuffix, func(s string) (err error) {
			set.text, err = texttemplate.New(name + textSuffix).Option("missingkey=error").Parse(s)
			return err
		}},
		{htmlSuffix, func(s string) (err error) {
			set.html, err = htmltemplate.New(name + htmlSuffix).Option("missingkey=error").Parse(s)
			return err
		}},
	} {
		body, err := read(part.suffix)
		if err != nil {
			return templateSet{}, err
		}
		if err := part.assign(body); err != nil {
			return templateSet{}, fmt.Errorf("mail: parse %s%s: %w", name, part.suffix, err)
		}
	}
	return set, nil
}

// Names lists the templates that exist, sorted.
func (r *Renderer) Names() []string {
	names := make([]string, 0, len(r.sets))
	for name := range r.sets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Render builds the message for name. orgName is the only branding a template
// gets; data supplies the rest. From and To are the caller's to fill in.
func (r *Renderer) Render(name, orgName string, data map[string]any) (Message, error) {
	set, ok := r.sets[name]
	if !ok {
		return Message{}, fmt.Errorf("%w: %s", ErrUnknownTemplate, name)
	}
	ctx := make(map[string]any, len(data)+1)
	for k, v := range data {
		ctx[k] = v
	}
	// Branding wins: a payload cannot rename the org it is sent on behalf of.
	ctx["OrgName"] = orgName

	var subject, text, html bytes.Buffer
	if err := set.subject.Execute(&subject, ctx); err != nil {
		return Message{}, fmt.Errorf("mail: render %s subject: %w", name, err)
	}
	if err := set.text.Execute(&text, ctx); err != nil {
		return Message{}, fmt.Errorf("mail: render %s text: %w", name, err)
	}
	if err := set.html.Execute(&html, ctx); err != nil {
		return Message{}, fmt.Errorf("mail: render %s html: %w", name, err)
	}
	return Message{
		Subject: strings.TrimSpace(subject.String()),
		Text:    text.String(),
		HTML:    html.String(),
	}, nil
}

// Bytes renders the RFC 5322 message: a multipart/alternative with the plain
// body first, so a client that cannot show HTML still reads something.
func (m Message) Bytes() []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", headerValue(m.From))
	fmt.Fprintf(&b, "To: %s\r\n", headerValue(m.To))
	// A subject is rendered from tenant data and may be non-ASCII; RFC 2047
	// encodes it only when it has to be.
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", headerValue(m.Subject)))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: %s\r\n", messageID(m.From))
	b.WriteString("MIME-Version: 1.0\r\n")
	if len(m.Attachments) == 0 {
		m.writeAlternative(&b)
		return b.Bytes()
	}
	// With attachments the bodies become the first part of a mixed message,
	// so a client shows the text and lists the files beside it.
	mixed := randomBoundary()
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", mixed)
	fmt.Fprintf(&b, "--%s\r\n", mixed)
	m.writeAlternative(&b)
	for _, a := range m.Attachments {
		fmt.Fprintf(&b, "--%s\r\n", mixed)
		fmt.Fprintf(&b, "Content-Type: %s; name=%q\r\n", a.ContentType, headerValue(a.Filename))
		fmt.Fprintf(&b, "Content-Disposition: attachment; filename=%q\r\n", headerValue(a.Filename))
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		b.WriteString(base64Lines(a.Body))
		b.WriteString("\r\n")
	}
	fmt.Fprintf(&b, "--%s--\r\n", mixed)
	return b.Bytes()
}

// writeAlternative writes the multipart/alternative body with its own
// Content-Type line: the whole message when there are no attachments, one
// part of the mixed message when there are.
func (m Message) writeAlternative(b *bytes.Buffer) {
	boundary := randomBoundary()
	fmt.Fprintf(b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	for _, part := range []struct{ contentType, body string }{
		{"text/plain; charset=utf-8", m.Text},
		{"text/html; charset=utf-8", m.HTML},
	} {
		fmt.Fprintf(b, "--%s\r\n", boundary)
		fmt.Fprintf(b, "Content-Type: %s\r\n\r\n", part.contentType)
		b.WriteString(crlf(part.body))
		b.WriteString("\r\n")
	}
	fmt.Fprintf(b, "--%s--\r\n", boundary)
}

// base64Lines encodes a body in the 76-column lines RFC 2045 asks for.
func base64Lines(body []byte) string {
	enc := base64.StdEncoding.EncodeToString(body)
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	return b.String()
}

// headerValue keeps a header on one line; a rendered subject must not be able
// to inject further headers.
func headerValue(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

func crlf(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

func randomBoundary() string {
	return "part" + randomHex()
}

// messageID gives every message the unique identifier threading and duplicate
// detection rely on, in the domain the sender belongs to.
func messageID(from string) string {
	domain := "localhost"
	if at := strings.LastIndex(from, "@"); at >= 0 && at+1 < len(from) {
		domain = strings.Trim(from[at+1:], "<> ")
	}
	return "<" + randomHex() + "@" + domain + ">"
}

func randomHex() string {
	var b [16]byte
	// crypto/rand.Read never fails on any platform Go supports.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
