package mail_test

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	recmail "recruiting/internal/mail"
)

func TestICSCarriesTheEventInUTC(t *testing.T) {
	start := time.Date(2026, 10, 2, 13, 30, 0, 0, time.FixedZone("CEST", 2*3600))
	ev := recmail.Event{
		UID: "interview-1@recruiting", Summary: "Interview: Backend Engineer", Description: "Bring questions; no prep needed.",
		Location: "https://app.example/book/abc", URL: "https://app.example/book/abc",
		Start: start, End: start.Add(30 * time.Minute),
	}
	ics := string(ev.ICS())
	for _, want := range []string{
		"BEGIN:VCALENDAR\r\n", "METHOD:REQUEST\r\n", "BEGIN:VEVENT\r\n",
		"UID:interview-1@recruiting\r\n",
		"DTSTART:20261002T113000Z\r\n", "DTEND:20261002T120000Z\r\n",
		"SUMMARY:Interview: Backend Engineer\r\n",
		`DESCRIPTION:Bring questions\; no prep needed.` + "\r\n",
		"END:VEVENT\r\n", "END:VCALENDAR\r\n",
	} {
		if !strings.Contains(ics, want) {
			t.Errorf("ics lacks %q:\n%s", want, ics)
		}
	}
	for _, line := range strings.Split(ics, "\r\n") {
		if len(line) > 75 {
			t.Errorf("line longer than 75 octets: %q", line)
		}
	}
	link := recmail.GoogleCalendarURL(ev)
	if !strings.Contains(link, "dates=20261002T113000Z%2F20261002T120000Z") || !strings.Contains(link, "text=Interview") {
		t.Errorf("google link = %s", link)
	}
}

func TestAMessageWithAnAttachmentIsMultipartMixed(t *testing.T) {
	m := recmail.Message{
		From: "no-reply@example.com", To: "ada@example.com", Subject: "Interview confirmed",
		Text: "plain", HTML: "<p>html</p>",
		Attachments: []recmail.Attachment{recmail.ICSAttachment(recmail.Event{UID: "u", Summary: "s", Start: time.Now(), End: time.Now().Add(time.Hour)})},
	}
	msg, err := mail.ReadMessage(bytes.NewReader(m.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" {
		t.Fatalf("content type = %s (%v), want multipart/mixed", mediaType, err)
	}
	var types []string
	var ics string
	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		ct := part.Header.Get("Content-Type")
		types = append(types, strings.SplitN(ct, ";", 2)[0])
		if strings.HasPrefix(ct, "text/calendar") {
			// The reader hands back the transfer encoding as written; the
			// attachment is base64, so decode it as a mail client would.
			body, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
			ics = string(body)
			if part.Header.Get("Content-Disposition") != `attachment; filename="interview.ics"` {
				t.Errorf("disposition = %q", part.Header.Get("Content-Disposition"))
			}
		}
	}
	if len(types) != 2 || types[0] != "multipart/alternative" || types[1] != "text/calendar" {
		t.Fatalf("parts = %v, want the bodies then the calendar", types)
	}
	if !strings.Contains(ics, "BEGIN:VCALENDAR") {
		t.Fatalf("calendar part did not decode: %q", ics)
	}
	// Without attachments the message stays the plain alternative it was.
	m.Attachments = nil
	plain, _ := mail.ReadMessage(bytes.NewReader(m.Bytes()))
	if mt, _, _ := mime.ParseMediaType(plain.Header.Get("Content-Type")); mt != "multipart/alternative" {
		t.Fatalf("content type without attachments = %s", mt)
	}
}
