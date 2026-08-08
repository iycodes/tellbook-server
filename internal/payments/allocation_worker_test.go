package payments

import (
	"encoding/json"
	"testing"
)

func TestVerifiedCollectionFeeUsesProviderUnits(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		evidence string
		want     int64
	}{
		{name: "Paystack minor units", provider: "paystack", evidence: `{"fees":150}`, want: 150},
		{name: "Payaza decimal major units", provider: "payaza", evidence: `{"transaction_fee":1.50}`, want: 150},
		{name: "Payaza string decimal", provider: "payaza", evidence: `{"transaction_fee":"2.75"}`, want: 275},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := verifiedCollectionFee(test.provider, json.RawMessage(test.evidence), 2)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("fee = %d, want %d", got, test.want)
			}
		})
	}
}

func TestVerifiedCollectionFeeRejectsMissingOrFractionalMinorFee(t *testing.T) {
	for _, evidence := range []string{`{}`, `{"fees":1.5}`, `{"fees":"01"}`} {
		if _, err := verifiedCollectionFee("paystack", json.RawMessage(evidence), 2); err == nil {
			t.Fatalf("expected evidence %s to fail", evidence)
		}
	}
}
