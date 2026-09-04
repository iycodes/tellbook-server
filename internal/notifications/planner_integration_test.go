package notifications

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"booking/go-server/internal/appdata"

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
		CustomerName: "Notification Planner", CustomerEmail: email, CustomerPhone: "+15555550400",
	})
	if err != nil {
		t.Fatal(err)
	}
	booking, err := bookingRepository.CreatePublicBooking(ctx, slug, appdata.CreatePublicBookingInput{
		QuoteToken: quote.QuoteToken, Source: "direct_public_page", FullName: "Notification Planner",
		Email: email, Phone: "+15555550400", EmailReminderConsent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	bookingID := uuid.MustParse(booking.BookingID)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM notifications WHERE booking_id=$1`, bookingID)
		_, _ = pool.Exec(ctx, `UPDATE booking_quotes SET booking_id=NULL,consumed_at=NULL WHERE public_token=$1`, quote.QuoteToken)
		_, _ = pool.Exec(ctx, `DELETE FROM bookings WHERE id=$1`, bookingID)
		_, _ = pool.Exec(ctx, `DELETE FROM booking_quotes WHERE public_token=$1`, quote.QuoteToken)
		_, _ = pool.Exec(ctx, `DELETE FROM customers WHERE email=$1`, email)
	})
	planner, err := NewRepository(pool, "notification-planner-integration-key-32-bytes", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE bookings SET payment_status='full_payment_pending',updated_at=NOW() WHERE id=$1`, bookingID); err != nil {
		t.Fatal(err)
	}
	createdEvent := eventForType(t, ctx, pool, bookingID, "booking_created")
	planEventDirectly(t, ctx, pool, planner, createdEvent)
	var customerReceived, prematureProvider int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE notification_type='customer_booking_received'),
		       COUNT(*) FILTER (WHERE notification_type='provider_new_booking')
		FROM notification_deliveries WHERE booking_id=$1
	`, bookingID).Scan(&customerReceived, &prematureProvider); err != nil {
		t.Fatal(err)
	}
	if customerReceived != 1 || prematureProvider != 0 {
		t.Fatalf("unsecured planning produced received/provider deliveries %d/%d", customerReceived, prematureProvider)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE bookings SET payment_status='paid_in_full',status='confirmed',updated_at=NOW() WHERE id=$1
	`, bookingID); err != nil {
		t.Fatal(err)
	}
	securedEvent := latestEvent(t, ctx, pool, bookingID)
	planEventDirectly(t, ctx, pool, planner, securedEvent)
	planEventDirectly(t, ctx, pool, planner, securedEvent)
	var providerNew, reminderCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE notification_type='provider_new_booking'),
		       COUNT(*) FILTER (WHERE notification_type='appointment_reminder')
		FROM notification_deliveries WHERE booking_id=$1
	`, bookingID).Scan(&providerNew, &reminderCount); err != nil {
		t.Fatal(err)
	}
	if providerNew != 1 || reminderCount != 2 {
		t.Fatalf("secured/replayed planning produced provider/reminder deliveries %d/%d", providerNew, reminderCount)
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
	if activeOld != 0 || activeNew != 2 || rescheduleCount != 2 {
		t.Fatalf("reschedule old/new/event deliveries = %d/%d/%d", activeOld, activeNew, rescheduleCount)
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
	claimedInApp, err := planner.ClaimInAppJobs(ctx, "recovery-owner", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var recovered *InAppJob
	for index := range claimedInApp {
		if claimedInApp[index].ID == inAppID {
			recovered = &claimedInApp[index]
			break
		}
	}
	if recovered == nil || recovered.LeaseOwner != "recovery-owner" {
		t.Fatalf("stale in-app lease was not recovered: %#v", claimedInApp)
	}
	if err := planner.CompleteInAppJob(ctx, *recovered); err != nil {
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
	if _, err := planner.AuthorizeDispatch(ctx, Delivery{ID: rescheduleDeliveryID, LeaseOwner: rescheduleOwner}); err != nil {
		t.Fatalf("unrelated later event rejected a still-current reschedule: %v", err)
	}
	var providerReminderID uuid.UUID
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
