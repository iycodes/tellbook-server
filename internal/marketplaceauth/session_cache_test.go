package marketplaceauth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"booking/go-server/internal/config"
	"booking/go-server/internal/redisstore"

	"github.com/google/uuid"
)

type fakeSessionRepository struct {
	mu        sync.Mutex
	principal SessionPrincipal
	loads     int
	hashes    [][]byte
	delay     time.Duration
	entered   chan struct{}
	release   chan struct{}
}

func (repo *fakeSessionRepository) SessionPrincipalByTokenHash(context.Context, []byte) (SessionPrincipal, error) {
	if repo.delay > 0 {
		time.Sleep(repo.delay)
	}
	if repo.entered != nil {
		select {
		case repo.entered <- struct{}{}:
		default:
		}
	}
	if repo.release != nil {
		<-repo.release
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.loads++
	return repo.principal, nil
}

func (repo *fakeSessionRepository) ActiveSessionTokenHashesPage(
	_ context.Context, _ uuid.UUID, after []byte, _ int,
) ([][]byte, error) {
	if len(after) > 0 {
		return nil, nil
	}
	return repo.hashes, nil
}

func (repo *fakeSessionRepository) loadCount() int {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	return repo.loads
}

type fakeSessionCache struct {
	mu      sync.Mutex
	values  map[string][]byte
	deletes []string
}

func newFakeSessionCache() *fakeSessionCache {
	return &fakeSessionCache{values: make(map[string][]byte)}
}

func (cache *fakeSessionCache) CacheGet(_ context.Context, scope, identity string, target any) error {
	cache.mu.Lock()
	payload, ok := cache.values[scope+":"+identity]
	cache.mu.Unlock()
	if !ok {
		return redisstore.ErrCacheMiss
	}
	return json.Unmarshal(payload, target)
}

func (cache *fakeSessionCache) CacheSet(_ context.Context, scope, identity string, value any, _ time.Duration) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	cache.mu.Lock()
	cache.values[scope+":"+identity] = payload
	cache.mu.Unlock()
	return nil
}

func (cache *fakeSessionCache) CacheDelete(_ context.Context, scope string, identities ...string) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for _, identity := range identities {
		delete(cache.values, scope+":"+identity)
		cache.deletes = append(cache.deletes, identity)
	}
	return nil
}

type fakeSessionCacheMetrics struct {
	mu       sync.Mutex
	outcomes map[string]int
}

func (metrics *fakeSessionCacheMetrics) ObserveCacheRequest(_, outcome string) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.outcomes[outcome]++
}

func TestAuthenticatePrincipalCachesOnlyMinimalPrincipal(t *testing.T) {
	principal := SessionPrincipal{
		SessionID: uuid.New(), CustomerID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour),
		SecurityRevision: 3, SessionRevision: 2,
	}
	repository := &fakeSessionRepository{principal: principal}
	cache := newFakeSessionCache()
	metrics := &fakeSessionCacheMetrics{outcomes: make(map[string]int)}
	service := NewService(nil, config.Config{AuthBcryptCost: 10}, nil)
	service.sessionRepo = repository
	service.ConfigureSessionCache(cache, 4, metrics)

	first, err := service.AuthenticatePrincipal(context.Background(), "opaque-token", false)
	if err != nil || !sameSessionPrincipal(first, principal) {
		t.Fatalf("first principal = %+v, %v", first, err)
	}
	second, err := service.AuthenticatePrincipal(context.Background(), "opaque-token", false)
	if err != nil || !sameSessionPrincipal(second, principal) {
		t.Fatalf("second principal = %+v, %v", second, err)
	}
	if repository.loadCount() != 1 {
		t.Fatalf("PostgreSQL principal loads = %d, want 1", repository.loadCount())
	}
	cache.mu.Lock()
	for _, payload := range cache.values {
		if string(payload) == "" || containsAny(string(payload), "email", "phone", "birthday", "full_name") {
			cache.mu.Unlock()
			t.Fatalf("cache contains profile data: %s", payload)
		}
	}
	cache.mu.Unlock()
	if metrics.outcomes["miss"] != 1 || metrics.outcomes["hit"] != 1 {
		t.Fatalf("cache outcomes = %+v", metrics.outcomes)
	}
}

