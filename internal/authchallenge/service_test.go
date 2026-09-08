package authchallenge

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNormalizeIdentifierRequiresMatchingDeliveryChannel(t *testing.T) {
	tests := []struct {
		name, identifier, channel, wantType, wantIdentifier string
		wantErr                                             error
	}{
		{name: "email", identifier: " Person@Example.COM ", channel: ChannelEmail, wantType: "email", wantIdentifier: "person@example.com"},
		{name: "Nigerian phone", identifier: "0803 555 0147", channel: ChannelWhatsApp, wantType: "phone", wantIdentifier: "+2348035550147"},
		{name: "email over WhatsApp", identifier: "person@example.com", channel: ChannelWhatsApp, wantErr: ErrInvalidIdentifier},
		{name: "phone over email", identifier: "+2348035550147", channel: ChannelEmail, wantErr: ErrInvalidIdentifier},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotType, gotIdentifier, gotChannel, err := NormalizeIdentifier(test.identifier, test.channel)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("NormalizeIdentifier() error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if gotType != test.wantType || gotIdentifier != test.wantIdentifier || gotChannel != test.channel {
				t.Fatalf("NormalizeIdentifier() = %q, %q, %q", gotType, gotIdentifier, gotChannel)
			}
		})
	}
}

func TestCapabilitiesDoNotAdvertiseDisabledChannels(t *testing.T) {
	disabled := NewCapabilities(nil, true)
	if disabled.Channels == nil || len(disabled.Channels) != 0 || disabled.CodeLength != 6 || !disabled.PasswordFallbackAvailable {
		t.Fatalf("disabled capabilities = %+v", disabled)
	}
	service := &Service{emailEnabled: true}
	enabled := NewCapabilities(service, true)
	if len(enabled.Channels) != 1 || enabled.Channels[0] != ChannelEmail {
		t.Fatalf("enabled capabilities = %+v", enabled)
	}
}

func TestMaskIdentifierDoesNotReturnRawDestination(t *testing.T) {
	for _, test := range []struct{ identityType, value string }{
		{identityType: "email", value: "person@example.com"},
		{identityType: "phone", value: "+2348035550147"},
	} {
		masked := MaskIdentifier(test.identityType, test.value)
		if masked == "" || masked == test.value {
			t.Fatalf("MaskIdentifier(%q) = %q", test.value, masked)
		}
	}
}

func TestResponseForPublishesOnlyContractDeliveryStates(t *testing.T) {
	now := time.Now().UTC()
	challenge := Challenge{
		ID: uuid.New(), IdentifierType: "email", Identifier: "person@example.com",
		DeliveryChannel: ChannelEmail, DeliveryDeadline: now.Add(time.Minute), CreatedAt: now,
	}
	for _, internalState := range []string{"pending", "processing", "retry", "queued"} {
		response := responseFor(challenge, internalState, now)
		if response.DeliveryState != "queued" || response.NextStatusCheckInSeconds == nil {
			t.Fatalf("%s response = %+v, want pollable queued state", internalState, response)
		}
	}
	response := responseFor(challenge, "unknown", now)
	if response.DeliveryState != "unknown" || response.NextStatusCheckInSeconds == nil {
		t.Fatalf("unknown response = %+v, want pollable unknown state", response)
	}
}

func TestResponseForExpiresUnacceptedChallengeAtDeliveryDeadline(t *testing.T) {
	now := time.Now().UTC()
	response := responseFor(Challenge{
		ID: uuid.New(), IdentifierType: "email", Identifier: "person@example.com",
		DeliveryChannel: ChannelEmail, DeliveryDeadline: now, CreatedAt: now.Add(-time.Minute),
	}, "unknown", now)
	if response.DeliveryState != "expired" || response.NextStatusCheckInSeconds != nil {
		t.Fatalf("expired response = %+v", response)
	}
}
