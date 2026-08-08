package payments

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"booking/go-server/internal/payments/capabilities"

	"github.com/google/uuid"
)

type DestinationService struct {
	ledger       *LedgerService
	repository   *LedgerRepository
	capabilities *capabilities.Registry
	environment  capabilities.Environment
	providers    map[string]DestinationProvider
	cacheMu      sync.RWMutex
	cache        map[string]cachedDestinationOptions
}

type cachedDestinationOptions struct {
	items     []DestinationOption
	expiresAt time.Time
}

type DestinationSelection struct {
	ClientID      uuid.UUID
	CountryCode   string
	CurrencyCode  string
	Rail          string
	Institution   string
	Identifier    string
	ConfirmedName string
	MakeDefault   bool
}

type DestinationOptionsResult struct {
	Provider string
	Input    capabilities.InputMetadata
	Items    []DestinationOption
}

func NewDestinationService(ledger *LedgerService, repository *LedgerRepository, registry *capabilities.Registry, environment capabilities.Environment, providers map[string]DestinationProvider) (*DestinationService, error) {
	if ledger == nil || repository == nil || registry == nil {
		return nil, errors.New("destination service dependencies are required")
	}
	if environment != capabilities.EnvironmentTest && environment != capabilities.EnvironmentLive {
		return nil, errors.New("destination service environment is invalid")
	}
	cleaned := make(map[string]DestinationProvider, len(providers))
	for name, provider := range providers {
		if provider != nil {
			cleaned[strings.ToLower(strings.TrimSpace(name))] = provider
		}
	}
	return &DestinationService{
		ledger: ledger, repository: repository, capabilities: registry,
		environment: environment, providers: cleaned, cache: make(map[string]cachedDestinationOptions),
	}, nil
}

func (s *DestinationService) AvailableRails(countryCode, currencyCode string) []string {
	return s.capabilities.AvailableRails(
		capabilities.OperationDestinationResolve,
		countryCode,
		currencyCode,
		s.environment,
	)
}

