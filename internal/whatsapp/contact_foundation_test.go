package whatsapp

import (
	"bytes"
	"testing"
)

func TestNormalizeE164ForCountry(t *testing.T) {
	tests := []struct {
		value   string
		country string
		want    string
	}{
		{value: "0803 168 5968", country: "NG", want: "+2348031685968"},
		{value: "024 123 4567", country: "GH", want: "+233241234567"},
		{value: "0712-345-678", country: "KE", want: "+254712345678"},
		{value: "+27 82 123 4567", country: "ZA", want: "+27821234567"},
		{value: "00225 01 23 45 67 89", country: "CI", want: "+2250123456789"},
	}
	for _, test := range tests {
		got, err := NormalizeE164ForCountry(test.value, test.country)
		if err != nil || got != test.want {
			t.Fatalf("NormalizeE164ForCountry(%q, %q) = %q, %v; want %q", test.value, test.country, got, err, test.want)
		}
	}
	for _, invalid := range []struct{ value, country string }{
		{value: "call-me", country: "NG"},
		{value: "0803", country: "NG"},
		{value: "08031234567", country: "ZZ"},
	} {
		if _, err := NormalizeE164ForCountry(invalid.value, invalid.country); err == nil {
			t.Fatalf("NormalizeE164ForCountry(%q, %q) accepted invalid value", invalid.value, invalid.country)
		}
	}
}

func TestClassifyInboundControlIsExactAndBounded(t *testing.T) {
	if got := classifyInboundControl("2348012345678", "text", " stop \n"); got.kind != "stop" || got.sender == "" {
		t.Fatalf("STOP control = %#v", got)
	}
	if got := classifyInboundControl("2348012345678", "text", "START"); got.kind != "start" {
		t.Fatalf("START control = %#v", got)
	}
	token := "abcdefghijklmnopqrstuvwxyzABCDEFGH1234567890_-"
	if got := classifyInboundControl("2348012345678", "text", "VERIFY "+token); got.kind != "verify" || got.token != token {
		t.Fatalf("VERIFY control = %#v", got)
	}
	for _, body := range []string{"STOP now", "START please", "VERIFY short", "hello"} {
		// Ordinary text goes to gated Tessa onboarding, never an exact control command.
		if got := classifyInboundControl("2348012345678", "text", body); got.kind != "tessa_onboarding" || got.text != body || got.sender != "2348012345678" {
			t.Fatalf("%q classified as %#v", body, got)
		}
	}
	if got := classifyInboundControl("2348012345678", "image", "STOP"); got.kind != "" {
		t.Fatalf("non-text message classified as %#v", got)
	}
}

func TestDestinationFingerprintIsChannelBound(t *testing.T) {
	key := bytes.Repeat([]byte("k"), 32)
	first := destinationFingerprint(key, "whatsapp", "+2348012345678")
	repeated := destinationFingerprint(key, "whatsapp", "+2348012345678")
	email := destinationFingerprint(key, "email", "+2348012345678")
	if !bytes.Equal(first, repeated) {
		t.Fatal("destination fingerprint is not deterministic")
	}
	if bytes.Equal(first, email) {
		t.Fatal("destination fingerprint is not channel-bound")
	}
}

func TestNormalizeEmailDestination(t *testing.T) {
	if got := normalizeEmailDestination("  Customer@Example.COM "); got != "customer@example.com" {
		t.Fatalf("normalized email = %q", got)
	}
	for _, invalid := range []string{"", "customer", "@example.com", "customer@example", "customer @example.com"} {
		if got := normalizeEmailDestination(invalid); got != "" {
			t.Fatalf("normalizeEmailDestination(%q) = %q", invalid, got)
		}
	}
}
