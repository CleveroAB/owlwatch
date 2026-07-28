package alerts

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Mailer is the delivery seam: tests inject a fake, production uses
// smtpMailer. One call sends one message to every configured recipient.
type Mailer interface {
	Send(subject, body string) error
}

// smtpMailer sends over a plain SMTP connection using only the standard
// library, upgrading with STARTTLS when the server offers it. PLAIN auth is
// used when a user is configured; net/smtp itself refuses to send credentials
// over an unencrypted connection to a non-localhost server.
type smtpMailer struct {
	cfg Config
}

const (
	dialTimeout = 10 * time.Second
	sendTimeout = 30 * time.Second // deadline for the whole SMTP conversation
)

func (m *smtpMailer) Send(subject, body string) error {
	addr := net.JoinHostPort(m.cfg.SMTPHost, strconv.Itoa(m.cfg.SMTPPort))
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()
	// net/smtp has no timeouts of its own; a single connection deadline
	// bounds the whole exchange so a hung server cannot wedge the notifier.
	_ = conn.SetDeadline(time.Now().Add(sendTimeout))

	c, err := smtp.NewClient(conn, m.cfg.SMTPHost)
	if err != nil {
		return fmt.Errorf("smtp handshake: %w", err)
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: m.cfg.SMTPHost}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if m.cfg.SMTPUser != "" {
		auth := smtp.PlainAuth("", m.cfg.SMTPUser, m.cfg.SMTPPass, m.cfg.SMTPHost)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}

	if err := c.Mail(m.cfg.From); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, rcpt := range m.cfg.To {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("rcpt %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		m.cfg.From, strings.Join(m.cfg.To, ", "), subject,
		time.Now().Format(time.RFC1123Z), body)
	if _, err := io.WriteString(w, msg); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close body: %w", err)
	}
	return c.Quit()
}
