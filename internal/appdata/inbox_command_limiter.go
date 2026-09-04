package appdata

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"booking/go-server/internal/redisstore"

	"github.com/google/uuid"
)

type inboxCommandClass string

const (
	inboxCommandCreate  inboxCommandClass = "create"
	inboxCommandSend    inboxCommandClass = "send"
	inboxCommandRead    inboxCommandClass = "read"
	inboxCommandArchive inboxCommandClass = "archive"
	inboxCommandAI      inboxCommandClass = "ai"
)

type inboxCommandPolicy struct {
	actorPerMinute    int
	actorBurst        int
	resourcePerMinute int
	resourceBurst     int
	sharedResource    bool
}

var inboxCommandPolicies = map[inboxCommandClass]inboxCommandPolicy{
	inboxCommandCreate:  {actorPerMinute: 30, actorBurst: 10, resourcePerMinute: 12, resourceBurst: 4},
	inboxCommandSend:    {actorPerMinute: 120, actorBurst: 30, resourcePerMinute: 40, resourceBurst: 12, sharedResource: true},
	inboxCommandRead:    {actorPerMinute: 240, actorBurst: 60, resourcePerMinute: 120, resourceBurst: 30},
	inboxCommandArchive: {actorPerMinute: 60, actorBurst: 20, resourcePerMinute: 30, resourceBurst: 10},
	inboxCommandAI:      {actorPerMinute: 6, actorBurst: 2, resourcePerMinute: 4, resourceBurst: 2},
}

type inboxCommandBucket struct {
	tokens     float64
	lastRefill time.Time
	lastSeen   time.Time
}

type InboxCommandLimiter struct {
	mu          sync.Mutex
	buckets     map[string]*inboxCommandBucket
	lastCleanup time.Time
	maxBuckets  int
	shared      interface {
		AllowTokenBuckets(context.Context, ...redisstore.TokenBucketRequest) (bool, time.Duration, error)
	}
}

func NewInboxCommandLimiter(shared ...interface {
	AllowTokenBuckets(context.Context, ...redisstore.TokenBucketRequest) (bool, time.Duration, error)
}) *InboxCommandLimiter {
	limiter := &InboxCommandLimiter{
		buckets:     make(map[string]*inboxCommandBucket),
		lastCleanup: time.Now(),
		maxBuckets:  100_000,
	}
	if len(shared) > 0 {
		limiter.shared = shared[0]
	}
	return limiter
}

func (limiter *InboxCommandLimiter) Allow(
	class inboxCommandClass,
	actorType string,
	actorID uuid.UUID,
	resourceID uuid.UUID,
	now time.Time,
) (bool, time.Duration) {
	return limiter.allowLocal(class, actorType, actorID, resourceID, now)
}

func (limiter *InboxCommandLimiter) AllowContext(
	ctx context.Context,
	class inboxCommandClass,
	actorType string,
	actorID uuid.UUID,
	resourceID uuid.UUID,
) (bool, time.Duration) {
	if limiter == nil {
		return true, 0
	}
	policy, ok := inboxCommandPolicies[class]
	if !ok || actorID == uuid.Nil || resourceID == uuid.Nil {
		return false, time.Minute
	}
	actorKey := string(class) + ":actor:" + actorType + ":" + actorID.String()
	resourceKey := string(class) + ":resource:"
	if !policy.sharedResource {
		resourceKey += actorType + ":" + actorID.String() + ":"
	}
	resourceKey += resourceID.String()
	if limiter.shared == nil {
		return limiter.allowLocal(class, actorType, actorID, resourceID, time.Now())
	}
	allowed, retryAfter, err := limiter.shared.AllowTokenBuckets(ctx,
		redisstore.TokenBucketRequest{
			Scope: "inbox_" + string(class) + "_actor", Identity: actorKey,
			PerMinute: policy.actorPerMinute, Burst: policy.actorBurst,
		},
		redisstore.TokenBucketRequest{
			Scope: "inbox_" + string(class) + "_resource", Identity: resourceKey,
			PerMinute: policy.resourcePerMinute, Burst: policy.resourceBurst,
		},
	)
	if err != nil {
		return limiter.allowLocal(class, actorType, actorID, resourceID, time.Now())
	}
	return allowed, retryAfter
}

