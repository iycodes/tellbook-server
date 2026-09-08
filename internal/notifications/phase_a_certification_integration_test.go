package notifications

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"booking/go-server/internal/mailer"
	"booking/go-server/internal/whatsapp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type certificationEmailSender struct {
	err   error
	delay time.Duration
	mu    sync.Mutex
	calls map[string]int
}

func (sender *certificationEmailSender) Enabled() bool { return true }

func (sender *certificationEmailSender) Send(ctx context.Context, message mailer.Message) error {
	if sender.delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sender.delay):
		}
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.calls == nil {
		sender.calls = make(map[string]int)
	}
	sender.calls[message.MessageID]++
	return sender.err
}

func (sender *certificationEmailSender) assertExactlyOnce(t *testing.T, want int) {
	t.Helper()
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.calls) != want {
		t.Fatalf("email sender received %d unique messages, want %d", len(sender.calls), want)
	}
	for messageID, count := range sender.calls {
		if count != 1 {
			t.Fatalf("email message %s was attempted %d times", messageID, count)
		}
	}
}

type certificationWhatsAppSender struct {
	err   error
	delay time.Duration
	mu    sync.Mutex
	calls map[string]int
}

func (sender *certificationWhatsAppSender) SendTemplate(
	ctx context.Context,
	message whatsapp.TemplateMessage,
) (whatsapp.SendResult, error) {
	if sender.delay > 0 {
		select {
		case <-ctx.Done():
			return whatsapp.SendResult{}, ctx.Err()
		case <-time.After(sender.delay):
		}
	}
	deliveryID := message.Values.OpaqueCallbackData
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.calls == nil {
		sender.calls = make(map[string]int)
	}
	sender.calls[deliveryID]++
	if sender.err != nil {
		return whatsapp.SendResult{}, sender.err
	}
	return whatsapp.SendResult{MessageID: "wamid.certification." + deliveryID}, nil
}

func (sender *certificationWhatsAppSender) assertExactlyOnce(t *testing.T, want int) {
	t.Helper()
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.calls) != want {
		t.Fatalf("WhatsApp sender received %d unique messages, want %d", len(sender.calls), want)
	}
	for deliveryID, count := range sender.calls {
		if count != 1 {
			t.Fatalf("WhatsApp delivery %s was attempted %d times", deliveryID, count)
		}
	}
}

