package mailer

import (
	"strings"
	"testing"
)

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
