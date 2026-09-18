package admin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // RFC 6238 authenticator compatibility; not used for password/token hashing.
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func hashToken(token string) []byte { v := sha256.Sum256([]byte(token)); return v[:] }
func newSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}
func totp(secret string, step int64) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		return ""
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 15
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}
func verifyTOTP(secret, code string, last int64, now time.Time) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	step := now.Unix() / 30
	for _, n := range []int64{step, step - 1, step + 1} {
		if n > last && subtle.ConstantTimeCompare([]byte(totp(secret, n)), []byte(code)) == 1 {
			return n, true
		}
	}
	return 0, false
}
func recoveryHash(code string) []byte {
	return hashToken(strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", "")))
}

var roleCapabilities = map[string][]string{
	"super_admin": {"businesses.read", "businesses.note", "businesses.manage", "bookings.read", "bookings.note", "bookings.manage", "customers.read", "customers.note", "support.read", "support.manage", "finance.read", "finance.manage", "finance.export", "marketplace.manage", "agreements.manage", "notifications.manage", "ai.manage", "reports.read", "reports.export", "staff.manage", "audit.read", "audit.export", "settings.read", "health.read"},
	"operations":  {"businesses.read", "businesses.note", "businesses.manage", "bookings.read", "bookings.note", "bookings.manage", "customers.read", "customers.note", "support.read", "finance.context", "marketplace.manage", "agreements.manage", "notifications.read", "ai.manage", "reports.read", "reports.export", "health.read"},
	"support":     {"businesses.read", "businesses.note", "bookings.read", "bookings.note", "customers.read", "customers.note", "support.read", "support.manage", "finance.context", "notifications.manage", "reports.read"},
	"finance":     {"businesses.read", "businesses.note", "bookings.read", "bookings.note", "customers.read", "customers.note", "finance.read", "finance.manage", "finance.export", "reports.read", "reports.export"},
	"analyst":     {"reports.read", "reports.export"},
}

func capabilities(role string) []string { return append([]string{}, roleCapabilities[role]...) }
func allowed(role, capability string) bool {
	for _, v := range roleCapabilities[role] {
		if v == capability {
			return true
		}
	}
	return false
}
