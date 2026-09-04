package appdata

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestKeysetCursorRoundTripAndFilterBinding(t *testing.T) {
	type position struct {
		CreatedAt time.Time `json:"created_at"`
		ID        uuid.UUID `json:"id"`
	}
	want := position{CreatedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC), ID: uuid.New()}
	fingerprint := keysetFilterFingerprint("active", "query")
	encoded, err := encodeKeysetCursor(fingerprint, want)
	if err != nil {
		t.Fatal(err)
	}
	var got position
	if err := decodeKeysetCursor(encoded, fingerprint, &got); err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || got.ID != want.ID {
		t.Fatalf("decoded cursor = %+v, want %+v", got, want)
	}
	if err := decodeKeysetCursor(encoded, keysetFilterFingerprint("different"), &got); !errors.Is(err, ErrInvalidKeysetCursor) {
		t.Fatalf("filter mismatch error = %v, want ErrInvalidKeysetCursor", err)
	}
}

func TestKeysetCursorRejectsMalformedValues(t *testing.T) {
	for _, raw := range []string{"invalid", "e30", stringsOfLength(2049)} {
		var position map[string]any
		if err := decodeKeysetCursor(raw, keysetFilterFingerprint("all"), &position); !errors.Is(err, ErrInvalidKeysetCursor) {
			t.Fatalf("decode %q error = %v, want ErrInvalidKeysetCursor", raw, err)
		}
	}
}

func stringsOfLength(length int) string {
	value := make([]byte, length)
	for index := range value {
		value[index] = 'a'
	}
	return string(value)
}
