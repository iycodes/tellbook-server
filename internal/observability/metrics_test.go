package observability

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestInstrumentHTTPClientUsesBoundedServiceAndOutcomeLabels(t *testing.T) {
	metrics := New()
	success := metrics.InstrumentHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	})}, "google_maps")
	request, err := http.NewRequest(http.MethodGet, "https://maps.example/location/private-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := success.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	failure := metrics.InstrumentHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	})}, "paystack")
	_, _ = failure.Do(request)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/internal/metrics", nil))
	body := recorder.Body.String()
	for _, want := range []string{
		`tellbook_external_requests_total{outcome="2xx",service="google_maps"} 1`,
		`tellbook_external_requests_total{outcome="network_error",service="paystack"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics output does not contain %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "private-token") {
		t.Fatal("external metrics leaked a raw request path")
	}
}

func TestDatabasePoolCollectorsRegisterDistinctPoolLabels(t *testing.T) {
	metrics := New()
	metrics.RegisterDatabasePool(nil)
	metrics.RegisterDirectDatabasePool(nil)
}
