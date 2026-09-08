package notifications

import (
	"context"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/mailer"

	"github.com/google/uuid"
)

func TestEmailFinalizationContextSurvivesWorkerCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	ctx, cancel := newEmailFinalizationContext(parent)
	defer cancel()
	if err := ctx.Err(); err != nil {
		t.Fatalf("finalization context inherited worker cancellation: %v", err)
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("finalization context is not bounded")
	}
}

func TestCustomerEmailContactIsOptionalAndAudienceBound(t *testing.T) {
	for _, audience := range []string{"customer", "provider"} {
		for _, phone := range []string{"", "+2348055555555"} {
			message, err := renderEmailTemplate(uuid.New(), emailTemplateData{Audience: audience, Type: "appointment_reminder", ProviderContactPhone: phone})
			if err != nil {
				t.Fatal(err)
			}
			want := audience == "customer" && phone != ""
			if strings.Contains(message.Text, "Provider contact:") != want || strings.Contains(message.HTML, "Provider contact:") != want {
				t.Fatalf("contact visibility for %s/%q", audience, phone)
			}
		}
	}
}

func TestEmailDispositionMetricsDistinguishRetryExhaustion(t *testing.T) {
	if outcome := emailDispositionOutcome(mailer.DispositionRetryable, maxDeliveryAttempts-1); outcome != "retry" {
		t.Fatalf("pre-exhaustion outcome = %q", outcome)
	}
	if outcome := emailDispositionOutcome(mailer.DispositionRetryable, maxDeliveryAttempts); outcome != "retry_exhausted" {
		t.Fatalf("exhausted outcome = %q", outcome)
	}
}

func TestEmailRegistryCoversEveryPlannedEmailCombination(t *testing.T) {
	wanted := []string{
		"customer:customer_booking_received", "customer:customer_booking_secured", "provider:provider_new_booking",
		"provider:appointment_reminder", "customer:appointment_reminder",
	}
	for _, notificationType := range []string{
		"booking_rescheduled", "booking_cancelled", "booking_expired", "payment_satisfied",
		"payment_failed", "payment_refunded", "payment_action_required",
	} {
		wanted = append(wanted, "provider:"+notificationType, "customer:"+notificationType)
	}
	for _, key := range wanted {
		if _, ok := emailTemplateRegistry[key]; !ok {
			t.Errorf("email registry is missing %q", key)
		}
	}
}

func TestGuestBookingActionKeepsClaimOutOfRequestURL(t *testing.T) {
	bookingID := uuid.MustParse("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	actionURL := emailBookingActionURL(
		"customer", false, bookingID, "secret/token", "https://client.test", "https://market.test/",
	)
	if actionURL != "https://market.test/bookings#claim=secret%2Ftoken" {
		t.Fatalf("guest action URL = %q", actionURL)
	}
	if strings.Contains(strings.SplitN(actionURL, "#", 2)[0], "secret") {
		t.Fatalf("guest claim leaked into request URL: %q", actionURL)
	}
}

func TestProviderAwaitingConfirmationReminderRequiresConfirmationWording(t *testing.T) {
	for _, status := range []string{"booked", "pending"} {
		message, err := renderEmailTemplate(uuid.New(), emailTemplateData{
			Audience: "provider", Type: "appointment_reminder", BookingStatus: status,
			RecipientEmail: "provider@example.com", RecipientName: "Provider",
			CustomerName: "Customer", ProviderName: "Provider", ServiceTitle: "Service",
			When: "Friday", Location: "Studio", Total: "₦10,000", Paid: "₦10,000", Due: "₦0",
			ActionURL: "https://provider.example.com/bookings?booking=abc",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.ToLower(message.Text), "awaiting your confirmation") ||
			!strings.Contains(strings.ToLower(message.HTML), "confirmation is still required") {
			t.Fatalf("%s reminder omitted confirmation requirement: %#v", status, message)
		}
	}
}

func TestProviderConfirmedReminderDoesNotUsePendingWording(t *testing.T) {
	message, err := renderEmailTemplate(uuid.New(), emailTemplateData{
		Audience: "provider", Type: "appointment_reminder", BookingStatus: "confirmed",
		RecipientEmail: "provider@example.com", RecipientName: "Provider",
		CustomerName: "Customer", ProviderName: "Provider", ServiceTitle: "Service",
		When: "Friday", Location: "Studio", Total: "₦10,000", Paid: "₦10,000", Due: "₦0",
		ActionURL: "https://provider.example.com/bookings?booking=abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(message.Text), "awaiting your confirmation") {
		t.Fatalf("confirmed reminder used pending wording: %s", message.Text)
	}
}

func TestRenderEmailTemplateIsDeterministicAndEscapesHTML(t *testing.T) {
	deliveryID := uuid.MustParse("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	data := emailTemplateData{
		Audience: "customer", Type: "appointment_reminder", RecipientEmail: "customer@example.com",
		RecipientName: "A <Customer>", CustomerName: "A <Customer>", ProviderName: "Studio & Co",
		ServiceTitle: "Cut & Style", When: "Friday", Location: "1 <Main> Street",
		Total: "₦10,000.00", Paid: "₦5,000.00", Due: "₦5,000.00",
		ActionURL: "https://example.com/bookings?booking=abc&source=email",
	}
	first, err := renderEmailTemplate(deliveryID, data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderEmailTemplate(deliveryID, data)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.MessageID != "<notification-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee@mail.tellbook.app>" {
		t.Fatalf("email rendering is not deterministic: %#v / %#v", first, second)
	}
	if strings.Contains(first.HTML, "A <Customer>") || !strings.Contains(first.HTML, "A &lt;Customer&gt;") {
		t.Fatalf("email HTML did not escape user-controlled values: %s", first.HTML)
	}
}

func TestDeliveryRetryAtIsStableAndBounded(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	id := uuid.MustParse("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	first := deliveryRetryAt(now, id, 1)
	if !first.Equal(deliveryRetryAt(now, id, 1)) || first.Before(now.Add(15*time.Second)) ||
		!first.Before(now.Add(18*time.Second+time.Millisecond)) {
		t.Fatalf("unexpected first retry time %v", first)
	}
	maximum := deliveryRetryAt(now, id, 50)
	if maximum.Before(now.Add(15*time.Minute)) || !maximum.Before(now.Add(18*time.Minute+time.Millisecond)) {
		t.Fatalf("unexpected maximum retry time %v", maximum)
	}
}
