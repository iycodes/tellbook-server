package publictoken

import (
	"encoding/base64"
	"testing"
)

func TestNewProducesURLSafeRandomToken(t *testing.T) {
	t.Parallel()

	first, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	second, err := New()
	if err != nil {
		t.Fatalf("New() second error = %v", err)
	}
	if first == second {
		t.Fatal("New() returned duplicate tokens")
	}

	decoded, err := base64.RawURLEncoding.DecodeString(first)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	if len(decoded) != byteLength {
		t.Fatalf("decoded token length = %d, want %d", len(decoded), byteLength)
	}
}