func TestNotificationPhaseAWorkerLoadAndFailureIsolation(t *testing.T) {
	if os.Getenv("RUN_NOTIFICATION_PHASE_A_CERTIFICATION") != "true" {
		t.Skip("RUN_NOTIFICATION_PHASE_A_CERTIFICATION is not true")
	}
	if os.Getenv("NOTIFICATION_CERTIFICATION_ALLOW_WRITES") != "true" {
		t.Fatal("NOTIFICATION_CERTIFICATION_ALLOW_WRITES=true is required")
	}
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required")
	}
	volume := certificationVolume(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	assertEmptyNotificationCertificationQueues(t, ctx, pool)

	clientID, bookingID, _, _ := insertWhatsAppOutboundFixture(t, ctx, pool)
	repository, err := NewRepository(
		pool,
		"notification-phase-a-certification-key-32-bytes",
		[]string{string(whatsapp.TemplateProviderNewBooking)},
		true,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}

	eventIDs := insertCertificationEvents(t, ctx, pool, clientID, bookingID, volume)
	planningStarted := time.Now()
	var planners sync.WaitGroup
	for range 4 {
		planners.Add(1)
		go func() {
			defer planners.Done()
			NewPlannerWorker(repository, nil, nil, 8).drain(ctx)
		}()
	}
	planners.Wait()
	plannerDuration := time.Since(planningStarted)
	assertCertificationEventStatus(t, ctx, pool, eventIDs, "completed", len(eventIDs))
	assertNoRunnableCertificationEvents(t, ctx, pool, eventIDs)

	var beforeBackfill int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notification_deliveries WHERE booking_id=$1
	`, bookingID).Scan(&beforeBackfill); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE notification_deliveries
		SET status='accepted',accepted_at=NOW(),completed_at=CASE WHEN channel='email' THEN NOW() ELSE completed_at END
		WHERE booking_id=$1 AND status IN ('pending','retry') AND notification_type<>'appointment_reminder'
	`, bookingID); err != nil {
		t.Fatal(err)
	}
	var acceptedLifecycleBefore int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notification_deliveries
		WHERE booking_id=$1 AND notification_type<>'appointment_reminder' AND status='accepted'
	`, bookingID).Scan(&acceptedLifecycleBefore); err != nil {
		t.Fatal(err)
	}
	if acceptedLifecycleBefore == 0 {
		t.Fatal("certification fixture has no accepted lifecycle delivery to protect during backfill")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE notification_event_jobs SET origin='backfill',status='pending',attempt_count=0,
			next_attempt_at=NOW(),lease_owner='',lease_expires_at=NULL,completed_at=NULL,last_error_code=''
		WHERE booking_event_id=ANY($1::uuid[])
	`, eventIDs); err != nil {
		t.Fatal(err)
	}
	NewPlannerWorker(repository, nil, nil, 8).drain(ctx)
	assertCertificationEventStatus(t, ctx, pool, eventIDs, "completed", len(eventIDs))
	var afterBackfill, customerBackfillDeliveries, acceptedLifecycleAfter int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*),COUNT(*) FILTER (WHERE audience_type='customer'),
		       COUNT(*) FILTER (WHERE notification_type<>'appointment_reminder' AND status='accepted')
		FROM notification_deliveries WHERE booking_id=$1
	`, bookingID).Scan(&afterBackfill, &customerBackfillDeliveries, &acceptedLifecycleAfter); err != nil {
		t.Fatal(err)
	}
	if afterBackfill != beforeBackfill || customerBackfillDeliveries != 0 ||
		acceptedLifecycleAfter != acceptedLifecycleBefore {
		t.Fatalf(
			"backfill count=%d->%d customer=%d accepted_lifecycle=%d->%d",
			beforeBackfill, afterBackfill, customerBackfillDeliveries,
			acceptedLifecycleBefore, acceptedLifecycleAfter,
		)
	}

	latest := latestEvent(t, ctx, pool, bookingID)
	waveOne := "notification-cert-email-degraded-" + uuid.NewString()
	insertCertificationDeliveries(t, ctx, pool, repository, waveOne, clientID, bookingID, latest, volume)
	emailDegraded := &certificationEmailSender{
		err: &mailer.TransportError{
			Disposition: mailer.DispositionRetryable,
			Stage:       "connect",
			Cause:       errors.New("certification SMTP unavailable"),
		},
		delay: time.Millisecond,
	}
	whatsAppHealthy := &certificationWhatsAppSender{delay: time.Millisecond}
	runCertificationDeliveryWave(ctx, repository, emailDegraded, whatsAppHealthy)
	assertCertificationDeliveryStatuses(t, ctx, pool, waveOne, volume, "retry", "accepted")
	emailDegraded.assertExactlyOnce(t, volume)
	whatsAppHealthy.assertExactlyOnce(t, volume)
	assertCertificationQueueDrained(t, ctx, pool, waveOne)

	waveTwo := "notification-cert-whatsapp-degraded-" + uuid.NewString()
	insertCertificationDeliveries(t, ctx, pool, repository, waveTwo, clientID, bookingID, latest, volume)
	emailHealthy := &certificationEmailSender{delay: time.Millisecond}
	whatsAppDegraded := &certificationWhatsAppSender{
		err: &whatsapp.GraphError{
			Class: whatsapp.ErrorClassRateLimit,
			Code:  4,
		},
		delay: time.Millisecond,
	}
	runCertificationDeliveryWave(ctx, repository, emailHealthy, whatsAppDegraded)
	assertCertificationDeliveryStatuses(t, ctx, pool, waveTwo, volume, "accepted", "retry")
	emailHealthy.assertExactlyOnce(t, volume)
	whatsAppDegraded.assertExactlyOnce(t, volume)
	assertCertificationQueueDrained(t, ctx, pool, waveTwo)

	assertCertificationLeaseRecovery(t, ctx, pool, repository, waveOne, waveTwo)
	if err := ctx.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf(
		"notification Phase A certification passed: planner_events=%d per_channel_per_wave=%d planner_duration=%s total_duration=%s",
		len(eventIDs), volume, plannerDuration, time.Since(planningStarted),
	)
}

func assertEmptyNotificationCertificationQueues(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	var rows int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM notification_event_jobs)+
			(SELECT COUNT(*) FROM notification_scope_replan_jobs)+
			(SELECT COUNT(*) FROM notification_in_app_jobs)+
			(SELECT COUNT(*) FROM notification_deliveries)+
			(SELECT COUNT(*) FROM meta_whatsapp_webhook_receipts)
	`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("certification requires empty notification queues; found %d existing rows", rows)
	}
}

