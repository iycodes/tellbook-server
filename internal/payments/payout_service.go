package payments

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/money"
	"booking/go-server/internal/payments/capabilities"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrInsufficientProviderLiquidity = errors.New("provider payout balance is insufficient")

type PayoutService struct {
	ledger       *LedgerService
	repository   *LedgerRepository
	capabilities *capabilities.Registry
	environment  capabilities.Environment
	providers    map[string]PayoutProvider
}

type PayoutServiceConfig struct {
	Ledger       *LedgerService
	Repository   *LedgerRepository
	Capabilities *capabilities.Registry
	Environment  capabilities.Environment
	Providers    map[string]PayoutProvider
}

type InitiatePayoutInput struct {
	ClientID            uuid.UUID
	PaymentAllocationID uuid.UUID
	PayoutDestinationID uuid.UUID
	IdempotencyKey      string
}

type EligiblePayoutAllocation struct {
	ID           uuid.UUID
	AmountMinor  money.Minor
	CurrencyCode string
	AvailableAt  time.Time
}

type PayoutOverview struct {
	CurrencyCode                 string
	AvailableAmountMinor         money.Minor
	PendingSettlementAmountMinor money.Minor
	PayoutInProgressAmountMinor  money.Minor
	PaidOutAmountMinor           money.Minor
	EligibleAllocations          []EligiblePayoutAllocation
	RecentPayouts                []FinancialPayout
}

type PayoutInitializationError struct {
	Payout    FinancialPayout
	Ambiguous bool
	Cause     error
}

func (e *PayoutInitializationError) Error() string {
	return "initialize provider payout: " + e.Cause.Error()
}
func (e *PayoutInitializationError) Unwrap() error { return e.Cause }

func NewPayoutService(config PayoutServiceConfig) (*PayoutService, error) {
	if config.Ledger == nil || config.Repository == nil || config.Capabilities == nil {
		return nil, errors.New("payout service dependencies are required")
	}
	if config.Environment != capabilities.EnvironmentTest && config.Environment != capabilities.EnvironmentLive {
		return nil, errors.New("payout service environment is invalid")
	}
	providers := make(map[string]PayoutProvider, len(config.Providers))
	for name, provider := range config.Providers {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || provider == nil {
			return nil, errors.New("payout service provider is invalid")
		}
		providers[name] = provider
	}
	return &PayoutService{
		ledger: config.Ledger, repository: config.Repository, capabilities: config.Capabilities,
		environment: config.Environment, providers: providers,
	}, nil
}

func (s *PayoutService) Initiate(ctx context.Context, input InitiatePayoutInput) (FinancialPayout, error) {
	allocation, err := s.repository.GetPaymentAllocation(ctx, input.ClientID, input.PaymentAllocationID)
	if err != nil {
		return FinancialPayout{}, err
	}
	destination, err := s.repository.GetPayoutDestination(ctx, input.ClientID, input.PayoutDestinationID)
	if err != nil {
		return FinancialPayout{}, err
	}
	capability, provider, err := s.providerFor(destination.Provider, destination.CountryCode, allocation.CurrencyCode, destination.Rail)
	if err != nil {
		return FinancialPayout{}, err
	}
	var result FinancialPayout
	err = s.repository.WithPayoutInitiationLock(ctx, destination.Provider, allocation.CurrencyCode, func() error {
		recipient, recipientErr := s.providerRecipient(ctx, destination)
		if recipientErr != nil {
			return recipientErr
		}
		payout, createErr := s.ledger.CreatePayoutAttempt(ctx, CreateFinancialPayoutInput{
			PaymentAllocationID: input.PaymentAllocationID, ClientID: input.ClientID,
			PayoutDestinationID: input.PayoutDestinationID, IdempotencyKey: input.IdempotencyKey,
		})
		if createErr != nil {
			return createErr
		}
		result = payout
		if payout.Status != PayoutStatusCreated {
			return nil
		}
		initialized, initializeErr := s.initializeCreatedPayout(ctx, payout, capability, provider, recipient)
		result = initialized
		return initializeErr
	})
	return result, err
}

