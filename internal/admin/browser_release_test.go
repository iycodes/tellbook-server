package admin

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Only called by the opt-in browser fixture against its guarded isolated DB.
// These records use real domain tables and the real command handlers.
func seedBrowserRelease(t *testing.T, s *Service, owner Session, credentials map[string]string) {
	t.Helper()
	ctx := context.Background()
	run := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, role := range []string{"operations", "support", "finance", "analyst"} {
		member, secret := enrolledReleaseStaff(t, s, owner, role+"@example.test", role)
		run(`UPDATE admin_staff SET full_name=$2,last_totp_step=-1 WHERE id=$1`, member.Staff.ID, "Release QA "+role)
		credentials[role+"_email"], credentials[role+"_secret"] = member.Staff.Email, secret
	}
	credentials["role_password"] = "staff-test-password-123"
	business, contact, account := uuid.New(), uuid.New(), uuid.New()
	run(`INSERT INTO clients(id,full_name) VALUES($1,'Release QA owner')`, business)
	run(`INSERT INTO client_profile_handles(client_id,handle_slug) VALUES($1,$2)`, business, "release-"+business.String())
	run(`INSERT INTO client_profiles(client_id,handle_slug,business_name) VALUES($1,$2,'Release QA studio')`, business, "release-"+business.String())
	// Same name/email deliberately do not establish an account link.
	run(`INSERT INTO customers(id,client_id,full_name,email,private_notes) VALUES($1,$2,'Release QA customer','identity@example.test','PROVIDER_PRIVATE_QA')`, contact, business)
	run(`INSERT INTO marketplace_customers(id,full_name,email,password_hash) VALUES($1,'Release QA customer',$2,'unused-test-hash')`, account, "identity-"+account.String()+"@example.test")
	// Use unique account email to avoid colliding with earlier interrupted fixtures.
	run(`UPDATE customers SET email=$2 WHERE id=$1`, contact, "identity-"+account.String()+"@example.test")
	credentials["contact_id"], credentials["account_id"], credentials["business_id"] = contact.String(), account.String(), business.String()
	end := time.Now().UTC().Add(-time.Hour)
	credentials["from"], credentials["to"] = end.Add(-time.Hour).Format("2006-01-02"), end.AddDate(0, 0, 1).Format("2006-01-02")
	for _, name := range []string{"complete", "no_show", "readonly", "unlinked"} {
		id := uuid.New()
		var linked any = account
		if name == "unlinked" {
			linked = nil
		}
		run(`INSERT INTO bookings(id,client_id,customer_id,marketplace_customer_id,title,status,payment_status,agreement_status,start_at,end_at,occupied_start_at,occupied_end_at,currency_code,country_code) VALUES($1,$2,$3,$4,$5,'confirmed','paid_in_full','not_required',$6,$7,$6,$7,'NGN','NG')`, id, business, contact, linked, "Release QA "+name, end.Add(-time.Hour), end)
		credentials[name+"_booking_id"] = id.String()
	}
}
