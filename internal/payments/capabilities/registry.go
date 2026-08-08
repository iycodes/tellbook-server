package capabilities

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type Provider string

const (
	ProviderPayaza   Provider = "payaza"
	ProviderPaystack Provider = "paystack"
)

type Operation string

const (
	OperationCollection         Operation = "collection"
	OperationPayout             Operation = "payout"
	OperationDestinationList    Operation = "destination_list"
	OperationDestinationResolve Operation = "destination_resolve"
)

type Environment string

const (
	EnvironmentTest Environment = "test"
	EnvironmentLive Environment = "live"
)

var (
	ErrUnsupportedCapability = errors.New("unsupported payment capability")
	ErrCapabilityNotReady    = errors.New("payment capability is not ready")
	ErrAmbiguousCapability   = errors.New("ambiguous payment capability configuration")
)

type InputMetadata struct {
	Label             string
	MinimumLength     int
	MaximumLength     int
	AllowedCharacters string
	ResolutionEnabled bool
}

type Capability struct {
	Provider          Provider
	Operation         Operation
	CountryCode       string
	CurrencyCode      string
	Rail              string
	ProviderChannel   string
	CurrencyExponent  uint8
	MinimumAmount     int64
	MaximumAmount     int64
	Input             InputMetadata
	Priority          int
	Configured        bool
	SandboxVerified   bool
	ProductionEnabled bool
}

type Query struct {
	Operation    Operation
	CountryCode  string
	CurrencyCode string
	Rail         string
	Environment  Environment
}

type Registry struct {
	entries []Capability
}

func (r *Registry) AvailableRails(operation Operation, countryCode, currencyCode string, environment Environment) []string {
	if r == nil {
		return []string{}
	}
	countryCode = strings.ToUpper(strings.TrimSpace(countryCode))
	currencyCode = strings.ToUpper(strings.TrimSpace(currencyCode))
	seen := make(map[string]struct{})
	for _, entry := range r.entries {
		if entry.Operation != operation || entry.CountryCode != countryCode ||
			entry.CurrencyCode != currencyCode || !isReady(entry, environment) {
			continue
		}
		seen[entry.Rail] = struct{}{}
	}
	rails := make([]string, 0, len(seen))
	for rail := range seen {
		rails = append(rails, rail)
	}
	sort.Strings(rails)
	return rails
}

func New(entries []Capability) (*Registry, error) {
	cloned := append([]Capability(nil), entries...)
	for index := range cloned {
		entry := &cloned[index]
		entry.CountryCode = strings.ToUpper(strings.TrimSpace(entry.CountryCode))
		entry.CurrencyCode = strings.ToUpper(strings.TrimSpace(entry.CurrencyCode))
		entry.Rail = strings.TrimSpace(entry.Rail)
		entry.ProviderChannel = strings.TrimSpace(entry.ProviderChannel)
		if err := validateCapability(*entry); err != nil {
			return nil, fmt.Errorf("capability %d: %w", index, err)
		}
	}
	return &Registry{entries: cloned}, nil
}

func (r *Registry) Lookup(query Query) (Capability, error) {
	if r == nil {
		return Capability{}, ErrUnsupportedCapability
	}
	query.CountryCode = strings.ToUpper(strings.TrimSpace(query.CountryCode))
	query.CurrencyCode = strings.ToUpper(strings.TrimSpace(query.CurrencyCode))
	query.Rail = strings.TrimSpace(query.Rail)

	matching := make([]Capability, 0, 2)
	ready := make([]Capability, 0, 2)
	for _, entry := range r.entries {
		if entry.Operation != query.Operation || entry.CountryCode != query.CountryCode ||
			entry.CurrencyCode != query.CurrencyCode || entry.Rail != query.Rail {
			continue
		}
		matching = append(matching, entry)
		if isReady(entry, query.Environment) {
			ready = append(ready, entry)
		}
	}
	if len(matching) == 0 {
		return Capability{}, ErrUnsupportedCapability
	}
	if len(ready) == 0 {
		return Capability{}, ErrCapabilityNotReady
	}

	sort.Slice(ready, func(i, j int) bool {
		return ready[i].Priority < ready[j].Priority
	})
	if len(ready) > 1 && ready[0].Priority == ready[1].Priority {
		return Capability{}, ErrAmbiguousCapability
	}
	return ready[0], nil
}

func (r *Registry) LookupProvider(query Query, provider Provider) (Capability, error) {
	if r == nil {
		return Capability{}, ErrUnsupportedCapability
	}
	query.CountryCode = strings.ToUpper(strings.TrimSpace(query.CountryCode))
	query.CurrencyCode = strings.ToUpper(strings.TrimSpace(query.CurrencyCode))
	query.Rail = strings.TrimSpace(query.Rail)
	var matched bool
	for _, entry := range r.entries {
		if entry.Provider != provider || entry.Operation != query.Operation ||
			entry.CountryCode != query.CountryCode || entry.CurrencyCode != query.CurrencyCode ||
			entry.Rail != query.Rail {
			continue
		}
		matched = true
		if isReady(entry, query.Environment) {
			return entry, nil
		}
	}
	if matched {
		return Capability{}, ErrCapabilityNotReady
	}
	return Capability{}, ErrUnsupportedCapability
}

// LookupProviderSupport resolves immutable provider metadata without deciding
// whether the provider may accept a new operation.
func (r *Registry) LookupProviderSupport(query Query, provider Provider) (Capability, error) {
	if r == nil {
		return Capability{}, ErrUnsupportedCapability
	}
	query.CountryCode = strings.ToUpper(strings.TrimSpace(query.CountryCode))
	query.CurrencyCode = strings.ToUpper(strings.TrimSpace(query.CurrencyCode))
	query.Rail = strings.TrimSpace(query.Rail)
	for _, entry := range r.entries {
		if entry.Provider == provider && entry.Operation == query.Operation &&
			entry.CountryCode == query.CountryCode && entry.CurrencyCode == query.CurrencyCode &&
			entry.Rail == query.Rail && entry.Configured {
			return entry, nil
		}
	}
	return Capability{}, ErrUnsupportedCapability
}

func isReady(capability Capability, environment Environment) bool {
	if !capability.Configured {
		return false
	}
	switch environment {
	case EnvironmentTest:
		return capability.SandboxVerified
	case EnvironmentLive:
		return capability.ProductionEnabled
	default:
		return false
	}
}

func validateCapability(capability Capability) error {
	if capability.Provider != ProviderPayaza && capability.Provider != ProviderPaystack {
		return errors.New("invalid provider")
	}
	switch capability.Operation {
	case OperationCollection, OperationPayout, OperationDestinationList, OperationDestinationResolve:
	default:
		return errors.New("invalid operation")
	}
	if !isUpperASCII(capability.CountryCode, 2) || !isUpperASCII(capability.CurrencyCode, 3) {
		return errors.New("invalid country or currency code")
	}
	if capability.Rail == "" || capability.ProviderChannel == "" {
		return errors.New("rail and provider channel are required")
	}
	if capability.CurrencyExponent > 18 {
		return errors.New("invalid currency exponent")
	}
	if capability.MinimumAmount < 0 || capability.MaximumAmount < 0 ||
		(capability.MaximumAmount > 0 && capability.MinimumAmount > capability.MaximumAmount) {
		return errors.New("invalid amount limits")
	}
	if capability.Priority < 0 {
		return errors.New("priority cannot be negative")
	}
	return nil
}

func isUpperASCII(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for index := range value {
		if value[index] < 'A' || value[index] > 'Z' {
			return false
		}
	}
	return true
}