func (s *PayoutService) RetryCreated(ctx context.Context, payout FinancialPayout) (FinancialPayout, error) {
	if payout.Status != PayoutStatusCreated {
		return payout, nil
	}
	destination, err := s.repository.getPayoutDestination(ctx, payout.PayoutDestinationID)
	if err != nil {
		return FinancialPayout{}, err
	}
	if destination.ClientID != payout.ClientID {
		return FinancialPayout{}, ErrLedgerRecordNotFound
	}
	capability, provider, err := s.providerFor(payout.Provider, payout.CountryCode, payout.CurrencyCode, payout.Rail)
	if err != nil {
		return FinancialPayout{}, err
	}
	recipient, err := s.providerRecipient(ctx, destination)
	if err != nil {
		return FinancialPayout{}, err
	}
	return s.initializeCreatedPayout(ctx, payout, capability, provider, recipient)
}

func (s *PayoutService) initializeCreatedPayout(
	ctx context.Context,
	payout FinancialPayout,
	capability capabilities.Capability,
	provider PayoutProvider,
	recipient ProviderRecipient,
) (FinancialPayout, error) {
	if liquidity, ok := provider.(PayoutLiquidityProvider); ok {
		available, err := liquidity.AvailablePayoutBalance(ctx, payout.CurrencyCode)
		if err != nil {
			return s.failPayoutBeforeInitiation(ctx, payout, "provider_liquidity_unavailable", err)
		}
		if available < money.Minor(payout.AmountMinor) {
			return s.failPayoutBeforeInitiation(ctx, payout, "insufficient_provider_liquidity", ErrInsufficientProviderLiquidity)
		}
	}
	result, providerErr := provider.InitiatePayout(ctx, PayoutSnapshot{
		PayoutID: payout.ID, Reference: payout.Reference, Provider: payout.Provider,
		Rail: payout.Rail, CountryCode: payout.CountryCode, CurrencyCode: payout.CurrencyCode,
		CurrencyExponent: capability.CurrencyExponent, AmountMinor: money.Minor(payout.AmountMinor),
		Narration: "TellBook payout",
	}, recipient)
	if providerErr != nil {
		ambiguous := !isDefinitiveProviderFailure(providerErr)
		status := PayoutStatusFailed
		update := PayoutTransitionUpdate{FailureMessage: providerFailureMessage(providerErr)}
		if ambiguous {
			status = PayoutStatusUnknown
			now := time.Now().UTC()
			update.ReconciledAt = &now
		}
		transitioned, transitionErr := s.repository.TransitionPayout(ctx, payout.ID, payout.Version, status, update)
		if transitionErr != nil {
			return FinancialPayout{}, transitionErr
		}
		return transitioned, &PayoutInitializationError{Payout: transitioned, Ambiguous: ambiguous, Cause: providerErr}
	}
	status := normalizePayoutInitiationStatus(result.Status)
	transitioned, err := s.repository.TransitionPayout(ctx, payout.ID, payout.Version, status, PayoutTransitionUpdate{
		ProviderReference: result.ProviderReference, ProviderStatus: result.ProviderStatus,
	})
	if err != nil {
		return FinancialPayout{}, err
	}
	return transitioned, nil
}

func (s *PayoutService) failPayoutBeforeInitiation(
	ctx context.Context,
	payout FinancialPayout,
	code string,
	cause error,
) (FinancialPayout, error) {
	transitioned, err := s.repository.TransitionPayout(ctx, payout.ID, payout.Version, PayoutStatusFailed, PayoutTransitionUpdate{
		FailureCode: code, FailureMessage: providerFailureMessage(cause),
	})
	if err != nil {
		return FinancialPayout{}, err
	}
	return transitioned, &PayoutInitializationError{Payout: transitioned, Ambiguous: false, Cause: cause}
}

func (r *LedgerRepository) WithPayoutInitiationLock(ctx context.Context, provider, currencyCode string, fn func() error) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	currencyCode = strings.ToUpper(strings.TrimSpace(currencyCode))
	if r == nil || r.db == nil || provider == "" || !isUpperASCII(currencyCode, 3) || fn == nil {
		return errors.New("invalid payout initiation lock")
	}
	hash := sha256.Sum256([]byte(provider + ":" + currencyCode))
	lockKey := int64(binary.BigEndian.Uint64(hash[:8]))
	connection, err := r.db.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire payout initiation lock connection: %w", err)
	}
	locked := false
	defer func() {
		if locked {
			unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, unlockErr := connection.Exec(unlockContext, `SELECT pg_advisory_unlock($1)`, lockKey); unlockErr != nil {
				_ = connection.Conn().Close(unlockContext)
			}
		}
		connection.Release()
	}()
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		return fmt.Errorf("acquire payout initiation lock: %w", err)
	}
	locked = true
	return fn()
}

