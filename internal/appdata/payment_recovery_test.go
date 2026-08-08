package appdata

import (
	"testing"
	"time"

	"booking/go-server/internal/payments"
)

func TestPublicPaymentRecoveryActionSeparatesProcessingFromResuming(t *testing.T) {
	tests := []struct {
		name           string
		status         payments.PaymentStatus
		providerStatus string
		createdAt      time.Time
		want           string
	}{
		{name: "processing transfer", status: payments.PaymentStatusPending, providerStatus: "Initialized", want: "wait_for_confirmation"},
		{name: "new provider reference is still propagating", status: payments.PaymentStatusPending, providerStatus: "not_found", createdAt: time.Now(), want: "wait_for_confirmation"},
		{name: "older missing provider reference can resume", status: payments.PaymentStatusPending, providerStatus: "not_found", createdAt: time.Now().Add(-3 * time.Minute), want: "resume_checkout"},
		{name: "failed", status: payments.PaymentStatusFailed, providerStatus: "Failed", want: "retry_checkout"},
		{name: "paid", status: payments.PaymentStatusPaid, providerStatus: "Completed", want: "none"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := publicPaymentRecoveryAction(payments.FinancialPayment{
				Status: testCase.status, ProviderStatus: testCase.providerStatus, CreatedAt: testCase.createdAt,
			})
			if got != testCase.want {
				t.Fatalf("publicPaymentRecoveryAction() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestBuildPublicPaymentStatusResponseCarriesLedgerOrdering(t *testing.T) {
	updatedAt := time.Date(2026, time.August, 5, 18, 30, 0, 123456000, time.UTC)
	response := buildPublicPaymentStatusResponse(payments.FinancialPayment{
		PublicToken: "payment-token",
		Status:      payments.PaymentStatusPending,
		Version:     7,
		UpdatedAt:   updatedAt,
	})

	if response.Version != 7 {
		t.Fatalf("version = %d, want 7", response.Version)
	}
	if response.StatusUpdatedAt != updatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("status_updated_at = %q, want %q", response.StatusUpdatedAt, updatedAt.Format(time.RFC3339Nano))
	}
}
