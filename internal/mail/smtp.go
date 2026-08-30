package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

// Sender delivers a rendered message. The email job depends on this rather
// than on SMTP, so a test can observe what would have been sent.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// DefaultFromLocalPart is the mailbox outbound mail comes from when the
// caller does not name one.
const DefaultFromLocalPart = "no-reply"

// sendTimeout bounds a whole delivery when the caller's context carries no
// deadline of its own; net/smtp is not context-aware, so without it a server
// that accepts the connection and then stalls would hold a worker forever.
const sendTimeout = 30 * time.Second

var (
	ErrNoSMTPURL = errors.New("mail: SMTP_URL is required")
	// ErrNoSTARTTLS reports a remote server that will not encrypt the
	// session. Mail carries reset links and candidate data; it does not go
	// out in the clear across a network.
	ErrNoSTARTTLS = errors.New("mail: server does not offer STARTTLS")
)

// SMTPSender talks to the SMTP server named by SMTP_URL.
//
//	smtps://           TLS from the first byte.
//	smtp://            Cleartext connection upgraded with STARTTLS, which is
//	                   required unless the host is loopback — a development
//	                   Mailpit offers no TLS and needs none, since nothing
//	                   leaves the machine.
//	smtp+insecure://   Cleartext with no upgrade, even to a remote host. An
//	                   explicit opt-out for a network that is private by other
//	                   means; never the default.
//
// Credentials, when the URL carries them, are sent with PLAIN auth.
type SMTPSender struct {
	addr       string
	from       string
	tls        bool
	requireTLS bool
	auth       smtp.Auth
	hostname   string
}

// InsecureScheme is the SMTP_URL scheme that waives the STARTTLS requirement.
const InsecureScheme = "smtp+insecure"

// NewSMTPSender parses smtpURL. from is the envelope and header sender.
func NewSMTPSender(smtpURL, from string) (*SMTPSender, error) {
	if strings.TrimSpace(smtpURL) == "" {
		return nil, ErrNoSMTPURL
	}
	u, err := url.Parse(smtpURL)
	if err != nil {
		return nil, fmt.Errorf("mail: bad SMTP_URL: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("mail: bad SMTP_URL %q: no host", smtpURL)
	}
	useTLS := false
	requireTLS := false
	switch u.Scheme {
	case "smtps":
		useTLS = true
	case "smtp":
		requireTLS = !isLoopback(host)
	case InsecureScheme:
	default:
		return nil, fmt.Errorf("mail: bad SMTP_URL scheme %q: want smtp, smtps, or %s", u.Scheme, InsecureScheme)
	}
	port := u.Port()
	if port == "" {
		port = "25"
		if useTLS {
			port = "465"
		}
	}
	s := &SMTPSender{
		addr:       net.JoinHostPort(host, port),
		from:       from,
		tls:        useTLS,
		requireTLS: requireTLS,
		hostname:   host,
	}
	if u.User != nil {
		pass, _ := u.User.Password()
		s.auth = smtp.PlainAuth("", u.User.Username(), pass, host)
	}
	return s, nil
}

// isLoopback reports whether host names this machine. A literal address is
// checked as one; "localhost" is taken at its word rather than resolved, so
// the answer does not depend on a DNS lookup.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// From is the address this sender puts on outbound mail.
func (s *SMTPSender) From() string { return s.from }

// RequiresTLS reports whether this sender refuses a server that does not
// offer STARTTLS.
func (s *SMTPSender) RequiresTLS() bool { return s.requireTLS }

// Send delivers m. The dial honours ctx, and the connection then carries a
// deadline for the rest of the exchange: ctx's own if it has one, else
// sendTimeout.
func (s *SMTPSender) Send(ctx context.Context, m Message) error {
	if m.From == "" {
		m.From = s.from
	}
	if m.To == "" {
		return errors.New("mail: message has no recipient")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("mail: dial %s: %w", s.addr, err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(sendTimeout)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return fmt.Errorf("mail: deadline: %w", err)
	}
	if s.tls {
		conn = tls.Client(conn, &tls.Config{ServerName: s.hostname, MinVersion: tls.VersionTLS12})
	}
	c, err := smtp.NewClient(conn, s.hostname)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mail: smtp %s: %w", s.addr, err)
	}
	defer func() { _ = c.Close() }()

	if !s.tls {
		ok, _ := c.Extension("STARTTLS")
		switch {
		case ok:
			if err := c.StartTLS(&tls.Config{ServerName: s.hostname, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("mail: starttls: %w", err)
			}
		case s.requireTLS:
			return fmt.Errorf("%w: %s", ErrNoSTARTTLS, s.addr)
		}
	}
	if s.auth != nil {
		if err := c.Auth(s.auth); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}
	if err := c.Mail(m.From); err != nil {
		return fmt.Errorf("mail: from %s: %w", m.From, err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return fmt.Errorf("mail: rcpt %s: %w", m.To, err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	if _, err := w.Write(m.Bytes()); err != nil {
		_ = w.Close()
		return fmt.Errorf("mail: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: close body: %w", err)
	}
	return c.Quit()
}

// FromAddress derives a sender address from the public origin, so a
// deployment needs no separate setting for it.
func FromAddress(baseURL string) string {
	host := "localhost"
	if u, err := url.Parse(baseURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	return DefaultFromLocalPart + "@" + host
}