func (s *PayoutService) Reconcile(ctx context.Context, payout FinancialPayout) (FinancialPayout, error) {
	capability, provider, err := s.providerFor(payout.Provider, payout.CountryCode, payout.CurrencyCode, payout.Rail)
	if err != nil {
		return FinancialPayout{}, err
	}
	destination, err := s.repository.getPayoutDestination(ctx, payout.PayoutDestinationID)
	if err != nil {
		return FinancialPayout{}, err
	}
	if destination.ClientID != payout.ClientID {
		return FinancialPayout{}, ErrLedgerRecordNotFound
	}
	recipient, err := s.reconciliationRecipient(ctx, destination)
	if err != nil {
		return FinancialPayout{}, err
	}
	reconciled, err := provider.ReconcilePayout(ctx, PayoutRecord{
		ID: payout.ID, Reference: payout.Reference, Provider: payout.Provider,
		ProviderReference: payout.ProviderReference, Status: payout.Status,
		CurrencyCode: payout.CurrencyCode, CurrencyExponent: capability.CurrencyExponent,
		AmountMinor: money.Minor(payout.AmountMinor), ExpectedRecipient: recipient,
	})
	if err != nil {
		return FinancialPayout{}, err
	}
	if reconciled.AmountMinor != money.Minor(payout.AmountMinor) ||
		!strings.EqualFold(strings.TrimSpace(reconciled.CurrencyCode), payout.CurrencyCode) {
		return FinancialPayout{}, errors.New("provider payout reconciliation does not match the payout ledger record")
	}
	now := time.Now().UTC()
	update := PayoutTransitionUpdate{ProviderStatus: reconciled.ProviderStatus, ReconciledAt: &now}
	status := normalizePayoutReconciliationStatus(reconciled.Status)
	if status == PayoutStatusSuccessful {
		update.CompletedAt = reconciled.CompletedAt
	}
	if status == PayoutStatusReversed {
		update.ReversedAt = reconciled.CompletedAt
	}
	return s.repository.TransitionPayout(ctx, payout.ID, payout.Version, status, update)
}

func (s *PayoutService) reconciliationRecipient(ctx context.Context, destination PayoutDestination) (ProviderRecipient, error) {
	identifier, err := s.ledger.RevealPayoutDestinationIdentifier(ctx, destination.ID)
	if err != nil {
		return ProviderRecipient{}, err
	}
	return ProviderRecipient{
		ProviderReference: destination.ProviderRecipientID,
		CountryCode:       destination.CountryCode, CurrencyCode: destination.CurrencyCode, Rail: destination.Rail,
		InstitutionCode: destination.InstitutionCode, InstitutionName: destination.InstitutionName,
		Identifier: identifier, AccountName: destination.ResolvedAccountName,
	}, nil
}

func normalizePayoutInitiationStatus(status PayoutStatus) PayoutStatus {
	switch status {
	case PayoutStatusPending, PayoutStatusRequiresAction, PayoutStatusFailed,
		PayoutStatusCancelled, PayoutStatusReversed:
		return status
	case PayoutStatusSuccessful:
		return PayoutStatusPending
	default:
		return PayoutStatusUnknown
	}
}

func normalizePayoutReconciliationStatus(status PayoutStatus) PayoutStatus {
	switch status {
	case PayoutStatusPending, PayoutStatusRequiresAction, PayoutStatusSuccessful,
		PayoutStatusFailed, PayoutStatusReversed, PayoutStatusCancelled, PayoutStatusUnknown:
		return status
	default:
		return PayoutStatusUnknown
	}
}

func (s *PayoutService) Overview(ctx context.Context, clientID uuid.UUID, currencyCode string) (PayoutOverview, error) {
	return s.repository.GetPayoutOverview(ctx, clientID, currencyCode)
}

func (s *PayoutService) Get(ctx context.Context, clientID, payoutID uuid.UUID) (FinancialPayout, error) {
	return s.repository.GetFinancialPayout(ctx, clientID, payoutID)
}

func (s *PayoutService) providerFor(providerName, countryCode, currencyCode, rail string) (capabilities.Capability, PayoutProvider, error) {
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	provider := s.providers[providerName]
	if provider == nil {
		return capabilities.Capability{}, nil, capabilities.ErrCapabilityNotReady
	}
	capability, err := s.capabilities.LookupProvider(capabilities.Query{
		Operation: capabilities.OperationPayout, CountryCode: countryCode,
		CurrencyCode: currencyCode, Rail: rail, Environment: s.environment,
	}, capabilities.Provider(providerName))
	return capability, provider, err
}

