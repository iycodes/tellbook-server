package secure

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"testing"
)

func encodedKey(fill byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, keySize))
}

func TestKeyringEncryptDecryptAndRotate(t *testing.T) {
	t.Parallel()
	raw := fmt.Sprintf(`{"v1":%q,"v2":%q}`, encodedKey(1), encodedKey(2))
	keyring, err := ParseKeyring(raw, "v2")
	if err != nil {
		t.Fatalf("ParseKeyring() error = %v", err)
	}
	aad := []byte("agreement:client-1:agreement-1")
	value, err := keyring.Encrypt([]byte("public-token"), aad)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if value.KeyVersion != "v2" {
		t.Fatalf("KeyVersion = %q", value.KeyVersion)
	}
	plaintext, err := keyring.Decrypt(value, aad)
	if err != nil || string(plaintext) != "public-token" {
		t.Fatalf("Decrypt() = %q, %v", plaintext, err)
	}
}

func TestKeyringRejectsWrongContextAndTampering(t *testing.T) {
	t.Parallel()
	keyring, err := ParseKeyring(fmt.Sprintf(`{"v1":%q}`, encodedKey(3)), "v1")
	if err != nil {
		t.Fatalf("ParseKeyring() error = %v", err)
	}
	value, err := keyring.Encrypt([]byte("value"), []byte("context-a"))
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if _, err := keyring.Decrypt(value, []byte("context-b")); err == nil {
		t.Fatal("Decrypt() accepted the wrong context")
	}
	value.Data[0] ^= 0xff
	if _, err := keyring.Decrypt(value, []byte("context-a")); err == nil {
		t.Fatal("Decrypt() accepted tampered data")
	}
}

func TestFingerprinterIsKeyedAndContextBound(t *testing.T) {
	t.Parallel()
	fingerprinter, err := NewFingerprinter(encodedKey(4))
	if err != nil {
		t.Fatalf("NewFingerprinter() error = %v", err)
	}
	first, err := fingerprinter.Sum([]byte("value"), []byte("context-a"))
	if err != nil {
		t.Fatalf("Sum() error = %v", err)
	}
	second, err := fingerprinter.Sum([]byte("value"), []byte("context-b"))
	if err != nil {
		t.Fatalf("Sum() error = %v", err)
	}
	if first == second || len(first) != 64 {
		t.Fatalf("fingerprints = %q and %q", first, second)
	}
}
