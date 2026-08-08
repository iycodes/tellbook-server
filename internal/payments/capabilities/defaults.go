package capabilities

type ProviderReadiness struct {
	PayazaConfigured     bool
	PayazaCard           CapabilityReadiness
	PayazaBankTransfer   CapabilityReadiness
	PayazaDestination    CapabilityReadiness
	PayazaPayout         CapabilityReadiness
	PaystackConfigured   bool
	PaystackCard         CapabilityReadiness
	PaystackBankTransfer CapabilityReadiness
	PaystackDestination  CapabilityReadiness
	PaystackPayout       CapabilityReadiness
}

type CapabilityReadiness struct {
	Configured        bool
	SandboxVerified   bool
	ProductionEnabled bool
}

func InitialEntries(readiness ProviderReadiness) []Capability {
	entries := make([]Capability, 0, 12)
	add := func(provider Provider, operation Operation, rail, channel string, priority int, ready CapabilityReadiness) {
		entries = append(entries, Capability{
			Provider: provider, Operation: operation, CountryCode: "NG", CurrencyCode: "NGN",
			Rail: rail, ProviderChannel: channel, CurrencyExponent: 2,
			Priority: priority, Configured: ready.Configured, SandboxVerified: ready.SandboxVerified,
			ProductionEnabled: ready.ProductionEnabled,
		})
	}

	add(ProviderPayaza, OperationCollection, "hosted_checkout", "hosted_checkout", 10, CapabilityReadiness{Configured: readiness.PayazaConfigured})
	add(ProviderPayaza, OperationCollection, "card", "checkout_card", 10, readiness.PayazaCard)
	add(ProviderPayaza, OperationCollection, "bank_transfer", "dynamic_virtual_account", 10, readiness.PayazaBankTransfer)
	add(ProviderPayaza, OperationDestinationList, "bank_account", "nuban", 10, readiness.PayazaDestination)
	add(ProviderPayaza, OperationDestinationResolve, "bank_account", "nuban", 10, readiness.PayazaDestination)
	add(ProviderPayaza, OperationPayout, "bank_account", "nuban", 10, readiness.PayazaPayout)

	add(ProviderPaystack, OperationCollection, "hosted_checkout", "hosted_checkout", 20, CapabilityReadiness{Configured: readiness.PaystackConfigured})
	// Kept non-ready only so historical USSD attempts can still be reconciled.
	add(ProviderPaystack, OperationCollection, "ussd", "ussd", 20, CapabilityReadiness{Configured: readiness.PaystackConfigured})
	add(ProviderPaystack, OperationCollection, "card", "card", 20, readiness.PaystackCard)
	add(ProviderPaystack, OperationCollection, "bank_transfer", "bank_transfer", 20, readiness.PaystackBankTransfer)
	add(ProviderPaystack, OperationDestinationList, "bank_account", "nuban", 20, readiness.PaystackDestination)
	add(ProviderPaystack, OperationDestinationResolve, "bank_account", "nuban", 20, readiness.PaystackDestination)
	add(ProviderPaystack, OperationPayout, "bank_account", "nuban", 20, readiness.PaystackPayout)

	for index := range entries {
		if entries[index].Operation == OperationDestinationList || entries[index].Operation == OperationDestinationResolve {
			entries[index].Input = InputMetadata{
				Label: "Account number", MinimumLength: 10, MaximumLength: 10,
				AllowedCharacters: "digits", ResolutionEnabled: true,
			}
		}
	}
	return entries
}
