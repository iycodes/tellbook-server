package service

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"testing"

	"booking/go-server/internal/secure"

	"github.com/google/uuid"
)

func TestPublicTokenManagerGeneratesRecoverableOpaqueToken(t *testing.T) {
	manager := testTokenManager(t)
	clientID := uuid.New()
	agreementID := uuid.New()
	token, err := manager.Generate(clientID, agreementID)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !ValidPublicToken(token.Value) || len(token.Hash) != 32 || bytes.Equal(token.Hash, []byte(token.Value)) {
		t.Fatalf("invalid token: %+v", token)
	}
	recovered, err := manager.Recover(clientID, agreementID, token.Ciphertext)
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if recovered != token.Value || !bytes.Equal(HashPublicToken(recovered), token.Hash) {
		t.Fatal("recovered token does not match")
	}
}

func TestPublicTokenCiphertextIsBoundToTenantAndAgreement(t *testing.T) {
	manager := testTokenManager(t)
	clientID := uuid.New()
	agreementID := uuid.New()
	token, err := manager.Generate(clientID, agreementID)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if _, err := manager.Recover(uuid.New(), agreementID, token.Ciphertext); err == nil {
		t.Fatal("Recover() accepted a different client")
	}
	if _, err := manager.Recover(clientID, uuid.New(), token.Ciphertext); err == nil {
		t.Fatal("Recover() accepted a different agreement")
	}
}

func testTokenManager(t *testing.T) *PublicTokenManager {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	keyring, err := secure.ParseKeyring(fmt.Sprintf(`{"v1":%q}`, key), "v1")
	if err != nil {
		t.Fatalf("ParseKeyring() error = %v", err)
	}
	manager, err := NewPublicTokenManager(keyring)
	if err != nil {
		t.Fatalf("NewPublicTokenManager() error = %v", err)
	}
	return manager
}
