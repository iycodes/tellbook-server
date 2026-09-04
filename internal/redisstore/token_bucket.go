package redisstore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/redis/go-redis/v9"
)

var tokenBucketScript = redis.NewScript(`
local current_time = redis.call('TIME')
local now_ms = (tonumber(current_time[1]) * 1000) + math.floor(tonumber(current_time[2]) / 1000)
local states = {}
local allowed = 1
local retry_ms = 0

for index, key in ipairs(KEYS) do
  local offset = ((index - 1) * 4)
  local rate_per_minute = tonumber(ARGV[offset + 1])
  local burst = tonumber(ARGV[offset + 2])
  local cost = tonumber(ARGV[offset + 3])
  local ttl_ms = tonumber(ARGV[offset + 4])
  local values = redis.call('HMGET', key, 'tokens', 'updated_ms')
  local tokens = tonumber(values[1])
  local updated_ms = tonumber(values[2])
  if tokens == nil then tokens = burst end
  if updated_ms == nil or updated_ms > now_ms then updated_ms = now_ms end
  tokens = math.min(burst, tokens + ((now_ms - updated_ms) * rate_per_minute / 60000))
  if tokens < cost then
    allowed = 0
    retry_ms = math.max(retry_ms, math.ceil((cost - tokens) * 60000 / rate_per_minute))
  end
  states[index] = {tokens = tokens, cost = cost, ttl_ms = ttl_ms}
end

for index, key in ipairs(KEYS) do
  local state = states[index]
  local tokens = state.tokens
  if allowed == 1 then tokens = tokens - state.cost end
  redis.call('HSET', key, 'tokens', tokens, 'updated_ms', now_ms)
  redis.call('PEXPIRE', key, state.ttl_ms)
end
return {allowed, retry_ms}
`)

type TokenBucketRequest struct {
	Scope     string
	Identity  string
	PerMinute int
	Burst     int
}

func (c *Client) AllowTokenBucket(
	ctx context.Context,
	scope string,
	identity string,
	perMinute int,
	burst int,
) (bool, time.Duration, error) {
	return c.AllowTokenBuckets(ctx, TokenBucketRequest{
		Scope: scope, Identity: identity, PerMinute: perMinute, Burst: burst,
	})
}

// AllowTokenBuckets atomically checks and consumes all requested buckets. A
// denial refills timestamps but consumes no token from any bucket in the set.
func (c *Client) AllowTokenBuckets(
	ctx context.Context,
	requests ...TokenBucketRequest,
) (bool, time.Duration, error) {
	if len(requests) == 0 {
		return false, 0, errors.New("at least one Redis token bucket is required")
	}
	keys := make([]string, 0, len(requests))
	arguments := make([]any, 0, len(requests)*4)
	for _, request := range requests {
		if request.PerMinute <= 0 || request.Burst <= 0 {
			return false, 0, errors.New("Redis token bucket rate and burst must be positive")
		}
		key, err := c.key(request.Scope, request.Identity)
		if err != nil {
			return false, 0, err
		}
		fullRefill := time.Duration(math.Ceil(float64(request.Burst)/float64(request.PerMinute)*60)) * time.Second
		ttl := max(time.Minute, fullRefill*2)
		keys = append(keys, key)
		arguments = append(arguments, request.PerMinute, request.Burst, 1, ttl.Milliseconds())
	}
	probe, err := c.beginOperation(time.Now())
	if err != nil {
		return false, 0, err
	}
	result, err := tokenBucketScript.Run(
		ctx,
		c.client,
		keys,
		arguments...,
	).Slice()
	c.finishOperation(probe, err)
	if err != nil {
		return false, 0, fmt.Errorf("apply Redis token bucket: %w", err)
	}
	if len(result) != 2 {
		return false, 0, errors.New("invalid Redis token bucket response")
	}
	allowed, ok := result[0].(int64)
	if !ok {
		return false, 0, errors.New("invalid Redis token bucket allowance")
	}
	retryMilliseconds, ok := result[1].(int64)
	if !ok {
		return false, 0, errors.New("invalid Redis token bucket retry delay")
	}
	return allowed == 1, time.Duration(retryMilliseconds) * time.Millisecond, nil
}
