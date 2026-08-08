package payments

import (
	"context"
	"testing"

	"booking/go-server/internal/payments/capabilities"
)

type destinationProviderStub struct {
	options    []DestinationOption
	resolved   ResolvedDestination
	listErr    error
	resolveErr error
}

func (s *destinationProviderStub) ListDestinations(context.Context, DestinationQuery) ([]DestinationOption, error) {
	return append([]DestinationOption(nil), s.options...), s.listErr
}

func (s *destinationProviderStub) ResolveDestination(context.Context, ResolveDestinationInput) (ResolvedDestination, error) {
	return s.resolved, s.resolveErr
}

func (s *destinationProviderStub) CreateProviderRecipient(context.Context, ResolvedDestination) (ProviderRecipient, error) {
	return ProviderRecipient{}, nil
}

func TestDestinationServiceOptionsAndResolveUseSameCapability(t *testing.T) {
	metadata := capabilities.InputMetadata{
		Label: "Account number", MinimumLength: 10, MaximumLength: 10,
		AllowedCharacters: "digits", ResolutionEnabled: true,
	}
	registry, err := capabilities.New([]capabilities.Capability{
		readyDestinationCapability(capabilities.OperationDestinationList, metadata),
		readyDestinationCapability(capabilities.OperationDestinationResolve, metadata),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &destinationProviderStub{
		options: []DestinationOption{{Code: "999", Name: "Zulu Bank"}, {Code: "001", Name: "Alpha Bank"}},
		resolved: ResolvedDestination{
			CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
			InstitutionCode: "001", InstitutionName: "Alpha Bank",
			Identifier: "0123456789", AccountName: "TEST RECIPIENT",
		},
	}
	service, err := NewDestinationService(
		&LedgerService{}, &LedgerRepository{}, registry, capabilities.EnvironmentTest,
		map[string]DestinationProvider{"payaza": provider},
	)
	if err != nil {
		t.Fatal(err)
	}

	options, err := service.Options(context.Background(), "ng", "ngn", "bank_account")
	if err != nil {
		t.Fatal(err)
	}
	if options.Provider != "payaza" || len(options.Items) != 2 || options.Items[0].Code != "001" {
		t.Fatalf("unexpected options: %#v", options)
	}
	resolved, providerName, err := service.Resolve(context.Background(), DestinationSelection{
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
		Institution: "001", Identifier: "0123456789",
	})
	if err != nil {
		t.Fatal(err)
	}
	if providerName != "payaza" || resolved.AccountName != "TEST RECIPIENT" {
		t.Fatalf("unexpected resolution: provider=%q destination=%#v", providerName, resolved)
	}
	if rails := service.AvailableRails("NG", "NGN"); len(rails) != 1 || rails[0] != "bank_account" {
		t.Fatalf("unexpected rails: %#v", rails)
	}
}

func TestDestinationServiceRejectsProviderResolutionMismatch(t *testing.T) {
	metadata := capabilities.InputMetadata{
		Label: "Account number", MinimumLength: 10, MaximumLength: 10,
		AllowedCharacters: "digits", ResolutionEnabled: true,
	}
	registry, err := capabilities.New([]capabilities.Capability{
		readyDestinationCapability(capabilities.OperationDestinationList, metadata),
		readyDestinationCapability(capabilities.OperationDestinationResolve, metadata),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &destinationProviderStub{
		options: []DestinationOption{{Code: "001", Name: "Alpha Bank"}},
		resolved: ResolvedDestination{
			CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
			InstitutionCode: "001", InstitutionName: "Alpha Bank",
			Identifier: "9999999999", AccountName: "TEST RECIPIENT",
		},
	}
	service, err := NewDestinationService(
		&LedgerService{}, &LedgerRepository{}, registry, capabilities.EnvironmentTest,
		map[string]DestinationProvider{"payaza": provider},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Resolve(context.Background(), DestinationSelection{
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
		Institution: "001", Identifier: "0123456789",
	}); err == nil {
		t.Fatal("expected mismatched provider resolution to fail")
	}
}

func readyDestinationCapability(operation capabilities.Operation, input capabilities.InputMetadata) capabilities.Capability {
	return capabilities.Capability{
		Provider: capabilities.ProviderPayaza, Operation: operation,
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account", ProviderChannel: "nuban",
		CurrencyExponent: 2, Input: input, Priority: 10, Configured: true, SandboxVerified: true,
	}
}
