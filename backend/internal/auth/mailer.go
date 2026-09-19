package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTPSettings mirrors the SMTP_* environment block. It is defined here so the
// auth package does not depend on the process configuration type.
type SMTPSettings struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string
}

type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
	Configured() bool
}

type SMTPMailer struct {
	settings SMTPSettings
}

// LogMailer is the development stand-in: it writes the message to the log
// instead of sending it, so local sign-up works without SMTP credentials.
type LogMailer struct{}

func NewMailer(settings SMTPSettings) Mailer {
	if settings.Host == "" || settings.Port == 0 || settings.From == "" {
		return LogMailer{}
	}
	return SMTPMailer{settings: settings}
}

func (LogMailer) Configured() bool { return false }

func (LogMailer) Send(_ context.Context, to, subject, body string) error {
	slog.Warn("smtp is not configured, message written to the log", "to", to, "subject", subject, "body", body)
	return nil
}

func (m SMTPMailer) Configured() bool { return true }

func (m SMTPMailer) Send(ctx context.Context, to, subject, body string) error {
	if !validAddress(to) || !validAddress(m.settings.From) || strings.ContainsAny(subject, "\r\n") {
		return errors.New("invalid message envelope")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	address := net.JoinHostPort(m.settings.Host, fmt.Sprint(m.settings.Port))
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return err
	}
	tlsConfig := &tls.Config{ServerName: m.settings.Host, MinVersion: tls.VersionTLS12}
	// 465 is implicit TLS. Everything else negotiates STARTTLS below.
	if m.settings.Port == 465 {
		conn = tls.Client(conn, tlsConfig)
	}
	client, err := smtp.NewClient(conn, m.settings.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer client.Close()
	if m.settings.Port != 465 {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			return errors.New("smtp server does not offer STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if m.settings.Username != "" {
		auth, err := m.authMechanism(client)
		if err != nil {
			return err
		}
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(m.settings.From); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(m.message(to, subject, body)); err != nil {
		writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// authMechanism picks from what the server actually offers. Several hosts,
// Beget among them, answer PLAIN with "504 authentication mechanism not
// supported" and accept only LOGIN.
func (m SMTPMailer) authMechanism(client *smtp.Client) (smtp.Auth, error) {
	ok, mechanisms := client.Extension("AUTH")
	if !ok {
		return nil, errors.New("smtp server does not offer authentication")
	}
	return mechanismFor(mechanisms, m.settings.Username, m.settings.Password, m.settings.Host)
}

func mechanismFor(offered, username, password, host string) (smtp.Auth, error) {
	upper := strings.ToUpper(offered)
	switch {
	case strings.Contains(upper, "PLAIN"):
		return smtp.PlainAuth("", username, password, host), nil
	case strings.Contains(upper, "LOGIN"):
		return loginAuth{username: username, password: password}, nil
	case strings.Contains(upper, "CRAM-MD5"):
		return smtp.CRAMMD5Auth(username, password), nil
	default:
		return nil, fmt.Errorf("smtp server offers no supported authentication mechanism (%s)", offered)
	}
}

// loginAuth implements the LOGIN mechanism, which net/smtp does not ship.
// The credentials are sent in clear text, so it refuses to run on a
// connection that is not encrypted.
type loginAuth struct {
	username string
	password string
}

func (a loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS {
		return "", nil, errors.New("refusing to send smtp credentials over an unencrypted connection")
	}
	return "LOGIN", nil, nil
}

func (a loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(strings.TrimSuffix(string(fromServer), ":"))) {
	case "username":
		return []byte(a.username), nil
	case "password":
		return []byte(a.password), nil
	default:
		return nil, fmt.Errorf("unexpected smtp challenge %q", fromServer)
	}
}

func (m SMTPMailer) message(to, subject, body string) []byte {
	from := m.settings.From
	if m.settings.FromName != "" {
		from = mime.QEncoding.Encode("utf-8", m.settings.FromName) + " <" + m.settings.From + ">"
	}
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		// Dot-stuffing: a lone leading dot would end the DATA block.
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		b.WriteString(line + "\r\n")
	}
	return []byte(b.String())
}

func validAddress(address string) bool {
	if len(address) < 3 || len(address) > 254 || strings.ContainsAny(address, " \r\n<>,;") {
		return false
	}
	at := strings.LastIndex(address, "@")
	return at > 0 && at < len(address)-1 && strings.Contains(address[at+1:], ".")
}
