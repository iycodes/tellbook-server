package redisstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	ErrCacheMiss       = errors.New("redis cache miss")
	ErrPayloadTooLarge = errors.New("redis cache payload exceeds configured limit")
	ErrCircuitOpen     = errors.New("redis circuit is open")
)

const (
	redisBreakerFailureThreshold = 3
	redisBreakerOpenDuration     = time.Second
)

var keyScopePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)

type Metrics interface {
	ObserveRedisOperation(operation, outcome string, duration time.Duration)
}

type Options struct {
	URL                string
	KeyPrefix          string
	KeyHMACSecret      string
	ClientName         string
	PoolSize           int
	MinIdleConnections int
	DialTimeout        time.Duration
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	PoolTimeout        time.Duration
	MaxPayloadBytes    int
	Metrics            Metrics
}

type Client struct {
	client           *redis.Client
	keyPrefix        string
	keyHMACKey       []byte
	maxPayloadBytes  int
	metrics          Metrics
	breakerMu        sync.Mutex
	breakerFailures  int
	breakerOpenUntil time.Time
	breakerProbe     bool
}

type PoolSnapshot struct {
	Hits             uint32
	Misses           uint32
	Timeouts         uint32
	WaitCount        uint32
	Unusable         uint32
	WaitDuration     time.Duration
	TotalConnections uint32
	IdleConnections  uint32
	StaleConnections uint32
	PendingRequests  uint32
}

func Open(ctx context.Context, options Options) (*Client, error) {
	if strings.TrimSpace(options.URL) == "" {
		return nil, nil
	}
	if len(options.KeyHMACSecret) < 32 {
		return nil, errors.New("Redis key HMAC secret must be at least 32 bytes")
	}
	parsed, err := redis.ParseURL(options.URL)
	if err != nil {
		return nil, fmt.Errorf("parse Redis URL: %w", err)
	}
	if parsed.TLSConfig != nil {
		parsed.TLSConfig.MinVersion = tls.VersionTLS12
	}
	parsed.ClientName = strings.TrimSpace(options.ClientName)
	parsed.PoolSize = options.PoolSize
	parsed.MaxActiveConns = options.PoolSize
	parsed.MaxConcurrentDials = max(1, min(options.PoolSize, 4))
	parsed.MinIdleConns = options.MinIdleConnections
	parsed.MaxIdleConns = options.PoolSize
	parsed.DialTimeout = options.DialTimeout
	parsed.ReadTimeout = options.ReadTimeout
	parsed.WriteTimeout = options.WriteTimeout
	parsed.PoolTimeout = options.PoolTimeout
	parsed.ConnMaxIdleTime = 5 * time.Minute
	parsed.ConnMaxLifetime = 30 * time.Minute
	parsed.ContextTimeoutEnabled = true
	parsed.MaxRetries = 1
	parsed.MinRetryBackoff = 10 * time.Millisecond
	parsed.MaxRetryBackoff = 50 * time.Millisecond
	parsed.DialerRetries = 1
	parsed.DialerRetryTimeout = 25 * time.Millisecond

	rawClient := redis.NewClient(parsed)
	if options.Metrics != nil {
		rawClient.AddHook(metricsHook{metrics: options.Metrics})
	}
	client := &Client{
		client:          rawClient,
		keyPrefix:       strings.TrimSuffix(options.KeyPrefix, ":"),
		keyHMACKey:      []byte(options.KeyHMACSecret),
		maxPayloadBytes: options.MaxPayloadBytes,
		metrics:         options.Metrics,
	}
	if err := client.Ping(ctx); err != nil {
		_ = rawClient.Close()
		return nil, fmt.Errorf("connect to Redis: %w", err)
	}
	return client, nil
}

func (c *Client) Ping(ctx context.Context) error {
	if c == nil || c.client == nil {
		return errors.New("Redis is not configured")
	}
	err := c.client.Ping(ctx).Err()
	c.finishOperation(false, err)
	return err
}

func (c *Client) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

func (c *Client) PoolSnapshot() PoolSnapshot {
	if c == nil || c.client == nil {
		return PoolSnapshot{}
	}
	stats := c.client.PoolStats()
	return PoolSnapshot{
		Hits: stats.Hits, Misses: stats.Misses, Timeouts: stats.Timeouts,
		WaitCount: stats.WaitCount, Unusable: stats.Unusable,
		WaitDuration:     time.Duration(stats.WaitDurationNs),
		TotalConnections: stats.TotalConns, IdleConnections: stats.IdleConns,
		StaleConnections: stats.StaleConns, PendingRequests: stats.PendingRequests,
	}
}

func (c *Client) CacheGet(ctx context.Context, scope, identity string, target any) error {
	key, err := c.key(scope, identity)
	if err != nil {
		return err
	}
	probe, err := c.beginOperation(time.Now())
	if err != nil {
		return err
	}
	payload, err := c.client.Get(ctx, key).Bytes()
	c.finishOperation(probe, err)
	if errors.Is(err, redis.Nil) {
		return ErrCacheMiss
	}
	if err != nil {
		return err
	}
	if len(payload) > c.maxPayloadBytes {
		_ = c.client.Del(context.WithoutCancel(ctx), key).Err()
		return ErrPayloadTooLarge
	}
	if err := json.Unmarshal(payload, target); err != nil {
		_ = c.client.Del(context.WithoutCancel(ctx), key).Err()
		return fmt.Errorf("decode Redis cache payload: %w", err)
	}
	return nil
}

