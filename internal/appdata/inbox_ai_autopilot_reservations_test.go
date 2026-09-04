package appdata

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestInboxAIReservationPresentationsAreTruthfulForEveryNextStep(t *testing.T) {
	bookingID := uuid.New()
	tests := []struct {
		nextStep    BookingNextStep
		wantState   string
		wantStatus  string
		wantContent string
	}{
		{BookingNextStepPayment, "awaiting_payment", "Reserved — payment required", "payment is still required"},
		{BookingNextStepAgreement, "awaiting_after_payment_agreement", "Reserved — agreement required", "agreement is still required"},
		{BookingNextStepProviderConfirmation, "awaiting_provider_confirmation", "Awaiting provider confirmation", "awaiting provider confirmation"},
		{BookingNextStepComplete, "completed", "Confirmed", "booking is confirmed"},
	}
	for _, test := range tests {
		t.Run(string(test.nextStep), func(t *testing.T) {
			reservation := BookingReservationResult{
				Booking: PublicBookingSummaryResponse{
					BookingID: bookingID.String(), ServiceTitle: "Knotless braids",
					Status: "booked", StartsAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
					EndsAt:   time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339),
					Timezone: "Africa/Lagos",
				},
				Lifecycle: BookingLifecycle{NextStep: test.nextStep},
			}
			created, next, content, _, err := inboxAIReservationPresentations(reservation, bookingID)
			if err != nil {
				t.Fatal(err)
			}
			if inboxAIReservationState(test.nextStep) != test.wantState {
				t.Fatalf("state = %q, want %q", inboxAIReservationState(test.nextStep), test.wantState)
			}
			if err := validateInboxMessagePresentation(&created); err != nil {
				t.Fatalf("reservation presentation: %v", err)
			}
			if err := validateInboxMessagePresentation(&next); err != nil {
				t.Fatalf("next-step presentation: %v", err)
			}
			var data struct {
				StatusLabel string `json:"status_label"`
			}
			if err := json.Unmarshal(created.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.StatusLabel != test.wantStatus || !strings.Contains(content, test.wantContent) {
				t.Fatalf("status/content = %q/%q", data.StatusLabel, content)
			}
		})
	}
}

func TestValidateInboxAIReservationProgressFencesChangedEvidence(t *testing.T) {
	proposalID, key := uuid.New(), uuid.New()
	progress := inboxAIReservationProgress{
		ProposalID: proposalID, ProposalRevision: 4,
		ProposalHash:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ConfirmationKey: key, WhatsAppConsent: true,
	}
	input := ConfirmInboxAIBookingProposalInput{
		IdempotencyKey: key.String(), ProposalRevision: 4, ProposalHash: progress.ProposalHash,
		ContactDetailsConfirmed: true, WhatsAppConsent: true,
	}
	if err := validateInboxAIReservationProgress(progress, proposalID, key, input); err != nil {
		t.Fatalf("valid progress: %v", err)
	}
	changedConsent := input
	changedConsent.WhatsAppConsent = false
	if err := validateInboxAIReservationProgress(progress, proposalID, key, changedConsent); !errors.Is(err, ErrInboxIdempotencyConflict) {
		t.Fatalf("changed consent error = %v", err)
	}
	changedHash := input
	changedHash.ProposalHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := validateInboxAIReservationProgress(progress, proposalID, key, changedHash); !errors.Is(err, ErrInboxAIProposalStale) {
		t.Fatalf("changed hash error = %v", err)
	}
}