func (s *PayoutService) providerRecipient(ctx context.Context, destination PayoutDestination) (ProviderRecipient, error) {
	recipient := ProviderRecipient{
		ProviderReference: destination.ProviderRecipientID, CountryCode: destination.CountryCode,
		CurrencyCode: destination.CurrencyCode, Rail: destination.Rail,
		InstitutionCode: destination.InstitutionCode, InstitutionName: destination.InstitutionName,
		AccountName: destination.ResolvedAccountName,
	}
	if recipient.ProviderReference == "" {
		identifier, err := s.ledger.RevealPayoutDestinationIdentifier(ctx, destination.ID)
		if err != nil {
			return ProviderRecipient{}, err
		}
		recipient.Identifier = identifier
	}
	return recipient, nil
}

func (r *LedgerRepository) GetPaymentAllocation(ctx context.Context, clientID, allocationID uuid.UUID) (PaymentAllocation, error) {
	const query = `
		SELECT id, payment_id, client_id, currency_code, gross_amount_minor,
			provider_collection_fee_minor, platform_fee_minor, tax_amount_minor,
			adjustment_amount_minor, business_net_amount_minor, policy_version,
			calculation_snapshot, settlement_status, settlement_reference,
			available_for_payout_at, status, created_at, updated_at
		FROM payment_allocations WHERE id = $1 AND client_id = $2
	`
	allocation, err := scanPaymentAllocation(r.db.QueryRow(ctx, query, allocationID, clientID))
	if errors.Is(err, pgx.ErrNoRows) {
		return PaymentAllocation{}, ErrLedgerRecordNotFound
	}
	return allocation, err
}

func (r *LedgerRepository) GetPayoutDestination(ctx context.Context, clientID, destinationID uuid.UUID) (PayoutDestination, error) {
	destination, err := r.getPayoutDestination(ctx, destinationID)
	if err != nil {
		return PayoutDestination{}, err
	}
	if destination.ClientID != clientID || destination.Status != "active" {
		return PayoutDestination{}, ErrLedgerRecordNotFound
	}
	return destination, nil
}

func (r *LedgerRepository) GetFinancialPayout(ctx context.Context, clientID, payoutID uuid.UUID) (FinancialPayout, error) {
	const query = `
		SELECT id, payment_allocation_id, client_id, payout_destination_id,
			provider, rail, country_code, currency_code, amount_minor, fee_minor,
			reference, provider_reference, idempotency_key, request_fingerprint,
			destination_snapshot, status, version, created_at, updated_at
		FROM payouts WHERE id = $1 AND client_id = $2
	`
	payout, err := scanFinancialPayout(r.db.QueryRow(ctx, query, payoutID, clientID))
	if errors.Is(err, pgx.ErrNoRows) {
		return FinancialPayout{}, ErrLedgerRecordNotFound
	}
	return payout, err
}

func (r *LedgerRepository) GetFinancialPayoutByReference(ctx context.Context, provider, reference string) (FinancialPayout, error) {
	const query = `
		SELECT id, payment_allocation_id, client_id, payout_destination_id,
			provider, rail, country_code, currency_code, amount_minor, fee_minor,
			reference, provider_reference, idempotency_key, request_fingerprint,
			destination_snapshot, status, version, created_at, updated_at
		FROM payouts WHERE provider = $1 AND reference = $2
	`
	payout, err := scanFinancialPayout(r.db.QueryRow(ctx, query, strings.TrimSpace(provider), strings.TrimSpace(reference)))
	if errors.Is(err, pgx.ErrNoRows) {
		return FinancialPayout{}, ErrLedgerRecordNotFound
	}
	return payout, err
}

