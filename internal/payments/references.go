package payments

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
)

const (
	paymentReferencePrefix       = "pay-"
	payoutReferencePrefix        = "pout-"
	legacyPaymentReferencePrefix = "pay_"
	legacyPayoutReferencePrefix  = "pout_"
	providerReferenceRandomBytes = 20
)

func newProviderReference(prefix string) (string, error) {
	if prefix != paymentReferencePrefix && prefix != payoutReferencePrefix {
		return "", errors.New("invalid provider reference prefix")
	}
	random := make([]byte, providerReferenceRandomBytes)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(random), nil
}

func isPaymentReference(reference string) bool {
	reference = strings.TrimSpace(reference)
	return strings.HasPrefix(reference, paymentReferencePrefix) ||
		strings.HasPrefix(reference, legacyPaymentReferencePrefix)
}

func isPayoutReference(reference string) bool {
	reference = strings.TrimSpace(reference)
	return strings.HasPrefix(reference, payoutReferencePrefix) ||
		strings.HasPrefix(reference, legacyPayoutReferencePrefix)
}
