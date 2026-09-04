package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrustedRealIPMiddlewareRejectsSpoofedForwardingHeaders(t *testing.T) {
	handler := trustedRealIPMiddleware([]string{"127.0.0.1/32"})(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(requestClientIP(r)))
		},
	))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "192.0.2.10:4000"
	request.Header.Set("X-Forwarded-For", "198.51.100.20")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)
	if got := recorder.Body.String(); got != "192.0.2.10" {
		t.Fatalf("client IP = %q, want socket peer", got)
	}
}

func TestTrustedRealIPMiddlewareAcceptsConfiguredProxy(t *testing.T) {
	handler := trustedRealIPMiddleware([]string{"127.0.0.1/32"})(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(requestClientIP(r)))
		},
	))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "127.0.0.1:4000"
	request.Header.Set("X-Forwarded-For", "198.51.100.20, 127.0.0.1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)
	if got := recorder.Body.String(); got != "198.51.100.20" {
		t.Fatalf("client IP = %q, want forwarded address", got)
	}
}