func (s *DestinationService) Options(ctx context.Context, countryCode, currencyCode, rail string) (DestinationOptionsResult, error) {
	capability, provider, err := s.providerFor(capabilities.OperationDestinationList, countryCode, currencyCode, rail)
	if err != nil {
		return DestinationOptionsResult{}, err
	}
	key := strings.Join([]string{string(capability.Provider), capability.CountryCode, capability.CurrencyCode, capability.Rail}, ":")
	s.cacheMu.RLock()
	cached, ok := s.cache[key]
	s.cacheMu.RUnlock()
	if ok && time.Now().Before(cached.expiresAt) {
		return DestinationOptionsResult{Provider: string(capability.Provider), Input: capability.Input, Items: append([]DestinationOption(nil), cached.items...)}, nil
	}
	items, err := provider.ListDestinations(ctx, DestinationQuery{CountryCode: capability.CountryCode, CurrencyCode: capability.CurrencyCode, Rail: capability.Rail})
	if err != nil {
		return DestinationOptionsResult{}, err
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	s.cacheMu.Lock()
	s.cache[key] = cachedDestinationOptions{items: append([]DestinationOption(nil), items...), expiresAt: time.Now().Add(24 * time.Hour)}
	s.cacheMu.Unlock()
	return DestinationOptionsResult{Provider: string(capability.Provider), Input: capability.Input, Items: items}, nil
}

func (s *DestinationService) Resolve(ctx context.Context, input DestinationSelection) (ResolvedDestination, string, error) {
	capability, provider, err := s.providerFor(capabilities.OperationDestinationResolve, input.CountryCode, input.CurrencyCode, input.Rail)
	if err != nil {
		return ResolvedDestination{}, "", err
	}
	identifier := strings.TrimSpace(input.Identifier)
	if err := validateDestinationIdentifier(identifier, capability.Input); err != nil {
		return ResolvedDestination{}, "", err
	}
	options, err := s.Options(ctx, input.CountryCode, input.CurrencyCode, input.Rail)
	if err != nil {
		return ResolvedDestination{}, "", err
	}
	if options.Provider != string(capability.Provider) {
		return ResolvedDestination{}, "", capabilities.ErrAmbiguousCapability
	}
	institutionName := ""
	for _, option := range options.Items {
		if option.Code == strings.TrimSpace(input.Institution) {
			institutionName = option.Name
			break
		}
	}
	if institutionName == "" {
		return ResolvedDestination{}, "", errors.New("payout institution is not supported")
	}
	resolved, err := provider.ResolveDestination(ctx, ResolveDestinationInput{
		CountryCode: capability.CountryCode, CurrencyCode: capability.CurrencyCode, Rail: capability.Rail,
		Institution: strings.TrimSpace(input.Institution), InstitutionName: institutionName, Identifier: identifier,
	})
	if err != nil {
		return ResolvedDestination{}, "", err
	}
	if strings.ToUpper(strings.TrimSpace(resolved.CountryCode)) != capability.CountryCode ||
		strings.ToUpper(strings.TrimSpace(resolved.CurrencyCode)) != capability.CurrencyCode ||
		strings.TrimSpace(resolved.Rail) != capability.Rail ||
		strings.TrimSpace(resolved.InstitutionCode) != strings.TrimSpace(input.Institution) ||
		strings.TrimSpace(resolved.InstitutionName) != institutionName ||
		strings.TrimSpace(resolved.Identifier) != identifier ||
		strings.TrimSpace(resolved.AccountName) == "" {
		return ResolvedDestination{}, "", errors.New("payout provider returned mismatched destination details")
	}
	return resolved, string(capability.Provider), nil
}

func (s *DestinationService) Save(ctx context.Context, input DestinationSelection) (PayoutDestination, error) {
	if input.ClientID == uuid.Nil {
		return PayoutDestination{}, errors.New("client is required")
	}
	resolved, providerName, err := s.Resolve(ctx, input)
	if err != nil {
		return PayoutDestination{}, err
	}
	if confirmedName := strings.TrimSpace(input.ConfirmedName); confirmedName == "" ||
		!strings.EqualFold(confirmedName, strings.TrimSpace(resolved.AccountName)) {
		return PayoutDestination{}, errors.New("confirmed payout account name does not match")
	}
	provider := s.providers[providerName]
	recipient, err := provider.CreateProviderRecipient(ctx, resolved)
	if err != nil {
		return PayoutDestination{}, err
	}
	if recipient.CountryCode != resolved.CountryCode || recipient.CurrencyCode != resolved.CurrencyCode ||
		recipient.Rail != resolved.Rail || recipient.InstitutionCode != resolved.InstitutionCode ||
		recipient.InstitutionName != resolved.InstitutionName || recipient.Identifier != resolved.Identifier ||
		recipient.AccountName != resolved.AccountName {
		return PayoutDestination{}, errors.New("payout provider returned mismatched recipient details")
	}
	destination, _, err := s.ledger.SavePayoutDestination(ctx, SavePayoutDestinationInput{
		ClientID: input.ClientID, Provider: providerName, CountryCode: resolved.CountryCode,
		CurrencyCode: resolved.CurrencyCode, Rail: resolved.Rail,
		InstitutionCode: resolved.InstitutionCode, InstitutionName: resolved.InstitutionName,
		Identifier: resolved.Identifier, ResolvedAccountName: resolved.AccountName,
		ProviderRecipientID: recipient.ProviderReference,
		MakeDefault:         input.MakeDefault, VerifiedAt: time.Now().UTC(),
	})
	return destination, err
}

func (s *DestinationService) List(ctx context.Context, clientID uuid.UUID) ([]PayoutDestination, error) {
	if clientID == uuid.Nil {
		return nil, errors.New("client is required")
	}
	return s.repository.ListPayoutDestinations(ctx, clientID)
}

func (s *DestinationService) Revoke(ctx context.Context, clientID, destinationID uuid.UUID) error {
	if clientID == uuid.Nil || destinationID == uuid.Nil {
		return errors.New("client and payout destination are required")
	}
	return s.repository.RevokePayoutDestination(ctx, clientID, destinationID)
}

func (s *DestinationService) providerFor(operation capabilities.Operation, countryCode, currencyCode, rail string) (capabilities.Capability, DestinationProvider, error) {
	capability, err := s.capabilities.Lookup(capabilities.Query{
		Operation: operation, CountryCode: countryCode, CurrencyCode: currencyCode,
		Rail: rail, Environment: s.environment,
	})
	if err != nil {
		return capabilities.Capability{}, nil, err
	}
	provider := s.providers[string(capability.Provider)]
	if provider == nil {
		return capabilities.Capability{}, nil, capabilities.ErrCapabilityNotReady
	}
	return capability, provider, nil
}

func validateDestinationIdentifier(value string, metadata capabilities.InputMetadata) error {
	if metadata.MinimumLength <= 0 || metadata.MaximumLength < metadata.MinimumLength {
		return errors.New("payout identifier rules are not configured")
	}
	if len(value) < metadata.MinimumLength || len(value) > metadata.MaximumLength {
		return errors.New("payout identifier length is invalid")
	}
	if metadata.AllowedCharacters == "digits" {
		for _, character := range value {
			if character < '0' || character > '9' {
				return errors.New("payout identifier must contain digits only")
			}
		}
	}
	return nil
}
