package notifications

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/appdata"
	"booking/go-server/internal/mailer"
	"booking/go-server/internal/whatsapp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNotificationPlannerLifecycleAndDispatchFence(t *testing.T) {
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
	bookingRepository := appdata.NewRepository(pool)
	var serviceID, clientID uuid.UUID
	var slug string
	if err := pool.QueryRow(ctx, `
		SELECT service.id,service.client_id,profile.handle_slug
		FROM services service
		JOIN client_profiles profile ON profile.client_id=service.client_id
		JOIN clients client ON client.id=service.client_id
		WHERE service.status='published' AND service.fulfillment_mode='provider_location'
		  AND service.agreement_timing IS NULL AND NOT service.standalone_signature_required
		  AND profile.marketplace_enabled AND profile.market_configured_at IS NOT NULL
		  AND client.email_verified_at IS NOT NULL
		ORDER BY service.created_at LIMIT 1
	`).Scan(&serviceID, &clientID, &slug); err != nil {
		t.Fatal(err)
	}
	availability, err := bookingRepository.GetPublicAvailabilityRange(ctx, slug, serviceID, nil, 14)
	if err != nil {
		t.Fatal(err)
	}
	var startsAt time.Time
	for _, day := range availability.Dates {
		for _, slot := range day.Slots {
			candidate, parseErr := time.Parse(time.RFC3339, slot.StartAt)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if candidate.After(time.Now().UTC().Add(26 * time.Hour)) {
				startsAt = candidate
			}
		}
	}
	if startsAt.IsZero() {
		t.Skip("seeded provider has no slot beyond the reminder window")
	}
	email := "notification-planner-" + uuid.NewString() + "@example.com"
	quote, err := bookingRepository.CreatePublicBookingQuote(ctx, slug, appdata.CreatePublicBookingQuoteInput{
		IdempotencyKey: uuid.NewString(), ServiceID: serviceID.String(), StartsAt: startsAt.Format(time.RFC3339),
		CustomerName: "Notification Planner", CustomerEmail: email, CustomerPhone: "+2348012345678",
	})
	if err != nil {
		t.Fatal(err)
	}
	booking, err := bookingRepository.CreatePublicBooking(ctx, slug, appdata.CreatePublicBookingInput{
		QuoteToken: quote.QuoteToken, Source: "direct_public_page", FullName: "Notification Planner",
		Email: email, Phone: "+2348012345678", EmailReminderConsent: true, WhatsAppConsent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	bookingID := uuid.MustParse(booking.BookingID)
	// Isolate provider-scoped preference and replan assertions from the seeded
	// provider that supplied the availability slot.
	clientID = uuid.New()
	providerEmail := "notification-provider-" + uuid.NewString() + "@example.com"
	if _, err := pool.Exec(ctx, `
		INSERT INTO clients (id,full_name,email,password_hash,email_verified_at)
		VALUES ($1,'Notification Provider',$2,'integration-test',NOW())
	`, clientID, providerEmail); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE bookings SET client_id=$2 WHERE id=$1`, bookingID, clientID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM notifications WHERE booking_id=$1`, bookingID)
		_, _ = pool.Exec(ctx, `UPDATE booking_quotes SET booking_id=NULL,consumed_at=NULL WHERE public_token=$1`, quote.QuoteToken)
		_, _ = pool.Exec(ctx, `DELETE FROM bookings WHERE id=$1`, bookingID)
		_, _ = pool.Exec(ctx, `DELETE FROM booking_quotes WHERE public_token=$1`, quote.QuoteToken)
		_, _ = pool.Exec(ctx, `DELETE FROM customers WHERE email=$1`, email)
		_, _ = pool.Exec(ctx, `DELETE FROM clients WHERE id=$1`, clientID)
	})
	planner, err := NewRepository(pool, "notification-planner-integration-key-32-bytes", nil, true, true)
	if err != nil {
		t.Fatal(err)
	}
	// A guest booking needs immutable consent and a verified provider contact
	// explicitly shared with booked customers.
	if _, err := pool.Exec(ctx, `INSERT INTO client_profile_handles(handle_slug,client_id) VALUES($2,$1)`, clientID, "notification-contact-"+clientID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO client_profiles(client_id,business_name,handle_slug,customer_contact_phone,customer_contact_verified_at,allow_booking_contact)
		VALUES($1,'Notification Provider',$2,'+2348012345678',NOW(),TRUE)`, clientID, "notification-contact-"+clientID.String()); err != nil {
		t.Fatal(err)
	}
	planner.enabledTemplates[whatsapp.TemplateUserReminder] = struct{}{}
	if _, err := pool.Exec(ctx, `UPDATE bookings SET payment_status='full_payment_pending',updated_at=NOW() WHERE id=$1`, bookingID); err != nil {
		t.Fatal(err)
	}
	createdEvent := eventForType(t, ctx, pool, bookingID, "booking_created")
	if _, err := pool.Exec(ctx, `
		UPDATE notification_event_jobs
		SET origin='backfill',status='processing',attempt_count=attempt_count+1,
			lease_owner='expired-event-owner',lease_expires_at='1900-01-01',completed_at=NULL
		WHERE booking_event_id=$1
	`, createdEvent.BookingEventID); err != nil {
		t.Fatal(err)
	}
	recoveredEvents, err := planner.ClaimEventJobs(ctx, "recovered-event-owner", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(recoveredEvents) != 1 || recoveredEvents[0].BookingEventID != createdEvent.BookingEventID ||
		recoveredEvents[0].Origin != "backfill" {
		t.Fatalf("stale event lease was not recovered: %#v", recoveredEvents)
	}
	if err := planner.PlanEventJob(ctx, recoveredEvents[0]); err != nil {
		t.Fatal(err)
	}
	var customerReceived, prematureProvider, prematureCustomerSecured int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE notification_type='customer_booking_received'),
		       COUNT(*) FILTER (WHERE notification_type='provider_new_booking'),
		       COUNT(*) FILTER (WHERE notification_type='customer_booking_secured')
		FROM notification_deliveries WHERE booking_id=$1
	`, bookingID).Scan(&customerReceived, &prematureProvider, &prematureCustomerSecured); err != nil {
		t.Fatal(err)
	}
	if customerReceived != 0 || prematureProvider != 0 || prematureCustomerSecured != 0 {
		t.Fatalf("backfill produced historical received/provider/customer-secured deliveries %d/%d/%d", customerReceived, prematureProvider, prematureCustomerSecured)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE notification_event_jobs SET origin='live' WHERE booking_event_id=$1
	`, createdEvent.BookingEventID); err != nil {
		t.Fatal(err)
	}
	planEventDirectly(t, ctx, pool, planner, createdEvent)
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE notification_type='customer_booking_received'),
		       COUNT(*) FILTER (WHERE notification_type='provider_new_booking'),
		       COUNT(*) FILTER (WHERE notification_type='customer_booking_secured')
		FROM notification_deliveries WHERE booking_id=$1
	`, bookingID).Scan(&customerReceived, &prematureProvider, &prematureCustomerSecured); err != nil {
		t.Fatal(err)
	}
	if customerReceived != 1 || prematureProvider != 0 || prematureCustomerSecured != 0 {
		t.Fatalf("unsecured planning produced received/provider/customer-secured deliveries %d/%d/%d", customerReceived, prematureProvider, prematureCustomerSecured)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE bookings SET payment_status='paid_in_full',status='confirmed',updated_at=NOW() WHERE id=$1
	`, bookingID); err != nil {
		t.Fatal(err)
	}
	securedEvent := latestEvent(t, ctx, pool, bookingID)
	planEventConcurrently(t, ctx, pool, planner, securedEvent)
	planEventDirectly(t, ctx, pool, planner, securedEvent)
	var providerNew, customerSecured, reminderCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE notification_type='provider_new_booking'),
		       COUNT(*) FILTER (WHERE notification_type='customer_booking_secured'),
		       COUNT(*) FILTER (WHERE notification_type='appointment_reminder')
		FROM notification_deliveries WHERE booking_id=$1
	`, bookingID).Scan(&providerNew, &customerSecured, &reminderCount); err != nil {
		t.Fatal(err)
	}
	if providerNew != 1 || customerSecured != 1 || reminderCount != 3 {
		t.Fatalf("secured/replayed planning produced provider/customer-secured/reminder deliveries %d/%d/%d", providerNew, customerSecured, reminderCount)
	}
	newStart := startsAt.Add(21 * 24 * time.Hour)
	if _, err := pool.Exec(ctx, `
		UPDATE bookings SET start_at=$2,end_at=end_at+INTERVAL '21 days',
			occupied_start_at=occupied_start_at+INTERVAL '21 days',
			occupied_end_at=occupied_end_at+INTERVAL '21 days',updated_at=NOW()
		WHERE id=$1
	`, bookingID, newStart); err != nil {
		t.Fatal(err)
	}
	rescheduledEvent := latestEvent(t, ctx, pool, bookingID)
	planEventDirectly(t, ctx, pool, planner, rescheduledEvent)
	var activeOld, activeNew, rescheduleCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE notification_type='appointment_reminder'
			AND reminder_occurrence_at=$2 AND status IN ('pending','retry')),
		       COUNT(*) FILTER (WHERE notification_type='appointment_reminder'
			AND reminder_occurrence_at=$3 AND status IN ('pending','retry')),
		       COUNT(*) FILTER (WHERE notification_type='booking_rescheduled')
		FROM notification_deliveries WHERE booking_id=$1
	`, bookingID, startsAt, newStart).Scan(&activeOld, &activeNew, &rescheduleCount); err != nil {
		t.Fatal(err)
	}
	if activeOld != 0 || activeNew != 3 || rescheduleCount != 2 {
		t.Fatalf("reschedule old/new/event deliveries = %d/%d/%d", activeOld, activeNew, rescheduleCount)
	}
	marketplaceCustomerID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO marketplace_customers (id,email,email_verified_at)
		VALUES ($1,$2,NOW())
	`, marketplaceCustomerID, email); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=$1`, marketplaceCustomerID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO marketplace_notification_preferences (
			marketplace_customer_id,booking_email,booking_whatsapp,updated_at
		) VALUES ($1,TRUE,FALSE,NOW())
	`, marketplaceCustomerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE bookings SET marketplace_customer_id=$1 WHERE id=$2
	`, marketplaceCustomerID, bookingID); err != nil {
		t.Fatal(err)
	}
	var customerWhatsAppReminderID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM notification_deliveries
		WHERE booking_id=$1 AND audience_type='customer' AND channel='whatsapp'
		  AND notification_type='appointment_reminder' AND status='pending'
	`, bookingID).Scan(&customerWhatsAppReminderID); err != nil {
		t.Fatal(err)
	}
	customerPreferenceOwner := "customer-preference-dispatch-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		UPDATE notification_deliveries SET status='processing',lease_owner=$2,
			lease_expires_at=NOW()+INTERVAL '1 minute' WHERE id=$1
	`, customerWhatsAppReminderID, customerPreferenceOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.AuthorizeDispatch(ctx, Delivery{
		ID: customerWhatsAppReminderID, LeaseOwner: customerPreferenceOwner,
	}); !errors.Is(err, ErrDispatchNotAuthorized) {
		t.Fatalf("later customer opt-out dispatch error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE marketplace_notification_preferences
		SET booking_whatsapp=TRUE,updated_at=NOW() WHERE marketplace_customer_id=$1
	`, marketplaceCustomerID); err != nil {
		t.Fatal(err)
	}
	planEventDirectly(t, ctx, pool, planner, rescheduledEvent)
	var restoredCustomerWhatsApp int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notification_deliveries
		WHERE id=$1 AND status='pending'
	`, customerWhatsAppReminderID).Scan(&restoredCustomerWhatsApp); err != nil || restoredCustomerWhatsApp != 1 {
		t.Fatalf("customer WhatsApp reminder was not restored after explicit opt-in: count=%d err=%v", restoredCustomerWhatsApp, err)
	}
	var inAppOld, inAppNew int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE reminder_occurrence_at=$2 AND status IN ('pending','retry')),
		       COUNT(*) FILTER (WHERE reminder_occurrence_at=$3 AND status IN ('pending','retry'))
		FROM notification_in_app_jobs WHERE booking_id=$1 AND audience_type='provider'
	`, bookingID, startsAt, newStart).Scan(&inAppOld, &inAppNew); err != nil {
		t.Fatal(err)
	}
	if inAppOld != 0 || inAppNew != 1 {
		t.Fatalf("rescheduled in-app reminder old/new = %d/%d", inAppOld, inAppNew)
	}

	var providerReminderID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM notification_deliveries
		WHERE booking_id=$1 AND audience_type='provider' AND channel='email'
		  AND notification_type='appointment_reminder' AND status='pending'
	`, bookingID).Scan(&providerReminderID); err != nil {
		t.Fatal(err)
	}
	preferenceOwner := "preference-dispatch-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		UPDATE notification_deliveries SET status='processing',lease_owner=$2,
			lease_expires_at=NOW()+INTERVAL '1 minute' WHERE id=$1
	`, providerReminderID, preferenceOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO provider_notification_preferences (client_id,booking_email,preference_revision)
		VALUES ($1,FALSE,2)
	`, clientID); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.AuthorizeDispatch(ctx, Delivery{
		ID: providerReminderID, LeaseOwner: preferenceOwner,
	}); !errors.Is(err, ErrDispatchNotAuthorized) {
		t.Fatalf("disabled provider preference dispatch error = %v", err)
	}
	var preferenceStatus, preferenceCode string
	if err := pool.QueryRow(ctx, `
		SELECT status,last_error_code FROM notification_deliveries WHERE id=$1
	`, providerReminderID).Scan(&preferenceStatus, &preferenceCode); err != nil {
		t.Fatal(err)
	}
	if preferenceStatus != "cancelled" || preferenceCode != "preference_or_destination_changed" {
		t.Fatalf("disabled preference fence state = %q/%q", preferenceStatus, preferenceCode)
	}
	planScopeDirectly(t, ctx, pool, planner, ScopeJob{ClientID: clientID, PreferenceRevision: 2})
	if _, err := pool.Exec(ctx, `
		UPDATE provider_notification_preferences
		SET booking_email=TRUE,preference_revision=3,updated_at=NOW() WHERE client_id=$1
	`, clientID); err != nil {
		t.Fatal(err)
	}
	planScopeDirectly(t, ctx, pool, planner, ScopeJob{ClientID: clientID, PreferenceRevision: 3})
	if err := pool.QueryRow(ctx, `
		SELECT id FROM notification_deliveries
		WHERE booking_id=$1 AND audience_type='provider' AND channel='email'
		  AND notification_type='appointment_reminder' AND status='pending'
	`, bookingID).Scan(&providerReminderID); err != nil {
		t.Fatalf("provider reminder was not restored by scope replan: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE notification_deliveries
		SET status='processing',attempt_count=attempt_count+1,lease_owner='expired-delivery-owner',
			lease_expires_at='1900-01-01',scheduled_for='1900-01-01',next_attempt_at='1900-01-01'
		WHERE id=$1
	`, providerReminderID); err != nil {
		t.Fatal(err)
	}
	recoveredDeliveries, err := planner.ClaimDeliveries(ctx, "email", "recovered-delivery-owner", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(recoveredDeliveries) != 1 || recoveredDeliveries[0].ID != providerReminderID {
		t.Fatalf("stale external delivery lease was not recovered: %#v", recoveredDeliveries)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE notification_deliveries
		SET status='pending',lease_owner='',lease_expires_at=NULL,
			scheduled_for=$2,next_attempt_at=$2 WHERE id=$1
	`, providerReminderID, newStart.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	var inAppID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM notification_in_app_jobs
		WHERE booking_id=$1 AND audience_type='provider' AND status='pending'
	`, bookingID).Scan(&inAppID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE notification_in_app_jobs
		SET status='processing',attempt_count=attempt_count+1,lease_owner='expired-owner',
			lease_expires_at=NOW()-INTERVAL '1 second',scheduled_for=NOW(),next_attempt_at=NOW()
		WHERE id=$1
	`, inAppID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE notification_in_app_jobs SET scheduled_for='1900-01-01',next_attempt_at='1900-01-01'
		WHERE id=$1
	`, inAppID); err != nil {
		t.Fatal(err)
	}
	claimedInApp, err := planner.ClaimInAppJobs(ctx, "recovery-owner", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimedInApp) != 1 || claimedInApp[0].ID != inAppID {
		t.Fatalf("stale in-app lease was not recovered: %#v", claimedInApp)
	}
	if err := planner.CompleteInAppJob(ctx, claimedInApp[0]); err != nil {
		t.Fatal(err)
	}
	var inAppNotificationCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notifications
		WHERE booking_id=$1 AND type='appointment_reminder'
	`, bookingID).Scan(&inAppNotificationCount); err != nil || inAppNotificationCount != 1 {
		t.Fatalf("in-app reminder notifications=%d err=%v", inAppNotificationCount, err)
	}
	var rescheduleDeliveryID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM notification_deliveries
		WHERE booking_id=$1 AND audience_type='provider' AND channel='email'
		  AND notification_type='booking_rescheduled' AND status='pending'
	`, bookingID).Scan(&rescheduleDeliveryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO booking_domain_events (id,client_id,booking_id,event_type,dedupe_key,payload)
		VALUES ($1,$2,$3,'booking_updated',$4,'{}'::jsonb)
	`, uuid.New(), clientID, bookingID, "notification-unrelated:"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	rescheduleOwner := "reschedule-dispatch-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		UPDATE notification_deliveries SET status='processing',lease_owner=$2,
			lease_expires_at=NOW()+INTERVAL '1 minute' WHERE id=$1
	`, rescheduleDeliveryID, rescheduleOwner); err != nil {
		t.Fatal(err)
	}
	authorizedReschedule, err := planner.AuthorizeDispatch(ctx, Delivery{ID: rescheduleDeliveryID, LeaseOwner: rescheduleOwner})
	if err != nil {
		t.Fatalf("unrelated later event rejected a still-current reschedule: %v", err)
	}
	providerMessage, err := planner.BuildEmailMessage(
		ctx, authorizedReschedule, "https://client.tellbook.test", "https://market.tellbook.test",
	)
	if err != nil {
		t.Fatal(err)
	}
	if providerMessage.ToEmail != providerEmail || !strings.Contains(providerMessage.HTML, "https://client.tellbook.test/bookings?booking=") ||
		providerMessage.MessageID != "<notification-"+rescheduleDeliveryID.String()+"@mail.tellbook.app>" {
		t.Fatalf("unexpected provider email message: %#v", providerMessage)
	}
	if err := planner.MarkEmailAccepted(ctx, rescheduleDeliveryID); err != nil {
		t.Fatal(err)
	}
	var acceptedStatus string
	var acceptedAt, acceptedCompletedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT status,accepted_at,completed_at FROM notification_deliveries WHERE id=$1
	`, rescheduleDeliveryID).Scan(&acceptedStatus, &acceptedAt, &acceptedCompletedAt); err != nil {
		t.Fatal(err)
	}
	if acceptedStatus != "accepted" || acceptedAt == nil || acceptedCompletedAt == nil {
		t.Fatalf("accepted email state = %q/%v/%v", acceptedStatus, acceptedAt, acceptedCompletedAt)
	}

	var customerRescheduleID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM notification_deliveries
		WHERE booking_id=$1 AND audience_type='customer' AND channel='email'
		  AND notification_type='booking_rescheduled' AND status='pending'
	`, bookingID).Scan(&customerRescheduleID); err != nil {
		t.Fatal(err)
	}
	customerRescheduleOwner := "customer-reschedule-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		UPDATE notification_deliveries SET status='processing',lease_owner=$2,
			lease_expires_at=NOW()+INTERVAL '1 minute' WHERE id=$1
	`, customerRescheduleID, customerRescheduleOwner); err != nil {
		t.Fatal(err)
	}
	authorizedCustomerReschedule, err := planner.AuthorizeDispatch(ctx, Delivery{
		ID: customerRescheduleID, LeaseOwner: customerRescheduleOwner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := planner.RecordEmailFailure(
		ctx, authorizedCustomerReschedule, mailer.DispositionRetryable, false, "smtp_transient",
	); err != nil {
		t.Fatal(err)
	}
	var retryStatus string
	var retryAuthorization *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT status,dispatch_authorized_at FROM notification_deliveries WHERE id=$1
	`, customerRescheduleID).Scan(&retryStatus, &retryAuthorization); err != nil {
		t.Fatal(err)
	}
	if retryStatus != "retry" || retryAuthorization != nil {
		t.Fatalf("retryable email state = %q auth=%v", retryStatus, retryAuthorization)
	}
	ambiguousOwner := "customer-reschedule-ambiguous-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		UPDATE notification_deliveries SET status='processing',lease_owner=$2,
			lease_expires_at=NOW()+INTERVAL '1 minute' WHERE id=$1
	`, customerRescheduleID, ambiguousOwner); err != nil {
		t.Fatal(err)
	}
	authorizedCustomerReschedule, err = planner.AuthorizeDispatch(ctx, Delivery{
		ID: customerRescheduleID, LeaseOwner: ambiguousOwner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := planner.RecordEmailFailure(
		ctx, authorizedCustomerReschedule, mailer.DispositionAmbiguous, false, "smtp_outcome_unknown",
	); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT status FROM notification_deliveries WHERE id=$1
	`, customerRescheduleID).Scan(&retryStatus); err != nil || retryStatus != "manual_review" {
		t.Fatalf("ambiguous email state = %q err=%v", retryStatus, err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT id FROM notification_deliveries
		WHERE booking_id=$1 AND audience_type='provider' AND channel='email'
		  AND notification_type='appointment_reminder' AND status='pending'
	`, bookingID).Scan(&providerReminderID); err != nil {
		t.Fatal(err)
	}
	reminderOwner := "cancel-dispatch-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		UPDATE notification_deliveries SET status='processing',lease_owner=$2,
			lease_expires_at=NOW()+INTERVAL '1 minute' WHERE id=$1
	`, providerReminderID, reminderOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE bookings SET status='cancelled',updated_at=NOW() WHERE id=$1`, bookingID); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.AuthorizeDispatch(ctx, Delivery{ID: providerReminderID, LeaseOwner: reminderOwner}); !errors.Is(err, ErrDispatchNotAuthorized) {
		t.Fatalf("cancelled booking reminder dispatch error = %v", err)
	}
	var cancelledReminderStatus, cancelledReminderCode string
	if err := pool.QueryRow(ctx, `
		SELECT status,last_error_code FROM notification_deliveries WHERE id=$1
	`, providerReminderID).Scan(&cancelledReminderStatus, &cancelledReminderCode); err != nil {
		t.Fatal(err)
	}
	if cancelledReminderStatus != "cancelled" || cancelledReminderCode != "reminder_no_longer_eligible" {
		t.Fatalf("cancelled reminder fence state = %q/%q", cancelledReminderStatus, cancelledReminderCode)
	}
	cancelledEvent := latestEvent(t, ctx, pool, bookingID)
	planEventDirectly(t, ctx, pool, planner, cancelledEvent)
	var cancellationCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notification_deliveries
		WHERE booking_id=$1 AND notification_type='booking_cancelled'
	`, bookingID).Scan(&cancellationCount); err != nil {
		t.Fatal(err)
	}
	if cancellationCount != 2 {
		t.Fatalf("cancellation delivery count = %d, want provider and customer email", cancellationCount)
	}
	var providerCancellationID uuid.UUID
	var providerCancellationHMAC []byte
	if err := pool.QueryRow(ctx, `
		SELECT id,destination_hmac FROM notification_deliveries
		WHERE booking_id=$1 AND audience_type='provider' AND channel='email'
		  AND notification_type='booking_cancelled' AND status='pending'
	`, bookingID).Scan(&providerCancellationID, &providerCancellationHMAC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM notification_contact_suppressions WHERE channel='email' AND destination_hmac=$1`, providerCancellationHMAC)
	})
	providerCancellationOwner := "provider-cancellation-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		UPDATE notification_deliveries SET status='processing',lease_owner=$2,
			lease_expires_at=NOW()+INTERVAL '1 minute' WHERE id=$1
	`, providerCancellationID, providerCancellationOwner); err != nil {
		t.Fatal(err)
	}
	authorizedCancellation, err := planner.AuthorizeDispatch(ctx, Delivery{
		ID: providerCancellationID, LeaseOwner: providerCancellationOwner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := planner.RecordEmailFailure(
		ctx, authorizedCancellation, mailer.DispositionPermanent, true, "smtp_permanent",
	); err != nil {
		t.Fatal(err)
	}
	var permanentStatus string
	var providerSuppressed bool
	if err := pool.QueryRow(ctx, `
		SELECT delivery.status,EXISTS (
			SELECT 1 FROM notification_contact_suppressions suppression
			WHERE suppression.channel='email' AND suppression.destination_hmac=$2
		) FROM notification_deliveries delivery WHERE delivery.id=$1
	`, providerCancellationID, providerCancellationHMAC).Scan(&permanentStatus, &providerSuppressed); err != nil {
		t.Fatal(err)
	}
	if permanentStatus != "failed" || !providerSuppressed {
		t.Fatalf("permanent email state = %q suppressed=%t", permanentStatus, providerSuppressed)
	}
	var reminderID uuid.UUID
	var reminderHMAC []byte
	if err := pool.QueryRow(ctx, `
		SELECT id,destination_hmac FROM notification_deliveries
		WHERE booking_id=$1 AND audience_type='customer' AND channel='email'
		  AND notification_type='booking_cancelled' AND status='pending'
		ORDER BY created_at DESC LIMIT 1
	`, bookingID).Scan(&reminderID, &reminderHMAC); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO notification_contact_suppressions(channel,destination_hmac,reason,source)
		VALUES ('email',$1,'manual','admin')
		ON CONFLICT (channel,destination_hmac) DO UPDATE SET reason='manual',source='admin'
	`, reminderHMAC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM notification_contact_suppressions WHERE channel='email' AND destination_hmac=$1`, reminderHMAC)
	})
	owner := "dispatch-test-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		UPDATE notification_deliveries SET status='processing',lease_owner=$2,
			lease_expires_at=NOW()+INTERVAL '1 minute' WHERE id=$1
	`, reminderID, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.AuthorizeDispatch(ctx, Delivery{ID: reminderID, LeaseOwner: owner}); !errors.Is(err, ErrDispatchNotAuthorized) {
		t.Fatalf("suppressed dispatch error = %v", err)
	}
	var suppressedStatus, suppressionCode string
	if err := pool.QueryRow(ctx, `SELECT status,last_error_code FROM notification_deliveries WHERE id=$1`, reminderID).Scan(&suppressedStatus, &suppressionCode); err != nil {
		t.Fatal(err)
	}
	if suppressedStatus != "cancelled" || suppressionCode != "destination_suppressed" {
		t.Fatalf("suppressed dispatch state = %q/%q", suppressedStatus, suppressionCode)
	}
}

func eventForType(t *testing.T, ctx context.Context, pool *pgxpool.Pool, bookingID uuid.UUID, eventType string) EventJob {
	t.Helper()
	var job EventJob
	if err := pool.QueryRow(ctx, `
		SELECT id,sequence FROM booking_domain_events
		WHERE booking_id=$1 AND event_type=$2 ORDER BY sequence LIMIT 1
	`, bookingID, eventType).Scan(&job.BookingEventID, &job.EventSequence); err != nil {
		t.Fatal(err)
	}
	return job
}

func latestEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, bookingID uuid.UUID) EventJob {
	t.Helper()
	var job EventJob
	if err := pool.QueryRow(ctx, `
		SELECT id,sequence FROM booking_domain_events WHERE booking_id=$1 ORDER BY sequence DESC LIMIT 1
	`, bookingID).Scan(&job.BookingEventID, &job.EventSequence); err != nil {
		t.Fatal(err)
	}
	return job
}

func planEventDirectly(t *testing.T, ctx context.Context, pool *pgxpool.Pool, planner *Repository, job EventJob) {
	t.Helper()
	job.LeaseOwner = "planner-test-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		UPDATE notification_event_jobs SET status='processing',attempt_count=attempt_count+1,
			lease_owner=$2,lease_expires_at=NOW()+INTERVAL '1 minute',completed_at=NULL
		WHERE booking_event_id=$1
	`, job.BookingEventID, job.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	if err := planner.PlanEventJob(ctx, job); err != nil {
		t.Fatalf("plan event %s: %v", job.BookingEventID, err)
	}
}

