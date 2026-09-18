package auth

import (
	"booking/go-server/internal/authchallenge"
	"booking/go-server/internal/emailtest"
	"context"
	"github.com/google/uuid"
	"testing"
)

func TestPasswordSecurityEventsIntegration(t *testing.T) {
	ctx := context.Background()
	f := emailtest.New(t, false)
	r := NewRepository(f.DB).WithAdditionalEmails(true)
	if _, err := r.ChangePassword(ctx, f.Client, "hash-first"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ChangePassword(ctx, f.Client, "hash-second"); err != nil {
		t.Fatal(err)
	}
	rows, err := f.DB.Query(ctx, `SELECT kind,recipient_email FROM account_security_events WHERE provider_client_id=$1 ORDER BY created_at`, f.Client)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var kind, email string
		if err = rows.Scan(&kind, &email); err != nil {
			t.Fatal(err)
		}
		if email != f.Email {
			t.Fatal("incorrect recipient")
		}
		kinds = append(kinds, kind)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 2 || kinds[0] != "password_set" || kinds[1] != "password_changed" {
		t.Fatalf("kinds: %v", kinds)
	}
	rows.Close()
	f.Exec(t, `UPDATE clients SET email_verified_at=NULL WHERE id=$1`, f.Client)
	if _, err = r.ChangePassword(ctx, f.Client, "hash-third"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.DB.QueryRow(ctx, `SELECT count(*) FROM account_security_events WHERE provider_client_id=$1`, f.Client).Scan(&count); err != nil || count != 2 {
		t.Fatal("unverified address received notice", err)
	}
}

func TestIdentitySecurityRecipientIntegration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verified bool
		kind     string
	}{{"previous verified", true, "email"}, {"first verified", false, "email"}, {"phone without email", false, "phone"}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := emailtest.New(t, false)
			r := NewRepository(f.DB).WithAdditionalEmails(true)
			if !tc.verified {
				f.Exec(t, `UPDATE clients SET email_verified_at=NULL WHERE id=$1`, f.Client)
			}
			identifier := "linked-" + f.Email
			channel := "email"
			if tc.kind == "phone" {
				identifier = "+2348012345678"
				channel = "whatsapp"
			}
			link := func() {
				t.Helper()
				id := uuid.New()
				hash := []byte("hash-test")
				f.Exec(t, `INSERT INTO provider_auth_challenges(id,identifier_type,identifier,delivery_channel,purpose,target_client_id,code_hash,delivery_deadline,delivery_accepted_at,verify_expires_at) VALUES($1,$2,$3,$4,'link_identity',$5,$6,NOW()+INTERVAL '1 minute',NOW(),NOW()+INTERVAL '10 minutes')`, id, tc.kind, identifier, channel, f.Client, hash)
				if _, err := r.CompleteIdentityLink(ctx, authchallenge.Challenge{ID: id, IdentifierType: tc.kind, Identifier: identifier, CodeHash: hash, TargetAccountID: &f.Client}); err != nil {
					t.Fatal(err)
				}
			}
			link()
			link()
			var count int
			var recipient string
			if err := f.DB.QueryRow(ctx, `SELECT count(*),COALESCE(min(recipient_email),'') FROM account_security_events WHERE provider_client_id=$1`, f.Client).Scan(&count, &recipient); err != nil {
				t.Fatal(err)
			}
			if tc.kind == "phone" {
				if count != 0 {
					t.Fatal("unverified email used")
				}
				return
			}
			want := identifier
			if tc.verified {
				want = f.Email
			}
			if count != 1 || recipient != want {
				t.Fatalf("count=%d recipient=%s want=%s", count, recipient, want)
			}
		})
	}
}
