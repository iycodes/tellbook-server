package mailer

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSMTPReplyToAndConfiguredFrom(t *testing.T) {
	server := newFakeSMTPServer(t, "success")
	message := testMessage()
	message.ReplyToEmail = "visitor@example.com"
	message.ReplyToName = "Visitor"
	if err := newFakeSMTPMailer(t, server).Send(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	content := server.messagesSnapshot()[0]
	if !strings.Contains(content, "Reply-To: \"Visitor\" <visitor@example.com>\r\n") || !strings.Contains(content, "From: \"TellBook\" <notifications@tellbook.test>\r\n") {
		t.Fatalf("missing safe routing headers: %s", content)
	}
}

func TestSMTPRejectsReplyToInjectionBeforeConnecting(t *testing.T) {
	sender, _ := NewSMTPMailer(Config{Username: "sender@example.com", Password: "secret", Host: "not-used.invalid"})
	for _, address := range []string{"a@example.com\nBcc: b@example.com", "A <a@example.com>", "a@example.com,b@example.com"} {
		message := testMessage()
		message.ReplyToEmail = address
		if err := sender.Send(context.Background(), message); err == nil {
			t.Fatalf("accepted %q", address)
		}
	}
	message := testMessage()
	message.ReplyToEmail = "a@example.com"
	message.ReplyToName = "Visitor\r\nBcc: a@example.com"
	if err := sender.Send(context.Background(), message); err == nil {
		t.Fatal("accepted name header injection")
	}
}

func TestSMTPGreetingHasDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	var numericPort int
	for _, character := range port {
		numericPort = numericPort*10 + int(character-'0')
	}
	sender, err := NewSMTPMailer(Config{Host: host, Port: numericPort, Username: "test", Password: "test", Security: "none", ConnectTimeout: 40 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := sender.Send(context.Background(), testMessage()); err == nil {
		t.Fatal("silent SMTP greeting succeeded")
	}
	if time.Since(start) > time.Second {
		t.Fatal("SMTP greeting was not bounded")
	}
	select {
	case conn := <-accepted:
		_ = conn.Close()
	case <-time.After(time.Second):
		t.Fatal("connection not accepted")
	}
}
