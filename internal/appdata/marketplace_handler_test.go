package appdata

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMarketplaceFloatQueryRejectsNonFiniteValues(t *testing.T) {
	for _, raw := range []string{"NaN", "+Inf", "-Inf"} {
		t.Run(raw, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			value, ok := marketplaceFloatQuery(recorder, raw, "minimum_rating", 0, 0, 5)
			if ok || value != 0 {
				t.Fatalf("marketplaceFloatQuery(%q) = (%v, %v), want rejected", raw, value, ok)
			}
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
		})
	}

	recorder := httptest.NewRecorder()
	value, ok := marketplaceFloatQuery(recorder, "4.5", "minimum_rating", 0, 0, 5)
	if !ok || math.Abs(value-4.5) > 0.001 {
		t.Fatalf("valid rating = (%v, %v), want (4.5, true)", value, ok)
	}
}
