package capabilities

import (
	"errors"
	"testing"
)

func testCapability(provider Provider, priority int) Capability {
	return Capability{
		Provider: provider, Operation: OperationCollection,
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "card",
		ProviderChannel: "card", CurrencyExponent: 2,
		Priority: priority, Configured: true, SandboxVerified: true,
	}
}

func TestLookupSelectsOneConfiguredPriority(t *testing.T) {
	t.Parallel()

	registry, err := New([]Capability{
		testCapability(ProviderPaystack, 20),
		testCapability(ProviderPayaza, 10),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	decision, err := registry.Lookup(Query{
		Operation: OperationCollection, CountryCode: "ng", CurrencyCode: "ngn",
		Rail: "card", Environment: EnvironmentTest,
	})
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if decision.Provider != ProviderPayaza {
		t.Fatalf("provider = %q, want payaza", decision.Provider)
	}
}

func TestLookupDoesNotEnableUnverifiedOrProductionDisabledCapability(t *testing.T) {
	t.Parallel()

	entry := testCapability(ProviderPayaza, 10)
	entry.SandboxVerified = false
	registry, err := New([]Capability{entry})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	query := Query{Operation: OperationCollection, CountryCode: "NG", CurrencyCode: "NGN", Rail: "card", Environment: EnvironmentTest}
	if _, err := registry.Lookup(query); !errors.Is(err, ErrCapabilityNotReady) {
		t.Fatalf("Lookup() error = %v, want not ready", err)
	}

	entry.SandboxVerified = true
	registry, err = New([]Capability{entry})
	if err != nil {
		t.Fatalf("New() production registry error = %v", err)
	}
	query.Environment = EnvironmentLive
	if _, err := registry.Lookup(query); !errors.Is(err, ErrCapabilityNotReady) {
		t.Fatalf("Lookup() live error = %v, want not ready", err)
	}
}

func TestLookupRejectsAmbiguousPriority(t *testing.T) {
	t.Parallel()

	registry, err := New([]Capability{
		testCapability(ProviderPaystack, 10),
		testCapability(ProviderPayaza, 10),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = registry.Lookup(Query{
		Operation: OperationCollection, CountryCode: "NG", CurrencyCode: "NGN",
		Rail: "card", Environment: EnvironmentTest,
	})
	if !errors.Is(err, ErrAmbiguousCapability) {
		t.Fatalf("Lookup() error = %v, want ambiguous", err)
	}
}

func TestInitialEntriesRetainHistoricalPaystackCheckoutSupport(t *testing.T) {
	t.Parallel()

	entries := InitialEntries(ProviderReadiness{
		PaystackConfigured: true,
	})
	registry, err := New(entries)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	capability, lookupErr := registry.LookupProviderSupport(Query{
		Operation: OperationCollection, CountryCode: "NG", CurrencyCode: "NGN",
		Rail: "hosted_checkout", Environment: EnvironmentLive,
	}, ProviderPaystack)
	if lookupErr != nil {
		t.Fatalf("Lookup(hosted_checkout) error = %v", lookupErr)
	}
	if capability.Provider != ProviderPaystack || capability.ProviderChannel != "hosted_checkout" {
		t.Fatalf("Lookup(hosted_checkout) = %#v", capability)
	}
}
