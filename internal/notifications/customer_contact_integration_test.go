package notifications

import (
	"booking/go-server/internal/whatsapp"
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCustomerReminderUsesSharedContact(t *testing.T) {
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
	clientID, bookingID, eventID, sequence := insertWhatsAppOutboundFixture(t, ctx, pool)
	handle := "contact-reminder-" + clientID.String()
	if _, err = pool.Exec(ctx, `INSERT INTO client_profile_handles(handle_slug,client_id) VALUES($1,$2)`, handle, clientID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO client_profiles(client_id,business_name,handle_slug,customer_contact_phone,customer_contact_verified_at,allow_booking_contact)
 VALUES($1,'Contact Provider',$2,'+2348055555555',NOW(),TRUE)`, clientID, handle); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE bookings SET customer_email_snapshot='customer@example.com',
 customer_whatsapp_e164_snapshot='+2348012345678',whatsapp_consent=TRUE,whatsapp_consent_at=NOW(),
 whatsapp_consent_source='public_checkout',notification_consent_policy_revision=1 WHERE id=$1`, bookingID); err != nil {
		t.Fatal(err)
	}
	repo, err := NewRepository(pool, "customer-contact-test-key-32-bytes", []string{"user_reminder"}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	var token string
	var startsAt time.Time
	if err = pool.QueryRow(ctx, `SELECT public_token,start_at FROM bookings WHERE id=$1`, bookingID).Scan(&token, &startsAt); err != nil {
		t.Fatal(err)
	}
	createDelivery := func() Delivery {
		t.Helper()
		id := uuid.New()
		if _, err := pool.Exec(ctx, `INSERT INTO notification_deliveries(id,idempotency_key,client_id,booking_id,booking_event_id,
  booking_event_sequence,audience_type,channel,notification_type,template_key,reminder_occurrence_at,reminder_offset_minutes,
  scheduled_for,destination_hmac,status,attempt_count,next_attempt_at,lease_owner,lease_expires_at)
  VALUES($1,$2,$3,$4,$5,$6,'customer','whatsapp','appointment_reminder','user_reminder',$7,1440,NOW(),$8,'processing',1,NOW(),'contact-test',NOW()+INTERVAL '1 minute')`,
			id, id.String(), clientID, bookingID, eventID, sequence, startsAt, repo.destinationFingerprint("whatsapp", "+2348012345678")); err != nil {
			t.Fatal(err)
		}
		return Delivery{ID: id, LeaseOwner: "contact-test"}
	}
	delivery, err := repo.AuthorizeDispatch(ctx, createDelivery())
	if err != nil {
		t.Fatal(err)
	}
	message, err := repo.BuildWhatsAppMessage(ctx, delivery)
	if err != nil {
		t.Fatal(err)
	}
	if message.Key != whatsapp.TemplateUserReminder || message.To != "+2348012345678" || message.Values.Body["provider_contact_phone"] != "+2348055555555" || message.Values.Button["booking_route_suffix"] != "#claim="+token {
		t.Fatal("reminder did not use the shared provider contact and guest claim link")
	}
	// A contact withdrawal after dispatch authorization is checked again before sending.
	if _, err = pool.Exec(ctx, `UPDATE client_profiles SET allow_booking_contact=FALSE,show_contact_on_public_profile=TRUE WHERE client_id=$1`, clientID); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.BuildWhatsAppMessage(ctx, delivery); !errors.Is(err, ErrWhatsAppNotDispatchable) {
		t.Fatalf("render after withdrawal: %v", err)
	}
	var status string
	if err = pool.QueryRow(ctx, `SELECT status FROM notification_deliveries WHERE id=$1`, delivery.ID).Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("withdrawn delivery status=%q err=%v", status, err)
	}
	if _, err = repo.AuthorizeDispatch(ctx, createDelivery()); !errors.Is(err, ErrDispatchNotAuthorized) {
		t.Fatalf("public-only contact passed booking fence: %v", err)
	}
	// Registered customers receive an ownership-checked booking link.
	owner := uuid.New()
	if _, err = pool.Exec(ctx, `INSERT INTO marketplace_customers(id,email,email_verified_at) VALUES($1,$2,NOW())`, owner, owner.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `UPDATE bookings SET marketplace_customer_id=NULL WHERE id=$1`, bookingID)
		_, _ = pool.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=$1`, owner)
	})
	if _, err = pool.Exec(ctx, `UPDATE bookings SET marketplace_customer_id=$2 WHERE id=$1`, bookingID, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE client_profiles SET allow_booking_contact=TRUE WHERE client_id=$1`, clientID); err != nil {
		t.Fatal(err)
	}
	delivery, err = repo.AuthorizeDispatch(ctx, createDelivery())
	if err != nil {
		t.Fatal(err)
	}
	message, err = repo.BuildWhatsAppMessage(ctx, delivery)
	if err != nil {
		t.Fatal(err)
	}
	if message.Values.Button["booking_route_suffix"] != "?booking="+bookingID.String() {
		t.Fatal("owned booking link mismatch")
	}
	contacts, err := whatsapp.NewContactFoundationRepository(pool, "customer-contact-test-key-32-bytes", "+2348031685968", true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	contact, err := contacts.GetCustomerContact(ctx, clientID)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	contact, err = contacts.UpdateCustomerContact(ctx, clientID, whatsapp.CustomerContactPatch{Revision: contact.Revision, AllowBookingContact: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT preference_revision FROM provider_notification_preferences WHERE client_id=$1`, clientID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	planScopeDirectly(t, ctx, pool, repo, ScopeJob{ClientID: clientID, PreferenceRevision: revision})
	enabled := true
	if _, err := contacts.UpdateCustomerContact(ctx, clientID, whatsapp.CustomerContactPatch{Revision: contact.Revision, AllowBookingContact: &enabled}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT preference_revision FROM provider_notification_preferences WHERE client_id=$1`, clientID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	planScopeDirectly(t, ctx, pool, repo, ScopeJob{ClientID: clientID, PreferenceRevision: revision})
	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE booking_id=$1 AND template_key='user_reminder' AND status='pending'`, bookingID).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("contact re-enable planned %d reminders, error=%v", pending, err)
	}
	delivery.DestinationHMAC = repo.destinationFingerprint("email", "customer@example.com")
	if _, err := pool.Exec(ctx, `UPDATE notification_deliveries SET channel='email',template_key='customer_booking_reminder',destination_hmac=$2 WHERE id=$1`, delivery.ID, delivery.DestinationHMAC); err != nil {
		t.Fatal(err)
	}
	email, err := repo.BuildEmailMessage(ctx, delivery, "https://provider.example", "https://marketplace.example")
	if err != nil || !strings.Contains(email.Text, "Provider contact: +2348055555555") {
		t.Fatalf("shared contact email: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE client_profiles SET allow_booking_contact=FALSE WHERE client_id=$1`, clientID); err != nil {
		t.Fatal(err)
	}
	email, err = repo.BuildEmailMessage(ctx, delivery, "https://provider.example", "https://marketplace.example")
	if err != nil || strings.Contains(email.Text, "Provider contact:") || strings.Contains(email.HTML, "Provider contact:") {
		t.Fatalf("email after withdrawal: %v", err)
	}
}
