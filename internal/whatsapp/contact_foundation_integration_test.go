package whatsapp

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProviderWhatsAppVerificationAndControls(t *testing.T) {
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

	clientID := uuid.New()
	handle := "notification-test-" + uuid.NewString()
	email := "notification-test-" + clientID.String() + "@example.com"
	if _, err := pool.Exec(ctx, `
		INSERT INTO clients (id,full_name,email,password_hash,email_verified_at)
		VALUES ($1,'Notification Test',$2,'test',NOW())
	`, clientID, email); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO client_profile_handles (handle_slug,client_id)
		VALUES ($2,$1)
	`, clientID, handle); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO client_profiles (
			client_id,business_name,handle_slug,country_code,currency_code,
			timezone,locale,market_configured_at
		) VALUES ($1,'Notification Test',$2,'NG','NGN','Africa/Lagos','en-NG',NOW())
	`, clientID, handle); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("foundation-key-", 3)
	foundation, err := NewContactFoundationRepository(pool, key, "+2348031685968", true)
	if err != nil {
		t.Fatal(err)
	}
	firstDestination := "+2348012345678"
	secondDestination := "+2348098765432"
	firstHMAC := destinationFingerprint([]byte(key), "whatsapp", firstDestination)
	secondHMAC := destinationFingerprint([]byte(key), "whatsapp", secondDestination)
	emailHMAC := destinationFingerprint([]byte(key), "email", "notification-test@example.com")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE wamid LIKE $1`, "%"+clientID.String())
		_, _ = pool.Exec(ctx, `DELETE FROM notification_contact_suppressions WHERE destination_hmac IN ($1,$2,$3)`, firstHMAC, secondHMAC, emailHMAC)
		_, _ = pool.Exec(ctx, `DELETE FROM clients WHERE id=$1`, clientID)
	})

	defaults, err := foundation.GetProviderPreferences(ctx, clientID)
	if err != nil || !defaults.BookingEmail || defaults.BookingWhatsApp || !defaults.EmailAvailable || !defaults.WhatsAppVerificationAvailable {
		t.Fatalf("provider defaults = %#v, %v", defaults, err)
	}
	unavailableFoundation, err := NewContactFoundationRepository(pool, key, "+2348031685968", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unavailableFoundation.StartProviderWhatsAppVerification(ctx, clientID, firstDestination); !errors.Is(err, ErrProviderWhatsAppVerificationUnavailable) {
		t.Fatalf("unavailable verification error = %v", err)
	}
	unsupportedReminderMinutes := 60
	if _, err := foundation.UpdateProviderPreferences(ctx, clientID, ProviderNotificationPreferencesPatch{
		AppointmentReminderMinutes: &unsupportedReminderMinutes,
	}); !errors.Is(err, ErrUnsupportedProviderReminderOffset) {
		t.Fatalf("unsupported reminder offset error = %v", err)
	}
	enabled := true
	if _, err := foundation.UpdateProviderPreferences(ctx, clientID, ProviderNotificationPreferencesPatch{
		BookingWhatsApp: &enabled,
	}); err != ErrProviderWhatsAppUnverified {
		t.Fatalf("unverified WhatsApp enable error = %v", err)
	}

	verification, err := foundation.StartProviderWhatsAppVerification(ctx, clientID, "0801 234 5678")
	if err != nil {
		t.Fatal(err)
	}
	token := verificationTokenFromURL(t, verification.VerificationURL)
	webhooks := NewWebhookRepository(pool, foundation)
	timestamp := time.Now().UTC()
	verifyReceipt := WebhookReceipt{
		DedupeKey:  receiptDedupeKey("333", "wamid.verify"+clientID.String()),
		BusinessID: "222", PhoneNumberID: "333", EventKind: "inbound_message",
		MessageID: "wamid.verify" + clientID.String(), ProviderTimestamp: &timestamp,
		ProcessingStatus: "completed",
		control:          inboundControl{kind: "verify", token: token, sender: strings.TrimPrefix(firstDestination, "+")},
	}
	if err := webhooks.StoreWebhookReceipts(ctx, []WebhookReceipt{verifyReceipt}); err != nil {
		t.Fatal(err)
	}
	verified, err := foundation.GetProviderPreferences(ctx, clientID)
	if err != nil || verified.WhatsAppVerifiedAt == nil || verified.BookingWhatsApp {
		t.Fatalf("verified preferences = %#v, %v", verified, err)
	}
	if err := webhooks.StoreWebhookReceipts(ctx, []WebhookReceipt{verifyReceipt}); err != nil {
		t.Fatalf("verification replay failed: %v", err)
	}
	verifiedAgain, _ := foundation.GetProviderPreferences(ctx, clientID)
	if verifiedAgain.PreferenceRevision != verified.PreferenceRevision {
		t.Fatalf("verification replay advanced revision: %d -> %d", verified.PreferenceRevision, verifiedAgain.PreferenceRevision)
	}

	enabledPreferences, err := foundation.UpdateProviderPreferences(ctx, clientID, ProviderNotificationPreferencesPatch{
		BookingWhatsApp: &enabled,
	})
	if err != nil || !enabledPreferences.BookingWhatsApp {
		t.Fatalf("enable verified WhatsApp = %#v, %v", enabledPreferences, err)
	}
	if err := foundation.SetDestinationSuppression(
		ctx, SuppressionChannelWhatsApp, firstDestination, "manual", "admin",
	); err != nil {
		t.Fatal(err)
	}

	stopReceipt := WebhookReceipt{
		DedupeKey:  receiptDedupeKey("333", "wamid.stop"+clientID.String()),
		BusinessID: "222", PhoneNumberID: "333", EventKind: "inbound_message",
		MessageID: "wamid.stop" + clientID.String(), ProviderTimestamp: &timestamp,
		ProcessingStatus: "completed",
		control:          inboundControl{kind: "stop", sender: strings.TrimPrefix(firstDestination, "+")},
	}
	if err := webhooks.StoreWebhookReceipts(ctx, []WebhookReceipt{stopReceipt}); err != nil {
		t.Fatal(err)
	}
	var suppressionCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notification_contact_suppressions
		WHERE channel='whatsapp' AND destination_hmac=$1
	`, firstHMAC).Scan(&suppressionCount); err != nil || suppressionCount != 1 {
		t.Fatalf("STOP suppression count=%d err=%v", suppressionCount, err)
	}
	var suppressionReason, suppressionSource string
	if err := pool.QueryRow(ctx, `
		SELECT reason, source FROM notification_contact_suppressions
		WHERE channel='whatsapp' AND destination_hmac=$1
	`, firstHMAC).Scan(&suppressionReason, &suppressionSource); err != nil || suppressionReason != "manual" || suppressionSource != "admin" {
		t.Fatalf("STOP replaced stronger suppression: reason=%q source=%q err=%v", suppressionReason, suppressionSource, err)
	}
	startReceipt := WebhookReceipt{
		DedupeKey:  receiptDedupeKey("333", "wamid.start"+clientID.String()),
		BusinessID: "222", PhoneNumberID: "333", EventKind: "inbound_message",
		MessageID: "wamid.start" + clientID.String(), ProviderTimestamp: &timestamp,
		ProcessingStatus: "completed",
		control:          inboundControl{kind: "start", sender: strings.TrimPrefix(firstDestination, "+")},
	}
	if err := webhooks.StoreWebhookReceipts(ctx, []WebhookReceipt{startReceipt}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notification_contact_suppressions
		WHERE channel='whatsapp' AND destination_hmac=$1
	`, firstHMAC).Scan(&suppressionCount); err != nil || suppressionCount != 1 {
		t.Fatalf("START cleared stronger suppression count=%d err=%v", suppressionCount, err)
	}
	if err := foundation.ClearDestinationSuppression(ctx, SuppressionChannelWhatsApp, firstDestination); err != nil {
		t.Fatal(err)
	}
	normalStopReceipt := stopReceipt
	normalStopReceipt.DedupeKey = receiptDedupeKey("333", "wamid.normal-stop"+clientID.String())
	normalStopReceipt.MessageID = "wamid.normal-stop" + clientID.String()
	if err := webhooks.StoreWebhookReceipts(ctx, []WebhookReceipt{normalStopReceipt}); err != nil {
		t.Fatal(err)
	}
	normalStartReceipt := startReceipt
	normalStartReceipt.DedupeKey = receiptDedupeKey("333", "wamid.normal-start"+clientID.String())
	normalStartReceipt.MessageID = "wamid.normal-start" + clientID.String()
	if err := webhooks.StoreWebhookReceipts(ctx, []WebhookReceipt{normalStartReceipt}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notification_contact_suppressions
		WHERE channel='whatsapp' AND destination_hmac=$1
	`, firstHMAC).Scan(&suppressionCount); err != nil || suppressionCount != 0 {
		t.Fatalf("normal START suppression count=%d err=%v", suppressionCount, err)
	}
	preferencesAfterStart, err := foundation.GetProviderPreferences(ctx, clientID)
	if err != nil || !preferencesAfterStart.BookingWhatsApp {
		t.Fatalf("START changed provider consent/preferences: %#v, %v", preferencesAfterStart, err)
	}

	secondVerification, err := foundation.StartProviderWhatsAppVerification(ctx, clientID, secondDestination)
	if err != nil {
		t.Fatal(err)
	}
	secondToken := verificationTokenFromURL(t, secondVerification.VerificationURL)
	wrongSenderReceipt := WebhookReceipt{
		DedupeKey:  receiptDedupeKey("333", "wamid.wrong"+clientID.String()),
		BusinessID: "222", PhoneNumberID: "333", EventKind: "inbound_message",
		MessageID: "wamid.wrong" + clientID.String(), ProviderTimestamp: &timestamp,
		ProcessingStatus: "completed",
		control:          inboundControl{kind: "verify", token: secondToken, sender: strings.TrimPrefix(firstDestination, "+")},
	}
	if err := webhooks.StoreWebhookReceipts(ctx, []WebhookReceipt{wrongSenderReceipt}); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := pool.QueryRow(ctx, `
		SELECT attempt_count FROM provider_whatsapp_verification_challenges
		WHERE client_id=$1 AND consumed_at IS NULL
	`, clientID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("wrong-sender attempts=%d err=%v", attempts, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE provider_whatsapp_verification_challenges
		SET created_at=NOW()-INTERVAL '2 minutes', expires_at=NOW()-INTERVAL '1 minute'
		WHERE client_id=$1 AND consumed_at IS NULL
	`, clientID); err != nil {
		t.Fatal(err)
	}
	expiredReceipt := WebhookReceipt{
		DedupeKey:  receiptDedupeKey("333", "wamid.expired"+clientID.String()),
		BusinessID: "222", PhoneNumberID: "333", EventKind: "inbound_message",
		MessageID: "wamid.expired" + clientID.String(), ProviderTimestamp: &timestamp,
		ProcessingStatus: "completed",
		control:          inboundControl{kind: "verify", token: secondToken, sender: strings.TrimPrefix(secondDestination, "+")},
	}
	if err := webhooks.StoreWebhookReceipts(ctx, []WebhookReceipt{expiredReceipt}); err != nil {
		t.Fatal(err)
	}
	var expiredConsumed bool
	if err := pool.QueryRow(ctx, `
		SELECT consumed_at IS NOT NULL FROM provider_whatsapp_verification_challenges
		WHERE client_id=$1 AND destination_hmac=$2
	`, clientID, secondHMAC).Scan(&expiredConsumed); err != nil || !expiredConsumed {
		t.Fatalf("expired challenge consumed=%v err=%v", expiredConsumed, err)
	}
	afterNumberChange, err := foundation.GetProviderPreferences(ctx, clientID)
	if err != nil || afterNumberChange.WhatsAppVerifiedAt != nil || afterNumberChange.BookingWhatsApp {
		t.Fatalf("number change did not clear verification: %#v, %v", afterNumberChange, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE clients SET email_verified_at=NULL WHERE id=$1`, clientID); err != nil {
		t.Fatal(err)
	}
	withoutVerifiedEmail, err := foundation.GetProviderPreferences(ctx, clientID)
	if err != nil || withoutVerifiedEmail.EmailAvailable {
		t.Fatalf("unverified provider email remained available: %#v, %v", withoutVerifiedEmail, err)
	}

	if err := foundation.SetDestinationSuppression(
		ctx, SuppressionChannelEmail, " Notification-Test@Example.com ",
		"invalid_address", "provider_response",
	); err != nil {
		t.Fatal(err)
	}
	emailSuppressed, err := foundation.DestinationSuppressed(
		ctx, SuppressionChannelEmail, "notification-test@example.com",
	)
	if err != nil || !emailSuppressed {
		t.Fatalf("email suppression=%v err=%v", emailSuppressed, err)
	}
	if err := foundation.SetDestinationSuppression(
		ctx, SuppressionChannelEmail, "notification-test@example.com",
		"user_opt_out", "customer_preference",
	); err != nil {
		t.Fatal(err)
	}
	if err := foundation.ClearDestinationSuppression(
		ctx, SuppressionChannelEmail, "notification-test@example.com",
	); err != nil {
		t.Fatal(err)
	}
	emailSuppressed, err = foundation.DestinationSuppressed(
		ctx, SuppressionChannelEmail, "notification-test@example.com",
	)
	if err != nil || emailSuppressed {
		t.Fatalf("cleared email suppression=%v err=%v", emailSuppressed, err)
	}

	var replanCount, uniqueReplanCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT preference_revision)
		FROM notification_scope_replan_jobs WHERE client_id=$1
	`, clientID).Scan(&replanCount, &uniqueReplanCount); err != nil || replanCount != uniqueReplanCount || replanCount < 4 {
		t.Fatalf("scope replans=%d unique=%d err=%v", replanCount, uniqueReplanCount, err)
	}
}

func verificationTokenFromURL(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Fields(parsed.Query().Get("text"))
	if len(parts) != 2 || parts[0] != "VERIFY" {
		t.Fatalf("invalid verification URL: %q", rawURL)
	}
	return parts[1]
}
