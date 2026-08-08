package service

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"booking/go-server/internal/publictoken"
	"booking/go-server/internal/secure"

	"github.com/google/uuid"
)

const publicTokenBytes = 32

type PublicToken struct {
	Value      string
	Hash       []byte
	Ciphertext secure.Ciphertext
}

type PublicTokenManager struct {
	keyring *secure.Keyring
}

func NewPublicTokenManager(keyring *secure.Keyring) (*PublicTokenManager, error) {
	if keyring == nil {
		return nil, errors.New("agreement token keyring is required")
	}
	return &PublicTokenManager{keyring: keyring}, nil
}

func (m *PublicTokenManager) Generate(clientID, agreementID uuid.UUID) (PublicToken, error) {
	if m == nil || m.keyring == nil {
		return PublicToken{}, errors.New("agreement token manager is not configured")
	}
	if err := validateTokenContext(clientID, agreementID); err != nil {
		return PublicToken{}, err
	}
	value, err := publictoken.New()
	if err != nil {
		return PublicToken{}, err
	}
	encrypted, err := m.keyring.Encrypt([]byte(value), publicTokenAAD(clientID, agreementID))
	if err != nil {
		return PublicToken{}, fmt.Errorf("encrypt agreement public token: %w", err)
	}
	return PublicToken{Value: value, Hash: HashPublicToken(value), Ciphertext: encrypted}, nil
}

func (m *PublicTokenManager) Recover(clientID, agreementID uuid.UUID, encrypted secure.Ciphertext) (string, error) {
	if m == nil || m.keyring == nil {
		return "", errors.New("agreement token manager is not configured")
	}
	if err := validateTokenContext(clientID, agreementID); err != nil {
		return "", err
	}
	plaintext, err := m.keyring.Decrypt(encrypted, publicTokenAAD(clientID, agreementID))
	if err != nil {
		return "", fmt.Errorf("decrypt agreement public token: %w", err)
	}
	value := string(plaintext)
	if !ValidPublicToken(value) {
		return "", errors.New("decrypted agreement public token is malformed")
	}
	return value, nil
}

func HashPublicToken(value string) []byte {
	digest := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return digest[:]
}

func ValidPublicToken(value string) bool {
	value = strings.TrimSpace(value)
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == publicTokenBytes && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func publicTokenAAD(clientID, agreementID uuid.UUID) []byte {
	return []byte("tellbook:agreement-public-token:v1:" + clientID.String() + ":" + agreementID.String())
}

func validateTokenContext(clientID, agreementID uuid.UUID) error {
	if clientID == uuid.Nil || agreementID == uuid.Nil {
		return errors.New("agreement and client IDs are required for public token encryption")
	}
	return nil
}
