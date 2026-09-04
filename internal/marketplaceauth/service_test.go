package marketplaceauth

import (
	"net/http"
	"testing"
	"time"
)

func TestSessionValidationPolicyRevalidatesCommands(t *testing.T) {
	if sessionRequiresFreshValidation(http.MethodGet) || sessionRequiresFreshValidation(http.MethodHead) {
		t.Fatal("read-only requests should accept a bounded positive session cache entry")
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if !sessionRequiresFreshValidation(method) {
			t.Fatalf("%s should revalidate the session in PostgreSQL", method)
		}
	}
}

func TestNormalizeIdentifier(t *testing.T) {
	tests := []struct {
		name, input, channel, wantType, wantIdentifier, wantChannel string
	}{
		{"email", " Customer@Example.COM ", "email", "email", "customer@example.com", "email"},
		{"nigerian local phone", "0803 555 0147", "sms", "phone", "+2348035550147", "sms"},
		{"whatsapp", "+234-803-555-0147", "whatsapp", "whatsapp", "+2348035550147", "whatsapp"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotType, gotIdentifier, gotChannel, err := normalizeIdentifier(test.input, test.channel)
			if err != nil {
				t.Fatal(err)
			}
			if gotType != test.wantType || gotIdentifier != test.wantIdentifier || gotChannel != test.wantChannel {
				t.Fatalf("normalizeIdentifier() = %q, %q, %q", gotType, gotIdentifier, gotChannel)
			}
		})
	}
}

func TestCustomerMatchesOnlyVerifiedBookingContact(t *testing.T) {
	now := time.Now()
	customer := Customer{Email: "customer@example.com", EmailVerifiedAt: &now, Phone: "+2348035550147"}
	if !CustomerMatchesBookingContact(customer, "CUSTOMER@example.com", "+234 999 000 0000") {
		t.Fatal("verified email did not match")
	}
	if CustomerMatchesBookingContact(customer, "other@example.com", "0803 555 0147") {
		t.Fatal("unverified phone matched")
	}
	customer.PhoneVerifiedAt = &now
	if !CustomerMatchesBookingContact(customer, "other@example.com", "0803 555 0147") {
		t.Fatal("verified normalized phone did not match")
	}
}