func (limiter *InboxCommandLimiter) allowLocal(
	class inboxCommandClass,
	actorType string,
	actorID uuid.UUID,
	resourceID uuid.UUID,
	now time.Time,
) (bool, time.Duration) {
	if limiter == nil {
		return true, 0
	}
	policy, ok := inboxCommandPolicies[class]
	if !ok || actorID == uuid.Nil || resourceID == uuid.Nil {
		return false, time.Minute
	}
	actorKey := string(class) + ":actor:" + actorType + ":" + actorID.String()
	resourceKey := string(class) + ":resource:"
	if !policy.sharedResource {
		resourceKey += actorType + ":" + actorID.String() + ":"
	}
	resourceKey += resourceID.String()

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if now.Sub(limiter.lastCleanup) >= time.Minute {
		for key, bucket := range limiter.buckets {
			if now.Sub(bucket.lastSeen) > 15*time.Minute {
				delete(limiter.buckets, key)
			}
		}
		limiter.lastCleanup = now
	}
	newBuckets := 0
	if limiter.buckets[actorKey] == nil {
		newBuckets++
	}
	if limiter.buckets[resourceKey] == nil {
		newBuckets++
	}
	if len(limiter.buckets)+newBuckets > limiter.maxBuckets {
		return false, time.Minute
	}
	actorBucket := limiter.refill(actorKey, policy.actorPerMinute, policy.actorBurst, now)
	resourceBucket := limiter.refill(
		resourceKey, policy.resourcePerMinute, policy.resourceBurst, now,
	)
	actorWait := inboxBucketWait(actorBucket, policy.actorPerMinute)
	resourceWait := inboxBucketWait(resourceBucket, policy.resourcePerMinute)
	if actorWait > 0 || resourceWait > 0 {
		return false, max(actorWait, resourceWait)
	}
	actorBucket.tokens--
	resourceBucket.tokens--
	return true, 0
}

func (limiter *InboxCommandLimiter) refill(
	key string,
	perMinute int,
	burst int,
	now time.Time,
) *inboxCommandBucket {
	bucket := limiter.buckets[key]
	if bucket == nil {
		bucket = &inboxCommandBucket{tokens: float64(burst), lastRefill: now}
		limiter.buckets[key] = bucket
	}
	refillPerSecond := float64(perMinute) / 60
	bucket.tokens = math.Min(
		float64(burst), bucket.tokens+now.Sub(bucket.lastRefill).Seconds()*refillPerSecond,
	)
	bucket.lastRefill = now
	bucket.lastSeen = now
	return bucket
}

func inboxBucketWait(bucket *inboxCommandBucket, perMinute int) time.Duration {
	if bucket.tokens >= 1 {
		return 0
	}
	wait := time.Duration(math.Ceil((1-bucket.tokens)/(float64(perMinute)/60))) * time.Second
	if wait < time.Second {
		return time.Second
	}
	return wait
}

func (h *Handler) enforceInboxCommandLimit(
	ctx context.Context,
	w http.ResponseWriter,
	class inboxCommandClass,
	actorType string,
	actorID uuid.UUID,
	resourceID uuid.UUID,
) bool {
	allowed, retryAfter := h.inboxCommands.AllowContext(ctx, class, actorType, actorID, resourceID)
	if allowed {
		return true
	}
	if h.inboxMetrics != nil {
		h.inboxMetrics.commandRateLimited.Add(1)
	}
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(retryAfter.Seconds())))))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many inbox updates. Please wait and try again.")
	return false
}
