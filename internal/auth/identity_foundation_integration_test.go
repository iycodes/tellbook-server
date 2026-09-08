package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"booking/go-server/internal/config"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuthIdentityOwnershipConstraints(t *testing.T) {
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
	var missingProviderEmails int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM clients client
		WHERE client.email IS NOT NULL AND client.email_verified_at IS NOT NULL
		  AND NOT EXISTS (
			SELECT 1 FROM provider_auth_identities identity
			WHERE identity.client_id=client.id
			  AND identity.identity_type='email'
			  AND identity.normalized_identifier=lower(btrim(client.email))
		  )
	`).Scan(&missingProviderEmails); err != nil {
		t.Fatal(err)
	}
	if missingProviderEmails != 0 {
		t.Fatalf("verified provider emails missing identity rows = %d", missingProviderEmails)
	}
	var missingMarketplaceEmails int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM marketplace_customers customer
		WHERE customer.email IS NOT NULL AND customer.email_verified_at IS NOT NULL
		  AND NOT EXISTS (
			SELECT 1 FROM marketplace_customer_identities identity
			WHERE identity.marketplace_customer_id=customer.id
			  AND identity.identifier_type='email'
			  AND identity.normalized_identifier=lower(btrim(customer.email))
		  )
	`).Scan(&missingMarketplaceEmails); err != nil {
		t.Fatal(err)
	}
	if missingMarketplaceEmails != 0 {
		t.Fatalf("verified marketplace emails missing identity rows = %d", missingMarketplaceEmails)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	now := time.Now().UTC()
	firstProviderID, secondProviderID := uuid.New(), uuid.New()
	firstEmail := fmt.Sprintf("auth-foundation-%s@example.test", firstProviderID)
	secondEmail := fmt.Sprintf("auth-foundation-%s@example.test", secondProviderID)
	if _, err := tx.Exec(ctx, `
		INSERT INTO clients (id,full_name,email,password_hash,email_verified_at)
		VALUES ($1,'',$2,NULL,$3),($4,'',$5,NULL,$3)
	`, firstProviderID, firstEmail, now, secondProviderID, secondEmail); err != nil {
		t.Fatal(err)
	}
	phone := "+234801" + fmt.Sprintf("%07d", now.UnixNano()%10_000_000)
	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_auth_identities (client_id,identity_type,normalized_identifier,verified_at)
		VALUES ($1,'phone',$2,$3)
	`, firstProviderID, phone, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT provider_collision`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_auth_identities (client_id,identity_type,normalized_identifier,verified_at)
		VALUES ($1,'phone',$2,$3)
	`, secondProviderID, phone, now); err == nil {
		t.Fatal("one provider phone identity was assigned to two accounts")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT provider_collision`); err != nil {
		t.Fatal(err)
	}

	firstCustomerID, secondCustomerID := uuid.New(), uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO marketplace_customers (id,email,email_verified_at)
		VALUES ($1,$2,$3),($4,$5,$3)
	`, firstCustomerID, "market-"+firstEmail, now, secondCustomerID, "market-"+secondEmail); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO marketplace_customer_identities
			(marketplace_customer_id,identifier_type,normalized_identifier,verified_at)
		VALUES ($1,'phone',$2,$3)
	`, firstCustomerID, phone, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT marketplace_collision`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO marketplace_customer_identities
			(marketplace_customer_id,identifier_type,normalized_identifier,verified_at)
		VALUES ($1,'phone',$2,$3)
	`, secondCustomerID, phone, now); err == nil {
		t.Fatal("one marketplace phone identity was assigned to two accounts")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT marketplace_collision`); err != nil {
		t.Fatal(err)
	}

	challengeID, deliveryID := uuid.New(), uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_auth_challenges (
			id,identifier_type,identifier,delivery_channel,purpose,code_hash,delivery_deadline
		) VALUES ($1,'phone',$2,'whatsapp','sign_in',$3,$4)
	`, challengeID, phone, []byte("code-hash"), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_code_delivery_jobs (
			id,realm,channel,template_key,provider_challenge_id,
			payload_ciphertext,payload_nonce,payload_key_version,destination_fingerprint,
			delivery_deadline
		) VALUES ($1,'provider','whatsapp','v_c_x',$2,$3,$4,'test',$5,$6)
	`, deliveryID, challengeID, []byte("encrypted"), bytes.Repeat([]byte{1}, 12),
		bytes.Repeat([]byte{2}, 32), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE auth_code_delivery_jobs
		SET status='accepted',payload_ciphertext=NULL,payload_nonce=NULL,
			payload_key_version=NULL,accepted_at=$2,completed_at=$2,updated_at=$2
		WHERE id=$1
	`, deliveryID, now); err != nil {
		t.Fatalf("clear accepted auth delivery payload: %v", err)
	}

	if _, err := tx.Exec(ctx, `SAVEPOINT invalid_delivery_state`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_auth_challenges (
			id,identifier_type,identifier,delivery_channel,purpose,code_hash,
			delivery_deadline,delivery_accepted_at,verify_expires_at
		) VALUES ($1,'phone',$2,'whatsapp','sign_in',$3,$4,$5,NULL)
	`, uuid.New(), phone+"2", []byte("code-hash"), now.Add(time.Minute), now); err == nil {
		t.Fatal("accepted provider challenge allowed a missing verification expiry")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_delivery_state`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT invalid_marketplace_delivery_state`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO marketplace_auth_challenges (
			id,identifier_type,identifier,delivery_channel,purpose,code_hash,
			delivery_deadline,delivery_accepted_at,verify_expires_at
		) VALUES ($1,'email',$2,'email','sign_in',$3,$4,$5,NULL)
	`, uuid.New(), "delivery-state-"+uuid.NewString()+"@example.test", []byte("code-hash"),
		now.Add(time.Minute), now); err == nil {
		t.Fatal("accepted marketplace challenge allowed a missing verification expiry")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_marketplace_delivery_state`); err != nil {
		t.Fatal(err)
	}

	missingPayloadChallengeID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_auth_challenges (
			id,identifier_type,identifier,delivery_channel,purpose,code_hash,delivery_deadline
		) VALUES ($1,'phone',$2,'whatsapp','sign_in',$3,$4)
	`, missingPayloadChallengeID, phone+"3", []byte("code-hash"), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT invalid_delivery_payload`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_code_delivery_jobs (
			id,realm,channel,template_key,provider_challenge_id,
			payload_ciphertext,payload_nonce,payload_key_version,destination_fingerprint,
			delivery_deadline,status
		) VALUES ($1,'provider','whatsapp','v_c_x',$2,NULL,NULL,NULL,$3,$4,'pending')
	`, uuid.New(), missingPayloadChallengeID, bytes.Repeat([]byte{3}, 32), now.Add(time.Minute)); err == nil {
		t.Fatal("pending auth delivery allowed a missing encrypted payload")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_delivery_payload`); err != nil {
		t.Fatal(err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO marketplace_customer_identities
			(marketplace_customer_id,identifier_type,normalized_identifier,verified_at)
		VALUES ($1,'whatsapp',$2,$3)
	`, secondCustomerID, phone+"1", now); err == nil {
		t.Fatal("WhatsApp was accepted as a distinct marketplace identity type")
	}
}