func TestAuthenticatePrincipalFreshValidationBypassesPositiveCache(t *testing.T) {
	principal := SessionPrincipal{
		SessionID: uuid.New(), CustomerID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour),
		SecurityRevision: 1, SessionRevision: 1,
	}
	repository := &fakeSessionRepository{principal: principal}
	cache := newFakeSessionCache()
	service := NewService(nil, config.Config{AuthBcryptCost: 10}, nil)
	service.sessionRepo = repository
	service.ConfigureSessionCache(cache, 4, nil)
	if _, err := service.AuthenticatePrincipal(context.Background(), "opaque-token", false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticatePrincipal(context.Background(), "opaque-token", true); err != nil {
		t.Fatal(err)
	}
	if repository.loadCount() != 2 {
		t.Fatalf("fresh validation loads = %d, want 2", repository.loadCount())
	}
}

func TestSessionInvalidationDeletesEveryActiveTokenHash(t *testing.T) {
	repository := &fakeSessionRepository{hashes: [][]byte{{1, 2, 3}, {4, 5, 6}}}
	cache := newFakeSessionCache()
	service := NewService(nil, config.Config{AuthBcryptCost: 10}, nil)
	service.sessionRepo = repository
	service.ConfigureSessionCache(cache, 4, nil)
	if err := service.invalidateCustomerSessions(context.Background(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	if len(cache.deletes) != 2 {
		t.Fatalf("deleted session keys = %d, want 2", len(cache.deletes))
	}
}

func TestAuthenticatePrincipalCoalescesConcurrentCacheMisses(t *testing.T) {
	principal := SessionPrincipal{
		SessionID: uuid.New(), CustomerID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour),
		SecurityRevision: 1, SessionRevision: 1,
	}
	repository := &fakeSessionRepository{principal: principal, delay: 25 * time.Millisecond}
	cache := newFakeSessionCache()
	service := NewService(nil, config.Config{AuthBcryptCost: 10}, nil)
	service.sessionRepo = repository
	service.ConfigureSessionCache(cache, 4, nil)

	const callers = 24
	var wait sync.WaitGroup
	errorsFound := make(chan error, callers)
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.AuthenticatePrincipal(context.Background(), "same-token", false)
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	if repository.loadCount() != 1 {
		t.Fatalf("coalesced PostgreSQL loads = %d, want 1", repository.loadCount())
	}
}

func TestAuthenticatePrincipalBoundsPostgresFallbackConcurrency(t *testing.T) {
	principal := SessionPrincipal{
		SessionID: uuid.New(), CustomerID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour),
		SecurityRevision: 1, SessionRevision: 1,
	}
	repository := &fakeSessionRepository{
		principal: principal, entered: make(chan struct{}, 1), release: make(chan struct{}),
	}
	service := NewService(nil, config.Config{AuthBcryptCost: 10}, nil)
	service.sessionRepo = repository
	service.ConfigureSessionCache(newFakeSessionCache(), 1, nil)

	firstDone := make(chan error, 1)
	go func() {
		_, err := service.AuthenticatePrincipal(context.Background(), "first-token", false)
		firstDone <- err
	}()
	select {
	case <-repository.entered:
	case <-time.After(time.Second):
		t.Fatal("first PostgreSQL fallback did not start")
	}
	if _, err := service.AuthenticatePrincipal(context.Background(), "second-token", false); !errors.Is(err, ErrAuthUnavailable) {
		t.Fatalf("overflow fallback error = %v, want ErrAuthUnavailable", err)
	}
	close(repository.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func sameSessionPrincipal(left, right SessionPrincipal) bool {
	return left.SessionID == right.SessionID && left.CustomerID == right.CustomerID &&
		left.ExpiresAt.Equal(right.ExpiresAt) && left.SecurityRevision == right.SecurityRevision &&
		left.SessionRevision == right.SessionRevision
}
