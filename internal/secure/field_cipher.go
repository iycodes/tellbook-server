package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const keySize = 32

var (
	ErrInvalidKeyring    = errors.New("invalid encryption keyring")
	ErrUnknownKeyVersion = errors.New("unknown encryption key version")
)

type Ciphertext struct {
	KeyVersion string
	Nonce      []byte
	Data       []byte
}

type Keyring struct {
	activeVersion string
	keys          map[string][]byte
}

// ParseKeyring parses a JSON object whose values are base64-encoded AES-256 keys.
func ParseKeyring(rawJSON, activeVersion string) (*Keyring, error) {
	var encoded map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(rawJSON)), &encoded); err != nil {
		return nil, fmt.Errorf("%w: decode JSON: %v", ErrInvalidKeyring, err)
	}
	if len(encoded) == 0 {
		return nil, fmt.Errorf("%w: at least one key is required", ErrInvalidKeyring)
	}

	keys := make(map[string][]byte, len(encoded))
	for version, value := range encoded {
		version = strings.TrimSpace(version)
		if version == "" {
			return nil, fmt.Errorf("%w: key version is empty", ErrInvalidKeyring)
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
		if err != nil || len(decoded) != keySize {
			return nil, fmt.Errorf("%w: version %q must contain a base64-encoded 32-byte key", ErrInvalidKeyring, version)
		}
		keys[version] = append([]byte(nil), decoded...)
	}

	activeVersion = strings.TrimSpace(activeVersion)
	if _, ok := keys[activeVersion]; !ok {
		return nil, fmt.Errorf("%w: active version %q is unavailable", ErrInvalidKeyring, activeVersion)
	}
	return &Keyring{activeVersion: activeVersion, keys: keys}, nil
}

func (k *Keyring) Encrypt(plaintext, additionalData []byte) (Ciphertext, error) {
	if k == nil {
		return Ciphertext{}, ErrInvalidKeyring
	}
	if len(plaintext) == 0 {
		return Ciphertext{}, errors.New("plaintext is required")
	}
	if len(additionalData) == 0 {
		return Ciphertext{}, errors.New("additional authenticated data is required")
	}

	aead, err := newAEAD(k.keys[k.activeVersion])
	if err != nil {
		return Ciphertext{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Ciphertext{}, fmt.Errorf("generate encryption nonce: %w", err)
	}
	data := aead.Seal(nil, nonce, plaintext, additionalData)
	return Ciphertext{KeyVersion: k.activeVersion, Nonce: nonce, Data: data}, nil
}

func (k *Keyring) Decrypt(value Ciphertext, additionalData []byte) ([]byte, error) {
	if k == nil {
		return nil, ErrInvalidKeyring
	}
	key, ok := k.keys[strings.TrimSpace(value.KeyVersion)]
	if !ok {
		return nil, ErrUnknownKeyVersion
	}
	if len(additionalData) == 0 {
		return nil, errors.New("additional authenticated data is required")
	}

	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	if len(value.Nonce) != aead.NonceSize() {
		return nil, errors.New("invalid encryption nonce")
	}
	plaintext, err := aead.Open(nil, value.Nonce, value.Data, additionalData)
	if err != nil {
		return nil, errors.New("decrypt data: authentication failed")
	}
	return plaintext, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create data cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create data AEAD: %w", err)
	}
	return aead, nil
}

type Fingerprinter struct {
	key []byte
}

func NewFingerprinter(encodedKey string) (*Fingerprinter, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedKey))
	if err != nil || len(key) != keySize {
		return nil, errors.New("fingerprint key must be a base64-encoded 32-byte key")
	}
	return &Fingerprinter{key: append([]byte(nil), key...)}, nil
}

func (f *Fingerprinter) Sum(value, context []byte) (string, error) {
	if f == nil || len(f.key) != keySize {
		return "", errors.New("fingerprinter is not configured")
	}
	if len(value) == 0 || len(context) == 0 {
		return "", errors.New("fingerprint value and context are required")
	}
	mac := hmac.New(sha256.New, f.key)
	_, _ = mac.Write(context)
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(value)
	return hex.EncodeToString(mac.Sum(nil)), nil
}
