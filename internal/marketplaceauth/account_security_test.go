package marketplaceauth

import (
	"booking/go-server/internal/authchallenge"
	"booking/go-server/internal/emailtest"
	"context"
	"github.com/google/uuid"
	"testing"
)

func TestMarketplaceAccountSecurityIntegration(t *testing.T) {
	ctx := context.Background()
	f := emailtest.New(t, false)
	id := uuid.New()
	email := "marketplace-" + f.Email
	f.Exec(t, `INSERT INTO marketplace_customers(id,full_name,email,email_verified_at) VALUES($1,'Email security customer',$2,NOW())`, id, email)
	t.Cleanup(func() { f.DB.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=$1`, id) })
	r := NewRepository(f.DB).WithAdditionalEmails(true)
	if _, _, err := r.SetPassword(ctx, id, "first-hash"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.SetPassword(ctx, id, "second-hash"); err != nil {
		t.Fatal(err)
	}
	linked := "linked-" + email
	for range 2 {
		challenge := uuid.New()
		hash := []byte("test-hash")
		f.Exec(t, `INSERT INTO marketplace_auth_challenges(id,identifier_type,identifier,delivery_channel,purpose,target_customer_id,code_hash,delivery_deadline,delivery_accepted_at,verify_expires_at) VALUES($1,'email',$2,'email','link_identity',$3,$4,NOW()+INTERVAL '1 minute',NOW(),NOW()+INTERVAL '10 minutes')`, challenge, linked, id, hash)
		if _, _, err := r.CompleteIdentityLink(ctx, authchallenge.Challenge{ID: challenge, IdentifierType: "email", Identifier: linked, CodeHash: hash, TargetAccountID: &id}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := f.DB.Query(ctx, `SELECT kind,recipient_email FROM account_security_events WHERE marketplace_customer_id=$1 ORDER BY created_at`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var kind, recipient string
		if err = rows.Scan(&kind, &recipient); err != nil {
			t.Fatal(err)
		}
		if recipient != email {
			t.Fatal("security notice retargeted")
		}
		kinds = append(kinds, kind)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 3 || kinds[0] != "password_set" || kinds[1] != "password_changed" || kinds[2] != "email_linked" {
		t.Fatalf("events %v", kinds)
	}
}
