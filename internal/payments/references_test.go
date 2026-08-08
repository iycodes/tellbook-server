package payments

import (
	"regexp"
	"testing"
)

func TestProviderReferencesSatisfyCollectionAndPayoutContracts(t *testing.T) {
	paymentPattern := regexp.MustCompile(`^pay-[a-f0-9]{40}$`)
	payoutPattern := regexp.MustCompile(`^pout-[a-f0-9]{40}$`)
	paymentReference, err := newProviderReference(paymentReferencePrefix)
	if err != nil || !paymentPattern.MatchString(paymentReference) {
		t.Fatalf("payment reference = %q, error = %v", paymentReference, err)
	}
	payoutReference, err := newProviderReference(payoutReferencePrefix)
	if err != nil || !payoutPattern.MatchString(payoutReference) || len(payoutReference) > 50 {
		t.Fatalf("payout reference = %q, error = %v", payoutReference, err)
	}
}

func TestReferenceRecognitionPreservesProviderLinkedLegacyRecords(t *testing.T) {
	for _, reference := range []string{"pay-abc", "pay_abc"} {
		if !isPaymentReference(reference) {
			t.Fatalf("payment reference %q was not recognized", reference)
		}
	}
	for _, reference := range []string{"pout-abc", "pout_abc"} {
		if !isPayoutReference(reference) {
			t.Fatalf("payout reference %q was not recognized", reference)
		}
	}
}
