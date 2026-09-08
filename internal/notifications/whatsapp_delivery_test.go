package notifications

import (
	"context"
	"errors"
	"testing"
	"time"

	"booking/go-server/internal/whatsapp"

	"github.com/google/uuid"
)

func TestWhatsAppFinalizationContextSurvivesWorkerCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	ctx, cancel := newWhatsAppFinalizationContext(parent)
	defer cancel()
	if err := ctx.Err(); err != nil {
		t.Fatalf("finalization context inherited worker cancellation: %v", err)
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("finalization context is not bounded")
	}
}

func TestClassifyWhatsAppSendError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		disposition whatsAppFailureDisposition
		code        string
	}{
		{"pre-write transport", &whatsapp.TransportError{Cause: errors.New("dial")}, whatsAppFailureRetryable, "meta_transport"},
		{"ambiguous transport", &whatsapp.TransportError{Cause: errors.New("reset"), Ambiguous: true}, whatsAppFailureUnknown, "meta_outcome_unknown"},
		{"preflight", &whatsapp.RequestError{Cause: errors.New("invalid template values")}, whatsAppFailurePermanent, "meta_request_invalid"},
		{"rate limit", &whatsapp.GraphError{Class: whatsapp.ErrorClassRateLimit, Code: 4}, whatsAppFailureRetryable, "meta_rate_limit_4"},
		{"auth", &whatsapp.GraphError{Class: whatsapp.ErrorClassAuth, Code: 190}, whatsAppFailurePermanent, "meta_auth_190"},
		{"untyped", errors.New("unknown sender error"), whatsAppFailureUnknown, "meta_outcome_unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			disposition, code, _ := classifyWhatsAppSendError(test.err)
			if disposition != test.disposition || code != test.code {
				t.Fatalf("classification = %q %q", disposition, code)
			}
		})
	}
}

func TestWhatsAppRetryHonorsBoundedProviderDelay(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	id := uuid.MustParse("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	if got := whatsAppRetryAt(now, id, 1, 10*time.Minute); !got.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("provider retry delay = %v", got)
	}
	if got := whatsAppRetryAt(now, id, 1, 2*time.Hour); !got.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("bounded provider retry delay = %v", got)
	}
}

func TestBookingStatusTemplateValuesAreStableAndComplete(t *testing.T) {
	bookingID := uuid.MustParse("12345678-90ab-4cde-8f01-234567890abc")
	if got := bookingDisplayReference(bookingID); got != "TB-1234567890AB" {
		t.Fatalf("booking reference = %q", got)
	}
	for notificationType, want := range map[string]string{
		"booking_rescheduled":     "Appointment rescheduled",
		"booking_cancelled":       "Booking cancelled",
		"booking_expired":         "Booking expired",
		"payment_satisfied":       "Payment requirement satisfied",
		"payment_failed":          "Payment needs attention",
		"payment_refunded":        "Payment refunded",
		"payment_action_required": "Payment action required",
	} {
		if got := bookingUpdateSummary(notificationType); got != want {
			t.Fatalf("summary for %s = %q, want %q", notificationType, got, want)
		}
	}
	if got := bookingUpdateSummary("unknown"); got != "" {
		t.Fatalf("unknown notification summary = %q", got)
	}
}

func TestWhatsAppStatusOrdering(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	older := now.Add(-time.Minute)
	if !shouldApplyWhatsAppStatus("accepted", &now, "sent", older) {
		t.Fatal("provider sent callback was incorrectly compared with the local accepted timestamp")
	}
	if shouldApplyWhatsAppStatus("delivered", &now, "sent", now.Add(time.Minute)) {
		t.Fatal("sent regressed delivered")
	}
	if shouldApplyWhatsAppStatus("delivered", &now, "read", older) {
		t.Fatal("older read callback advanced delivered")
	}
	if !shouldApplyWhatsAppStatus("read", &now, "failed", now.Add(time.Minute)) {
		t.Fatal("newer provider failure did not become terminal")
	}
	if shouldApplyWhatsAppStatus("failed", &now, "read", now.Add(time.Minute)) {
		t.Fatal("terminal provider failure regressed")
	}
}
