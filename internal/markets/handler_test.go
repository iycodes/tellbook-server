package markets

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerReturnsCacheablePublicCatalog(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/v1/meta/markets", nil)
	recorder := httptest.NewRecorder()
	Handler(DefaultCatalog()).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if recorder.Header().Get("ETag") == "" {
		t.Fatal("ETag header is missing")
	}
	if !strings.Contains(recorder.Header().Get("Cache-Control"), "max-age=3600") {
		t.Fatalf("Cache-Control = %q", recorder.Header().Get("Cache-Control"))
	}
	if strings.Contains(recorder.Body.String(), "ProviderParameters") || strings.Contains(recorder.Body.String(), "provider_parameters") {
		t.Fatal("response exposed internal provider parameters")
	}

	var response catalogResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Version != CatalogVersion || len(response.Items) != 5 {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestHandlerHonorsIfNoneMatch(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/v1/meta/markets", nil)
	request.Header.Set("If-None-Match", DefaultCatalog().ETag())
	recorder := httptest.NewRecorder()
	Handler(DefaultCatalog()).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotModified)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("304 response body = %q", recorder.Body.String())
	}
}
