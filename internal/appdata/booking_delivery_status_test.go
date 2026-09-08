package appdata

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDeliveryChannelStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected BookingDeliveryChannelStatus
	}{
		{
			name:     "worker state is pending",
			input:    "processing",
			expected: BookingDeliveryChannelStatus{Requested: true, Eligible: true, Status: "pending"},
		},
		{
			name:     "provider accepted state remains accepted",
			input:    "accepted",
			expected: BookingDeliveryChannelStatus{Requested: true, Eligible: true, Status: "accepted"},
		},
		{
			name:     "deleted delivery is failed",
			input:    "deleted",
			expected: BookingDeliveryChannelStatus{Requested: true, Eligible: true, Status: "failed"},
		},
		{
			name:     "missing delivery was not requested",
			input:    "",
			expected: BookingDeliveryChannelStatus{Requested: true, Eligible: false, Status: "not_requested"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if actual := deliveryChannelStatus(test.input); actual != test.expected {
				t.Fatalf("deliveryChannelStatus(%q) = %#v, want %#v", test.input, actual, test.expected)
			}
		})
	}
}

func TestTransactionalCustomerEmailDeliveryOverridesReminderConsentIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var bookingID, clientID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id,client_id
		FROM bookings
		WHERE NOT email_reminder_consent
		ORDER BY created_at DESC
		LIMIT 1
	`).Scan(&bookingID, &clientID); errors.Is(err, pgx.ErrNoRows) {
		t.Skip("no booking without email reminder consent is available for the delivery-status integration test")
	} else if err != nil {
		t.Fatal(err)
	}

	deliveryID := uuid.New()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM notification_deliveries WHERE id=$1`, deliveryID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO notification_deliveries (
			id,idempotency_key,client_id,booking_id,booking_event_sequence,audience_type,
			channel,notification_type,scheduled_for,destination_hmac,status,next_attempt_at,
			accepted_at,completed_at,created_at,updated_at
		) VALUES ($1,$2,$3,$4,0,'customer','email','customer_booking_received',NOW(),$5,'accepted',NOW(),NOW(),NOW(),NOW(),NOW())
	`, deliveryID, "delivery-status-test:"+deliveryID.String(), clientID, bookingID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}

	status, err := NewRepository(pool).loadBookingDeliveryStatus(ctx, bookingID)
	if err != nil {
		t.Fatal(err)
	}
	if !status.CustomerEmail.Requested || !status.CustomerEmail.Eligible || status.CustomerEmail.Status != "accepted" {
		t.Fatalf("customer email delivery = %#v", status.CustomerEmail)
	}
	if status.CustomerWhatsApp.Requested || status.CustomerSMS.Requested {
		t.Fatalf("unrequested channels changed = %#v", status)
	}
}