func certificationVolume(t *testing.T) int {
	t.Helper()
	const defaultVolume = 250
	raw := os.Getenv("NOTIFICATION_CERTIFICATION_VOLUME")
	if raw == "" {
		return defaultVolume
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 32 || value > 10_000 {
		t.Fatalf("NOTIFICATION_CERTIFICATION_VOLUME must be an integer between 32 and 10000")
	}
	return value
}

func insertCertificationEvents(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	clientID uuid.UUID,
	bookingID uuid.UUID,
	volume int,
) []uuid.UUID {
	t.Helper()
	eventIDs := make([]uuid.UUID, volume)
	rows := make([][]any, volume)
	for index := range volume {
		eventIDs[index] = uuid.New()
		rows[index] = []any{
			eventIDs[index], clientID, bookingID, "booking_updated",
			fmt.Sprintf("notification-certification:%s:%06d", bookingID, index), []byte(`{}`),
		}
	}
	if _, err := pool.CopyFrom(
		ctx,
		pgx.Identifier{"booking_domain_events"},
		[]string{"id", "client_id", "booking_id", "event_type", "dedupe_key", "payload"},
		pgx.CopyFromRows(rows),
	); err != nil {
		t.Fatal(err)
	}
	return eventIDs
}

func assertCertificationEventStatus(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	eventIDs []uuid.UUID,
	status string,
	want int,
) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notification_event_jobs
		WHERE booking_event_id=ANY($1::uuid[]) AND status=$2
	`, eventIDs, status).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("notification event jobs in %s = %d, want %d", status, count, want)
	}
}

func assertNoRunnableCertificationEvents(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	eventIDs []uuid.UUID,
) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notification_event_jobs
		WHERE booking_event_id=ANY($1::uuid[])
		  AND status IN ('pending','retry','processing')
		  AND (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()
	`, eventIDs).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("certification planner queue retained %d runnable jobs", count)
	}
}

func insertCertificationDeliveries(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	repository *Repository,
	prefix string,
	clientID uuid.UUID,
	bookingID uuid.UUID,
	event EventJob,
	volume int,
) {
	t.Helper()
	rows := make([][]any, 0, volume*2)
	scheduledFor := time.Now().UTC().Add(-2 * time.Second)
	var providerEmail string
	if err := pool.QueryRow(ctx, `SELECT email FROM clients WHERE id=$1`, clientID).Scan(&providerEmail); err != nil {
		t.Fatal(err)
	}
	for index := range volume {
		for _, channel := range []string{"email", "whatsapp"} {
			templateKey := "provider_new_booking"
			destination := providerEmail
			if channel == "whatsapp" {
				destination = "+2348098765432"
			}
			rows = append(rows, []any{
				uuid.New(), fmt.Sprintf("%s:%s:%06d", prefix, channel, index), clientID,
				bookingID, event.BookingEventID, event.EventSequence, "provider", channel,
				"provider_new_booking", templateKey, scheduledFor,
				repository.destinationFingerprint(channel, destination), int64(1), "pending",
				0, scheduledFor,
			})
		}
	}
	if _, err := pool.CopyFrom(
		ctx,
		pgx.Identifier{"notification_deliveries"},
		[]string{
			"id", "idempotency_key", "client_id", "booking_id", "booking_event_id",
			"booking_event_sequence", "audience_type", "channel", "notification_type",
			"template_key", "scheduled_for", "destination_hmac", "preference_revision",
			"status", "attempt_count", "next_attempt_at",
		},
		pgx.CopyFromRows(rows),
	); err != nil {
		t.Fatal(err)
	}
}

