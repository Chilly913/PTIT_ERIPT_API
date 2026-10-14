// Package mail sends transactional e-mail through a driver chosen by
// MAIL_MAILER: "log" (writes the message to the logger, for development) or
// "smtp".
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
)

// Message is one e-mail. HTML is optional; Text is always sent.
type Message struct {
	To      string
	ToName  string
	Subject string
	Text    string
	HTML    string
}

// Mailer sends messages.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// New returns the driver selected by cfg.Mailer.
func New(cfg config.MailConfig, log *slog.Logger) (Mailer, error) {
	switch cfg.Mailer {
	case "", "log":
		return &LogMailer{Log: log, From: cfg.FromAddress}, nil
	case "smtp":
		return &SMTPMailer{Cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("mail: unknown MAIL_MAILER %q", cfg.Mailer)
	}
}

// LogMailer writes messages to the logger instead of sending them.
type LogMailer struct {
	Log  *slog.Logger
	From string
}

// Send implements Mailer.
func (l *LogMailer) Send(ctx context.Context, m Message) error {
	log := l.Log
	if log == nil {
		log = slog.Default()
	}
	log.InfoContext(ctx, "mail (log driver)", "from", l.From, "to", m.To, "subject", m.Subject, "body", m.Text)
	return nil
}

// Memory records messages in memory (tests).
type Memory struct {
	mu   sync.Mutex
	msgs []Message
}

// Send implements Mailer.
func (m *Memory) Send(_ context.Context, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, msg)
	return nil
}

// Messages returns a copy of the recorded messages.
func (m *Memory) Messages() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Message(nil), m.msgs...)
}

// Last returns the most recent message sent to addr.
func (m *Memory) Last(addr string) (Message, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.msgs) - 1; i >= 0; i-- {
		if strings.EqualFold(m.msgs[i].To, addr) {
			return m.msgs[i], true
		}
	}
	return Message{}, false
}

// SMTPMailer sends through an SMTP server. Encryption: "tls" = implicit TLS
// (port 465), "starttls" = require STARTTLS, "" = STARTTLS when offered.
type SMTPMailer struct {
	Cfg     config.MailConfig
	Timeout time.Duration
}

// Send implements Mailer.
func (s *SMTPMailer) Send(ctx context.Context, m Message) error {
	raw, err := Build(s.Cfg.FromAddress, s.Cfg.FromName, m)
	if err != nil {
		return err
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < timeout {
		timeout = time.Until(dl)
	}
	addr := net.JoinHostPort(s.Cfg.Host, strconv.Itoa(s.Cfg.Port))
	tlsCfg := &tls.Config{ServerName: s.Cfg.Host, MinVersion: tls.VersionTLS12}

	dialer := &net.Dialer{Timeout: timeout}
	var conn net.Conn
	if s.Cfg.Encryption == "tls" || s.Cfg.Encryption == "ssl" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mail: dial %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	c, err := smtp.NewClient(conn, s.Cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mail: smtp client: %w", err)
	}
	defer c.Close()

	if s.Cfg.Encryption != "tls" && s.Cfg.Encryption != "ssl" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("mail: starttls: %w", err)
			}
		} else if s.Cfg.Encryption == "starttls" {
			return errors.New("mail: server does not support STARTTLS")
		}
	}
	if s.Cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(smtp.PlainAuth("", s.Cfg.Username, s.Cfg.Password, s.Cfg.Host)); err != nil {
				return fmt.Errorf("mail: auth: %w", err)
			}
		}
	}
	if err := c.Mail(s.Cfg.FromAddress); err != nil {
		return fmt.Errorf("mail: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return fmt.Errorf("mail: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("mail: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: close data: %w", err)
	}
	return c.Quit()
}

// Build renders an RFC 5322 message (multipart/alternative when HTML is set).
func Build(fromAddr, fromName string, m Message) ([]byte, error) {
	if _, err := mail.ParseAddress(m.To); err != nil {
		return nil, fmt.Errorf("mail: invalid recipient %q: %w", m.To, err)
	}
	var b bytes.Buffer
	from := mail.Address{Name: fromName, Address: fromAddr}
	to := mail.Address{Name: m.ToName, Address: m.To}
	domain := "localhost"
	if i := strings.LastIndexByte(fromAddr, '@'); i >= 0 {
		domain = fromAddr[i+1:]
	}
	fmt.Fprintf(&b, "From: %s\r\n", from.String())
	fmt.Fprintf(&b, "To: %s\r\n", to.String())
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", randomID(), domain)
	b.WriteString("MIME-Version: 1.0\r\n")

	writePart := func(contentType, body string) {
		fmt.Fprintf(&b, "Content-Type: %s; charset=UTF-8\r\n", contentType)
		b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		qp := quotedprintable.NewWriter(&b)
		_, _ = qp.Write([]byte(body))
		_ = qp.Close()
		b.WriteString("\r\n")
	}
	if m.HTML == "" {
		writePart("text/plain", m.Text)
		return b.Bytes(), nil
	}
	boundary := "=_" + randomID()
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	writePart("text/plain", m.Text)
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	writePart("text/html", m.HTML)
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes(), nil
}

func randomID() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
