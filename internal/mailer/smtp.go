package mailer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
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
	SendTimeout        time.Duration
	MaxConnections     int
}

type Message struct {
	ToEmail   string
	ToName    string
	Subject   string
	Text      string
	HTML      string
	MessageID string
}

type TransportDisposition string

const (
	DispositionRetryable TransportDisposition = "retryable"
	DispositionPermanent TransportDisposition = "permanent"
	DispositionAmbiguous TransportDisposition = "ambiguous"
)

type TransportError struct {
	Disposition         TransportDisposition
	Stage               string
	Code                int
	SuppressDestination bool
	Cause               error
}

func (e *TransportError) Error() string {
	if e == nil {
		return "SMTP transport error"
	}
	return fmt.Sprintf("SMTP %s failed: %v", e.Stage, e.Cause)
}

func (e *TransportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func ClassifyTransportError(err error) (TransportDisposition, bool) {
	var transportError *TransportError
	if errors.As(err, &transportError) {
		return transportError.Disposition, transportError.SuppressDestination
	}
	// A sender implementation must explicitly prove that an error happened
	// before SMTP commit before the durable worker may retry it. Treat unknown
	// errors conservatively so a new sender or wrapper cannot duplicate a
	// message whose acceptance state is unknown.
	return DispositionAmbiguous, false
}

type Sender interface {
	Send(ctx context.Context, message Message) error
	Enabled() bool
}

type SMTPMailer struct {
	cfg   Config
	idle  chan *smtpSession
	slots chan struct{}
}

type smtpSession struct {
	client *smtp.Client
	conn   net.Conn
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
	if cfg.SendTimeout <= 0 {
		cfg.SendTimeout = 30 * time.Second
	}
	if cfg.MaxConnections < 1 || cfg.MaxConnections > 32 {
		cfg.MaxConnections = 4
	}

	if strings.TrimSpace(cfg.FromEmail) == "" {
		cfg.FromEmail = strings.TrimSpace(cfg.Username)
	}

	if strings.TrimSpace(cfg.FromName) == "" {
		cfg.FromName = "Booking"
	}

	return &SMTPMailer{
		cfg: cfg, idle: make(chan *smtpSession, cfg.MaxConnections),
		slots: make(chan struct{}, cfg.MaxConnections),
	}, nil
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
		return permanentTransportError("content", errors.New("recipient email is required"), false)
	}

	subject := strings.TrimSpace(message.Subject)
	if subject == "" {
		return permanentTransportError("content", errors.New("message subject is required"), false)
	}
	if !validHeaderValue(subject) || !validHeaderValue(message.ToName) ||
		!validHeaderValue(toEmail) || !validHeaderValue(message.MessageID) {
		return permanentTransportError("content", errors.New("message headers contain invalid characters"), false)
	}

	body := strings.TrimSpace(message.Text)
	if body == "" {
		return permanentTransportError("content", errors.New("message body is required"), false)
	}
	session, err := m.acquire(ctx)
	if err != nil {
		return classifySMTPError("connect", err, false)
	}
	reusable := false
	defer func() { m.release(session, reusable) }()
	deadline := time.Now().Add(m.cfg.SendTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := session.conn.SetDeadline(deadline); err != nil {
		return classifySMTPError("deadline", err, false)
	}

	fromEmail := strings.TrimSpace(m.cfg.FromEmail)
	if err := session.client.Mail(fromEmail); err != nil {
		return classifySMTPError("mail_from", err, false)
	}

	if err := session.client.Rcpt(toEmail); err != nil {
		return classifySMTPError("recipient", err, false)
	}

	writer, err := session.client.Data()
	if err != nil {
		return classifySMTPError("data", err, false)
	}

	if err := writeSMTPData(writer, buildMessage(m.cfg, message)); err != nil {
		return err
	}
	if err := session.client.Reset(); err == nil {
		reusable = true
	}
	return nil
}

func writeSMTPData(writer io.WriteCloser, message []byte) error {
	if _, err := writer.Write(message); err != nil {
		// Do not close the DATA writer here: Close writes SMTP's terminating dot
		// and could commit a partial message. The caller must discard the SMTP
		// session so the server cannot accept it.
		return classifySMTPError("body", err, false)
	}
	if err := writer.Close(); err != nil {
		return classifySMTPError("data_commit", err, true)
	}
	return nil
}

func (m *SMTPMailer) acquire(ctx context.Context) (*smtpSession, error) {
	select {
	case m.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case session := <-m.idle:
		return session, nil
	default:
	}
	session, err := m.connect(ctx)
	if err != nil {
		<-m.slots
		return nil, err
	}
	return session, nil
}

func (m *SMTPMailer) release(session *smtpSession, reusable bool) {
	defer func() { <-m.slots }()
	if session == nil {
		return
	}
	if reusable {
		_ = session.conn.SetDeadline(time.Time{})
		select {
		case m.idle <- session:
			return
		default:
		}
		_ = session.conn.SetDeadline(time.Now().Add(time.Second))
		_ = session.client.Quit()
	}
	_ = session.client.Close()
}

func (m *SMTPMailer) connect(ctx context.Context) (*smtpSession, error) {
	addr := net.JoinHostPort(m.cfg.Host, fmt.Sprintf("%d", m.cfg.Port))
	dialer := &net.Dialer{Timeout: m.cfg.ConnectTimeout}
	var conn net.Conn
	var err error
	if m.cfg.Security == "tls" {
		rawConn, dialErr := dialer.DialContext(ctx, "tcp", addr)
		if dialErr != nil {
			return nil, dialErr
		}
		tlsConn := tls.Client(rawConn, &tls.Config{
			ServerName: m.cfg.Host, InsecureSkipVerify: m.cfg.InsecureSkipVerify,
		})
		if err = tlsConn.HandshakeContext(ctx); err != nil {
			_ = rawConn.Close()
			return nil, err
		}
		conn = tlsConn
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
	}
	client, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if m.cfg.Security == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			_ = client.Close()
			return nil, errors.New("SMTP server does not support STARTTLS")
		}
		if err := client.StartTLS(&tls.Config{
			ServerName: m.cfg.Host, InsecureSkipVerify: m.cfg.InsecureSkipVerify,
		}); err != nil {
			_ = client.Close()
			return nil, err
		}
	}
	if ok, _ := client.Extension("AUTH"); !ok {
		_ = client.Close()
		return nil, errors.New("SMTP server does not advertise AUTH")
	}
	if err := client.Auth(smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)); err != nil {
		_ = client.Close()
		return nil, err
	}
	return &smtpSession{client: client, conn: conn}, nil
}

