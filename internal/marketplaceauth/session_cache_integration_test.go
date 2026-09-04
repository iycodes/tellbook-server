package marketplaceauth

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/config"
	"booking/go-server/internal/redisstore"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSessionCacheInvalidationAcrossPostgresAndRedis(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	redisURL := strings.TrimSpace(os.Getenv("REDIS_TEST_URL"))
	if databaseURL == "" || redisURL == "" {
		t.Skip("TEST_DATABASE_URL and REDIS_TEST_URL are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	cache, err := redisstore.Open(ctx, redisstore.Options{
		URL: redisURL, KeyPrefix: "tellbook:test:session-" + uuid.NewString(),
		KeyHMACSecret: "test-redis-key-hmac-secret-at-least-32-bytes",
		ClientName:    "tellbook-session-integration", PoolSize: 4,
		DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second,
		PoolTimeout: time.Second, MaxPayloadBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })

	primaryEmail := "marketplace-cache-" + uuid.NewString() + "@example.com"
	linkedPhone := fmt.Sprintf("+2348%09d", time.Now().UnixNano()%1_000_000_000)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM marketplace_auth_challenges WHERE identifier IN ($1,$2)`, primaryEmail, linkedPhone)
		_, _ = pool.Exec(context.Background(), `DELETE FROM marketplace_customers WHERE email=$1`, primaryEmail)
	})

	repo := NewRepository(pool)
	sender := &captureMailer{}
	service := NewService(repo, config.Config{
		AuthRefreshTokenTTL: 30 * 24 * time.Hour, AuthBcryptCost: 10,
	}, sender)
	service.ConfigureSessionCache(cache, 4, nil)
	challenge, err := service.StartChallenge(ctx, primaryEmail, "email")
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`\b\d{6}\b`).FindString(sender.message.Text)
	customer, rawToken, err := service.VerifyChallenge(ctx, challenge.ChallengeID, code, "integration-test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	cacheIdentity := hex.EncodeToString(hashToken(rawToken))

	cachePrincipal := func() {
		t.Helper()
		if _, err := service.AuthenticatePrincipal(ctx, rawToken, false); err != nil {
			t.Fatal(err)
		}
		var principal SessionPrincipal
		if err := cache.CacheGet(ctx, "marketplace_session", cacheIdentity, &principal); err != nil {
			t.Fatalf("session was not written to Redis: %v", err)
		}
	}
	assertInvalidated := func(change string) {
		t.Helper()
		var principal SessionPrincipal
		if err := cache.CacheGet(ctx, "marketplace_session", cacheIdentity, &principal); !errors.Is(err, redisstore.ErrCacheMiss) {
			t.Fatalf("%s left a cached principal behind: %v", change, err)
		}
	}

	cachePrincipal()
	if _, err := service.UpdateProfile(ctx, customer.ID, ProfileInput{FullName: "Cache Test"}); err != nil {
		t.Fatal(err)
	}
	assertInvalidated("profile update")

	cachePrincipal()
	if _, err := service.SetPassword(ctx, customer.ID, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	assertInvalidated("password update")

	cachePrincipal()
	linkCode, linkHash, err := newSixDigitCode()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	linkChallenge := Challenge{
		ID: uuid.New(), IdentifierType: "phone", Identifier: linkedPhone,
		DeliveryChannel: "sms", Purpose: "link_identity", TargetCustomerID: &customer.ID,
		CodeHash: linkHash, ExpiresAt: now.Add(challengeTTL), CreatedAt: now,
	}
	if err := repo.CreateChallenge(ctx, linkChallenge); err != nil {
		t.Fatal(err)
	}
	if _, err := service.VerifyIdentityLink(ctx, customer.ID, linkChallenge.ID, linkCode); err != nil {
		t.Fatal(err)
	}
	assertInvalidated("identity link")

	cachePrincipal()
	if err := service.Logout(ctx, rawToken); err != nil {
		t.Fatal(err)
	}
	assertInvalidated("logout")
	if _, err := service.AuthenticatePrincipal(ctx, rawToken, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("logged-out principal error = %v, want ErrNotFound", err)
	}
}
