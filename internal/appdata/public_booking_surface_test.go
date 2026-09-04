package appdata

import "testing"

func TestNormalizePublicBookingSource(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{"", "direct_public_page", true},
		{"direct_public_page", "direct_public_page", true},
		{"marketplace", "marketplace", true},
		{"external", "", false},
	}
	for _, test := range tests {
		got, err := normalizePublicBookingSource(test.input)
		if (err == nil) != test.ok || got != test.want {
			t.Fatalf("normalizePublicBookingSource(%q) = %q, %v", test.input, got, err)
		}
	}
}

func TestPublicCheckoutReturnURLTemplate(t *testing.T) {
	handler := &Handler{
		publicBaseURL:      "https://client.tellbook.test",
		marketplaceBaseURL: "https://market.tellbook.test",
	}

	direct, err := handler.publicCheckoutReturnURLTemplate("direct_public_page", "amara & co")
	if err != nil || direct != "https://client.tellbook.test/p/amara%20&%20co/booking/payment/return?payment={payment_token}" {
		t.Fatalf("direct return URL = %q, %v", direct, err)
	}
	marketplace, err := handler.publicCheckoutReturnURLTemplate("marketplace", "ignored")
	if err != nil || marketplace != "https://market.tellbook.test/booking/payment/return?payment={payment_token}" {
		t.Fatalf("marketplace return URL = %q, %v", marketplace, err)
	}
	if _, err := handler.publicCheckoutReturnURLTemplate("https://evil.test", "ignored"); err == nil {
		t.Fatal("arbitrary return surface was accepted")
	}
}
