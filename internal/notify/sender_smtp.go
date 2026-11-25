package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// SMTP security modes (smtp.security).
const (
	smtpStartTLS = "starttls"
	smtpTLS      = "tls"
	smtpNone     = "none"
)

// smtpConfig is the validated email configuration.
type smtpConfig struct {
	host     string
	port     int
	security string
	username string
	from     string
	to       []string
}

// readSMTP reads and checks the smtp config fields. Plain text is allowed
// only to loopback (D12).
func readSMTP(ch Channel) (smtpConfig, error) {
	var c smtpConfig
	c.host, _ = ch.Config["host"].(string)
	if c.host == "" {
		return c, permanent("smtp host is required")
	}
	c.security, _ = ch.Config["security"].(string)
	if c.security == "" {
		c.security = smtpStartTLS
	}
	switch c.security {
	case smtpStartTLS, smtpTLS, smtpNone:
	default:
		return c, permanent("smtp security %q is not starttls, tls or none", c.security)
	}
	c.port = 587
	if c.security == smtpTLS {
		c.port = 465
	}
	if n, ok := ch.Config["port"].(float64); ok && n > 0 {
		c.port = int(n)
	}
	if c.security == smtpNone && !isLoopbackHost(c.host) {
		return c, permanent("smtp security none is allowed only to loopback")
	}
	c.username, _ = ch.Config["username"].(string)
	c.from, _ = ch.Config["from"].(string)
	if c.from == "" {
		return c, permanent("smtp from is required")
	}
	if raw, ok := ch.Config["to"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				c.to = append(c.to, s)
			}
		}
	}
	if len(c.to) == 0 {
		return c, permanent("smtp to is required")
	}
	return c, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// plainAuth is PLAIN without the stdlib's refusal of unencrypted links, used
// only on loopback where security none is allowed.
type plainAuth struct{ user, pass string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}

func (a plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge")
	}
	return nil, nil
}

// sendSMTP delivers one plain-text message. A 4xx reply is retried, a 5xx
// is permanent. The message carries the kind, key and link in the body.
func (d *Dispatcher) sendSMTP(ctx context.Context, ch Channel, secret map[string]string, p payload) error {
	cfg, err := readSMTP(ch)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(cfg.host, strconv.Itoa(cfg.port))

	dialer := net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %s", redact(err.Error(), secretValues(secret)...))
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	tlsCfg := d.smtpTLS
	if tlsCfg == nil {
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	tlsCfg = tlsCfg.Clone()
	tlsCfg.ServerName = cfg.host

	if cfg.security == smtpTLS {
		conn = tls.Client(conn, tlsCfg)
	}
	c, err := smtp.NewClient(conn, cfg.host)
	if err != nil {
		_ = conn.Close()
		return smtpErr(err, secret)
	}
	defer func() { _ = c.Close() }()

	if err := c.Hello("localhost"); err != nil {
		return smtpErr(err, secret)
	}
	if cfg.security == smtpStartTLS {
		if err := c.StartTLS(tlsCfg); err != nil {
			return smtpErr(err, secret)
		}
	}
	if cfg.username != "" {
		if err := c.Auth(plainAuth{user: cfg.username, pass: secret["password"]}); err != nil {
			return smtpErr(err, secret)
		}
	}
	if err := c.Mail(cfg.from); err != nil {
		return smtpErr(err, secret)
	}
	for _, to := range cfg.to {
		if err := c.Rcpt(to); err != nil {
			return smtpErr(err, secret)
		}
	}
	w, err := c.Data()
	if err != nil {
		return smtpErr(err, secret)
	}
	if _, err := w.Write(buildMessage(cfg, p)); err != nil {
		_ = w.Close()
		return smtpErr(err, secret)
	}
	if err := w.Close(); err != nil {
		return smtpErr(err, secret)
	}
	return c.Quit()
}

// smtpErr classifies an SMTP error: 5xx is permanent, everything else retries.
func smtpErr(err error, secret map[string]string) error {
	red := redact(err.Error(), secretValues(secret)...)
	var tp *textproto.Error
	if errors.As(err, &tp) && tp.Code >= 500 {
		return &permanentError{err: errors.New(red)}
	}
	return errors.New(red)
}

// buildMessage renders the plain-text message. Header values are single
// lines (the subject is normalised) and the subject is RFC 2047 encoded.
func buildMessage(cfg smtpConfig, p payload) []byte {
	var b strings.Builder
	b.WriteString("From: " + cfg.from + "\r\n")
	b.WriteString("To: " + strings.Join(cfg.to, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subjectLine(p)) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	if p.Body != "" {
		b.WriteString(strings.ReplaceAll(p.Body, "\n", "\r\n") + "\r\n\r\n")
	}
	b.WriteString("kind: " + p.Kind + "\r\n")
	b.WriteString("key: " + p.Key + "\r\n")
	b.WriteString("time: " + p.At.UTC().Format(time.RFC3339) + "\r\n")
	if p.Link != "" {
		b.WriteString("link: " + p.Link + "\r\n")
	}
	return []byte(b.String())
}

// validEmail reports whether s parses as one address.
func validEmail(s string) bool {
	_, err := mail.ParseAddress(s)
	return err == nil
}