func (c *Client) CacheSet(ctx context.Context, scope, identity string, value any, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("Redis cache TTL must be positive")
	}
	key, err := c.key(scope, identity)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode Redis cache payload: %w", err)
	}
	if len(payload) > c.maxPayloadBytes {
		return ErrPayloadTooLarge
	}
	probe, err := c.beginOperation(time.Now())
	if err != nil {
		return err
	}
	err = c.client.Set(ctx, key, payload, ttl).Err()
	c.finishOperation(probe, err)
	return err
}

func (c *Client) CacheDelete(ctx context.Context, scope string, identities ...string) error {
	if len(identities) == 0 {
		return nil
	}
	keys := make([]string, 0, len(identities))
	for _, identity := range identities {
		key, err := c.key(scope, identity)
		if err != nil {
			return err
		}
		keys = append(keys, key)
	}
	probe, err := c.beginOperation(time.Now())
	if err != nil {
		return err
	}
	err = c.client.Del(ctx, keys...).Err()
	c.finishOperation(probe, err)
	return err
}

// JitterTTL spreads simultaneous expirations without allowing a value to live
// outside the caller's explicit minimum and maximum bounds.
func JitterTTL(minimum, maximum time.Duration) time.Duration {
	if maximum <= minimum {
		return minimum
	}
	return minimum + time.Duration(rand.Int64N(int64(maximum-minimum)+1))
}

func (c *Client) key(scope string, identities ...string) (string, error) {
	if c == nil || c.client == nil {
		return "", errors.New("Redis is not configured")
	}
	if !keyScopePattern.MatchString(scope) {
		return "", errors.New("invalid Redis key scope")
	}
	key := c.keyPrefix + ":" + scope
	for _, identity := range identities {
		mac := hmac.New(sha256.New, c.keyHMACKey)
		_, _ = mac.Write([]byte(identity))
		key += ":" + hex.EncodeToString(mac.Sum(nil)[:16])
	}
	return key, nil
}

func (c *Client) beginOperation(now time.Time) (bool, error) {
	if c == nil || c.client == nil {
		return false, errors.New("Redis is not configured")
	}
	c.breakerMu.Lock()
	defer c.breakerMu.Unlock()
	if c.breakerOpenUntil.IsZero() {
		return false, nil
	}
	if now.Before(c.breakerOpenUntil) || c.breakerProbe {
		if c.metrics != nil {
			c.metrics.ObserveRedisOperation("circuit", "open", 0)
		}
		return false, ErrCircuitOpen
	}
	c.breakerProbe = true
	return true, nil
}

func (c *Client) finishOperation(probe bool, operationErr error) {
	if c == nil {
		return
	}
	if errors.Is(operationErr, context.Canceled) || errors.Is(operationErr, context.DeadlineExceeded) {
		if probe {
			c.breakerMu.Lock()
			c.breakerProbe = false
			c.breakerOpenUntil = time.Now().Add(redisBreakerOpenDuration)
			c.breakerMu.Unlock()
		}
		return
	}
	healthy := operationErr == nil || errors.Is(operationErr, redis.Nil)
	c.breakerMu.Lock()
	defer c.breakerMu.Unlock()
	if healthy {
		c.breakerFailures = 0
		c.breakerOpenUntil = time.Time{}
		c.breakerProbe = false
		return
	}
	c.breakerFailures++
	if probe || c.breakerFailures >= redisBreakerFailureThreshold {
		c.breakerOpenUntil = time.Now().Add(redisBreakerOpenDuration)
		c.breakerProbe = false
	}
}

type metricsHook struct{ metrics Metrics }

func (hook metricsHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		startedAt := time.Now()
		connection, err := next(ctx, network, addr)
		hook.metrics.ObserveRedisOperation("dial", redisOutcome(err), time.Since(startedAt))
		return connection, err
	}
}

func (hook metricsHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		startedAt := time.Now()
		err := next(ctx, command)
		hook.metrics.ObserveRedisOperation(redisOperation(command.Name()), redisOutcome(err), time.Since(startedAt))
		return err
	}
}

func (hook metricsHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		startedAt := time.Now()
		err := next(ctx, commands)
		hook.metrics.ObserveRedisOperation("pipeline", redisOutcome(err), time.Since(startedAt))
		return err
	}
}

func redisOperation(name string) string {
	switch strings.ToLower(name) {
	case "ping", "get", "set", "del", "eval", "evalsha", "script", "hget", "hset", "hmget", "pexpire", "client":
		return strings.ToLower(name)
	default:
		return "other"
	}
}

func redisOutcome(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, redis.Nil) {
		return "miss"
	}
	return "error"
}
