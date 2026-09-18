package admin

import (
	"bytes"
	"encoding/base32"
	"testing"
	"time"
)

func TestTOTPRFC6238AndReplay(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	for _, v := range []struct {
		timestamp int64
		code      string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}} {
		now := time.Unix(v.timestamp, 0)
		step, ok := verifyTOTP(secret, v.code, -1, now)
		if !ok {
			t.Fatalf("RFC vector %d failed", v.timestamp)
		}
		if _, ok = verifyTOTP(secret, v.code, step, now); ok {
			t.Fatal("TOTP replay accepted")
		}
		if _, ok = verifyTOTP(secret, v.code, -1, now.Add(2*time.Minute)); ok {
			t.Fatal("expired code accepted")
		}
	}
}
func TestRoleIsolation(t *testing.T) {
	for _, c := range []string{"businesses.read", "customers.read", "finance.read", "staff.manage"} {
		if allowed("analyst", c) {
			t.Fatalf("analyst received %s", c)
		}
	}
	if allowed("support", "finance.export") || allowed("operations", "finance.manage") || allowed("provider", "businesses.read") {
		t.Fatal("role boundary breached")
	}
	v := capabilities("analyst")
	v[0] = "staff.manage"
	if allowed("analyst", "staff.manage") {
		t.Fatal("caller mutated role policy")
	}
}
func TestRecoveryNormalization(t *testing.T) {
	if !bytes.Equal(recoveryHash(" abcd-efgh "), recoveryHash("ABCDEFGH")) {
		t.Fatal("recovery normalization")
	}
	if bytes.Equal(hashToken(randomToken()), hashToken(randomToken())) {
		t.Fatal("duplicate token")
	}
}
