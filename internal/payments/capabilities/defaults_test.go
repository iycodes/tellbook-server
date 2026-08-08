package capabilities

import "testing"

func TestInitialEntriesPreferVerifiedPayazaWithoutFallbackExecution(t *testing.T) {
	registry, err := New(InitialEntries(ProviderReadiness{
		PayazaConfigured: true, PayazaCard: CapabilityReadiness{Configured: true, SandboxVerified: true},
		PaystackConfigured: true, PaystackCard: CapabilityReadiness{Configured: true, SandboxVerified: true},
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	capability, err := registry.Lookup(Query{
		Operation: OperationCollection, CountryCode: "NG", CurrencyCode: "NGN",
		Rail: "card", Environment: EnvironmentTest,
	})
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if capability.Provider != ProviderPayaza {
		t.Fatalf("provider = %q", capability.Provider)
	}
}

func TestInitialEntriesDoNotEnableDocumentedButUnverifiedProvider(t *testing.T) {
	registry, err := New(InitialEntries(ProviderReadiness{
		PayazaConfigured: true, PayazaCard: CapabilityReadiness{Configured: true},
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = registry.Lookup(Query{
		Operation: OperationCollection, CountryCode: "NG", CurrencyCode: "NGN",
		Rail: "card", Environment: EnvironmentTest,
	})
	if err != ErrCapabilityNotReady {
		t.Fatalf("Lookup() error = %v", err)
	}
}
