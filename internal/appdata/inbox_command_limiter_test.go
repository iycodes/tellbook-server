package appdata

import (
	"context"
	"errors"
	"testing"
	"time"

	"booking/go-server/internal/redisstore"

	"github.com/google/uuid"
)

type inboxSharedLimitCall struct {
	scope, identity  string
	perMinute, burst int
}

type fakeInboxSharedLimiter struct {
	err   error
	calls []inboxSharedLimitCall
}

func (limiter *fakeInboxSharedLimiter) AllowTokenBuckets(
	_ context.Context, requests ...redisstore.TokenBucketRequest,
) (bool, time.Duration, error) {
	for _, request := range requests {
		limiter.calls = append(limiter.calls, inboxSharedLimitCall{
			scope: request.Scope, identity: request.Identity,
			perMinute: request.PerMinute, burst: request.Burst,
		})
	}
	return true, 0, limiter.err
}

func TestInboxCommandLimiterConsumesActorAndResourceAtomically(t *testing.T) {
	limiter := NewInboxCommandLimiter()
	actorID, conversationID := uuid.New(), uuid.New()
	now := time.Now()
	for range inboxCommandPolicies[inboxCommandSend].resourceBurst {
		allowed, _ := limiter.Allow(
			inboxCommandSend, "provider", actorID, conversationID, now,
		)
		if !allowed {
			t.Fatal("send was limited before the resource burst was consumed")
		}
	}
	if allowed, retryAfter := limiter.Allow(
		inboxCommandSend, "provider", actorID, conversationID, now,
	); allowed || retryAfter < time.Second {
		t.Fatalf("overflow allowed=%v retry=%v", allowed, retryAfter)
	}
	if allowed, _ := limiter.Allow(
		inboxCommandSend, "provider", actorID, uuid.New(), now,
	); !allowed {
		t.Fatal("one hot conversation exhausted the actor's larger burst")
	}
}

func TestInboxCommandLimiterBoundsFallbackBucketMemory(t *testing.T) {
	limiter := NewInboxCommandLimiter()
	limiter.maxBuckets = 1
	allowed, retryAfter := limiter.Allow(
		inboxCommandSend, "provider", uuid.New(), uuid.New(), time.Now(),
	)
	if allowed || retryAfter != time.Minute {
		t.Fatalf("bucket overflow = %v, %v", allowed, retryAfter)
	}
}

func TestInboxCommandLimiterSeparatesActorsAndRefills(t *testing.T) {
	limiter := NewInboxCommandLimiter()
	firstActor, secondActor, conversationID := uuid.New(), uuid.New(), uuid.New()
	now := time.Now()
	for range inboxCommandPolicies[inboxCommandArchive].resourceBurst {
		if allowed, _ := limiter.Allow(
			inboxCommandArchive, "provider", firstActor, conversationID, now,
		); !allowed {
			t.Fatal("archive was limited too early")
		}
	}
	if allowed, _ := limiter.Allow(
		inboxCommandArchive, "provider", secondActor, conversationID, now,
	); !allowed {
		t.Fatal("another actor inherited a foreign command limit")
	}
	if allowed, _ := limiter.Allow(
		inboxCommandArchive, "provider", firstActor, conversationID, now.Add(2*time.Second),
	); !allowed {
		t.Fatal("resource bucket did not refill")
	}
}

func TestInboxCommandLimiterSharesConversationSendLimitAcrossActors(t *testing.T) {
	limiter := NewInboxCommandLimiter()
	conversationID := uuid.New()
	now := time.Now()
	for range inboxCommandPolicies[inboxCommandSend].resourceBurst {
		if allowed, _ := limiter.Allow(
			inboxCommandSend, "provider", uuid.New(), conversationID, now,
		); !allowed {
			t.Fatal("shared conversation send limit was consumed too early")
		}
	}
	if allowed, _ := limiter.Allow(
		inboxCommandSend, "marketplace_customer", uuid.New(), conversationID, now,
	); allowed {
		t.Fatal("another actor bypassed the shared conversation send limit")
	}
}

func TestInboxCommandLimiterDoesNotShareProviderCreationLimitAcrossCustomers(t *testing.T) {
	limiter := NewInboxCommandLimiter()
	providerID := uuid.New()
	now := time.Now()
	for range 20 {
		if allowed, _ := limiter.Allow(
			inboxCommandCreate, "marketplace_customer", uuid.New(), providerID, now,
		); !allowed {
			t.Fatal("one customer's creation attempt limited another customer opening the same provider")
		}
	}
}

func TestInboxCommandLimiterUsesSharedActorAndResourceBuckets(t *testing.T) {
	shared := &fakeInboxSharedLimiter{}
	limiter := NewInboxCommandLimiter(shared)
	actorID, conversationID := uuid.New(), uuid.New()
	allowed, retryAfter := limiter.AllowContext(
		context.Background(), inboxCommandSend, "provider", actorID, conversationID,
	)
	if !allowed || retryAfter != 0 {
		t.Fatalf("AllowContext() = %v, %v", allowed, retryAfter)
	}
	if len(shared.calls) != 2 {
		t.Fatalf("shared calls = %d, want 2", len(shared.calls))
	}
	if shared.calls[0].scope != "inbox_send_actor" || shared.calls[0].identity != "send:actor:provider:"+actorID.String() {
		t.Fatalf("unexpected actor bucket: %+v", shared.calls[0])
	}
	if shared.calls[1].scope != "inbox_send_resource" || shared.calls[1].identity != "send:resource:"+conversationID.String() {
		t.Fatalf("unexpected resource bucket: %+v", shared.calls[1])
	}
}

func TestInboxCommandLimiterFallsBackLocallyWhenRedisIsUnavailable(t *testing.T) {
	shared := &fakeInboxSharedLimiter{err: errors.New("Redis unavailable")}
	limiter := NewInboxCommandLimiter(shared)
	actorID, conversationID := uuid.New(), uuid.New()
	for index := range inboxCommandPolicies[inboxCommandAI].resourceBurst + 1 {
		allowed, retryAfter := limiter.AllowContext(
			context.Background(), inboxCommandAI, "provider", actorID, conversationID,
		)
		if index < inboxCommandPolicies[inboxCommandAI].resourceBurst && !allowed {
			t.Fatalf("fallback request %d was limited early", index+1)
		}
		if index == inboxCommandPolicies[inboxCommandAI].resourceBurst && (allowed || retryAfter <= 0) {
			t.Fatalf("fallback overflow = %v, %v", allowed, retryAfter)
		}
	}
}
