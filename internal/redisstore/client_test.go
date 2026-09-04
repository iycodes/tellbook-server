package redisstore

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

const testRedisKeyHMACSecret = "test-redis-key-hmac-secret-at-least-32-bytes"

func TestKeyHashesIdentityAndKeepsOnlyBoundedScope(t *testing.T) {
	client := &Client{
		client:    redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
		keyPrefix: "tellbook:test:v1", keyHMACKey: []byte(testRedisKeyHMACSecret),
	}
	defer client.client.Close()
	identity := "customer@example.com"
	key, err := client.key("session", identity)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(key, identity) || !strings.HasPrefix(key, "tellbook:test:v1:session:") {
		t.Fatalf("unsafe Redis key %q", key)
	}
	if _, err := client.key("bad:scope", identity); err == nil {
		t.Fatal("key accepted an unbounded scope")
	}
	other := &Client{
		client:    redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
		keyPrefix: "tellbook:test:v1", keyHMACKey: []byte("different-test-redis-key-hmac-secret-at-least-32-bytes"),
	}
	defer other.client.Close()
	otherKey, err := other.key("session", identity)
	if err != nil {
		t.Fatal(err)
	}
	if key == otherKey {
		t.Fatal("dynamic key segment did not depend on the HMAC secret")
	}
}

func TestCacheSetRejectsOversizedPayloadBeforeNetwork(t *testing.T) {
	client := &Client{
		client:    redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
		keyPrefix: "tellbook:test:v1", keyHMACKey: []byte(testRedisKeyHMACSecret),
		maxPayloadBytes: 32,
	}
	defer client.client.Close()
	err := client.CacheSet(context.Background(), "session", "opaque", strings.Repeat("x", 64), time.Minute)
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("CacheSet() error = %v, want ErrPayloadTooLarge", err)
	}
}

func TestRedisCircuitOpensAndAllowsOneRecoveryProbe(t *testing.T) {
	client := &Client{client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})}
	defer client.client.Close()
	for range redisBreakerFailureThreshold {
		client.finishOperation(false, errors.New("Redis unavailable"))
	}
	if _, err := client.beginOperation(time.Now()); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("beginOperation() error = %v, want ErrCircuitOpen", err)
	}
	client.breakerMu.Lock()
	client.breakerOpenUntil = time.Now().Add(-time.Millisecond)
	client.breakerMu.Unlock()
	probe, err := client.beginOperation(time.Now())
	if err != nil || !probe {
		t.Fatalf("recovery probe = %v, %v", probe, err)
	}
	if _, err := client.beginOperation(time.Now()); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("concurrent probe error = %v, want ErrCircuitOpen", err)
	}
	client.finishOperation(probe, nil)
	if probe, err := client.beginOperation(time.Now()); err != nil || probe {
		t.Fatalf("closed circuit operation = %v, %v", probe, err)
	}
}

func TestJitterTTLStaysWithinExplicitBounds(t *testing.T) {
	for range 100 {
		value := JitterTTL(2*time.Minute, 5*time.Minute)
		if value < 2*time.Minute || value > 5*time.Minute {
			t.Fatalf("JitterTTL() = %v, outside bounds", value)
		}
	}
}

func TestRedisTokenBucketIsSharedAcrossClients(t *testing.T) {
	redisURL := strings.TrimSpace(os.Getenv("REDIS_TEST_URL"))
	if redisURL == "" {
		t.Skip("REDIS_TEST_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prefix := "tellbook:test:" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "-")
	options := Options{
		URL: redisURL, KeyPrefix: prefix, KeyHMACSecret: testRedisKeyHMACSecret, ClientName: "tellbook-test",
		PoolSize: 4, DialTimeout: time.Second, ReadTimeout: time.Second,
		WriteTimeout: time.Second, PoolTimeout: time.Second, MaxPayloadBytes: 1024,
	}
	first, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	defer first.CacheDelete(context.Background(), "route_limit", "same-actor")

	allowed, _, err := first.AllowTokenBucket(ctx, "route_limit", "same-actor", 60, 1)
	if err != nil || !allowed {
		t.Fatalf("first token = %v, %v", allowed, err)
	}
	allowed, retryAfter, err := second.AllowTokenBucket(ctx, "route_limit", "same-actor", 60, 1)
	if err != nil || allowed || retryAfter <= 0 {
		t.Fatalf("shared second token = %v, retry %v, error %v", allowed, retryAfter, err)
	}
}

func TestRedisTokenBucketsDoNotPartiallyConsumeOnDenial(t *testing.T) {
	redisURL := strings.TrimSpace(os.Getenv("REDIS_TEST_URL"))
	if redisURL == "" {
		t.Skip("REDIS_TEST_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prefix := "tellbook:test:atomic-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "-")
	client, err := Open(ctx, Options{
		URL: redisURL, KeyPrefix: prefix, KeyHMACSecret: testRedisKeyHMACSecret,
		ClientName: "tellbook-test", PoolSize: 4, DialTimeout: time.Second,
		ReadTimeout: time.Second, WriteTimeout: time.Second, PoolTimeout: time.Second,
		MaxPayloadBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer client.CacheDelete(context.Background(), "pair_actor", "actor")
	defer client.CacheDelete(context.Background(), "pair_resource", "resource")

	if allowed, _, err := client.AllowTokenBucket(ctx, "pair_resource", "resource", 1, 1); err != nil || !allowed {
		t.Fatalf("resource setup = %v, %v", allowed, err)
	}
	allowed, _, err := client.AllowTokenBuckets(ctx,
		TokenBucketRequest{Scope: "pair_actor", Identity: "actor", PerMinute: 1, Burst: 1},
		TokenBucketRequest{Scope: "pair_resource", Identity: "resource", PerMinute: 1, Burst: 1},
	)
	if err != nil || allowed {
		t.Fatalf("paired request = %v, %v; want denial", allowed, err)
	}
	if allowed, _, err := client.AllowTokenBucket(ctx, "pair_actor", "actor", 1, 1); err != nil || !allowed {
		t.Fatalf("actor bucket was partially consumed: %v, %v", allowed, err)
	}
}

func TestRedisJSONCacheIsSharedAndDeletedByOpaqueIdentity(t *testing.T) {
	redisURL := strings.TrimSpace(os.Getenv("REDIS_TEST_URL"))
	if redisURL == "" {
		t.Skip("REDIS_TEST_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prefix := "tellbook:test:cache-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "-")
	options := Options{
		URL: redisURL, KeyPrefix: prefix, KeyHMACSecret: testRedisKeyHMACSecret, ClientName: "tellbook-test",
		PoolSize: 4, DialTimeout: time.Second, ReadTimeout: time.Second,
		WriteTimeout: time.Second, PoolTimeout: time.Second, MaxPayloadBytes: 1024,
	}
	writer, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	type payload struct {
		ID string `json:"id"`
	}
	if err := writer.CacheSet(ctx, "marketplace_session", "token-hash", payload{ID: "principal"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	var cached payload
	if err := reader.CacheGet(ctx, "marketplace_session", "token-hash", &cached); err != nil {
		t.Fatal(err)
	}
	if cached.ID != "principal" {
		t.Fatalf("cached payload = %+v", cached)
	}
	if err := writer.CacheDelete(ctx, "marketplace_session", "token-hash"); err != nil {
		t.Fatal(err)
	}
	if err := reader.CacheGet(ctx, "marketplace_session", "token-hash", &cached); !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("deleted cache error = %v, want ErrCacheMiss", err)
	}
}