func runCertificationDeliveryWave(
	ctx context.Context,
	repository *Repository,
	emailSender *certificationEmailSender,
	whatsAppSender *certificationWhatsAppSender,
) {
	emailWorker := NewEmailWorker(
		repository, emailSender, nil, nil, nil, 8, 10*time.Second,
		"https://client.tellbook.test", "https://market.tellbook.test",
	)
	whatsAppWorker := NewWhatsAppWorker(
		repository, whatsAppSender, nil, nil, nil, 8, 10*time.Second, true,
	)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		emailWorker.drain(ctx)
	}()
	go func() {
		defer workers.Done()
		for whatsAppWorker.processDeliveryCycle(ctx) {
		}
	}()
	workers.Wait()
}

func assertCertificationDeliveryStatuses(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	prefix string,
	volume int,
	emailStatus string,
	whatsAppStatus string,
) {
	t.Helper()
	var emailCount, whatsAppCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE channel='email' AND status=$2),
		       COUNT(*) FILTER (WHERE channel='whatsapp' AND status=$3)
		FROM notification_deliveries WHERE idempotency_key LIKE $1
	`, prefix+":%", emailStatus, whatsAppStatus).Scan(&emailCount, &whatsAppCount); err != nil {
		t.Fatal(err)
	}
	if emailCount != volume || whatsAppCount != volume {
		t.Fatalf(
			"delivery results email=%d/%s WhatsApp=%d/%s, want %d each",
			emailCount, emailStatus, whatsAppCount, whatsAppStatus, volume,
		)
	}
}

func assertCertificationQueueDrained(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	prefix string,
) {
	t.Helper()
	var runnable int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notification_deliveries
		WHERE idempotency_key LIKE $1 AND status IN ('pending','retry','processing')
		  AND scheduled_for<=NOW()
		  AND (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()
	`, prefix+":%").Scan(&runnable); err != nil {
		t.Fatal(err)
	}
	if runnable != 0 {
		t.Fatalf("certification delivery queue retained %d runnable rows", runnable)
	}
}

func assertCertificationLeaseRecovery(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	repository *Repository,
	emailRetryPrefix string,
	whatsAppRetryPrefix string,
) {
	t.Helper()
	rows, err := pool.Query(ctx, `
		UPDATE notification_deliveries SET status='processing',lease_owner='terminated-worker',
			lease_expires_at=NOW()-INTERVAL '1 second',next_attempt_at=NOW()-INTERVAL '1 second'
		WHERE id IN (
			(SELECT id FROM notification_deliveries WHERE idempotency_key LIKE $1 AND channel='email' LIMIT 1),
			(SELECT id FROM notification_deliveries WHERE idempotency_key LIKE $2 AND channel='whatsapp' LIMIT 1)
		)
		RETURNING id,channel
	`, emailRetryPrefix+":%", whatsAppRetryPrefix+":%")
	if err != nil {
		t.Fatal(err)
	}
	want := make(map[string]uuid.UUID, 2)
	for rows.Next() {
		var id uuid.UUID
		var channel string
		if err := rows.Scan(&id, &channel); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		want[channel] = id
	}
	rows.Close()
	if len(want) != 2 {
		t.Fatalf("prepared %d expired leases, want 2", len(want))
	}
	for _, channel := range []string{"email", "whatsapp"} {
		claimed, err := repository.ClaimDeliveries(ctx, channel, "replacement-"+channel, 100, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, delivery := range claimed {
			if delivery.ID == want[channel] {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expired %s lease was not recovered", channel)
		}
	}
}
