package mail_test

import (
	"strings"
	"testing"

	"recruiting/internal/mail"
)

// sampleData is a superset of every variable the templates use, so one map
// renders them all.
var sampleData = map[string]any{
	"CandidateName":     "Ada Lovelace",
	"JobTitle":          "Backend Engineer",
	"BookingURL":        "https://example.test/book/abc",
	"AssessmentURL":     "https://example.test/assess/abc",
	"ApplicationURL":    "https://example.test/app/applications/abc",
	"PortalURL":         "https://example.test/client/jobs",
	"ResetURL":          "https://example.test/app/reset/abc",
	"ExpiresAt":         "2026-09-01 10:00 UTC",
	"StartsAt":          "2026-09-02 14:00",
	"Timezone":          "Europe/Berlin",
	"Change":            "rescheduled",
	"Reason":            "The interviewer is unavailable.",
	"ContactName":       "Grace Hopper",
	"ClientContactName": "Grace Hopper",
	"ClientCompanyName": "Acme",
	"RecruiterName":     "Alan Turing",
	"CandidateCount":    3,
	"Message":           "Can we see the scorecard?",
}

// Every template the specification lists must exist with a subject and both
// bodies.
func TestRenderEveryTemplate(t *testing.T) {
	r, err := mail.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		mail.TemplateApplyReceived,
		mail.TemplateBookingInvite,
		mail.TemplateBookingConfirmation,
		mail.TemplateReminder,
		mail.TemplateRescheduleCancel,
		mail.TemplateAssessmentInvite,
		mail.TemplateAssessmentReminder,
		mail.TemplateAssessmentDeclined,
		mail.TemplateClientReleaseNotice,
		mail.TemplateClientRequestInfo,
		mail.TemplateClientShortlist,
		mail.TemplatePasswordReset,
	}
	if got := r.Names(); len(got) != len(want) {
		t.Errorf("Names() = %v, want the %d specified templates", got, len(want))
	}
	for _, name := range want {
		msg, err := r.Render(name, "Northwind Recruiting", sampleData)
		if err != nil {
			t.Errorf("render %s: %v", name, err)
			continue
		}
		if msg.Subject == "" || msg.Text == "" || msg.HTML == "" {
			t.Errorf("render %s: subject/text/html must all be set, got %+v", name, msg)
		}
		if !strings.Contains(msg.Text, "Northwind Recruiting") {
			t.Errorf("render %s: text body does not carry the org name", name)
		}
	}
}

func TestRenderUnknownTemplate(t *testing.T) {
	r, err := mail.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Render("no_such_template", "Northwind", sampleData); err == nil {
		t.Fatal("want an error for an unknown template")
	}
}

// A template variable the payload does not carry is a bug in the enqueuing
// code; rendering must fail rather than send a half-filled email.
func TestRenderMissingVariable(t *testing.T) {
	r, err := mail.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Render(mail.TemplateApplyReceived, "Northwind", map[string]any{"CandidateName": "Ada"})
	if err == nil {
		t.Fatal("want an error when a variable is missing")
	}
	if !strings.Contains(err.Error(), mail.TemplateApplyReceived) {
		t.Errorf("error %q should name the template", err)
	}
}

// The wire format has to carry both bodies so every client shows something.
func TestMessageBytesIsMultipartAlternative(t *testing.T) {
	msg := mail.Message{
		From:    "no-reply@example.test",
		To:      "ada@example.test",
		Subject: "Hello",
		Text:    "plain body",
		HTML:    "<p>rich body</p>",
	}
	raw := string(msg.Bytes())
	for _, want := range []string{
		"To: ada@example.test",
		"Subject: Hello",
		"multipart/alternative",
		"text/plain",
		"text/html",
		"plain body",
		"rich body",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("message does not contain %q:\n%s", want, raw)
		}
	}
}

// Mail carries reset links and candidate data, so a cleartext session to a
// remote server is refused unless the URL opts out in so many words.
func TestSMTPSchemeDecidesWhetherTLSIsRequired(t *testing.T) {
	for _, tc := range []struct {
		url        string
		requireTLS bool
	}{
		{"smtp://localhost:1025", false},
		{"smtp://127.0.0.1:1025", false},
		{"smtp://[::1]:1025", false},
		{"smtp://mail.example.test:587", true},
		{"smtps://mail.example.test:465", false}, // TLS from the first byte
		{"smtp+insecure://mail.example.test:25", false},
	} {
		s, err := mail.NewSMTPSender(tc.url, "no-reply@example.test")
		if err != nil {
			t.Errorf("NewSMTPSender(%q): %v", tc.url, err)
			continue
		}
		if s.RequiresTLS() != tc.requireTLS {
			t.Errorf("NewSMTPSender(%q).RequiresTLS() = %v, want %v", tc.url, s.RequiresTLS(), tc.requireTLS)
		}
	}
}

func TestSMTPSenderRejectsAnUnknownScheme(t *testing.T) {
	if _, err := mail.NewSMTPSender("http://mail.example.test", "no-reply@example.test"); err == nil {
		t.Fatal("want an error for a non-SMTP scheme")
	}
}

// Every message needs the headers a receiving server expects to see.
func TestMessageCarriesDateAndMessageID(t *testing.T) {
	raw := string(mail.Message{
		From: "no-reply@example.test", To: "ada@example.test",
		Subject: "Hallo, schöne Grüße", Text: "t", HTML: "<p>h</p>",
	}.Bytes())
	for _, want := range []string{"Date: ", "Message-ID: <", "@example.test>"} {
		if !strings.Contains(raw, want) {
			t.Errorf("message does not contain %q:\n%s", want, raw)
		}
	}
	// A non-ASCII subject travels RFC 2047 encoded, never as raw bytes.
	if strings.Contains(raw, "schöne") {
		t.Errorf("non-ASCII subject was not encoded:\n%s", raw)
	}
	if !strings.Contains(raw, "Subject: =?utf-8?q?") {
		t.Errorf("subject is not RFC 2047 encoded:\n%s", raw)
	}
}
