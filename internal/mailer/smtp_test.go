package mailer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSMTPDataWriteFailureDoesNotFinalizeMessage(t *testing.T) {
	writer := &failingDataWriter{writeError: errors.New("connection reset")}
	err := writeSMTPData(writer, []byte("partial message"))
	disposition, suppress := ClassifyTransportError(err)
	if disposition != DispositionRetryable || suppress {
		t.Fatalf("body write classification = %q suppress=%t err=%v", disposition, suppress, err)
	}
	if writer.closed {
		t.Fatal("failed DATA body was finalized")
	}
}

func TestUnknownSenderErrorIsAmbiguous(t *testing.T) {
	disposition, suppress := ClassifyTransportError(errors.New("unknown sender failure"))
	if disposition != DispositionAmbiguous || suppress {
		t.Fatalf("unknown sender classification = %q suppress=%t", disposition, suppress)
	}
}

type failingDataWriter struct {
	writeError error
	closed     bool
}

func (writer *failingDataWriter) Write([]byte) (int, error) { return 0, writer.writeError }
func (writer *failingDataWriter) Close() error {
	writer.closed = true
	return nil
}

func TestBuildMessageIncludesMessageID(t *testing.T) {
	content := string(buildMessage(Config{FromEmail: "hello@tellbook.test", FromName: "TellBook"}, Message{
		ToEmail: "customer@example.com", ToName: "Customer",
		Subject: "Agreement ready", Text: "Open your agreement.",
		MessageID: "<agreement-test@tellbook.local>",
	}))
	if !strings.Contains(content, "Message-ID: <agreement-test@tellbook.local>\r\n") {
		t.Fatalf("message ID header missing from %q", content)
	}
}

func TestHeaderValueRejectsLineBreaks(t *testing.T) {
	if validHeaderValue("safe\r\nBcc: attacker@example.com") {
		t.Fatal("header value with a line break was accepted")
	}
}

