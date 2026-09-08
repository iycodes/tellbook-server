package whatsapp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCustomerContactVerificationAndVisibility(t *testing.T) {
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
	id := uuid.New()
	handle := "contact-" + id.String()
	if _, err = pool.Exec(ctx, `INSERT INTO clients(id,full_name,email,password_hash) VALUES($1,'Contact Test',$2,'test')`, id, handle+"@example.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM clients WHERE id=$1`, id) })
	if _, err = pool.Exec(ctx, `INSERT INTO client_profile_handles(handle_slug,client_id) VALUES($1,$2)`, handle, id); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO client_profiles(client_id,business_name,handle_slug,country_code,currency_code,timezone,locale,market_configured_at)
 VALUES($1,'Contact Test',$2,'NG','NGN','Africa/Lagos','en-NG',NOW())`, id, handle); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("contact-key-", 4)
	repo, err := NewContactFoundationRepository(pool, key, "+2348031685968", true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	contact, err := repo.GetCustomerContact(ctx, id)
	if err != nil || contact.Phone != "" || contact.AllowBookingContact || contact.ShowOnPublicProfile || len(contact.ReusableVerifiedNumbers) != 0 {
		t.Fatalf("defaults=%#v err=%v", contact, err)
	}
	yes, no := true, false
	if _, err = repo.UpdateCustomerContact(ctx, id, CustomerContactPatch{Revision: contact.Revision, ShowOnPublicProfile: &yes}); !errors.Is(err, ErrCustomerContactUnverified) {
		t.Fatalf("unverified sharing: %v", err)
	}
	if _, err = repo.ReuseCustomerContact(ctx, id, "+2348012345678", contact.Revision); !errors.Is(err, ErrCustomerContactUnverified) {
		t.Fatalf("unowned reuse: %v", err)
	}
	verify, err := repo.StartCustomerContactVerification(ctx, id, "08012345678", contact.Revision)
	if err != nil {
		t.Fatal(err)
	}
	token := verificationTokenFromURL(t, verify.VerificationURL)
	consume := func(phone, token string) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if err = repo.applyInboundControlTx(ctx, tx, inboundControl{kind: "verify", sender: phone, token: token}); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	consume("+2348099999999", token)
	contact, err = repo.GetCustomerContact(ctx, id)
	if err != nil || contact.VerifiedAt != nil {
		t.Fatalf("wrong sender verified: %#v %v", contact, err)
	}
	consume("+2348012345678", token)
	contact, err = repo.GetCustomerContact(ctx, id)
	if err != nil || contact.VerifiedAt == nil || contact.AllowBookingContact || contact.ShowOnPublicProfile {
		t.Fatalf("verified state: %#v %v", contact, err)
	}
	oldRevision := contact.Revision
	consume("+2348012345678", token)
	contact, _ = repo.GetCustomerContact(ctx, id)
	if contact.Revision != oldRevision {
		t.Fatal("replayed verification mutated contact")
	}
	checkVisibility := func(public, booking string) {
		t.Helper()
		var gotPublic, gotBooking string
		if err := pool.QueryRow(ctx, `SELECT COALESCE(public_contact_phone,''),COALESCE(booking_contact_phone,'') FROM client_profiles WHERE client_id=$1`, id).Scan(&gotPublic, &gotBooking); err != nil {
			t.Fatal(err)
		}
		if gotPublic != public || gotBooking != booking {
			t.Fatalf("public=%q booking=%q", gotPublic, gotBooking)
		}
	}
	contact, err = repo.UpdateCustomerContact(ctx, id, CustomerContactPatch{Revision: contact.Revision, AllowBookingContact: &yes})
	if err != nil {
		t.Fatal(err)
	}
	checkVisibility("", "+2348012345678")
	if _, err = repo.UpdateCustomerContact(ctx, id, CustomerContactPatch{Revision: oldRevision, ShowOnPublicProfile: &yes}); !errors.Is(err, ErrCustomerContactConflict) {
		t.Fatalf("stale update: %v", err)
	}
	contact, err = repo.UpdateCustomerContact(ctx, id, CustomerContactPatch{Revision: contact.Revision, ShowOnPublicProfile: &yes, AllowBookingContact: &no})
	if err != nil {
		t.Fatal(err)
	}
	checkVisibility("+2348012345678", "")
	var before int64
	if err = pool.QueryRow(ctx, `SELECT revision FROM public_provider_resource_revisions WHERE client_id=$1`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	verify, err = repo.StartCustomerContactVerification(ctx, id, "08022222222", contact.Revision)
	if err != nil {
		t.Fatal(err)
	}
	checkVisibility("", "")
	var after int64
	if err = pool.QueryRow(ctx, `SELECT revision FROM public_provider_resource_revisions WHERE client_id=$1`, id).Scan(&after); err != nil || after <= before {
		t.Fatalf("cache revision did not advance: %v", err)
	}
	contact, _ = repo.GetCustomerContact(ctx, id)
	superseded := verificationTokenFromURL(t, verify.VerificationURL)
	verify, err = repo.StartCustomerContactVerification(ctx, id, "08033333333", contact.Revision)
	if err != nil {
		t.Fatal(err)
	}
	consume("+2348022222222", superseded)
	contact, _ = repo.GetCustomerContact(ctx, id)
	if contact.VerifiedAt != nil {
		t.Fatal("superseded token verified new number")
	}
	repo.now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	consume("+2348033333333", verificationTokenFromURL(t, verify.VerificationURL))
	contact, _ = repo.GetCustomerContact(ctx, id)
	if contact.VerifiedAt != nil {
		t.Fatal("expired token verified number")
	}
	repo.now = func() time.Time { return time.Now().UTC() }
	if _, err = pool.Exec(ctx, `INSERT INTO provider_notification_preferences(client_id,whatsapp_e164,whatsapp_verified_at,whatsapp_verification_method)
 VALUES($1,'+2348044444444',NOW(),'inbound_challenge') ON CONFLICT(client_id) DO UPDATE SET
 whatsapp_e164=EXCLUDED.whatsapp_e164,whatsapp_verified_at=EXCLUDED.whatsapp_verified_at,whatsapp_verification_method=EXCLUDED.whatsapp_verification_method`, id); err != nil {
		t.Fatal(err)
	}
	contact, err = repo.ReuseCustomerContact(ctx, id, "+2348044444444", contact.Revision)
	if err != nil || contact.VerifiedAt == nil {
		t.Fatalf("reuse=%#v err=%v", contact, err)
	}
	checkVisibility("", "")
	if len(contact.ReusableVerifiedNumbers) != 1 {
		t.Fatal("verified reuse option missing")
	}
	share := true
	contact, err = repo.UpdateCustomerContact(ctx, id, CustomerContactPatch{Revision: contact.Revision, AllowBookingContact: &share, ShowOnPublicProfile: &share})
	if err != nil {
		t.Fatal(err)
	}
	revision := contact.Revision
	contact, err = repo.ReuseCustomerContact(ctx, id, contact.Phone, revision)
	if err != nil || contact.Revision != revision || !contact.AllowBookingContact || !contact.ShowOnPublicProfile {
		t.Fatalf("same-number reuse must preserve sharing: %#v err=%v", contact, err)
	}
	if err = repo.RemoveCustomerContact(ctx, id, contact.Revision); err != nil {
		t.Fatal(err)
	}
	contact, err = repo.GetCustomerContact(ctx, id)
	if err != nil || contact.Phone != "" || contact.VerifiedAt != nil {
		t.Fatalf("remove=%#v err=%v", contact, err)
	}
	var privateNumber string
	if err = pool.QueryRow(ctx, `SELECT whatsapp_e164 FROM provider_notification_preferences WHERE client_id=$1`, id).Scan(&privateNumber); err != nil || privateNumber != "+2348044444444" {
		t.Fatal("customer contact changed notification destination")
	}
}
