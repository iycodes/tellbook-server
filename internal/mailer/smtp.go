package mailer

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

type Config struct {
	Host               string
	Port               int
	Username           string
	Password           string
	FromEmail          string
	FromName           string
	Security           string
	InsecureSkipVerify bool
	ConnectTimeout     time.Duration
}

type Message struct {
	ToEmail   string
	ToName    string
	Subject   string
	Text      string
	MessageID string
}

type Sender interface {
	Send(ctx context.Context, message Message) error
	Enabled() bool
}

type SMTPMailer struct {
	cfg Config
}

func NewSMTPMailer(cfg Config) (*SMTPMailer, error) {
	if strings.TrimSpace(cfg.Username) == "" && strings.TrimSpace(cfg.Password) == "" {
		return nil, nil
	}

	if strings.TrimSpace(cfg.Username) == "" || strings.TrimSpace(cfg.Password) == "" {
		return nil, errors.New("SMTP_USERNAME and SMTP_PASSWORD are required when SMTP is configured")
	}

	if strings.TrimSpace(cfg.Host) == "" {
		cfg.Host = "smtp.zoho.com"
	}

	if cfg.Port <= 0 {
		cfg.Port = 465
	}

	cfg.Security = strings.ToLower(strings.TrimSpace(cfg.Security))
	if cfg.Security == "" {
		cfg.Security = "tls"
	}

	switch cfg.Security {
	case "starttls", "tls", "none":
	default:
		return nil, fmt.Errorf("unsupported SMTP_SECURITY %q", cfg.Security)
	}

	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}

	if strings.TrimSpace(cfg.FromEmail) == "" {
		cfg.FromEmail = strings.TrimSpace(cfg.Username)
	}

	if strings.TrimSpace(cfg.FromName) == "" {
		cfg.FromName = "Booking"
	}

	return &SMTPMailer{cfg: cfg}, nil
}

func (m *SMTPMailer) Enabled() bool {
	return m != nil
}

func (m *SMTPMailer) Send(ctx context.Context, message Message) error {
	if m == nil {
		return nil
	}

	toEmail := strings.TrimSpace(message.ToEmail)
	if toEmail == "" {
		return errors.New("recipient email is required")
	}

	subject := strings.TrimSpace(message.Subject)
	if subject == "" {
		return errors.New("message subject is required")
	}
	if !validHeaderValue(subject) || !validHeaderValue(message.ToName) || !validHeaderValue(message.MessageID) {
		return errors.New("message headers contain invalid characters")
	}

	body := strings.TrimSpace(message.Text)
	if body == "" {
		return errors.New("message body is required")
	}

	addr := net.JoinHostPort(m.cfg.Host, fmt.Sprintf("%d", m.cfg.Port))

	var (
		client *smtp.Client
		err    error
	)

	switch m.cfg.Security {
	case "tls":
		tlsConn, dialErr := tls.DialWithDialer(&net.Dialer{Timeout: m.cfg.ConnectTimeout}, "tcp", addr, &tls.Config{
			ServerName:         m.cfg.Host,
			InsecureSkipVerify: m.cfg.InsecureSkipVerify,
		})
		if dialErr != nil {
			return fmt.Errorf("dial SMTP over TLS: %w", dialErr)
		}

		client, err = smtp.NewClient(tlsConn, m.cfg.Host)
		if err != nil {
			_ = tlsConn.Close()
			return fmt.Errorf("create SMTP client: %w", err)
		}
	default:
		dialer := &net.Dialer{Timeout: m.cfg.ConnectTimeout}
		conn, dialErr := dialer.DialContext(ctx, "tcp", addr)
		if dialErr != nil {
			return fmt.Errorf("dial SMTP: %w", dialErr)
		}

		client, err = smtp.NewClient(conn, m.cfg.Host)
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("create SMTP client: %w", err)
		}
	}

	defer client.Close()

	if m.cfg.Security == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not support STARTTLS")
		}

		if err := client.StartTLS(&tls.Config{
			ServerName:         m.cfg.Host,
			InsecureSkipVerify: m.cfg.InsecureSkipVerify,
		}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}

	if m.cfg.Username != "" || m.cfg.Password != "" {
		auth := smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
		if ok, _ := client.Extension("AUTH"); ok {
			if err := client.Auth(auth); err != nil {
				return fmt.Errorf("smtp auth: %w", err)
			}
		}
	}

	fromEmail := strings.TrimSpace(m.cfg.FromEmail)
	if err := client.Mail(fromEmail); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}

	if err := client.Rcpt(toEmail); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}

	if _, err := writer.Write(buildMessage(m.cfg, message)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write smtp message: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("close smtp message: %w", err)
	}

	if err := client.Quit(); err != nil {
		return fmt.Errorf("smtp quit: %w", err)
	}

	return nil
}

func buildMessage(cfg Config, message Message) []byte {
	var buffer bytes.Buffer

	fromValue := cfg.FromEmail
	if strings.TrimSpace(cfg.FromName) != "" {
		fromValue = fmt.Sprintf("%s <%s>", strings.TrimSpace(cfg.FromName), cfg.FromEmail)
	}

	toValue := strings.TrimSpace(message.ToEmail)
	if strings.TrimSpace(message.ToName) != "" {
		toValue = fmt.Sprintf("%s <%s>", strings.TrimSpace(message.ToName), message.ToEmail)
	}

	buffer.WriteString("MIME-Version: 1.0\r\n")
	buffer.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	buffer.WriteString(fmt.Sprintf("From: %s\r\n", fromValue))
	buffer.WriteString(fmt.Sprintf("To: %s\r\n", toValue))
	buffer.WriteString(fmt.Sprintf("Subject: %s\r\n", strings.TrimSpace(message.Subject)))
	if messageID := strings.TrimSpace(message.MessageID); messageID != "" {
		buffer.WriteString(fmt.Sprintf("Message-ID: %s\r\n", messageID))
	}
	buffer.WriteString("\r\n")
	buffer.WriteString(strings.ReplaceAll(message.Text, "\n", "\r\n"))

	return buffer.Bytes()
}

func validHeaderValue(value string) bool {
	return !strings.ContainsAny(value, "\r\n")
}
