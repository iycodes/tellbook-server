package appdata

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMarketplaceCustomerCursorRoundTrip(t *testing.T) {
	wantTime := time.Date(2026, time.August, 31, 12, 13, 14, 987654321, time.UTC)
	wantID := uuid.MustParse("4de64e07-5fbd-4562-a634-c00d3c4ee459")

	got, err := decodeMarketplaceCustomerCursor(encodeMarketplaceCustomerCursor(wantTime, wantID))
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if !got.CreatedAt.Equal(wantTime) || got.ID != wantID {
		t.Fatalf("decoded cursor = %#v, want %s and %s", got, wantTime, wantID)
	}
}

func TestMarketplaceCustomerCursorRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{"not-base64!", "dG9vLWZldy1wYXJ0cw", "MjAyNi0wOC0zMXxiYWQtaWQ"} {
		if _, err := decodeMarketplaceCustomerCursor(raw); !errors.Is(err, ErrMarketplaceCustomerCursor) {
			t.Fatalf("decode %q error = %v, want ErrMarketplaceCustomerCursor", raw, err)
		}
	}
}
