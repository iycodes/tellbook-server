package welcomeemail

import (
	"strings"
	"testing"

	"booking/go-server/internal/mailer"
)

func TestRenderTemplateEscapesHTMLValues(t *testing.T) {
	rendered, err := renderTemplate("<p>{{name}} — {{email}}</p>", `<Sam & Co>`, "sam@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if rendered != "<p>&lt;Sam &amp; Co&gt; — sam@example.com</p>" {
		t.Fatalf("renderTemplate() = %q", rendered)
	}
}

func TestRenderTemplateRejectsUnsupportedPlaceholder(t *testing.T) {
	_, err := renderTemplate("Hello {{business_name}}", "Sam", "sam@example.com", false)
	if err == nil || !strings.Contains(err.Error(), "unsupported placeholder") {
		t.Fatalf("renderTemplate() error = %v", err)
	}
}

func TestNormalizeEmailRejectsDisplayName(t *testing.T) {
	if _, err := normalizeEmail("Sam <sam@example.com>"); err == nil {
		t.Fatal("normalizeEmail() accepted a display name")
	}
}

func TestFailureOutcome(t *testing.T) {
	if got := failureOutcome(mailer.DispositionRetryable, maxAttempts); got != "retry_exhausted" {
		t.Fatalf("failureOutcome() = %q", got)
	}
}
