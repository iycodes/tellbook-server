package config

import (
	"strings"
	"testing"
)

func TestSupportEmailConfiguration(t *testing.T) {
	for _, value := range []string{"", "support@example.com", " privacy@example.com "} {
		t.Run("valid "+value, func(t *testing.T) {
			setRequiredConfig(t)
			t.Setenv("SUPPORT_EMAIL", value)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.SupportEmail != strings.TrimSpace(value) {
				t.Fatalf("destination=%q", cfg.SupportEmail)
			}
			// A missing SMTP transport leaves support unavailable without breaking
			// unrelated API/worker startup or falling back to another destination.
			if cfg.SMTPConfigured() {
				t.Fatal("SMTP unexpectedly configured")
			}
		})
	}
	for _, value := range []string{"invalid", "Operator <support@example.com>", "a@example.com,b@example.com", "a@example.com\r\nBcc: attacker@example.com"} {
		t.Run("invalid "+value, func(t *testing.T) {
			setRequiredConfig(t)
			t.Setenv("SUPPORT_EMAIL", value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SUPPORT_EMAIL") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestSMTPFromAddressRetainsExistingFallback(t *testing.T) {
	if (Config{SMTPUsername: "sender@example.com"}).SMTPFromAddress() != "sender@example.com" {
		t.Fatal("existing username fallback missing")
	}
	if (Config{SMTPUsername: "sender@example.com", SMTPFromEmail: "hello@example.com"}).SMTPFromAddress() != "hello@example.com" {
		t.Fatal("explicit From ignored")
	}
}
