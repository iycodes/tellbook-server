package welcomeemail

import (
	"strings"
	"testing"

	"booking/go-server/internal/mailer"
)

func TestRenderTemplateEscapesHTMLValues(t *testing.T) {
	rendered, err := renderTemplate("<p>{{name}} — {{email}}</p>", `<Sam & Co>`, "sam@example.com", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if rendered != "<p>&lt;Sam &amp; Co&gt; — sam@example.com</p>" {
		t.Fatalf("renderTemplate() = %q", rendered)
	}
}

func TestRenderTemplateRejectsUnsupportedPlaceholder(t *testing.T) {
	_, err := renderTemplate("Hello {{business_name}}", "Sam", "sam@example.com", "", false)
	if err == nil || !strings.Contains(err.Error(), "unsupported placeholder") {
		t.Fatalf("renderTemplate() error = %v", err)
	}
}

func TestNormalizeEmailRejectsDisplayName(t *testing.T) {
	if _, err := normalizeEmail("Sam <sam@example.com>"); err == nil {
		t.Fatal("normalizeEmail() accepted a display name")
	}
}

func TestWelcomeActionURLValidation(t *testing.T) {
	for _, action := range []string{"", "/relative", "javascript:alert(1)", "https://user:pass@example.com"} {
		if _, err := renderTemplate(`<a href="{{action_url}}">Start</a>`, "Sam", "sam@example.com", action, true); err == nil {
			t.Errorf("accepted invalid action URL %q", action)
		}
	}
	const action = "https://example.com/start?a=1&b=2"
	rendered, err := renderTemplate(`<a href="{{action_url}}">Start</a>`, "Sam", "sam@example.com", action, true)
	if err != nil || rendered != `<a href="https://example.com/start?a=1&amp;b=2">Start</a>` {
		t.Fatalf("action URL escaping = %q, %v", rendered, err)
	}
	// Version 1 remains usable without a URL while the new drafts await activation.
	if _, err := renderTemplate("Hi {{name}}", "Sam", "sam@example.com", "", false); err != nil {
		t.Fatal(err)
	}
}

func TestFailureOutcome(t *testing.T) {
	if got := failureOutcome(mailer.DispositionRetryable, maxAttempts); got != "retry_exhausted" {
		t.Fatalf("failureOutcome() = %q", got)
	}
}
