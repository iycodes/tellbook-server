package appdata

import (
	"errors"
	"strings"
	"testing"

	aisvc "booking/go-server/internal/ai"
	"booking/go-server/internal/aierror"
)

func TestNormalizeSemiPilotFinalDecisionKeepsWorkflowStateApplicationOwned(t *testing.T) {
	tests := []struct {
		name               string
		decisionState      string
		authoritativeState string
		linkCreated        bool
		want               string
	}{
		{name: "cannot invent service selection", decisionState: "service_identified", authoritativeState: "qualifying", want: "qualifying"},
		{name: "cannot regress selected service", decisionState: "qualifying", authoritativeState: "service_identified", want: "service_identified"},
		{name: "link action commits link sent", decisionState: "link_ready", authoritativeState: "link_ready", linkCreated: true, want: "link_ready"},
		{name: "completed only after prior link", decisionState: "completed", authoritativeState: "link_sent", want: "completed"},
		{name: "handoff is preserved", decisionState: "handoff", authoritativeState: "service_identified", want: "handoff"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := aisvc.SemiPilotTurnDecision{NextState: test.decisionState}
			got := normalizeSemiPilotFinalDecision(
				decision, test.authoritativeState, test.linkCreated,
			)
			if got.NextState != test.want {
				t.Fatalf("state=%q, want %q", got.NextState, test.want)
			}
		})
	}
}

func TestShouldRetryInboxAIGenerationOnlyRetriesTypedTransientFailures(t *testing.T) {
	transient := aierror.Transient("call model", aierror.KindUnavailable, nil)
	permanent := aierror.Terminal("call model", aierror.KindAuthentication, nil)
	if !shouldRetryInboxAIGeneration(transient, 1, 3) {
		t.Fatal("transient provider failure was not retried")
	}
	if shouldRetryInboxAIGeneration(transient, 3, 3) {
		t.Fatal("transient provider failure exceeded the job attempt limit")
	}
	if shouldRetryInboxAIGeneration(permanent, 1, 3) {
		t.Fatal("permanent provider failure was retried")
	}
	if shouldRetryInboxAIGeneration(errors.New("unclassified failure"), 1, 3) {
		t.Fatal("unclassified generation failure was retried")
	}
}

func TestRedactSemiPilotCustomerTextExcludesContactAndStreetAddress(t *testing.T) {
	input := "Email me at Ada.Okafor@example.com or call +234 803 123 4567. I live at 12 Allen Avenue, Ikeja."
	got := redactSemiPilotCustomerText(input)
	for _, secret := range []string{"Ada.Okafor@example.com", "+234 803 123 4567", "12 Allen Avenue"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redaction leaked %q in %q", secret, got)
		}
	}
	for _, marker := range []string{"[email redacted]", "[phone redacted]", "[address redacted]"} {
		if !strings.Contains(got, marker) {
			t.Fatalf("redaction missing %q in %q", marker, got)
		}
	}
}

func TestRedactSemiPilotCustomerTextCoversCommonNigerianAddressForms(t *testing.T) {
	t.Parallel()
	tests := []string{
		"My address is Plot 4, Block B, Admiralty Way, Lekki Phase 1.",
		"Deliver to Flat 3, 12A Awolowo Road, Ikoyi.",
		"I stay at House 14, Road 7, Festac Town.",
		"Find me opposite Ikeja City Mall beside the bank.",
		"The office is at Km 14 Lekki-Epe Expressway.",
	}
	for _, input := range tests {
		redacted := redactSemiPilotCustomerText(input)
		if !strings.Contains(redacted, "[address redacted]") {
			t.Fatalf("address was not redacted: input=%q output=%q", input, redacted)
		}
		for _, secret := range []string{"Admiralty Way", "Awolowo Road", "Road 7", "Ikeja City Mall", "Lekki-Epe Expressway"} {
			if strings.Contains(redacted, secret) {
				t.Fatalf("address fragment %q leaked from %q as %q", secret, input, redacted)
			}
		}
	}
}
