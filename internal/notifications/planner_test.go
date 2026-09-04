package notifications

import (
	"testing"
	"time"

	"booking/go-server/internal/whatsapp"

	"github.com/google/uuid"
)

func TestPlannerDoesNotTreatRawCreationAsSecured(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repository := plannerTestRepository(now, whatsapp.TemplateProviderNewBooking)
	state := plannerTestBooking(now)
	state.PaymentStatus = "full_payment_pending"
	if candidates := repository.reminderCandidates(state, now); len(candidates) != 0 {
		t.Fatalf("unsecured booking produced %d reminders", len(candidates))
	}
	if bookingSecured(state) {
		t.Fatal("unpaid booking was treated as secured")
	}
	state.PaymentStatus = "paid_in_full"
	if !bookingSecured(state) {
		t.Fatal("paid booking without an agreement was not secured")
	}
	state.PaymentStatus = "deposit_paid_balance_due"
	if !bookingSecured(state) {
		t.Fatal("deposit-satisfied booking was not secured")
	}
}

func TestReminderCandidatesRespectAudienceAndTemplateGates(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repository := plannerTestRepository(now, whatsapp.TemplateProviderBookingReminder)
	state := plannerTestBooking(now)
	candidates := repository.reminderCandidates(state, now)
	if len(candidates) != 3 {
		t.Fatalf("reminder candidates = %d, want provider email/WhatsApp and customer email", len(candidates))
	}
	for _, candidate := range candidates {
		if candidate.audience == "customer" && candidate.channel == "whatsapp" {
			t.Fatal("customer WhatsApp reminder bypassed the held template")
		}
		if !candidate.scheduledFor.Equal(state.StartAt.Add(-24 * time.Hour)) {
			t.Fatalf("scheduled_for = %v", candidate.scheduledFor)
		}
	}
	repository.enabledTemplates[whatsapp.TemplateUserReminder] = struct{}{}
	if candidates := repository.reminderCandidates(state, now); len(candidates) != 4 {
		t.Fatalf("enabled customer template candidates = %d, want 4", len(candidates))
	}
}

func TestLegacyConsentRevisionCannotCreateCustomerReminders(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repository := plannerTestRepository(
		now,
		whatsapp.TemplateProviderBookingReminder,
	)
	state := plannerTestBooking(now)
	state.ConsentPolicyRevision = 0
	for _, candidate := range repository.reminderCandidates(state, now) {
		if candidate.audience == "customer" {
			t.Fatalf("legacy consent revision created a customer delivery: %#v", candidate)
		}
	}
}

func TestReminderInsideTwoHourFloorIsNotCreated(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repository := plannerTestRepository(now)
	state := plannerTestBooking(now)
	state.StartAt = now.Add(90 * time.Minute)
	if candidates := repository.reminderCandidates(state, now); len(candidates) != 0 {
		t.Fatalf("inside-floor booking produced %d reminders", len(candidates))
	}
	state.StartAt = now.Add(3 * time.Hour)
	candidates := repository.reminderCandidates(state, now)
	if len(candidates) == 0 || !candidates[0].scheduledFor.Equal(now) {
		t.Fatalf("eligible inside-window reminder was not due immediately: %#v", candidates)
	}
}

func TestEventCandidatesUseStableSourceSequenceForReorderedEvents(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repository := plannerTestRepository(now)
	state := plannerTestBooking(now)
	state.CurrentBookingEventSequence = 42
	event := bookingEvent{Sequence: 12, Payload: eventPayload{PreviousStartsAt: state.StartAt.Add(-time.Hour)}}
	candidates := repository.eventCandidates(state, event, now)
	if len(candidates) != 2 {
		t.Fatalf("reschedule candidates = %d, want provider/customer email", len(candidates))
	}
	for _, candidate := range candidates {
		if candidate.notificationType != "booking_rescheduled" ||
			candidate.idempotencyKey[len(candidate.idempotencyKey)-3:] != ":12" || candidate.eventSequence != 12 {
			t.Fatalf("unexpected reschedule candidate: %#v", candidate)
		}
	}
}

func TestRetryDelayIsBounded(t *testing.T) {
	if retryDelay(1) != 5*time.Second {
		t.Fatalf("first retry delay = %v", retryDelay(1))
	}
	if retryDelay(100) != 10*time.Minute {
		t.Fatalf("maximum retry delay = %v", retryDelay(100))
	}
}

func plannerTestRepository(now time.Time, templates ...whatsapp.TemplateKey) *Repository {
	enabled := make(map[whatsapp.TemplateKey]struct{}, len(templates))
	for _, template := range templates {
		enabled[template] = struct{}{}
	}
	return &Repository{
		destinationKey:   []byte("notification-planner-test-key-32-bytes"),
		enabledTemplates: enabled,
		now:              func() time.Time { return now },
	}
}

func plannerTestBooking(now time.Time) bookingState {
	return bookingState{
		ID: uuid.New(), ClientID: uuid.New(), Status: "confirmed", PaymentStatus: "paid_in_full",
		StartAt: now.Add(48 * time.Hour), CustomerEmail: "customer@example.com",
		CustomerWhatsApp: "+2348012345678", EmailReminderConsent: true, WhatsAppConsent: true,
		ConsentPolicyRevision: 1, MarketplaceBookingEmail: true, MarketplaceBookingWhatsApp: true,
		ProviderEmail: "provider@example.com", ProviderEmailVerified: true,
		ProviderBookingEmail: true, ProviderBookingWhatsApp: true, ProviderReminderEnabled: true,
		ProviderReminderMinutes: 1440, ProviderWhatsApp: "+2348098765432",
		ProviderWhatsAppVerified: true, ProviderPreferenceRevision: 1,
		CurrentBookingEventSequence: 1,
	}
}
