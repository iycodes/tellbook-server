package notifications

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"booking/go-server/internal/whatsapp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type whatsAppSenderStub struct {
	result   whatsapp.SendResult
	err      error
	messages []whatsapp.TemplateMessage
}

func (sender *whatsAppSenderStub) SendTemplate(_ context.Context, message whatsapp.TemplateMessage) (whatsapp.SendResult, error) {
	sender.messages = append(sender.messages, message)
	return sender.result, sender.err
}

func TestWhatsAppOutboundWorkerPersistsSendOutcomes(t *testing.T) {
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

	clientID, bookingID, eventID, eventSequence := insertWhatsAppOutboundFixture(t, ctx, pool)
	repository, err := NewRepository(
		pool, "whatsapp-outbound-integration-key-32-bytes",
		[]string{string(whatsapp.TemplateProviderNewBooking)}, false, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	destination := "+2348098765432"
	tests := []struct {
		name           string
		sender         *whatsAppSenderStub
		wantStatus     string
		wantProviderID string
	}{
		{
			name: "accepted", sender: &whatsAppSenderStub{result: whatsapp.SendResult{MessageID: "wamid.outbound.accepted"}},
			wantStatus: "accepted", wantProviderID: "wamid.outbound.accepted",
		},
		{
			name: "preflight permanent", sender: &whatsAppSenderStub{err: &whatsapp.RequestError{Cause: errors.New("invalid template")}},
			wantStatus: "failed",
		},
		{
			name: "ambiguous", sender: &whatsAppSenderStub{err: &whatsapp.TransportError{Cause: errors.New("connection reset"), Ambiguous: true}},
			wantStatus: "unknown",
		},
		{
			name: "rate limited", sender: &whatsAppSenderStub{err: &whatsapp.GraphError{Class: whatsapp.ErrorClassRateLimit, Code: 4, RetryAfter: time.Minute}},
			wantStatus: "retry",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deliveryID := uuid.New()
			leaseOwner := "whatsapp-outbound-" + deliveryID.String()
			if _, err := pool.Exec(ctx, `
				INSERT INTO notification_deliveries (
					id,idempotency_key,client_id,booking_id,booking_event_id,booking_event_sequence,
					audience_type,channel,notification_type,template_key,scheduled_for,destination_hmac,
					preference_revision,status,attempt_count,next_attempt_at,lease_owner,lease_expires_at
				) VALUES ($1,$2,$3,$4,$5,$6,'provider','whatsapp','provider_new_booking',
					'provider_new_booking',NOW(),$7,1,'processing',1,NOW(),$8,NOW()+INTERVAL '1 minute')
			`, deliveryID, "whatsapp-outbound:"+deliveryID.String(), clientID, bookingID, eventID,
				eventSequence, repository.destinationFingerprint("whatsapp", destination), leaseOwner); err != nil {
				t.Fatal(err)
			}
			worker := NewWhatsAppWorker(repository, test.sender, nil, nil, nil, 1, time.Second, true)
			worker.processDelivery(ctx, Delivery{
				ID: deliveryID, LeaseOwner: leaseOwner, ScheduledFor: time.Now(),
				TemplateKey: string(whatsapp.TemplateProviderNewBooking),
			})
			if len(test.sender.messages) != 1 {
				t.Fatalf("sender calls = %d", len(test.sender.messages))
			}
			message := test.sender.messages[0]
			if message.To != destination || message.Key != whatsapp.TemplateProviderNewBooking ||
				message.Values.OpaqueCallbackData != deliveryID.String() ||
				message.Values.Button["booking_route_suffix"] != "?booking="+bookingID.String() {
				t.Fatalf("rendered message = %#v", message)
			}
			var status, providerMessageID string
			var acceptedAt, reconcileAfter *time.Time
			if err := pool.QueryRow(ctx, `
				SELECT status,provider_message_id,accepted_at,reconcile_after
				FROM notification_deliveries WHERE id=$1
			`, deliveryID).Scan(&status, &providerMessageID, &acceptedAt, &reconcileAfter); err != nil {
				t.Fatal(err)
			}
			if status != test.wantStatus || providerMessageID != test.wantProviderID {
				t.Fatalf("delivery status/message=%q/%q want %q/%q", status, providerMessageID, test.wantStatus, test.wantProviderID)
			}
			if status == "accepted" && acceptedAt == nil {
				t.Fatal("accepted delivery has no accepted_at")
			}
			if status == "unknown" && reconcileAfter == nil {
				t.Fatal("ambiguous delivery has no reconciliation deadline")
			}
		})
	}
}

func insertWhatsAppOutboundFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) (uuid.UUID, uuid.UUID, uuid.UUID, int64) {
	t.Helper()
	clientID, customerID, bookingID, eventID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	email := "whatsapp-outbound-" + clientID.String() + "@example.com"
	if _, err := pool.Exec(ctx, `
		INSERT INTO clients (id,full_name,email,password_hash,email_verified_at)
		VALUES ($1,'Outbound Provider',$2,'integration-test',NOW())
	`, clientID, email); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM clients WHERE id=$1`, clientID) })
	if _, err := pool.Exec(ctx, `
		INSERT INTO customers (id,client_id,full_name,email,phone)
		VALUES ($2,$1,'Outbound Customer','customer@example.com','+2348012345678')
	`, clientID, customerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO provider_notification_preferences (
			client_id,booking_whatsapp,whatsapp_e164,whatsapp_verified_at,
			whatsapp_verification_method,whatsapp_verification_revision,preference_revision
		) VALUES ($1,TRUE,'+2348098765432',NOW(),'inbound_challenge',1,1)
	`, clientID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bookings (
			id,client_id,customer_id,title,stylist_name,source,status,payment_status,agreement_status,
			start_at,end_at,base_service_amount_minor,total_amount_minor,currency_code,duration_minutes,
			location_label,original_amount_minor,discounted_service_amount_minor,country_code,
			occupied_start_at,occupied_end_at
		) VALUES (
			$3,$1,$2,'Hair styling','Outbound Provider','direct_public_page','confirmed','paid_in_full','not_required',
			NOW()+INTERVAL '2 days',NOW()+INTERVAL '2 days 1 hour',1000000,1000000,'NGN',60,
			'12 Test Street',1000000,1000000,'NG',NOW()+INTERVAL '2 days',NOW()+INTERVAL '2 days 1 hour'
		)
	`, clientID, customerID, bookingID); err != nil {
		t.Fatal(err)
	}
	var sequence int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO booking_domain_events (id,client_id,booking_id,event_type,dedupe_key,payload)
		VALUES ($1,$2,$3,'booking_confirmed',$4,jsonb_build_object(
			'status','confirmed','payment_status','paid_in_full','agreement_status','not_required',
			'starts_at',NOW()+INTERVAL '2 days')) RETURNING sequence
	`, eventID, clientID, bookingID, "whatsapp-outbound-event:"+eventID.String()).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	return clientID, bookingID, eventID, sequence
}