func planScopeDirectly(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	planner *Repository,
	job ScopeJob,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO notification_scope_replan_jobs (
			client_id,preference_revision,status,attempt_count,next_attempt_at,
			lease_owner,lease_expires_at
		) VALUES ($1,$2,'processing',1,NOW(),'expired-scope-owner','1900-01-01')
		ON CONFLICT (client_id,preference_revision) DO UPDATE SET
			status='processing',attempt_count=notification_scope_replan_jobs.attempt_count+1,
			lease_owner='expired-scope-owner',lease_expires_at='1900-01-01',
			completed_at=NULL
	`, job.ClientID, job.PreferenceRevision); err != nil {
		t.Fatal(err)
	}
	claimed, err := planner.ClaimScopeJobs(ctx, "recovered-scope-owner", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ClientID != job.ClientID ||
		claimed[0].PreferenceRevision != job.PreferenceRevision {
		t.Fatalf("stale scope lease was not recovered: %#v", claimed)
	}
	if err := planner.PlanScopeJob(ctx, claimed[0]); err != nil {
		t.Fatalf("plan scope %s/%d: %v", job.ClientID, job.PreferenceRevision, err)
	}
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT status FROM notification_scope_replan_jobs
		WHERE client_id=$1 AND preference_revision=$2
	`, job.ClientID, job.PreferenceRevision).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("scope replan status = %q", status)
	}
}

func planEventConcurrently(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	planner *Repository,
	job EventJob,
) {
	t.Helper()
	job.LeaseOwner = "concurrent-planner-test-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		UPDATE notification_event_jobs SET status='processing',attempt_count=attempt_count+1,
			lease_owner=$2,lease_expires_at=NOW()+INTERVAL '1 minute',completed_at=NULL
		WHERE booking_event_id=$1
	`, job.BookingEventID, job.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- planner.PlanEventJob(ctx, job) }()
	}
	var succeeded, leaseLost int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case strings.Contains(err.Error(), "lease was lost"):
			leaseLost++
		default:
			t.Fatalf("concurrent planner error = %v", err)
		}
	}
	if succeeded != 1 || leaseLost != 1 {
		t.Fatalf("concurrent planner results succeeded/lost = %d/%d", succeeded, leaseLost)
	}
}
