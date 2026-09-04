package appdata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicMarketplaceMetadataUsesSharedCacheValidators(t *testing.T) {
	payload := map[string]any{"items": []MarketplaceCategory{{ID: "category-1", Name: "Beauty"}}}
	request := httptest.NewRequest(http.MethodGet, "/v1/marketplace/categories", nil)
	response := httptest.NewRecorder()
	if err := writePublicMarketplaceMetadata(response, request, payload); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Header().Get("Cache-Control") != marketplaceMetadataCacheControl {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
	if response.Header().Get("CDN-Cache-Control") != marketplaceMetadataCDNCacheControl {
		t.Fatalf("CDN-Cache-Control = %q", response.Header().Get("CDN-Cache-Control"))
	}
	vary := strings.Join(response.Header().Values("Vary"), ",")
	for _, expected := range []string{"Origin", "Authorization", "Cookie"} {
		if !strings.Contains(vary, expected) {
			t.Fatalf("Vary = %q, missing %q", vary, expected)
		}
	}
	etag := response.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag is missing")
	}

	conditional := httptest.NewRequest(http.MethodGet, "/v1/marketplace/categories", nil)
	conditional.Header.Set("If-None-Match", "W/"+etag)
	notModified := httptest.NewRecorder()
	if err := writePublicMarketplaceMetadata(notModified, conditional, payload); err != nil {
		t.Fatal(err)
	}
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("conditional response = %d, %q", notModified.Code, notModified.Body.String())
	}

	changed := httptest.NewRecorder()
	if err := writePublicMarketplaceMetadata(
		changed,
		httptest.NewRequest(http.MethodGet, "/v1/marketplace/categories", nil),
		map[string]any{"items": []MarketplaceCategory{{ID: "category-1", Name: "Wellness"}}},
	); err != nil {
		t.Fatal(err)
	}
	if changed.Header().Get("ETag") == etag {
		t.Fatal("ETag did not change with the metadata payload")
	}
}

func TestPublicMarketplaceMetadataDoesNotCacheCookieRequests(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/v1/marketplace/categories", nil)
	request.Header.Set("Cookie", "tellbook_marketplace_session=session")
	response := httptest.NewRecorder()
	if err := writePublicMarketplaceMetadata(response, request, map[string]any{"items": []string{"one"}}); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if got := response.Header().Get("Cache-Control"); got != privateNoStoreCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, privateNoStoreCacheControl)
	}
	if got := response.Header().Get("ETag"); got != "" {
		t.Fatalf("ETag = %q, want empty", got)
	}
}

func TestGoogleLocationResolutionCoalescesConcurrentIdenticalRequests(t *testing.T) {
	repository := NewRepository(nil)
	const callers = 24
	start := make(chan struct{})
	loaderEntered := make(chan struct{}, 1)
	releaseLoader := make(chan struct{})
	var loadCount atomic.Int32
	load := func(context.Context) (resolvedAddress, error) {
		if loadCount.Add(1) == 1 {
			loaderEntered <- struct{}{}
		}
		<-releaseLoader
		return resolvedAddress{FormattedAddress: "Lagos"}, nil
	}

	var ready sync.WaitGroup
	var complete sync.WaitGroup
	errorsFound := make(chan error, callers)
	ready.Add(callers)
	complete.Add(callers)
	for range callers {
		go func() {
			defer complete.Done()
			ready.Done()
			<-start
			address, err := repository.coalesceGoogleAddress(context.Background(), "address", "lagos", load)
			if err == nil && address.FormattedAddress != "Lagos" {
				err = context.Canceled
			}
			errorsFound <- err
		}()
	}
	ready.Wait()
	close(start)
	select {
	case <-loaderEntered:
	case <-time.After(time.Second):
		t.Fatal("coalesced loader did not start")
	}
	// Give callers released by the same barrier time to join the in-flight key.
	time.Sleep(20 * time.Millisecond)
	close(releaseLoader)
	complete.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count := loadCount.Load(); count != 1 {
		t.Fatalf("map provider loads = %d, want 1", count)
	}
}
