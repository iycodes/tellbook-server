package payments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/secure"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type PayoutDestination struct {
	ID                      uuid.UUID
	ClientID                uuid.UUID
	Provider                string
	CountryCode             string
	CurrencyCode            string
	Rail                    string
	InstitutionCode         string
	InstitutionName         string
	MaskedIdentifier        string
	IdentifierCiphertext    []byte
	IdentifierNonce         []byte
	EncryptionKeyVersion    string
	ResolvedAccountName     string
	ProviderRecipientID     string
	VerificationFingerprint string
	VerifiedAt              time.Time
	IsDefault               bool
	Status                  string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type SavePayoutDestinationInput struct {
	ClientID            uuid.UUID
	Provider            string
	CountryCode         string
	CurrencyCode        string
	Rail                string
	InstitutionCode     string
	InstitutionName     string
	Identifier          string
	ResolvedAccountName string
	ProviderRecipientID string
	MakeDefault         bool
	VerifiedAt          time.Time
}

func (s *LedgerService) SavePayoutDestination(
	ctx context.Context,
	input SavePayoutDestinationInput,
) (PayoutDestination, bool, error) {
	if s == nil || s.repository == nil || s.keyring == nil || s.fingerprinter == nil {
		return PayoutDestination{}, false, errors.New("secure payout destination storage is not configured")
	}
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	input.CountryCode = strings.ToUpper(strings.TrimSpace(input.CountryCode))
	input.CurrencyCode = strings.ToUpper(strings.TrimSpace(input.CurrencyCode))
	input.Rail = strings.TrimSpace(input.Rail)
	input.InstitutionCode = strings.TrimSpace(input.InstitutionCode)
	input.InstitutionName = strings.TrimSpace(input.InstitutionName)
	input.Identifier = strings.TrimSpace(input.Identifier)
	input.ResolvedAccountName = strings.TrimSpace(input.ResolvedAccountName)
	input.ProviderRecipientID = strings.TrimSpace(input.ProviderRecipientID)
	if input.ClientID == uuid.Nil || (input.Provider != "payaza" && input.Provider != "paystack") ||
		!isUpperASCII(input.CountryCode, 2) || !isUpperASCII(input.CurrencyCode, 3) ||
		input.Rail == "" || input.InstitutionCode == "" || input.InstitutionName == "" ||
		input.Identifier == "" || input.ResolvedAccountName == "" {
		return PayoutDestination{}, false, errors.New("invalid payout destination")
	}
	if input.VerifiedAt.IsZero() {
		input.VerifiedAt = time.Now().UTC()
	}

	fingerprintContext := strings.Join([]string{
		input.Provider, input.CountryCode, input.CurrencyCode, input.Rail, input.InstitutionCode,
	}, ":")
	fingerprint, err := s.fingerprinter.Sum([]byte(input.Identifier), []byte(fingerprintContext))
	if err != nil {
		return PayoutDestination{}, false, err
	}
	destinationID := uuid.New()
	aad := payoutDestinationAAD(input.ClientID, destinationID)
	encrypted, err := s.keyring.Encrypt([]byte(input.Identifier), aad)
	if err != nil {
		return PayoutDestination{}, false, err
	}

	return s.repository.savePayoutDestination(ctx, savePayoutDestinationParams{
		ID: destinationID, Input: input, MaskedIdentifier: maskFinancialIdentifier(input.Identifier),
		Fingerprint: fingerprint, Ciphertext: encrypted,
	})
}

func (s *LedgerService) RevealPayoutDestinationIdentifier(
	ctx context.Context,
	destinationID uuid.UUID,
) (string, error) {
	if s == nil || s.repository == nil || s.keyring == nil {
		return "", errors.New("secure payout destination storage is not configured")
	}
	destination, err := s.repository.getPayoutDestination(ctx, destinationID)
	if err != nil {
		return "", err
	}
	if len(destination.IdentifierCiphertext) == 0 {
		return "", errors.New("payout destination identifier was not retained")
	}
	plaintext, err := s.keyring.Decrypt(secure.Ciphertext{
		KeyVersion: destination.EncryptionKeyVersion,
		Nonce:      destination.IdentifierNonce,
		Data:       destination.IdentifierCiphertext,
	}, payoutDestinationAAD(destination.ClientID, destination.ID))
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func payoutDestinationAAD(clientID, destinationID uuid.UUID) []byte {
	return []byte("payout-destination:" + clientID.String() + ":" + destinationID.String())
}

func maskFinancialIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 4 {
		return strings.Repeat("*", len(value))
	}
	return strings.Repeat("*", len(value)-4) + value[len(value)-4:]
}

