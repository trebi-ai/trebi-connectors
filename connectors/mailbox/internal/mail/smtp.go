package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// smtpSend sends b over SMTP. Port 465 uses TLS. Other ports must offer
// STARTTLS: the program never sends the password in clear text.
func (m *Mailbox) smtpSend(ctx context.Context, b Built) error {
	addr := m.cfg.SMTPHost
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("SMTP server %q: %w", addr, err)
	}
	d := &net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	if port == "465" {
		conn = tls.Client(conn, m.tlsFor(addr))
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("SMTP greeting from %s: %w", addr, err)
	}
	defer c.Close() //nolint:errcheck // Quit closes on success
	if port != "465" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("%s does not offer STARTTLS", addr)
		}
		if err := c.StartTLS(m.tlsFor(addr)); err != nil {
			return fmt.Errorf("STARTTLS with %s: %w", addr, err)
		}
	}
	if err := c.Auth(smtp.PlainAuth("", m.cfg.Address, m.cfg.Password, host)); err != nil {
		var te *textproto.Error
		if errors.As(err, &te) && te.Code == 535 {
			return fmt.Errorf("%w: %s", ErrAuth, te.Msg)
		}
		return fmt.Errorf("SMTP login: %w", err)
	}
	if err := c.Mail(m.cfg.Address); err != nil {
		return fmt.Errorf("SMTP MAIL FROM: %w", err)
	}
	for _, r := range b.Recipients {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("SMTP recipient %s: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := w.Write(crlf(b.Wire)); err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	return c.Quit()
}

// crlf makes each line end with CRLF.
func crlf(b []byte) []byte {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	return []byte(strings.ReplaceAll(s, "\n", "\r\n"))
}
