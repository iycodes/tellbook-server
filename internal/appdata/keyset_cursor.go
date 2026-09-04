package appdata

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const keysetCursorVersion = 1

var ErrInvalidKeysetCursor = errors.New("invalid pagination cursor")

type keysetCursorEnvelope struct {
	Version     int             `json:"v"`
	Fingerprint string          `json:"f"`
	Position    json.RawMessage `json:"p"`
}

func keysetFilterFingerprint(parts ...string) string {
	normalized := make([]string, len(parts))
	for index, part := range parts {
		normalized[index] = strings.TrimSpace(strings.ToLower(part))
	}
	sum := sha256.Sum256([]byte(strings.Join(normalized, "\x00")))
	return hex.EncodeToString(sum[:16])
}

func encodeKeysetCursor(fingerprint string, position any) (string, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return "", ErrInvalidKeysetCursor
	}
	payload, err := json.Marshal(position)
	if err != nil {
		return "", ErrInvalidKeysetCursor
	}
	envelope, err := json.Marshal(keysetCursorEnvelope{
		Version: keysetCursorVersion, Fingerprint: fingerprint, Position: payload,
	})
	if err != nil {
		return "", ErrInvalidKeysetCursor
	}
	return base64.RawURLEncoding.EncodeToString(envelope), nil
}

func decodeKeysetCursor(raw, fingerprint string, position any) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if len(raw) > 2048 || strings.TrimSpace(fingerprint) == "" || position == nil {
		return ErrInvalidKeysetCursor
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return ErrInvalidKeysetCursor
	}
	var envelope keysetCursorEnvelope
	if json.Unmarshal(decoded, &envelope) != nil || envelope.Version != keysetCursorVersion ||
		envelope.Fingerprint != fingerprint || len(envelope.Position) == 0 {
		return ErrInvalidKeysetCursor
	}
	if err := json.Unmarshal(envelope.Position, position); err != nil {
		return ErrInvalidKeysetCursor
	}
	return nil
}