type savePayoutDestinationParams struct {
	ID               uuid.UUID
	Input            SavePayoutDestinationInput
	MaskedIdentifier string
	Fingerprint      string
	Ciphertext       secure.Ciphertext
}

func (r *LedgerRepository) savePayoutDestination(
	ctx context.Context,
	params savePayoutDestinationParams,
) (PayoutDestination, bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return PayoutDestination{}, false, fmt.Errorf("begin payout destination save: %w", err)
	}
	defer tx.Rollback(ctx)

	existing, err := getPayoutDestinationByFingerprintTx(ctx, tx, params)
	if err == nil {
		if params.Input.MakeDefault && !existing.IsDefault {
			existing, err = setDefaultPayoutDestinationTx(ctx, tx, existing)
			if err != nil {
				return PayoutDestination{}, false, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return PayoutDestination{}, false, fmt.Errorf("commit existing payout destination: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, ErrLedgerRecordNotFound) {
		return PayoutDestination{}, false, err
	}
	if params.Input.MakeDefault {
		if _, err := tx.Exec(ctx, `
			UPDATE payout_destinations
			SET is_default = FALSE, updated_at = NOW()
			WHERE client_id = $1 AND country_code = $2 AND currency_code = $3 AND rail = $4
		`, params.Input.ClientID, params.Input.CountryCode, params.Input.CurrencyCode, params.Input.Rail); err != nil {
			return PayoutDestination{}, false, fmt.Errorf("clear payout destination default: %w", err)
		}
	}

	const query = `
		INSERT INTO payout_destinations (
			id, client_id, provider, country_code, currency_code, rail,
			institution_code, institution_name, masked_identifier,
			identifier_ciphertext, identifier_nonce, encryption_key_version,
			resolved_account_name, provider_recipient_id, verification_fingerprint,
			verified_at, is_default, status, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,'active',NOW(),NOW())
		ON CONFLICT (
			client_id, provider, country_code, currency_code, rail, institution_code, verification_fingerprint
		) WHERE status = 'active' DO NOTHING
		RETURNING
			id, client_id, provider, country_code, currency_code, rail,
			institution_code, institution_name, masked_identifier,
			COALESCE(identifier_ciphertext, ''::bytea), COALESCE(identifier_nonce, ''::bytea),
			COALESCE(encryption_key_version, ''),
			resolved_account_name, provider_recipient_id, verification_fingerprint,
			verified_at, is_default, status, created_at, updated_at
	`
	var ciphertext any
	var nonce any
	var keyVersion any
	if len(params.Ciphertext.Data) > 0 {
		ciphertext = params.Ciphertext.Data
		nonce = params.Ciphertext.Nonce
		keyVersion = params.Ciphertext.KeyVersion
	}
	destination, err := scanPayoutDestination(tx.QueryRow(
		ctx, query, params.ID, params.Input.ClientID, params.Input.Provider,
		params.Input.CountryCode, params.Input.CurrencyCode, params.Input.Rail,
		params.Input.InstitutionCode, params.Input.InstitutionName, params.MaskedIdentifier,
		ciphertext, nonce, keyVersion, params.Input.ResolvedAccountName,
		params.Input.ProviderRecipientID, params.Fingerprint, params.Input.VerifiedAt,
		params.Input.MakeDefault,
	))
	created := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		destination, err = getPayoutDestinationByFingerprintTx(ctx, tx, params)
		if err == nil && params.Input.MakeDefault && !destination.IsDefault {
			destination, err = setDefaultPayoutDestinationTx(ctx, tx, destination)
		}
	}
	if err != nil {
		return PayoutDestination{}, false, fmt.Errorf("insert payout destination: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PayoutDestination{}, false, fmt.Errorf("commit payout destination: %w", err)
	}
	return destination, created, nil
}

func getPayoutDestinationByFingerprintTx(
	ctx context.Context,
	tx pgx.Tx,
	params savePayoutDestinationParams,
) (PayoutDestination, error) {
	const query = `
		SELECT
			id, client_id, provider, country_code, currency_code, rail,
			institution_code, institution_name, masked_identifier,
			COALESCE(identifier_ciphertext, ''::bytea), COALESCE(identifier_nonce, ''::bytea),
			COALESCE(encryption_key_version, ''),
			resolved_account_name, provider_recipient_id, verification_fingerprint,
			verified_at, is_default, status, created_at, updated_at
		FROM payout_destinations
		WHERE client_id = $1 AND provider = $2 AND country_code = $3 AND currency_code = $4
		  AND rail = $5 AND institution_code = $6 AND verification_fingerprint = $7
		  AND status = 'active'
		FOR UPDATE
	`
	destination, err := scanPayoutDestination(tx.QueryRow(
		ctx, query, params.Input.ClientID, params.Input.Provider, params.Input.CountryCode,
		params.Input.CurrencyCode, params.Input.Rail, params.Input.InstitutionCode, params.Fingerprint,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return PayoutDestination{}, ErrLedgerRecordNotFound
	}
	if err != nil {
		return PayoutDestination{}, fmt.Errorf("get payout destination by fingerprint: %w", err)
	}
	return destination, nil
}

func setDefaultPayoutDestinationTx(
	ctx context.Context,
	tx pgx.Tx,
	destination PayoutDestination,
) (PayoutDestination, error) {
	if _, err := tx.Exec(ctx, `
		UPDATE payout_destinations
		SET is_default = FALSE, updated_at = NOW()
		WHERE client_id = $1 AND country_code = $2 AND currency_code = $3 AND rail = $4
	`, destination.ClientID, destination.CountryCode, destination.CurrencyCode, destination.Rail); err != nil {
		return PayoutDestination{}, fmt.Errorf("clear payout destination default: %w", err)
	}
	destination.IsDefault = true
	if _, err := tx.Exec(ctx, `UPDATE payout_destinations SET is_default = TRUE, updated_at = NOW() WHERE id = $1`, destination.ID); err != nil {
		return PayoutDestination{}, fmt.Errorf("set payout destination default: %w", err)
	}
	return destination, nil
}

func (r *LedgerRepository) getPayoutDestination(ctx context.Context, destinationID uuid.UUID) (PayoutDestination, error) {
	const query = `
		SELECT
			id, client_id, provider, country_code, currency_code, rail,
			institution_code, institution_name, masked_identifier,
			COALESCE(identifier_ciphertext, ''::bytea), COALESCE(identifier_nonce, ''::bytea),
			COALESCE(encryption_key_version, ''),
			resolved_account_name, provider_recipient_id, verification_fingerprint,
			verified_at, is_default, status, created_at, updated_at
		FROM payout_destinations
		WHERE id = $1
	`
	destination, err := scanPayoutDestination(r.db.QueryRow(ctx, query, destinationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return PayoutDestination{}, ErrLedgerRecordNotFound
	}
	if err != nil {
		return PayoutDestination{}, fmt.Errorf("get payout destination: %w", err)
	}
	return destination, nil
}

func (r *LedgerRepository) ListPayoutDestinations(ctx context.Context, clientID uuid.UUID) ([]PayoutDestination, error) {
	const query = `
		SELECT
			id, client_id, provider, country_code, currency_code, rail,
			institution_code, institution_name, masked_identifier,
			COALESCE(identifier_ciphertext, ''::bytea), COALESCE(identifier_nonce, ''::bytea),
			COALESCE(encryption_key_version, ''), resolved_account_name,
			provider_recipient_id, verification_fingerprint, verified_at,
			is_default, status, created_at, updated_at
		FROM payout_destinations
		WHERE client_id = $1 AND status = 'active'
		ORDER BY is_default DESC, created_at DESC
	`
	rows, err := r.db.Query(ctx, query, clientID)
	if err != nil {
		return nil, fmt.Errorf("list payout destinations: %w", err)
	}
	defer rows.Close()
	items := make([]PayoutDestination, 0)
	for rows.Next() {
		item, err := scanPayoutDestination(rows)
		if err != nil {
			return nil, fmt.Errorf("scan payout destination: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *LedgerRepository) RevokePayoutDestination(ctx context.Context, clientID, destinationID uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE payout_destinations
		SET status = 'disabled', is_default = FALSE, updated_at = NOW()
		WHERE id = $1 AND client_id = $2 AND status = 'active'
	`, destinationID, clientID)
	if err != nil {
		return fmt.Errorf("revoke payout destination: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLedgerRecordNotFound
	}
	return nil
}

func scanPayoutDestination(row rowScanner) (PayoutDestination, error) {
	var destination PayoutDestination
	if err := row.Scan(
		&destination.ID, &destination.ClientID, &destination.Provider,
		&destination.CountryCode, &destination.CurrencyCode, &destination.Rail,
		&destination.InstitutionCode, &destination.InstitutionName, &destination.MaskedIdentifier,
		&destination.IdentifierCiphertext, &destination.IdentifierNonce, &destination.EncryptionKeyVersion,
		&destination.ResolvedAccountName, &destination.ProviderRecipientID,
		&destination.VerificationFingerprint, &destination.VerifiedAt,
		&destination.IsDefault, &destination.Status, &destination.CreatedAt, &destination.UpdatedAt,
	); err != nil {
		return PayoutDestination{}, err
	}
	return destination, nil
}

type PaymentAllocation struct {
	ID                   uuid.UUID
	PaymentID            uuid.UUID
	ClientID             uuid.UUID
	CurrencyCode         string
	Amounts              AllocationAmounts
	PolicyVersion        string
	CalculationSnapshot  json.RawMessage
	SettlementStatus     string
	SettlementReference  string
	AvailableForPayoutAt *time.Time
	Status               string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type CreatePaymentAllocationInput struct {
	PaymentID            uuid.UUID
	ClientID             uuid.UUID
	CurrencyCode         string
	Amounts              AllocationAmounts
	PolicyVersion        string
	CalculationSnapshot  map[string]string
	SettlementStatus     string
	SettlementReference  string
	AvailableForPayoutAt *time.Time
}

func (r *LedgerRepository) CreatePaymentAllocation(
	ctx context.Context,
	input CreatePaymentAllocationInput,
) (PaymentAllocation, bool, error) {
	if err := input.Amounts.Validate(); err != nil {
		return PaymentAllocation{}, false, err
	}
	input.CurrencyCode = strings.ToUpper(strings.TrimSpace(input.CurrencyCode))
	input.PolicyVersion = strings.TrimSpace(input.PolicyVersion)
	input.SettlementStatus = strings.TrimSpace(input.SettlementStatus)
	if input.PaymentID == uuid.Nil || input.ClientID == uuid.Nil || !isUpperASCII(input.CurrencyCode, 3) ||
		input.PolicyVersion == "" || (input.SettlementStatus != "pending" && input.SettlementStatus != "available") {
		return PaymentAllocation{}, false, errors.New("invalid payment allocation")
	}
	status := "pending"
	if input.SettlementStatus == "available" {
		if input.AvailableForPayoutAt == nil {
			return PaymentAllocation{}, false, errors.New("available settlement requires payout availability time")
		}
		status = "eligible"
	}
	snapshot, err := json.Marshal(input.CalculationSnapshot)
	if err != nil {
		return PaymentAllocation{}, false, fmt.Errorf("encode allocation snapshot: %w", err)
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return PaymentAllocation{}, false, fmt.Errorf("begin allocation create: %w", err)
	}
	defer tx.Rollback(ctx)
	payment, err := getPaymentForUpdate(ctx, tx, input.PaymentID)
	if err != nil {
		return PaymentAllocation{}, false, err
	}
	if payment.Status != PaymentStatusPaid || payment.ClientID != input.ClientID ||
		payment.CurrencyCode != input.CurrencyCode || int64(payment.AmountMinor) != input.Amounts.GrossMinor {
		return PaymentAllocation{}, false, errors.New("allocation does not match a paid payment")
	}

	const query = `
		INSERT INTO payment_allocations (
			id, payment_id, client_id, currency_code, gross_amount_minor,
			provider_collection_fee_minor, platform_fee_minor, tax_amount_minor,
			adjustment_amount_minor, business_net_amount_minor, policy_version,
			calculation_snapshot, settlement_status, settlement_reference,
			available_for_payout_at, status, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,NOW(),NOW())
		ON CONFLICT (payment_id) DO NOTHING
		RETURNING
			id, payment_id, client_id, currency_code, gross_amount_minor,
			provider_collection_fee_minor, platform_fee_minor, tax_amount_minor,
			adjustment_amount_minor, business_net_amount_minor, policy_version,
			calculation_snapshot, settlement_status, settlement_reference,
			available_for_payout_at, status, created_at, updated_at
	`
	allocation, err := scanPaymentAllocation(tx.QueryRow(
		ctx, query, uuid.New(), input.PaymentID, input.ClientID, input.CurrencyCode,
		input.Amounts.GrossMinor, input.Amounts.ProviderFeeMinor, input.Amounts.PlatformFeeMinor,
		input.Amounts.TaxMinor, input.Amounts.AdjustmentMinor, input.Amounts.BusinessNetAmountMinor,
		input.PolicyVersion, snapshot, input.SettlementStatus, strings.TrimSpace(input.SettlementReference),
		input.AvailableForPayoutAt, status,
	))
	created := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		allocation, err = getPaymentAllocationByPaymentTx(ctx, tx, input.PaymentID)
	}
	if err != nil {
		return PaymentAllocation{}, false, fmt.Errorf("create payment allocation: %w", err)
	}
	if !created && (allocation.Amounts != input.Amounts || allocation.PolicyVersion != input.PolicyVersion ||
		allocation.CurrencyCode != input.CurrencyCode) {
		return PaymentAllocation{}, false, ErrIdempotencyConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return PaymentAllocation{}, false, fmt.Errorf("commit payment allocation: %w", err)
	}
	return allocation, created, nil
}

func scanPaymentAllocation(row rowScanner) (PaymentAllocation, error) {
	var allocation PaymentAllocation
	if err := row.Scan(
		&allocation.ID, &allocation.PaymentID, &allocation.ClientID, &allocation.CurrencyCode,
		&allocation.Amounts.GrossMinor, &allocation.Amounts.ProviderFeeMinor,
		&allocation.Amounts.PlatformFeeMinor, &allocation.Amounts.TaxMinor,
		&allocation.Amounts.AdjustmentMinor, &allocation.Amounts.BusinessNetAmountMinor,
		&allocation.PolicyVersion, &allocation.CalculationSnapshot,
		&allocation.SettlementStatus, &allocation.SettlementReference,
		&allocation.AvailableForPayoutAt, &allocation.Status,
		&allocation.CreatedAt, &allocation.UpdatedAt,
	); err != nil {
		return PaymentAllocation{}, err
	}
	return allocation, nil
}

func getPaymentAllocationByPaymentTx(ctx context.Context, tx pgx.Tx, paymentID uuid.UUID) (PaymentAllocation, error) {
	const query = `
		SELECT
			id, payment_id, client_id, currency_code, gross_amount_minor,
			provider_collection_fee_minor, platform_fee_minor, tax_amount_minor,
			adjustment_amount_minor, business_net_amount_minor, policy_version,
			calculation_snapshot, settlement_status, settlement_reference,
			available_for_payout_at, status, created_at, updated_at
		FROM payment_allocations
		WHERE payment_id = $1
		FOR UPDATE
	`
	return scanPaymentAllocation(tx.QueryRow(ctx, query, paymentID))
}

type FinancialPayout struct {
	ID                  uuid.UUID
	PaymentAllocationID uuid.UUID
	ClientID            uuid.UUID
	PayoutDestinationID uuid.UUID
	Provider            string
	Rail                string
	CountryCode         string
	CurrencyCode        string
	AmountMinor         int64
	FeeMinor            int64
	Reference           string
	ProviderReference   string
	IdempotencyKey      string
	RequestFingerprint  string
	DestinationSnapshot json.RawMessage
	Status              PayoutStatus
	Version             int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type PayoutTransitionUpdate struct {
	ProviderReference string
	ProviderStatus    string
	FailureCode       string
	FailureMessage    string
	FeeMinor          int64
	InitiatedAt       *time.Time
	CompletedAt       *time.Time
	ReversedAt        *time.Time
	ReconciledAt      *time.Time
}

type CreateFinancialPayoutInput struct {
	PaymentAllocationID uuid.UUID
	ClientID            uuid.UUID
	PayoutDestinationID uuid.UUID
	IdempotencyKey      string
}

type ActivePayoutError struct {
	Payout FinancialPayout
}

func (e *ActivePayoutError) Error() string {
	return "an active payout already exists for this allocation"
}

func (s *LedgerService) CreatePayoutAttempt(
	ctx context.Context,
	input CreateFinancialPayoutInput,
) (FinancialPayout, error) {
	if s == nil || s.repository == nil || input.PaymentAllocationID == uuid.Nil ||
		input.ClientID == uuid.Nil || input.PayoutDestinationID == uuid.Nil ||
		!idempotencyKeyPattern.MatchString(strings.TrimSpace(input.IdempotencyKey)) {
		return FinancialPayout{}, errors.New("invalid payout attempt")
	}
	reference, err := newProviderReference(payoutReferencePrefix)
	if err != nil {
		return FinancialPayout{}, err
	}
	return s.repository.createPayoutAttempt(ctx, input, reference)
}

func (r *LedgerRepository) createPayoutAttempt(
	ctx context.Context,
	input CreateFinancialPayoutInput,
	reference string,
) (FinancialPayout, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return FinancialPayout{}, fmt.Errorf("begin payout attempt: %w", err)
	}
	defer tx.Rollback(ctx)

	existing, err := getFinancialPayoutByIdempotencyTx(ctx, tx, input.ClientID, strings.TrimSpace(input.IdempotencyKey))
	if err == nil {
		if existing.PaymentAllocationID != input.PaymentAllocationID || existing.PayoutDestinationID != input.PayoutDestinationID {
			return FinancialPayout{}, ErrIdempotencyConflict
		}
		return existing, nil
	}
	if !errors.Is(err, ErrLedgerRecordNotFound) {
		return FinancialPayout{}, err
	}

	const allocationQuery = `
		SELECT
			id, payment_id, client_id, currency_code, gross_amount_minor,
			provider_collection_fee_minor, platform_fee_minor, tax_amount_minor,
			adjustment_amount_minor, business_net_amount_minor, policy_version,
			calculation_snapshot, settlement_status, settlement_reference,
			available_for_payout_at, status, created_at, updated_at
		FROM payment_allocations
		WHERE id = $1
		FOR UPDATE
	`
	allocation, err := scanPaymentAllocation(tx.QueryRow(ctx, allocationQuery, input.PaymentAllocationID))
	if err != nil {
		return FinancialPayout{}, fmt.Errorf("lock payment allocation: %w", err)
	}
	if allocation.ClientID != input.ClientID || allocation.Status != "eligible" ||
		allocation.SettlementStatus != "available" || allocation.AvailableForPayoutAt == nil ||
		allocation.AvailableForPayoutAt.After(time.Now()) || allocation.Amounts.BusinessNetAmountMinor <= 0 {
		active, activeErr := getActivePayoutByAllocationTx(ctx, tx, input.PaymentAllocationID)
		if activeErr == nil {
			return FinancialPayout{}, &ActivePayoutError{Payout: active}
		}
		return FinancialPayout{}, errors.New("payment allocation is not eligible for payout")
	}

	const destinationQuery = `
		SELECT
			id, client_id, provider, country_code, currency_code, rail,
			institution_code, institution_name, masked_identifier,
			COALESCE(identifier_ciphertext, ''::bytea), COALESCE(identifier_nonce, ''::bytea),
			COALESCE(encryption_key_version, ''),
			resolved_account_name, provider_recipient_id, verification_fingerprint,
			verified_at, is_default, status, created_at, updated_at
		FROM payout_destinations
		WHERE id = $1
		FOR UPDATE
	`
	destination, err := scanPayoutDestination(tx.QueryRow(ctx, destinationQuery, input.PayoutDestinationID))
	if err != nil {
		return FinancialPayout{}, fmt.Errorf("lock payout destination: %w", err)
	}
	if destination.ClientID != input.ClientID || destination.Status != "active" ||
		destination.CurrencyCode != allocation.CurrencyCode {
		return FinancialPayout{}, errors.New("payout destination does not match allocation")
	}
	destinationSnapshot, err := json.Marshal(map[string]string{
		"provider": destination.Provider, "rail": destination.Rail,
		"country_code": destination.CountryCode, "currency_code": destination.CurrencyCode,
		"institution_code": destination.InstitutionCode, "institution_name": destination.InstitutionName,
		"masked_identifier": destination.MaskedIdentifier, "account_name": destination.ResolvedAccountName,
		"provider_recipient_id": destination.ProviderRecipientID,
	})
	if err != nil {
		return FinancialPayout{}, fmt.Errorf("encode payout destination snapshot: %w", err)
	}
	fingerprintPayload := strings.Join([]string{
		input.PaymentAllocationID.String(), input.ClientID.String(), input.PayoutDestinationID.String(),
		fmt.Sprintf("%d", allocation.Amounts.BusinessNetAmountMinor), allocation.CurrencyCode,
	}, ":")
	fingerprintHash := sha256.Sum256([]byte(fingerprintPayload))
	fingerprint := hex.EncodeToString(fingerprintHash[:])

	const insertQuery = `
		INSERT INTO payouts (
			id, payment_allocation_id, client_id, payout_destination_id,
			provider, rail, country_code, currency_code, amount_minor, fee_minor,
			reference, idempotency_key, request_fingerprint, destination_snapshot,
			status, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,0,$10,$11,$12,$13,'created',NOW(),NOW())
		ON CONFLICT DO NOTHING
		RETURNING
			id, payment_allocation_id, client_id, payout_destination_id,
			provider, rail, country_code, currency_code, amount_minor, fee_minor,
			reference, provider_reference, idempotency_key, request_fingerprint,
			destination_snapshot, status, version, created_at, updated_at
	`
	payout, err := scanFinancialPayout(tx.QueryRow(
		ctx, insertQuery, uuid.New(), allocation.ID, input.ClientID, destination.ID,
		destination.Provider, destination.Rail, destination.CountryCode, allocation.CurrencyCode,
		allocation.Amounts.BusinessNetAmountMinor, reference, strings.TrimSpace(input.IdempotencyKey),
		fingerprint, destinationSnapshot,
	))
	created := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		payout, err = getFinancialPayoutByIdempotencyTx(ctx, tx, input.ClientID, strings.TrimSpace(input.IdempotencyKey))
		if err == nil && (payout.PaymentAllocationID != input.PaymentAllocationID || payout.PayoutDestinationID != input.PayoutDestinationID) {
			return FinancialPayout{}, ErrIdempotencyConflict
		}
		if errors.Is(err, ErrLedgerRecordNotFound) {
			active, activeErr := getActivePayoutByAllocationTx(ctx, tx, input.PaymentAllocationID)
			if activeErr == nil {
				return FinancialPayout{}, &ActivePayoutError{Payout: active}
			}
		}
	}
	if err != nil {
		return FinancialPayout{}, fmt.Errorf("insert payout attempt: %w", err)
	}
	if created {
		tag, err := tx.Exec(ctx, `UPDATE payment_allocations SET status = 'reserved', updated_at = NOW() WHERE id = $1 AND status = 'eligible'`, allocation.ID)
		if err != nil {
			return FinancialPayout{}, fmt.Errorf("reserve payment allocation: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return FinancialPayout{}, ErrConcurrentUpdate
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return FinancialPayout{}, fmt.Errorf("commit payout attempt: %w", err)
	}
	return payout, nil
}

func scanFinancialPayout(row rowScanner) (FinancialPayout, error) {
	var payout FinancialPayout
	var status string
	if err := row.Scan(
		&payout.ID, &payout.PaymentAllocationID, &payout.ClientID, &payout.PayoutDestinationID,
		&payout.Provider, &payout.Rail, &payout.CountryCode, &payout.CurrencyCode,
		&payout.AmountMinor, &payout.FeeMinor, &payout.Reference, &payout.ProviderReference,
		&payout.IdempotencyKey, &payout.RequestFingerprint, &payout.DestinationSnapshot,
		&status, &payout.Version, &payout.CreatedAt, &payout.UpdatedAt,
	); err != nil {
		return FinancialPayout{}, err
	}
	payout.Status = PayoutStatus(status)
	return payout, nil
}

func (r *LedgerRepository) TransitionPayout(
	ctx context.Context,
	payoutID uuid.UUID,
	expectedVersion int64,
	to PayoutStatus,
	update PayoutTransitionUpdate,
) (FinancialPayout, error) {
	if update.FeeMinor < 0 {
		return FinancialPayout{}, errors.New("payout fee cannot be negative")
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return FinancialPayout{}, fmt.Errorf("begin payout transition: %w", err)
	}
	defer tx.Rollback(ctx)

	const lockQuery = `
		SELECT
			id, payment_allocation_id, client_id, payout_destination_id,
			provider, rail, country_code, currency_code, amount_minor, fee_minor,
			reference, provider_reference, idempotency_key, request_fingerprint,
			destination_snapshot, status, version, created_at, updated_at
		FROM payouts
		WHERE id = $1
		FOR UPDATE
	`
	payout, err := scanFinancialPayout(tx.QueryRow(ctx, lockQuery, payoutID))
	if errors.Is(err, pgx.ErrNoRows) {
		return FinancialPayout{}, ErrLedgerRecordNotFound
	}
	if err != nil {
		return FinancialPayout{}, fmt.Errorf("lock payout: %w", err)
	}
	if payout.Version != expectedVersion {
		return FinancialPayout{}, ErrConcurrentUpdate
	}
	if err := ValidatePayoutTransition(payout.Status, to); err != nil {
		return FinancialPayout{}, err
	}
	initiatedAt := update.InitiatedAt
	if (to == PayoutStatusPending || to == PayoutStatusRequiresAction || to == PayoutStatusUnknown) && initiatedAt == nil {
		now := time.Now().UTC()
		initiatedAt = &now
	}
	completedAt := update.CompletedAt
	if to == PayoutStatusSuccessful && completedAt == nil {
		now := time.Now().UTC()
		completedAt = &now
	}
	reversedAt := update.ReversedAt
	if to == PayoutStatusReversed && reversedAt == nil {
		now := time.Now().UTC()
		reversedAt = &now
	}

	const transitionQuery = `
		UPDATE payouts
		SET
			status = $3,
			provider_reference = CASE WHEN $4 <> '' THEN $4 ELSE provider_reference END,
			provider_status = $5,
			failure_code = $6,
			failure_message = $7,
			fee_minor = $8,
			initiated_at = COALESCE($9, initiated_at),
			completed_at = COALESCE($10, completed_at),
				reversed_at = COALESCE($11, reversed_at),
				last_reconciled_at = COALESCE($12, last_reconciled_at),
				reconciliation_lease_owner = '',
				reconciliation_lease_expires_at = NULL,
			version = version + 1,
			updated_at = NOW()
		WHERE id = $1 AND version = $2
		RETURNING
			id, payment_allocation_id, client_id, payout_destination_id,
			provider, rail, country_code, currency_code, amount_minor, fee_minor,
			reference, provider_reference, idempotency_key, request_fingerprint,
			destination_snapshot, status, version, created_at, updated_at
	`
	transitioned, err := scanFinancialPayout(tx.QueryRow(
		ctx, transitionQuery, payoutID, expectedVersion, string(to),
		strings.TrimSpace(update.ProviderReference), strings.TrimSpace(update.ProviderStatus),
		strings.TrimSpace(update.FailureCode), strings.TrimSpace(update.FailureMessage),
		update.FeeMinor, initiatedAt, completedAt, reversedAt, update.ReconciledAt,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return FinancialPayout{}, ErrConcurrentUpdate
	}
	if err != nil {
		return FinancialPayout{}, fmt.Errorf("transition payout: %w", err)
	}

	switch to {
	case PayoutStatusSuccessful:
		if _, err := tx.Exec(ctx, `UPDATE payment_allocations SET status = 'paid', updated_at = NOW() WHERE id = $1`, payout.PaymentAllocationID); err != nil {
			return FinancialPayout{}, fmt.Errorf("complete payout allocation: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO business_balance_entries (
				id, client_id, payment_adjustment_id, currency_code,
				amount_minor, kind, status, description, created_at
			)
			SELECT gen_random_uuid(), adjustment_payment.client_id, adjustment.id,
				adjustment.currency_code, -adjustment.allocation_impact_minor,
				'debt', 'open', 'Post-payout payment adjustment', NOW()
			FROM payment_allocations allocation
			INNER JOIN payments adjustment_payment ON adjustment_payment.id = allocation.payment_id
			INNER JOIN payment_adjustments adjustment ON adjustment.payment_id = adjustment_payment.id
			WHERE allocation.id = $1
			  AND adjustment.status = 'successful'
			  AND adjustment.allocation_impact_minor > 0
			ON CONFLICT (payment_adjustment_id) WHERE payment_adjustment_id IS NOT NULL DO NOTHING
		`, payout.PaymentAllocationID); err != nil {
			return FinancialPayout{}, fmt.Errorf("record payout adjustment debt: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE payment_adjustments adjustment
			SET funds_already_paid_out = TRUE, updated_at = NOW()
			FROM payment_allocations allocation
			WHERE allocation.id = $1
			  AND adjustment.payment_id = allocation.payment_id
			  AND adjustment.status = 'successful'
			  AND adjustment.allocation_impact_minor > 0
		`, payout.PaymentAllocationID); err != nil {
			return FinancialPayout{}, fmt.Errorf("mark adjustments paid out: %w", err)
		}
	case PayoutStatusFailed, PayoutStatusCancelled, PayoutStatusReversed:
		if _, err := tx.Exec(ctx, `UPDATE payment_allocations SET status = 'blocked', updated_at = NOW() WHERE id = $1`, payout.PaymentAllocationID); err != nil {
			return FinancialPayout{}, fmt.Errorf("block payout allocation: %w", err)
		}
		if err := enqueueFinancialJobTx(ctx, tx, FinancialJobParams{
			ID: uuid.New(), Kind: "reevaluate_payment_allocation", AggregateType: "payment_allocation",
			AggregateID:      payout.PaymentAllocationID,
			DeduplicationKey: fmt.Sprintf("reevaluate_payment_allocation:%s:%d", payout.PaymentAllocationID, transitioned.Version),
			Payload:          json.RawMessage(`{}`),
		}); err != nil {
			return FinancialPayout{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return FinancialPayout{}, fmt.Errorf("commit payout transition: %w", err)
	}
	return transitioned, nil
}

func getFinancialPayoutByIdempotencyTx(
	ctx context.Context,
	tx pgx.Tx,
	clientID uuid.UUID,
	idempotencyKey string,
) (FinancialPayout, error) {
	const query = `
		SELECT
			id, payment_allocation_id, client_id, payout_destination_id,
			provider, rail, country_code, currency_code, amount_minor, fee_minor,
			reference, provider_reference, idempotency_key, request_fingerprint,
			destination_snapshot, status, version, created_at, updated_at
		FROM payouts
		WHERE client_id = $1 AND idempotency_key = $2
	`
	payout, err := scanFinancialPayout(tx.QueryRow(ctx, query, clientID, idempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return FinancialPayout{}, ErrLedgerRecordNotFound
	}
	if err != nil {
		return FinancialPayout{}, fmt.Errorf("get payout by idempotency key: %w", err)
	}
	return payout, nil
}

func getActivePayoutByAllocationTx(
	ctx context.Context,
	tx pgx.Tx,
	allocationID uuid.UUID,
) (FinancialPayout, error) {
	const query = `
		SELECT
			id, payment_allocation_id, client_id, payout_destination_id,
			provider, rail, country_code, currency_code, amount_minor, fee_minor,
			reference, provider_reference, idempotency_key, request_fingerprint,
			destination_snapshot, status, version, created_at, updated_at
		FROM payouts
		WHERE payment_allocation_id = $1
		  AND status IN ('created', 'pending', 'requires_action', 'unknown')
		ORDER BY created_at DESC
		LIMIT 1
	`
	payout, err := scanFinancialPayout(tx.QueryRow(ctx, query, allocationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return FinancialPayout{}, ErrLedgerRecordNotFound
	}
	if err != nil {
		return FinancialPayout{}, fmt.Errorf("get active payout by allocation: %w", err)
	}
	return payout, nil
}
