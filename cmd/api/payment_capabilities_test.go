package main

import (
	"testing"

	"booking/go-server/internal/config"
	"booking/go-server/internal/payments/capabilities"
)

func TestPaymentCapabilitySelectionIsScopedToDeployment(t *testing.T) {
	for _, environment := range []string{"test", "live"} {
		for _, provider := range []string{"payaza", "paystack"} {
			for _, capability := range []string{"card", "bank_transfer", "destination", "payout"} {
				t.Run(environment+"/"+provider+"/"+capability, func(t *testing.T) {
					cfg := config.Config{PaymentsEnvironment: environment}
					if provider == "payaza" {
						cfg.PayazaEnabledCapabilities = []string{capability}
					} else {
						cfg.PaystackEnabledCapabilities = []string{capability}
					}
					for _, configured := range []bool{false, true} {
						ready := paymentCapabilityReadiness(cfg, configured, provider, capability)
						readiness := capabilities.ProviderReadiness{}
						if provider == "payaza" {
							readiness.PayazaCard, readiness.PayazaBankTransfer, readiness.PayazaDestination, readiness.PayazaPayout = ready, ready, ready, ready
						} else {
							readiness.PaystackCard, readiness.PaystackBankTransfer, readiness.PaystackDestination, readiness.PaystackPayout = ready, ready, ready, ready
						}
						registry, err := capabilities.New(capabilities.InitialEntries(readiness))
						if err != nil {
							t.Fatal(err)
						}
						operation, rail := capabilities.OperationCollection, capability
						if capability == "destination" {
							operation, rail = capabilities.OperationDestinationResolve, "bank_account"
						}
						if capability == "payout" {
							operation, rail = capabilities.OperationPayout, "bank_account"
						}
						for _, queryEnvironment := range []capabilities.Environment{capabilities.EnvironmentTest, capabilities.EnvironmentLive} {
							_, err := registry.Lookup(capabilities.Query{Operation: operation, CountryCode: "NG", CurrencyCode: "NGN", Rail: rail, Environment: queryEnvironment})
							wantReady := configured && string(queryEnvironment) == environment
							if (err == nil) != wantReady {
								t.Fatalf("configured=%v, query environment=%s: error=%v; want ready=%v", configured, queryEnvironment, err, wantReady)
							}
						}
						for _, other := range []string{"card", "bank_transfer", "destination", "payout"} {
							if other == capability {
								continue
							}
							unselected := paymentCapabilityReadiness(cfg, configured, provider, other)
							if unselected.SandboxVerified || unselected.ProductionEnabled {
								t.Fatalf("unselected %s capability enabled", other)
							}
						}
					}
				})
			}
		}
	}
}
