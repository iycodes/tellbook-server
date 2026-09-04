package appdata

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteRevisionedPublicJSONCachesOnlyAnonymousRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		header      string
		value       string
		cacheable   bool
		wantControl string
		wantETag    bool
	}{
		{name: "anonymous", cacheable: true, wantControl: publicProviderCacheControl, wantETag: true},
		{name: "authorization", header: "Authorization", value: "Bearer token", cacheable: true, wantControl: privateNoStoreCacheControl},
		{name: "cookie", header: "Cookie", value: "tellbook_marketplace_session=session", cacheable: true, wantControl: privateNoStoreCacheControl},
		{name: "sensitive resource", cacheable: false, wantControl: privateNoStoreCacheControl},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, "/v1/public/clients/demo?include=services", nil)
			if test.header != "" {
				request.Header.Set(test.header, test.value)
			}
			response := httptest.NewRecorder()
			if err := writeRevisionedPublicJSON(response, request, map[string]string{"value": "ok"}, "public-profile", []string{"7"}, publicProviderCacheControl, test.cacheable); err != nil {
				t.Fatalf("write response: %v", err)
			}
			if got := response.Header().Get("Cache-Control"); got != test.wantControl {
				t.Fatalf("Cache-Control = %q, want %q", got, test.wantControl)
			}
			if got := response.Header().Get("ETag") != ""; got != test.wantETag {
				t.Fatalf("ETag presence = %t, want %t", got, test.wantETag)
			}
			if test.wantETag {
				if got := response.Header().Get("CDN-Cache-Control"); got != publicProviderCDNCacheControl {
					t.Fatalf("CDN-Cache-Control = %q, want %q", got, publicProviderCDNCacheControl)
				}
				vary := response.Header().Values("Vary")
				joined := strings.Join(vary, ",")
				for _, expected := range []string{"Origin", "Authorization", "Cookie"} {
					if !strings.Contains(joined, expected) {
						t.Fatalf("Vary = %q, missing %q", joined, expected)
					}
				}
			}
		})
	}
}

func TestKnownRevisionCanReturnNotModifiedBeforePayloadSerialization(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/v1/public/clients/demo?include=services", nil)
	etag := knownRevisionETag(request, "public-profile", []string{"7"})
	request.Header.Set("If-None-Match", etag)
	response := httptest.NewRecorder()

	if !writeKnownRevisionNotModified(response, request, "public-profile", []string{"7"}, publicProviderCacheControl) {
		t.Fatal("matching known revision was not handled")
	}
	if response.Code != http.StatusNotModified || response.Body.Len() != 0 {
		t.Fatalf("response = status %d body %q, want empty 304", response.Code, response.Body.String())
	}
	if got := response.Header().Get("CDN-Cache-Control"); got != publicProviderCDNCacheControl {
		t.Fatalf("CDN-Cache-Control = %q, want %q", got, publicProviderCDNCacheControl)
	}
}

func TestKnownRevisionETagChangesWithRevisionAndRequest(t *testing.T) {
	t.Parallel()

	firstRequest := httptest.NewRequest(http.MethodGet, "/v1/public/clients/demo", nil)
	secondRequest := httptest.NewRequest(http.MethodGet, "/v1/public/clients/demo?include=services", nil)
	first := knownRevisionETag(firstRequest, "public-profile", []string{"7"})
	if got := knownRevisionETag(firstRequest, "public-profile", []string{"8"}); got == first {
		t.Fatalf("revision change retained ETag %q", got)
	}
	if got := knownRevisionETag(secondRequest, "public-profile", []string{"7"}); got == first {
		t.Fatalf("request URI change retained ETag %q", got)
	}
}

func TestWriteRevisionedPublicJSONReturnsNotModifiedForMatchingETag(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/v1/marketplace/providers?category_id=category", nil)
	first := httptest.NewRecorder()
	if err := writeRevisionedPublicJSON(first, request, map[string]string{"value": "ok"}, "marketplace-providers", []string{"9"}, publicDiscoveryCacheControl, true); err != nil {
		t.Fatalf("write first response: %v", err)
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/marketplace/providers?category_id=category", nil)
	request.Header.Set("If-None-Match", first.Header().Get("ETag"))
	second := httptest.NewRecorder()
	if err := writeRevisionedPublicJSON(second, request, map[string]string{"value": "ok"}, "marketplace-providers", []string{"9"}, publicDiscoveryCacheControl, true); err != nil {
		t.Fatalf("write conditional response: %v", err)
	}
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want %d", second.Code, http.StatusNotModified)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 body = %q, want empty", second.Body.String())
	}
}

func TestWriteRevisionedPublicJSONScopesETagToRequestURI(t *testing.T) {
	t.Parallel()

	write := func(target string) string {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		if err := writeRevisionedPublicJSON(response, request, map[string]string{"value": "ok"}, "public-resource", []string{"3"}, publicProviderCacheControl, true); err != nil {
			t.Fatalf("write response: %v", err)
		}
		return response.Header().Get("ETag")
	}
	if first, second := write("/resource?page=1"), write("/resource?page=2"); first == second {
		t.Fatalf("different request URIs produced the same ETag %q", first)
	}
}
