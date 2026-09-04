# Redis operations

Tellbook uses Redis only for replica-shared ephemeral coordination: distributed rate limits,
short-lived minimal session principals, and specifically approved small caches. PostgreSQL remains
the source of truth. Redis must never contain a raw session token, contact detail, exact GPS
coordinate, message body, booking record, or payment state.

## Deployment configuration

- Give every deployment environment its own Redis instance or logical database and a unique,
  versioned `REDIS_KEY_PREFIX`, such as `tellbook:production:v1`.
- Supply `REDIS_KEY_HMAC_SECRET` as a secret with at least 32 random bytes. It HMACs every dynamic
  key segment so low-entropy email and phone identifiers cannot be enumerated from Redis keys.
  Rotating it intentionally abandons existing ephemeral entries; use a new key prefix at the same
  time and let the old namespace expire.
- Use `rediss://` when the managed service exposes TLS. The client requires TLS 1.2 or newer and
  accepts Redis ACL credentials from `REDIS_URL`; the URL must be supplied as a secret and must not
  be logged.
- Keep `REDIS_POOL_SIZE` within the managed service connection budget across all API replicas.
  `PoolSize` and `MaxActiveConns` are both capped by this value, preventing hidden overflow
  connections. Start with 32 per API replica and tune from pool waits and measured latency.
- Alert on readiness failures, operation errors, pool timeouts, pending requests, and Redis p95
  operation latency above 5 ms inside the deployment network.

## Memory, eviction, and persistence

Set an explicit `maxmemory` below the service memory limit and use `allkeys-lfu`. Every Tellbook
entry has an expiry, and no Redis value is durable business state, so eviction is safe. Enable the
managed service's normal snapshot/AOF option only when it materially improves recovery time; the
application does not rely on Redis persistence for correctness.

Cache payloads are capped by `REDIS_MAX_PAYLOAD_BYTES` (64 KiB by default). Feature-specific TTLs
must be short and jittered. Do not add unbounded collections or cache free-text/GPS marketplace
search combinations.

### Marketplace sessions

The `marketplace_session` cache stores only session ID, customer ID, expiry, security revision,
and session revision. Dynamic key components are HMACed by the Redis key builder. The cache never
stores the raw cookie or customer profile.

Positive entries live for a jittered two to five minutes, capped by the durable session expiry.
`GET`/`HEAD` middleware reads may accept that bounded positive entry. Every mutation revalidates
the session in PostgreSQL. `/v1/marketplace/auth/session` and `/v1/marketplace/me` separately load
the current customer profile instead of returning cached profile data. Logout deletes the current
entry; profile, verified-identity, and password changes delete entries for all active sessions.
Redis invalidation is best effort after the durable PostgreSQL change, so an ordinary read can
retain access for at most the remaining cache TTL if Redis is unavailable; mutations are still
rejected by fresh PostgreSQL validation.

Concurrent misses for one session are coalesced inside each API process. PostgreSQL fallbacks are
capped by `REDIS_FALLBACK_MAX_CONCURRENCY`; excess fallback requests receive a temporary auth
unavailable response rather than exhausting the database pool. Track the session hit ratio with
`tellbook_cache_requests_total{cache="marketplace_session",outcome=...}`.

## Failure behavior

Production API processes require `REDIS_URL`. `/v1/readyz` reports Redis as degraded during an
outage without removing an otherwise healthy replica from service. A short process-local circuit
breaker prevents every request from waiting on a known-unhealthy Redis connection and admits one
recovery probe after its cool-down. General cached reads may fall back to PostgreSQL only through
their configured concurrency bound. Authentication/OTP
issuance and other explicitly sensitive Redis-coordinated operations fail closed during a Redis
outage. Durable workers and PostgreSQL writes do not depend on Redis, so an outage cannot lose
committed work.

Actor/resource and actor/IP policies are evaluated by one Lua operation using Redis server time.
If any bucket denies the request, no bucket in that set consumes a token.

## Catalog and location boundary

Marketplace categories and Nigerian region lists are anonymous, globally shared metadata, so they
use response ETags and short browser/CDN cache directives instead of duplicating the same payload
in Redis. The location-sensitive home/provider response remains `no-store`; the marketplace loads
canonical categories separately and in parallel.

Google place, forward-geocoding, and reverse-geocoding responses are not persisted in the general
Redis cache. Identical concurrent lookups are coalesced inside each API process using only a
SHA-256-derived in-flight key. Each caller still receives its own protected, 30-minute PostgreSQL
location token, and Nigerian state/LGA assignment continues to use Tellbook's local polygons.
