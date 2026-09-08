package marketplaceauth

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
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
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM marketplace_auth_challenges WHERE identifier=$1`, primaryEmail)
		_, _ = pool.Exec(context.Background(), `DELETE FROM marketplace_customers WHERE email=$1`, primaryEmail)
	})

	repo := NewRepository(pool)
	challenges := newTestChallengeService(t, pool)
	sender := startTestAuthWorker(t, challenges)
	service := NewService(repo, config.Config{
		AuthRefreshTokenTTL: 30 * 24 * time.Hour, AuthBcryptCost: 10,
	}, challenges)
	service.ConfigureSessionCache(cache, 4, nil)
	challenge, err := service.StartChallenge(ctx, primaryEmail, "email")
	if err != nil {
		t.Fatal(err)
	}
	code := awaitAuthCode(t, sender)
	awaitMarketplaceChallengeReady(t, service, challenge.ChallengeID)
	customer, rawToken, _, err := service.VerifyChallenge(ctx, challenge.ChallengeID, code, "integration-test", "127.0.0.1")
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
	if _, err := service.SetPassword(ctx, customer.ID, "", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	assertInvalidated("password update")

	cachePrincipal()
	if err := service.Logout(ctx, rawToken); err != nil {
		t.Fatal(err)
	}
	assertInvalidated("logout")
	if _, err := service.AuthenticatePrincipal(ctx, rawToken, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("logged-out principal error = %v, want ErrNotFound", err)
	}
}
