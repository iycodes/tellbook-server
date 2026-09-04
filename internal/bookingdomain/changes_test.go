package bookingdomain

import (
	"testing"
	"time"
)

func TestStructuredPolicyCanonicalOptions(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		notice     int
		refundBPS  int64
		reschedule bool
	}{
		{"free", "No cancellation fee", 0, 10000, true},
		{"one day", "24h notice required", 1440, 10000, true},
		{"two days", "48h notice required", 2880, 10000, true},
		{"strict", "Strict - No refunds", 0, 0, true},
		{"custom is conservative", "Call us if your plans change", 0, 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := StructuredPolicy(test.text)
			if policy.CancellationNoticeMinutes != test.notice ||
				policy.CancellationRefundBPS != test.refundBPS ||
				policy.AutomatedReschedule != test.reschedule {
				t.Fatalf("unexpected policy: %#v", policy)
			}
		})
	}
}

func TestCancellationRefundHonorsDeadline(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	policy := StructuredPolicy("24h notice required")
	if got := CancellationRefund(5000, now.Add(25*time.Hour), now, policy); got != 5000 {
		t.Fatalf("expected full refund, got %d", got)
	}
	if got := CancellationRefund(5000, now.Add(23*time.Hour), now, policy); got != 0 {
		t.Fatalf("expected no late refund, got %d", got)
	}
}

func TestPermissionsRespectActorAndTime(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	start := now.Add(48 * time.Hour)
	end := start.Add(time.Hour)
	booked := Permissions("booked", start, end, now, StructuredPolicy("24h notice required"))
	if !booked.CustomerCancel || !booked.CustomerReschedule || !booked.ProviderConfirm || !booked.ProviderDecline {
		t.Fatalf("booked permissions incomplete: %#v", booked)
	}
	completedWindow := Permissions("confirmed", now.Add(-2*time.Hour), now.Add(-time.Hour), now, ChangePolicy{})
	if !completedWindow.ProviderComplete || !completedWindow.ProviderNoShow || completedWindow.CustomerCancel {
		t.Fatalf("completed-window permissions invalid: %#v", completedWindow)
	}
}