func TestSMTPMailerReusesConnectionAndSendsMultipartMessage(t *testing.T) {
	server := newFakeSMTPServer(t, "success")
	sender := newFakeSMTPMailer(t, server)
	for index := range 2 {
		err := sender.Send(context.Background(), Message{
			ToEmail: "customer@example.com", ToName: "Customer", Subject: "Booking update",
			Text: "Booking text", HTML: "<strong>Booking HTML</strong>",
			MessageID: fmt.Sprintf("<notification-%d@mail.tellbook.test>", index),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if server.connectionCount() != 1 {
		t.Fatalf("SMTP connections = %d, want one reused connection", server.connectionCount())
	}
	messages := server.messagesSnapshot()
	if len(messages) != 2 || !strings.Contains(messages[0], "multipart/alternative") ||
		!strings.Contains(messages[0], "Message-ID: <notification-0@mail.tellbook.test>") {
		t.Fatalf("unexpected SMTP messages: %#v", messages)
	}
}

func TestSMTPMailerClassifiesTransientRecipientFailure(t *testing.T) {
	server := newFakeSMTPServer(t, "transient_recipient")
	err := newFakeSMTPMailer(t, server).Send(context.Background(), testMessage())
	disposition, suppress := ClassifyTransportError(err)
	if disposition != DispositionRetryable || suppress {
		t.Fatalf("transient recipient classification = %q suppress=%t err=%v", disposition, suppress, err)
	}
}

func TestSMTPMailerClassifiesExplicitInvalidRecipient(t *testing.T) {
	server := newFakeSMTPServer(t, "invalid_recipient")
	err := newFakeSMTPMailer(t, server).Send(context.Background(), testMessage())
	disposition, suppress := ClassifyTransportError(err)
	if disposition != DispositionPermanent || !suppress {
		t.Fatalf("invalid recipient classification = %q suppress=%t err=%v", disposition, suppress, err)
	}
}

func TestSMTPMailerClassifiesPostDataDisconnectAsAmbiguous(t *testing.T) {
	server := newFakeSMTPServer(t, "ambiguous_data")
	err := newFakeSMTPMailer(t, server).Send(context.Background(), testMessage())
	disposition, suppress := ClassifyTransportError(err)
	if disposition != DispositionAmbiguous || suppress {
		t.Fatalf("post-DATA classification = %q suppress=%t err=%v", disposition, suppress, err)
	}
}

func testMessage() Message {
	return Message{
		ToEmail: "customer@example.com", Subject: "Booking update", Text: "Booking text",
		MessageID: "<notification-test@mail.tellbook.test>",
	}
}

func newFakeSMTPMailer(t *testing.T, server *fakeSMTPServer) *SMTPMailer {
	t.Helper()
	host, rawPort, err := net.SplitHostPort(server.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := NewSMTPMailer(Config{
		Host: host, Port: port, Username: "test", Password: "secret",
		FromEmail: "notifications@tellbook.test", FromName: "TellBook", Security: "none",
		ConnectTimeout: time.Second, SendTimeout: time.Second, MaxConnections: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sender
}

type fakeSMTPServer struct {
	listener net.Listener
	mode     string
	mu       sync.Mutex
	conns    []net.Conn
	messages []string
}

func newFakeSMTPServer(t *testing.T, mode string) *fakeSMTPServer {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &fakeSMTPServer{listener: listener, mode: mode}
	go server.serve()
	t.Cleanup(server.close)
	return server
}

func (server *fakeSMTPServer) serve() {
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			return
		}
		server.mu.Lock()
		server.conns = append(server.conns, connection)
		server.mu.Unlock()
		go server.handle(connection)
	}
}

func (server *fakeSMTPServer) handle(connection net.Conn) {
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	writeSMTPLine(writer, "220 fake-smtp ESMTP")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(command, "EHLO"):
			_, _ = writer.WriteString("250-fake-smtp\r\n250 AUTH PLAIN\r\n")
			_ = writer.Flush()
		case strings.HasPrefix(command, "AUTH"):
			writeSMTPLine(writer, "235 2.7.0 authenticated")
		case strings.HasPrefix(command, "MAIL FROM"):
			writeSMTPLine(writer, "250 2.1.0 sender ok")
		case strings.HasPrefix(command, "RCPT TO") && server.mode == "transient_recipient":
			writeSMTPLine(writer, "450 4.2.0 mailbox temporarily unavailable")
		case strings.HasPrefix(command, "RCPT TO") && server.mode == "invalid_recipient":
			writeSMTPLine(writer, "551 5.1.1 no such user")
		case strings.HasPrefix(command, "RCPT TO"):
			writeSMTPLine(writer, "250 2.1.5 recipient ok")
		case command == "DATA":
			writeSMTPLine(writer, "354 end with dot")
			var message strings.Builder
			for {
				bodyLine, readErr := reader.ReadString('\n')
				if readErr != nil {
					return
				}
				if bodyLine == ".\r\n" {
					break
				}
				message.WriteString(bodyLine)
			}
			server.mu.Lock()
			server.messages = append(server.messages, message.String())
			server.mu.Unlock()
			if server.mode == "ambiguous_data" {
				_ = connection.Close()
				return
			}
			writeSMTPLine(writer, "250 2.0.0 accepted")
		case command == "RSET":
			writeSMTPLine(writer, "250 2.0.0 reset")
		case command == "QUIT":
			writeSMTPLine(writer, "221 2.0.0 bye")
			_ = connection.Close()
			return
		default:
			writeSMTPLine(writer, "500 unsupported")
		}
	}
}

func writeSMTPLine(writer *bufio.Writer, line string) {
	_, _ = writer.WriteString(line + "\r\n")
	_ = writer.Flush()
}

func (server *fakeSMTPServer) connectionCount() int {
	server.mu.Lock()
	defer server.mu.Unlock()
	return len(server.conns)
}

func (server *fakeSMTPServer) messagesSnapshot() []string {
	server.mu.Lock()
	defer server.mu.Unlock()
	return append([]string(nil), server.messages...)
}

func (server *fakeSMTPServer) close() {
	_ = server.listener.Close()
	server.mu.Lock()
	defer server.mu.Unlock()
	for _, connection := range server.conns {
		_ = connection.Close()
	}
}
