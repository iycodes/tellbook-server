package publictoken

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

const byteLength = 32

// New returns a URL-safe token with 256 bits of cryptographic randomness.
func New() (string, error) {
	raw := make([]byte, byteLength)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate public token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