func classifySMTPError(stage string, err error, postData bool) error {
	var protocolError *textproto.Error
	if errors.As(err, &protocolError) {
		disposition := DispositionPermanent
		if protocolError.Code >= 400 && protocolError.Code < 500 {
			disposition = DispositionRetryable
		}
		return &TransportError{
			Disposition: disposition, Stage: stage, Code: protocolError.Code,
			SuppressDestination: stage == "recipient" && explicitInvalidRecipient(protocolError.Code, protocolError.Msg),
			Cause:               err,
		}
	}
	if postData {
		return &TransportError{Disposition: DispositionAmbiguous, Stage: stage, Cause: err}
	}
	return &TransportError{Disposition: DispositionRetryable, Stage: stage, Cause: err}
}

func permanentTransportError(stage string, err error, suppress bool) error {
	return &TransportError{
		Disposition: DispositionPermanent, Stage: stage,
		SuppressDestination: suppress, Cause: err,
	}
}

func explicitInvalidRecipient(code int, message string) bool {
	if code == 551 || code == 553 {
		return true
	}
	if code != 550 {
		return false
	}
	message = strings.ToLower(message)
	for _, marker := range []string{"user unknown", "no such user", "mailbox unavailable", "recipient address rejected"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func buildMessage(cfg Config, message Message) []byte {
	var buffer bytes.Buffer

	fromValue := (&mail.Address{Name: strings.TrimSpace(cfg.FromName), Address: cfg.FromEmail}).String()
	toValue := (&mail.Address{Name: strings.TrimSpace(message.ToName), Address: message.ToEmail}).String()

	buffer.WriteString("MIME-Version: 1.0\r\n")
	buffer.WriteString(fmt.Sprintf("From: %s\r\n", fromValue))
	buffer.WriteString(fmt.Sprintf("To: %s\r\n", toValue))
	buffer.WriteString(fmt.Sprintf("Subject: %s\r\n", mime.QEncoding.Encode("UTF-8", strings.TrimSpace(message.Subject))))
	if messageID := strings.TrimSpace(message.MessageID); messageID != "" {
		buffer.WriteString(fmt.Sprintf("Message-ID: %s\r\n", messageID))
	}
	if strings.TrimSpace(message.HTML) == "" {
		buffer.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
		buffer.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		writeQuotedPrintable(&buffer, message.Text)
		return buffer.Bytes()
	}
	boundarySeed := sha256.Sum256([]byte(message.MessageID))
	boundary := fmt.Sprintf("tellbook-%x", boundarySeed[:12])
	buffer.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary))
	writer := multipart.NewWriter(&buffer)
	_ = writer.SetBoundary(boundary)
	textHeaders := textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=UTF-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	}
	textPart, _ := writer.CreatePart(textHeaders)
	writeQuotedPrintable(textPart, message.Text)
	htmlHeaders := textproto.MIMEHeader{
		"Content-Type":              {"text/html; charset=UTF-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	}
	htmlPart, _ := writer.CreatePart(htmlHeaders)
	writeQuotedPrintable(htmlPart, message.HTML)
	_ = writer.Close()

	return buffer.Bytes()
}

func writeQuotedPrintable(writer io.Writer, value string) {
	encoded := quotedprintable.NewWriter(writer)
	_, _ = encoded.Write([]byte(strings.ReplaceAll(value, "\n", "\r\n")))
	_ = encoded.Close()
}

func validHeaderValue(value string) bool {
	return !strings.ContainsAny(value, "\r\n")
}