func (r *LedgerRepository) ClaimStalePayouts(
	ctx context.Context,
	workerID string,
	before time.Time,
	limit int,
	leaseDuration time.Duration,
) ([]FinancialPayout, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || before.IsZero() || limit < 1 || limit > 500 || leaseDuration <= 0 {
		return nil, errors.New("invalid stale payout query")
	}
	leaseSeconds := max(int64(leaseDuration/time.Second), 1)
	const query = `
		WITH candidates AS (
			SELECT id FROM payouts
			WHERE status IN ('created','pending','requires_action','unknown')
			  AND updated_at <= $1
			  AND (reconciliation_lease_expires_at IS NULL OR reconciliation_lease_expires_at <= NOW())
			ORDER BY updated_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE payouts AS payout
		SET reconciliation_lease_owner = $3,
			reconciliation_lease_expires_at = NOW() + ($4 * INTERVAL '1 second')
		FROM candidates
		WHERE payout.id = candidates.id
		RETURNING payout.id, payout.payment_allocation_id, payout.client_id, payout.payout_destination_id,
			payout.provider, payout.rail, payout.country_code, payout.currency_code, payout.amount_minor, payout.fee_minor,
			payout.reference, payout.provider_reference, payout.idempotency_key, payout.request_fingerprint,
			payout.destination_snapshot, payout.status, payout.version, payout.created_at, payout.updated_at
	`
	rows, err := r.db.Query(ctx, query, before, limit, workerID, leaseSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]FinancialPayout, 0, limit)
	for rows.Next() {
		item, err := scanFinancialPayout(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *LedgerRepository) DelayPayoutReconciliation(
	ctx context.Context,
	payoutID uuid.UUID,
	workerID string,
	retryAt time.Time,
) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE payouts
		SET reconciliation_lease_expires_at = $3
		WHERE id = $1 AND reconciliation_lease_owner = $2
	`, payoutID, strings.TrimSpace(workerID), retryAt)
	if err != nil {
		return fmt.Errorf("delay payout reconciliation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConcurrentUpdate
	}
	return nil
}

func (r *LedgerRepository) ListFinancialPayouts(ctx context.Context, clientID uuid.UUID, limit int) ([]FinancialPayout, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	const query = `
		SELECT id, payment_allocation_id, client_id, payout_destination_id,
			provider, rail, country_code, currency_code, amount_minor, fee_minor,
			reference, provider_reference, idempotency_key, request_fingerprint,
			destination_snapshot, status, version, created_at, updated_at
		FROM payouts WHERE client_id = $1 ORDER BY created_at DESC LIMIT $2
	`
	rows, err := r.db.Query(ctx, query, clientID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]FinancialPayout, 0, limit)
	for rows.Next() {
		item, err := scanFinancialPayout(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *LedgerRepository) GetPayoutOverview(ctx context.Context, clientID uuid.UUID, currencyCode string) (PayoutOverview, error) {
	currencyCode = strings.ToUpper(strings.TrimSpace(currencyCode))
	if clientID == uuid.Nil || len(currencyCode) != 3 {
		return PayoutOverview{}, errors.New("invalid payout overview scope")
	}
	overview := PayoutOverview{CurrencyCode: currencyCode, EligibleAllocations: []EligiblePayoutAllocation{}, RecentPayouts: []FinancialPayout{}}
	if err := r.db.QueryRow(ctx, `
		SELECT
			COALESCE((SELECT SUM(business_net_amount_minor) FROM payment_allocations WHERE client_id = $1 AND currency_code = $2 AND status = 'eligible'), 0),
			COALESCE((SELECT SUM(business_net_amount_minor) FROM payment_allocations WHERE client_id = $1 AND currency_code = $2 AND status = 'pending'), 0),
			COALESCE((SELECT SUM(amount_minor) FROM payouts WHERE client_id = $1 AND currency_code = $2 AND status IN ('created','pending','requires_action','unknown')), 0),
			COALESCE((SELECT SUM(amount_minor) FROM payouts WHERE client_id = $1 AND currency_code = $2 AND status = 'successful'), 0)
	`, clientID, currencyCode).Scan(
		&overview.AvailableAmountMinor, &overview.PendingSettlementAmountMinor,
		&overview.PayoutInProgressAmountMinor, &overview.PaidOutAmountMinor,
	); err != nil {
		return PayoutOverview{}, err
	}
	rows, err := r.db.Query(ctx, `
		SELECT id, business_net_amount_minor, currency_code, available_for_payout_at
		FROM payment_allocations
		WHERE client_id = $1 AND currency_code = $2 AND status = 'eligible'
		  AND available_for_payout_at <= NOW()
		ORDER BY available_for_payout_at, created_at
		LIMIT 100
	`, clientID, currencyCode)
	if err != nil {
		return PayoutOverview{}, err
	}
	for rows.Next() {
		var item EligiblePayoutAllocation
		if err := rows.Scan(&item.ID, &item.AmountMinor, &item.CurrencyCode, &item.AvailableAt); err != nil {
			rows.Close()
			return PayoutOverview{}, err
		}
		overview.EligibleAllocations = append(overview.EligibleAllocations, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PayoutOverview{}, err
	}
	rows.Close()
	overview.RecentPayouts, err = r.ListFinancialPayouts(ctx, clientID, 20)
	if err != nil {
		return PayoutOverview{}, err
	}
	return overview, nil
}