func TestProviderSecurityRevisionRevokesAccessAndRefresh(t *testing.T) {
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

	providerID := uuid.New()
	email := "security-revision-" + providerID.String() + "@example.test"
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO clients (id,full_name,email,email_verified_at,security_revision)
		VALUES ($1,'Provider',$2,$3,1)
	`, providerID, email, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM clients WHERE id=$1`, providerID) })
	if _, err := pool.Exec(ctx, `
		INSERT INTO provider_auth_identities (client_id,identity_type,normalized_identifier,verified_at)
		VALUES ($1,'email',$2,$3)
	`, providerID, email, now); err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(pool)
	service := NewService(repo, config.Config{
		AuthAccessTokenSecret: "security-revision-integration-secret",
		AuthAccessTokenTTL:    time.Hour,
		AuthRefreshTokenTTL:   24 * time.Hour,
		AuthIssuer:            "tellbook-auth-integration",
	}, nil, nil)
	user, err := repo.GetUserByID(ctx, providerID)
	if err != nil {
		t.Fatal(err)
	}
	accessToken, err := service.signAccessToken(user, now)
	if err != nil {
		t.Fatal(err)
	}
	refreshToken, refreshHash, err := newOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRefreshSession(ctx, RefreshSession{
		ID: uuid.New(), UserID: providerID, TokenHash: refreshHash,
		ExpiresAt: now.Add(24 * time.Hour), LastUsedAt: now, CreatedAt: now,
		SessionRevision: user.SecurityRevision,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateAccessToken(ctx, accessToken); err != nil {
		t.Fatalf("fresh access token failed: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE clients SET security_revision=security_revision+1 WHERE id=$1`, providerID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateAccessToken(ctx, accessToken); !errors.Is(err, ErrInvalidAccess) {
		t.Fatalf("stale access token error = %v, want ErrInvalidAccess", err)
	}
	if _, _, _, err := service.Refresh(ctx, refreshToken, sessionMetadata{}); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("stale refresh token error = %v, want ErrInvalidRefresh", err)
	}
}
