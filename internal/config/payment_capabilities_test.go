package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestPaymentCapabilityLists(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        []string
		invalid     bool
	}{
		{name: "disabled by default"},
		{name: "one capability", input: "card", want: []string{"card"}},
		{name: "normalize and deduplicate", input: " CARD, bank_transfer, card, destination, payout ", want: []string{"card", "bank_transfer", "destination", "payout"}},
		{name: "unknown capability", input: "card,bank_transfers", invalid: true},
		{name: "no wildcard", input: "all", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredConfig(t)
			t.Setenv("PAYSTACK_ENABLED_CAPABILITIES", tc.input)
			got, err := loadPaymentCapabilities("PAYSTACK")
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), "PAYSTACK_ENABLED_CAPABILITIES") {
					t.Fatalf("expected actionable validation error, got %v", err)
				}
				if _, err := Load(); err == nil {
					t.Fatal("Load accepted an invalid list")
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
			cfg := Config{PaystackEnabledCapabilities: got}
			if cfg.PaymentCapabilityEnabled("payaza", "card") || cfg.PaymentCapabilityEnabled("unknown", "card") {
				t.Fatal("selection leaked across providers")
			}
			if cfg.AnyPaymentCapabilityEnabled() != (len(tc.want) > 0) {
				t.Fatal("incorrect financial security gate")
			}
		})
	}
}

func TestLoadRequiresMigrationOfLegacyPaymentFlags(t *testing.T) {
	for _, provider := range []string{"PAYAZA", "PAYSTACK"} {
		for _, capability := range []string{"CARD", "BANK_TRANSFER", "DESTINATION", "PAYOUT"} {
			for _, suffix := range []string{"SANDBOX_VERIFIED", "PRODUCTION_ENABLED"} {
				key := provider + "_" + capability + "_" + suffix
				for _, value := range []string{"true", "false"} {
					t.Run(key+"="+value, func(t *testing.T) {
						setRequiredConfig(t)
						t.Setenv(key, value)
						_, err := Load()
						if err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), provider+"_ENABLED_CAPABILITIES") {
							t.Fatalf("expected migration error, got %v", err)
						}
					})
				}
			}
		}
	}
}

func TestAllEnabledCapabilitiesRequireFinancialSecurity(t *testing.T) {
	for _, provider := range []string{"PAYAZA", "PAYSTACK"} {
		for _, capability := range []string{"card", "bank_transfer", "destination", "payout"} {
			t.Run(provider+"/"+capability, func(t *testing.T) {
				setRequiredConfig(t)
				t.Setenv(provider+"_ENABLED_CAPABILITIES", capability)
				_, err := Load()
				if err == nil || !strings.Contains(err.Error(), "financial data encryption must be configured") {
					t.Fatalf("expected financial security requirement, got %v", err)
				}
			})
		}
	}
}
