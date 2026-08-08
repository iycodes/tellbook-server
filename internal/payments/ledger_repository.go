package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/money"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrLedgerRecordNotFound     = errors.New("financial ledger record not found")
	ErrIdempotencyConflict      = errors.New("idempotency key was reused with different input")
	ErrConcurrentUpdate         = errors.New("financial record changed concurrently")
	ErrProviderEvidenceConflict = errors.New("provider evidence conflicts with the stored payment")
)

type ActivePaymentError struct {
	Payment FinancialPayment
}

func (e *ActivePaymentError) Error() string {
	return "an active payment already exists for this booking obligation"
}

type FinancialPayment struct {
	ID                   uuid.UUID
	PublicToken          string
	BookingID            uuid.UUID
	ClientID             uuid.UUID
	CustomerID           uuid.UUID
	Purpose              PaymentPurpose
	Provider             string
	Method               string
	CountryCode          string
	CurrencyCode         string
	AmountMinor          money.Minor
	PriceSnapshot        json.RawMessage
	Reference            string
	ProviderReference    string
	ProviderChannel      string
	IdempotencyKey       string
	RequestFingerprint   string
	Status               PaymentStatus
	ProviderStatus       string
	ReconciliationReason string
	FailureCode          string
	FailureMessage       string
	CheckoutURL          string
	ExpiresAt            *time.Time
	PaidAt               *time.Time
	LastReconciledAt     *time.Time
	Version              int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type CreateFinancialPaymentParams struct {
	ID                                   uuid.UUID
	PublicToken                          string
	BookingID                            uuid.UUID
	ClientID                             uuid.UUID
	CustomerID                           uuid.UUID
	Purpose                              PaymentPurpose
	Provider                             string
	Method                               string
	CountryCode                          string
	CurrencyCode                         string
	AmountMinor                          money.Minor
	PriceSnapshot                        json.RawMessage
	Reference                            string
	IdempotencyKey                       string
	RequestFingerprint                   string
	CheckoutDetails                      json.RawMessage
	CheckoutInitializationState          CheckoutInitializationState
	CheckoutInitializationLeaseOwner     string
	CheckoutInitializationLeaseExpiresAt *time.Time
	NextProviderCheckAt                  *time.Time
}

type PaymentTransitionUpdate struct {
	ProviderReference                string
	ProviderChannel                  string
	ProviderStatus                   string
	ReconciliationReason             string
	FailureCode                      string
	FailureMessage                   string
	CheckoutURL                      string
	ExpiresAt                        *time.Time
	PaidAt                           *time.Time
	ReconciledAt                     *time.Time
	CheckoutDetails                  json.RawMessage
	CheckoutInitializationState      CheckoutInitializationState
	ExpectedInitializationLeaseOwner string
	NextProviderCheckAt              *time.Time
}

type StoredCheckoutRecord struct {
	Record         CheckoutRecord
	State          CheckoutInitializationState
	LeaseOwner     string
	LeaseExpiresAt *time.Time
	NextCheckAt    *time.Time
}

type PaymentExceptionInput struct {
	PaymentID           uuid.UUID
	BookingID           uuid.UUID
	Provider            string
	Kind                string
	ProviderReference   string
	EvidenceSource      string
	EvidenceReference   string
	ObservedAmountMinor money.Minor
	CurrencyCode        string
}

type LedgerRepository struct {
	db *pgxpool.Pool
}

type BookingPaymentObligation struct {
	ClientID           uuid.UUID
	CustomerID         uuid.UUID
	CountryCode        string
	CurrencyCode       string
	Purpose            PaymentPurpose
	AmountMinor        money.Minor
	TotalAmountMinor   money.Minor
	DepositAmountMinor money.Minor
}

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func NewLedgerRepository(db *pgxpool.Pool) *LedgerRepository {
	return &LedgerRepository{db: db}
}

func (r *LedgerRepository) GetOutstandingBookingPaymentObligation(
	ctx context.Context,
	bookingID uuid.UUID,
) (BookingPaymentObligation, error) {
	if r == nil || r.db == nil || bookingID == uuid.Nil {
		return BookingPaymentObligation{}, errors.New("invalid booking payment obligation query")
	}
	return loadBookingPaymentObligation(ctx, r.db, bookingID)
}

func (r *LedgerRepository) GetActivePaymentForObligation(
	ctx context.Context,
	bookingID uuid.UUID,
	purpose PaymentPurpose,
) (FinancialPayment, error) {
	if r == nil || r.db == nil || bookingID == uuid.Nil || !purpose.Valid() {
		return FinancialPayment{}, errors.New("invalid active payment query")
	}
	return getActivePaymentTx(ctx, r.db, bookingID, purpose)
}

func (r *LedgerRepository) GetCheckoutRecord(ctx context.Context, paymentID uuid.UUID) (StoredCheckoutRecord, error) {
	var stored StoredCheckoutRecord
	var raw json.RawMessage
	var state string
	err := r.db.QueryRow(ctx, `
		SELECT checkout_details, checkout_initialization_state,
		       checkout_initialization_lease_owner, checkout_initialization_lease_expires_at,
		       next_provider_check_at
		FROM payments
		WHERE id = $1
	`, paymentID).Scan(&raw, &state, &stored.LeaseOwner, &stored.LeaseExpiresAt, &stored.NextCheckAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredCheckoutRecord{}, ErrLedgerRecordNotFound
	}
	if err != nil {
		return StoredCheckoutRecord{}, fmt.Errorf("load checkout record: %w", err)
	}
	if len(raw) == 0 || string(raw) == "{}" {
		return StoredCheckoutRecord{}, ErrLedgerRecordNotFound
	}
	if err := json.Unmarshal(raw, &stored.Record); err != nil || stored.Record.Version != 1 {
		return StoredCheckoutRecord{}, errors.New("stored checkout record is invalid")
	}
	stored.State = CheckoutInitializationState(state)
	return stored, nil
}

func (r *LedgerRepository) RecordPaymentException(ctx context.Context, input PaymentExceptionInput) error {
	if r == nil || r.db == nil || input.PaymentID == uuid.Nil || input.BookingID == uuid.Nil ||
		(input.Provider != "payaza" && input.Provider != "paystack") ||
		(input.Kind != "late_success" && input.Kind != "amount_mismatch") ||
		strings.TrimSpace(input.ProviderReference) == "" || strings.TrimSpace(input.EvidenceSource) == "" ||
		strings.TrimSpace(input.EvidenceReference) == "" || input.ObservedAmountMinor < 0 ||
		len(strings.TrimSpace(input.CurrencyCode)) != 3 {
		return errors.New("invalid payment exception")
	}
	_, err := r.db.Exec(ctx, `
		INSERT INTO payment_exceptions (
			id, payment_id, booking_id, provider, exception_kind, provider_reference,
			evidence_source, evidence_reference, observed_amount_minor, currency_code
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (provider, evidence_reference, exception_kind) DO NOTHING
	`, uuid.New(), input.PaymentID, input.BookingID, input.Provider, input.Kind,
		strings.TrimSpace(input.ProviderReference), strings.TrimSpace(input.EvidenceSource),
		strings.TrimSpace(input.EvidenceReference), input.ObservedAmountMinor,
		strings.ToUpper(strings.TrimSpace(input.CurrencyCode)))
	if err != nil {
		return fmt.Errorf("record payment exception: %w", err)
	}
	return nil
}

func (r *LedgerRepository) ClaimCheckoutInitialization(
	ctx context.Context,
	paymentID uuid.UUID,
	leaseOwner string,
	leaseExpiresAt time.Time,
) (FinancialPayment, bool, error) {
	leaseOwner = strings.TrimSpace(leaseOwner)
	if paymentID == uuid.Nil || leaseOwner == "" || !leaseExpiresAt.After(time.Now().UTC()) {
		return FinancialPayment{}, false, errors.New("invalid checkout initialization lease")
	}
	const query = `
		UPDATE payments
		SET checkout_initialization_lease_owner = $2,
		    checkout_initialization_lease_expires_at = $3,
		    version = version + 1,
		    updated_at = NOW()
		WHERE id = $1
		  AND checkout_initialization_state IN ('prepared', 'unknown')
		  AND status IN ('created', 'pending', 'requires_action')
		  AND (next_provider_check_at IS NULL OR next_provider_check_at <= NOW())
		  AND (
		      checkout_initialization_lease_expires_at IS NULL
		      OR checkout_initialization_lease_expires_at <= NOW()
		  )
		RETURNING
			id, public_token, booking_id, client_id, customer_id, purpose, provider, method,
			country_code, currency_code, amount_minor, price_snapshot, reference,
			provider_reference, provider_channel, idempotency_key, request_fingerprint, status, provider_status,
			reconciliation_reason, failure_code, failure_message, checkout_url, expires_at,
			paid_at, last_reconciled_at, version, created_at, updated_at
	`
	payment, err := scanFinancialPayment(r.db.QueryRow(ctx, query, paymentID, leaseOwner, leaseExpiresAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return FinancialPayment{}, false, nil
	}
	if err != nil {
		return FinancialPayment{}, false, fmt.Errorf("claim checkout initialization: %w", err)
	}
	return payment, true, nil
}

func (r *LedgerRepository) CreatePayment(ctx context.Context, params CreateFinancialPaymentParams) (FinancialPayment, bool, error) {
	if r == nil || r.db == nil {
		return FinancialPayment{}, false, errors.New("financial ledger repository is not configured")
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return FinancialPayment{}, false, fmt.Errorf("begin financial payment create: %w", err)
	}
	defer tx.Rollback(ctx)

	existing, err := getPaymentByIdempotencyTx(ctx, tx, params.BookingID, params.Purpose, params.IdempotencyKey)
	if err == nil {
		if existing.RequestFingerprint != params.RequestFingerprint {
			return FinancialPayment{}, false, ErrIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return FinancialPayment{}, false, fmt.Errorf("commit idempotent payment lookup: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, ErrLedgerRecordNotFound) {
		return FinancialPayment{}, false, err
	}
	if err := lockBookingFinancialStateTx(ctx, tx, params.BookingID); err != nil {
		return FinancialPayment{}, false, err
	}
	obligation, err := loadBookingPaymentObligation(ctx, tx, params.BookingID)
	if err != nil {
		return FinancialPayment{}, false, err
	}
	if obligation.ClientID != params.ClientID || obligation.CustomerID != params.CustomerID ||
		obligation.CountryCode != params.CountryCode || obligation.CurrencyCode != params.CurrencyCode {
		return FinancialPayment{}, false, errors.New("payment attempt does not match the booking owner or market")
	}
	if obligation.Purpose != params.Purpose || obligation.AmountMinor != params.AmountMinor {
		return FinancialPayment{}, false, ErrConcurrentUpdate
	}

	const query = `
		INSERT INTO payments (
			id, public_token, booking_id, client_id, customer_id, purpose, provider, method,
			country_code, currency_code, amount_minor, price_snapshot, reference,
			idempotency_key, request_fingerprint, status, checkout_details,
			checkout_initialization_state, checkout_initialization_lease_owner,
			checkout_initialization_lease_expires_at, next_provider_check_at, created_at, updated_at
		)
		VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,'created',$16,$17,$18,$19,$20,NOW(),NOW()
		)
		ON CONFLICT DO NOTHING
		RETURNING
			id, public_token, booking_id, client_id, customer_id, purpose, provider, method,
			country_code, currency_code, amount_minor, price_snapshot, reference,
				provider_reference, provider_channel, idempotency_key, request_fingerprint, status, provider_status,
			reconciliation_reason, failure_code, failure_message, checkout_url, expires_at,
			paid_at, last_reconciled_at, version, created_at, updated_at
	`

	payment, err := scanFinancialPayment(tx.QueryRow(
		ctx,
		query,
		params.ID,
		params.PublicToken,
		params.BookingID,
		params.ClientID,
		params.CustomerID,
		string(params.Purpose),
		params.Provider,
		params.Method,
		params.CountryCode,
		params.CurrencyCode,
		int64(params.AmountMinor),
		params.PriceSnapshot,
		params.Reference,
		params.IdempotencyKey,
		params.RequestFingerprint,
		nonEmptyJSONObject(params.CheckoutDetails),
		nonEmptyCheckoutInitializationState(params.CheckoutInitializationState),
		strings.TrimSpace(params.CheckoutInitializationLeaseOwner),
		params.CheckoutInitializationLeaseExpiresAt,
		params.NextProviderCheckAt,
	))
	created := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		payment, err = resolvePaymentCreateConflictTx(ctx, tx, params)
	}
	if err != nil {
		return FinancialPayment{}, false, fmt.Errorf("create financial payment: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return FinancialPayment{}, false, fmt.Errorf("commit financial payment create: %w", err)
	}
	return payment, created, nil
}

func (r *LedgerRepository) GetPaymentByPublicToken(ctx context.Context, token string) (FinancialPayment, error) {
	const query = `
		SELECT
			id, public_token, booking_id, client_id, customer_id, purpose, provider, method,
			country_code, currency_code, amount_minor, price_snapshot, reference,
				provider_reference, provider_channel, idempotency_key, request_fingerprint, status, provider_status,
			reconciliation_reason, failure_code, failure_message, checkout_url, expires_at,
			paid_at, last_reconciled_at, version, created_at, updated_at
		FROM payments
		WHERE public_token = $1
	`
	payment, err := scanFinancialPayment(r.db.QueryRow(ctx, query, strings.TrimSpace(token)))
	if errors.Is(err, pgx.ErrNoRows) {
		return FinancialPayment{}, ErrLedgerRecordNotFound
	}
	if err != nil {
		return FinancialPayment{}, fmt.Errorf("get payment by public token: %w", err)
	}
	return payment, nil
}

func (r *LedgerRepository) GetPaymentByReference(ctx context.Context, provider, reference string) (FinancialPayment, error) {
	const query = `
		SELECT
			id, public_token, booking_id, client_id, customer_id, purpose, provider, method,
			country_code, currency_code, amount_minor, price_snapshot, reference,
				provider_reference, provider_channel, idempotency_key, request_fingerprint, status, provider_status,
			reconciliation_reason, failure_code, failure_message, checkout_url, expires_at,
			paid_at, last_reconciled_at, version, created_at, updated_at
		FROM payments
		WHERE provider = $1 AND reference = $2
	`
	payment, err := scanFinancialPayment(r.db.QueryRow(
		ctx, query, strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(reference),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return FinancialPayment{}, ErrLedgerRecordNotFound
	}
	if err != nil {
		return FinancialPayment{}, fmt.Errorf("get payment by provider reference: %w", err)
	}
	return payment, nil
}

func (r *LedgerRepository) GetLatestPaymentForBooking(ctx context.Context, bookingID uuid.UUID) (FinancialPayment, error) {
	const query = `
		SELECT
			id, public_token, booking_id, client_id, customer_id, purpose, provider, method,
			country_code, currency_code, amount_minor, price_snapshot, reference,
				provider_reference, provider_channel, idempotency_key, request_fingerprint, status, provider_status,
			reconciliation_reason, failure_code, failure_message, checkout_url, expires_at,
			paid_at, last_reconciled_at, version, created_at, updated_at
		FROM payments
		WHERE booking_id = $1
		ORDER BY created_at DESC
		LIMIT 1
	`
	payment, err := scanFinancialPayment(r.db.QueryRow(ctx, query, bookingID))
	if errors.Is(err, pgx.ErrNoRows) {
		return FinancialPayment{}, ErrLedgerRecordNotFound
	}
	if err != nil {
		return FinancialPayment{}, fmt.Errorf("get latest booking payment: %w", err)
	}
	return payment, nil
}

func (r *LedgerRepository) ClaimStalePayments(
	ctx context.Context,
	workerID string,
	staleBefore time.Time,
	limit int,
	leaseDuration time.Duration,
) ([]FinancialPayment, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || limit <= 0 || limit > 500 || leaseDuration <= 0 {
		return nil, errors.New("worker ID, stale payment limit, and lease duration are required")
	}
	leaseSeconds := max(int64(leaseDuration/time.Second), 1)
	const query = `
		WITH candidates AS (
			SELECT id
			FROM payments
			WHERE status IN ('created', 'pending', 'requires_action')
			  AND updated_at <= $1
			  AND (reconciliation_lease_expires_at IS NULL OR reconciliation_lease_expires_at <= NOW())
			ORDER BY updated_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE payments AS payment
		SET reconciliation_lease_owner = $3,
			reconciliation_lease_expires_at = NOW() + ($4 * INTERVAL '1 second')
		FROM candidates
		WHERE payment.id = candidates.id
		RETURNING
			payment.id, payment.public_token, payment.booking_id, payment.client_id, payment.customer_id,
			payment.purpose, payment.provider, payment.method,
			payment.country_code, payment.currency_code, payment.amount_minor, payment.price_snapshot, payment.reference,
				payment.provider_reference, payment.provider_channel, payment.idempotency_key, payment.request_fingerprint, payment.status, payment.provider_status,
			payment.reconciliation_reason, payment.failure_code, payment.failure_message, payment.checkout_url, payment.expires_at,
			payment.paid_at, payment.last_reconciled_at, payment.version, payment.created_at, payment.updated_at
	`
	rows, err := r.db.Query(ctx, query, staleBefore, limit, workerID, leaseSeconds)
	if err != nil {
		return nil, fmt.Errorf("list stale payments: %w", err)
	}
	defer rows.Close()

	result := make([]FinancialPayment, 0)
	for rows.Next() {
		payment, err := scanFinancialPayment(rows)
		if err != nil {
			return nil, fmt.Errorf("scan stale payment: %w", err)
		}
		result = append(result, payment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stale payments: %w", err)
	}
	return result, nil
}

func (r *LedgerRepository) DelayPaymentReconciliation(
	ctx context.Context,
	paymentID uuid.UUID,
	workerID string,
	retryAt time.Time,
) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE payments
		SET reconciliation_lease_expires_at = $3
		WHERE id = $1 AND reconciliation_lease_owner = $2
	`, paymentID, strings.TrimSpace(workerID), retryAt)
	if err != nil {
		return fmt.Errorf("delay payment reconciliation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConcurrentUpdate
	}
	return nil
}

func (r *LedgerRepository) WithPaymentReconciliationLock(
	ctx context.Context,
	paymentID uuid.UUID,
	fn func() (FinancialPayment, error),
) (FinancialPayment, error) {
	if r == nil || r.db == nil || paymentID == uuid.Nil || fn == nil {
		return FinancialPayment{}, errors.New("invalid payment reconciliation lock")
	}
	connection, err := r.db.Acquire(ctx)
	if err != nil {
		return FinancialPayment{}, fmt.Errorf("acquire payment reconciliation connection: %w", err)
	}
	defer connection.Release()
	lockKey := "payment-reconciliation:" + paymentID.String()
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return FinancialPayment{}, fmt.Errorf("acquire payment reconciliation lock: %w", err)
	}
	defer func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = connection.Exec(unlockContext, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, lockKey)
	}()
	return fn()
}

func (r *LedgerRepository) TransitionPayment(
	ctx context.Context,
	paymentID uuid.UUID,
	expectedVersion int64,
	to PaymentStatus,
	update PaymentTransitionUpdate,
) (FinancialPayment, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return FinancialPayment{}, fmt.Errorf("begin payment transition: %w", err)
	}
	defer tx.Rollback(ctx)
	var bookingID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT booking_id FROM payments WHERE id = $1`, paymentID).Scan(&bookingID); errors.Is(err, pgx.ErrNoRows) {
		return FinancialPayment{}, ErrLedgerRecordNotFound
	} else if err != nil {
		return FinancialPayment{}, fmt.Errorf("load payment booking: %w", err)
	}
	if err := lockBookingFinancialStateTx(ctx, tx, bookingID); err != nil {
		return FinancialPayment{}, err
	}

	payment, err := getPaymentForUpdate(ctx, tx, paymentID)
	if err != nil {
		return FinancialPayment{}, err
	}
	if payment.Version != expectedVersion {
		return FinancialPayment{}, ErrConcurrentUpdate
	}
	if err := ValidatePaymentTransition(payment.Status, to); err != nil {
		return FinancialPayment{}, err
	}
	providerChannel := strings.ToLower(strings.TrimSpace(update.ProviderChannel))
	if payment.ProviderChannel != "" && providerChannel != "" && payment.ProviderChannel != providerChannel {
		return FinancialPayment{}, ErrProviderEvidenceConflict
	}
	paidAt := update.PaidAt
	if to == PaymentStatusPaid && paidAt == nil {
		now := time.Now().UTC()
		paidAt = &now
	}
	const query = `
		UPDATE payments
		SET
			status = $3,
				provider_reference = CASE WHEN $4 <> '' THEN $4 ELSE provider_reference END,
				provider_channel = CASE WHEN $5 <> '' THEN $5 ELSE provider_channel END,
				provider_status = $6,
				reconciliation_reason = $7,
				failure_code = $8,
				failure_message = $9,
				checkout_url = CASE WHEN $10 <> '' THEN $10 ELSE checkout_url END,
				expires_at = COALESCE($11, expires_at),
				paid_at = COALESCE($12, paid_at),
				last_reconciled_at = COALESCE($13, last_reconciled_at),
					checkout_details = CASE WHEN $14::jsonb IS NOT NULL THEN $14::jsonb ELSE checkout_details END,
				reconciliation_lease_owner = '',
					reconciliation_lease_expires_at = NULL,
				checkout_initialization_state = CASE WHEN $15 <> '' THEN $15 ELSE checkout_initialization_state END,
				checkout_initialization_lease_owner = CASE WHEN $15 <> '' THEN '' ELSE checkout_initialization_lease_owner END,
				checkout_initialization_lease_expires_at = CASE WHEN $15 <> '' THEN NULL ELSE checkout_initialization_lease_expires_at END,
				next_provider_check_at = CASE WHEN $15 <> '' THEN $16 ELSE next_provider_check_at END,
			version = version + 1,
			updated_at = NOW()
		WHERE id = $1 AND version = $2
		  AND ($17 = '' OR checkout_initialization_lease_owner = $17)
		RETURNING
			id, public_token, booking_id, client_id, customer_id, purpose, provider, method,
			country_code, currency_code, amount_minor, price_snapshot, reference,
				provider_reference, provider_channel, idempotency_key, request_fingerprint, status, provider_status,
			reconciliation_reason, failure_code, failure_message, checkout_url, expires_at,
			paid_at, last_reconciled_at, version, created_at, updated_at
	`
	transitioned, err := scanFinancialPayment(tx.QueryRow(
		ctx, query, paymentID, expectedVersion, string(to), strings.TrimSpace(update.ProviderReference), providerChannel,
		strings.TrimSpace(update.ProviderStatus), strings.TrimSpace(update.ReconciliationReason),
		strings.TrimSpace(update.FailureCode), strings.TrimSpace(update.FailureMessage),
		strings.TrimSpace(update.CheckoutURL), update.ExpiresAt, paidAt, update.ReconciledAt,
		nullableJSON(update.CheckoutDetails),
		string(update.CheckoutInitializationState), update.NextProviderCheckAt,
		strings.TrimSpace(update.ExpectedInitializationLeaseOwner),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return FinancialPayment{}, ErrConcurrentUpdate
	}
	if err != nil {
		return FinancialPayment{}, fmt.Errorf("transition payment: %w", err)
	}

	if to == PaymentStatusPaid {
		if err := enqueueFinancialJobTx(ctx, tx, FinancialJobParams{
			ID:               uuid.New(),
			Kind:             "create_payment_allocation",
			AggregateType:    "payment",
			AggregateID:      paymentID,
			DeduplicationKey: "create_payment_allocation:" + paymentID.String(),
			Payload:          json.RawMessage(`{}`),
		}); err != nil {
			return FinancialPayment{}, err
		}
	}
	if err := recomputeBookingPaymentStateTx(ctx, tx, payment.BookingID); err != nil {
		return FinancialPayment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FinancialPayment{}, fmt.Errorf("commit payment transition: %w", err)
	}
	return transitioned, nil
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func nonEmptyJSONObject(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

func nonEmptyCheckoutInitializationState(value CheckoutInitializationState) CheckoutInitializationState {
	if value == "" {
		return CheckoutInitializationReady
	}
	return value
}

func getPaymentByIdempotencyTx(
	ctx context.Context,
	querier queryRower,
	bookingID uuid.UUID,
	purpose PaymentPurpose,
	idempotencyKey string,
) (FinancialPayment, error) {
	const query = `
		SELECT
			id, public_token, booking_id, client_id, customer_id, purpose, provider, method,
			country_code, currency_code, amount_minor, price_snapshot, reference,
				provider_reference, provider_channel, idempotency_key, request_fingerprint, status, provider_status,
			reconciliation_reason, failure_code, failure_message, checkout_url, expires_at,
			paid_at, last_reconciled_at, version, created_at, updated_at
		FROM payments
		WHERE booking_id = $1 AND purpose = $2 AND idempotency_key = $3
	`
	payment, err := scanFinancialPayment(querier.QueryRow(ctx, query, bookingID, string(purpose), idempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return FinancialPayment{}, ErrLedgerRecordNotFound
	}
	if err != nil {
		return FinancialPayment{}, fmt.Errorf("get payment by idempotency key: %w", err)
	}
	return payment, nil
}

func getActivePaymentTx(ctx context.Context, querier queryRower, bookingID uuid.UUID, purpose PaymentPurpose) (FinancialPayment, error) {
	const query = `
		SELECT
			id, public_token, booking_id, client_id, customer_id, purpose, provider, method,
			country_code, currency_code, amount_minor, price_snapshot, reference,
				provider_reference, provider_channel, idempotency_key, request_fingerprint, status, provider_status,
			reconciliation_reason, failure_code, failure_message, checkout_url, expires_at,
			paid_at, last_reconciled_at, version, created_at, updated_at
		FROM payments
		WHERE booking_id = $1 AND purpose = $2 AND status IN ('created', 'pending', 'requires_action')
		ORDER BY created_at DESC
		LIMIT 1
	`
	payment, err := scanFinancialPayment(querier.QueryRow(ctx, query, bookingID, string(purpose)))
	if errors.Is(err, pgx.ErrNoRows) {
		return FinancialPayment{}, ErrLedgerRecordNotFound
	}
	if err != nil {
		return FinancialPayment{}, fmt.Errorf("get active payment: %w", err)
	}
	return payment, nil
}

func resolvePaymentCreateConflictTx(ctx context.Context, tx pgx.Tx, params CreateFinancialPaymentParams) (FinancialPayment, error) {
	existing, err := getPaymentByIdempotencyTx(ctx, tx, params.BookingID, params.Purpose, params.IdempotencyKey)
	if err == nil {
		if existing.RequestFingerprint != params.RequestFingerprint {
			return FinancialPayment{}, ErrIdempotencyConflict
		}
		return existing, nil
	}
	if !errors.Is(err, ErrLedgerRecordNotFound) {
		return FinancialPayment{}, err
	}
	active, err := getActivePaymentTx(ctx, tx, params.BookingID, params.Purpose)
	if err == nil {
		return FinancialPayment{}, &ActivePaymentError{Payment: active}
	}
	return FinancialPayment{}, err
}

func lockBookingFinancialStateTx(ctx context.Context, tx pgx.Tx, bookingID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, bookingID.String()); err != nil {
		return fmt.Errorf("lock booking financial state: %w", err)
	}
	return nil
}

func loadBookingPaymentObligation(
	ctx context.Context,
	querier queryRower,
	bookingID uuid.UUID,
) (BookingPaymentObligation, error) {
	const query = `
		SELECT
			b.client_id,
			b.customer_id,
			b.country_code,
			b.currency_code,
			b.total_amount_minor,
			b.deposit_amount_minor,
			COALESCE((
				SELECT SUM(p.amount_minor)
				FROM payments p
				WHERE p.booking_id = b.id
				  AND p.status IN ('paid', 'partially_refunded', 'refunded', 'disputed', 'reversed')
			), 0),
			COALESCE((
				SELECT SUM(a.allocation_impact_minor)
				FROM payment_adjustments a
				INNER JOIN payments adjusted_payment ON adjusted_payment.id = a.payment_id
				WHERE adjusted_payment.booking_id = b.id AND a.status = 'successful'
			), 0)
		FROM bookings b
		WHERE b.id = $1
	`
	var obligation BookingPaymentObligation
	var totalMinor, depositMinor, grossPaidMinor, adjustmentMinor int64
	if err := querier.QueryRow(ctx, query, bookingID).Scan(
		&obligation.ClientID, &obligation.CustomerID, &obligation.CountryCode, &obligation.CurrencyCode,
		&totalMinor, &depositMinor, &grossPaidMinor, &adjustmentMinor,
	); errors.Is(err, pgx.ErrNoRows) {
		return BookingPaymentObligation{}, ErrLedgerRecordNotFound
	} else if err != nil {
		return BookingPaymentObligation{}, fmt.Errorf("load booking payment obligation: %w", err)
	}
	if totalMinor <= 0 || depositMinor < 0 || depositMinor > totalMinor {
		return BookingPaymentObligation{}, errors.New("booking has invalid payment amounts")
	}
	netPaidMinor := grossPaidMinor - adjustmentMinor
	if netPaidMinor < 0 {
		netPaidMinor = 0
	}
	if netPaidMinor >= totalMinor {
		return BookingPaymentObligation{}, ErrPaymentObligationSatisfied
	}

	purpose := PaymentPurposeFull
	payableMinor := totalMinor - netPaidMinor
	if depositMinor > 0 && depositMinor < totalMinor {
		if netPaidMinor < depositMinor {
			purpose = PaymentPurposeDeposit
			payableMinor = depositMinor - netPaidMinor
		} else {
			purpose = PaymentPurposeBalance
		}
	}
	obligation.Purpose = purpose
	obligation.AmountMinor = money.Minor(payableMinor)
	obligation.TotalAmountMinor = money.Minor(totalMinor)
	obligation.DepositAmountMinor = money.Minor(depositMinor)
	return obligation, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanFinancialPayment(row rowScanner) (FinancialPayment, error) {
	var payment FinancialPayment
	var purpose string
	var status string
	var amount int64
	var providerChannel *string
	if err := row.Scan(
		&payment.ID, &payment.PublicToken, &payment.BookingID, &payment.ClientID, &payment.CustomerID,
		&purpose, &payment.Provider, &payment.Method, &payment.CountryCode, &payment.CurrencyCode,
		&amount, &payment.PriceSnapshot, &payment.Reference, &payment.ProviderReference, &providerChannel,
		&payment.IdempotencyKey, &payment.RequestFingerprint, &status, &payment.ProviderStatus,
		&payment.ReconciliationReason, &payment.FailureCode, &payment.FailureMessage,
		&payment.CheckoutURL, &payment.ExpiresAt, &payment.PaidAt, &payment.LastReconciledAt,
		&payment.Version, &payment.CreatedAt, &payment.UpdatedAt,
	); err != nil {
		return FinancialPayment{}, err
	}
	payment.Purpose = PaymentPurpose(purpose)
	payment.Status = PaymentStatus(status)
	payment.AmountMinor = money.Minor(amount)
	if providerChannel != nil {
		payment.ProviderChannel = *providerChannel
	}
	return payment, nil
}

func getPaymentForUpdate(ctx context.Context, tx pgx.Tx, paymentID uuid.UUID) (FinancialPayment, error) {
	const query = `
		SELECT
			id, public_token, booking_id, client_id, customer_id, purpose, provider, method,
			country_code, currency_code, amount_minor, price_snapshot, reference,
				provider_reference, provider_channel, idempotency_key, request_fingerprint, status, provider_status,
			reconciliation_reason, failure_code, failure_message, checkout_url, expires_at,
			paid_at, last_reconciled_at, version, created_at, updated_at
		FROM payments
		WHERE id = $1
		FOR UPDATE
	`
	payment, err := scanFinancialPayment(tx.QueryRow(ctx, query, paymentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return FinancialPayment{}, ErrLedgerRecordNotFound
	}
	if err != nil {
		return FinancialPayment{}, fmt.Errorf("lock payment: %w", err)
	}
	return payment, nil
}

func recomputeBookingPaymentStateTx(ctx context.Context, tx pgx.Tx, bookingID uuid.UUID) error {
	const query = `
		SELECT
			b.total_amount_minor,
			b.deposit_amount_minor,
			COALESCE(SUM(p.amount_minor) FILTER (
				WHERE p.status IN ('paid', 'partially_refunded', 'refunded', 'disputed', 'reversed')
			), 0),
			COALESCE((
				SELECT SUM(a.allocation_impact_minor)
				FROM payment_adjustments a
				JOIN payments adjusted_payment ON adjusted_payment.id = a.payment_id
				WHERE adjusted_payment.booking_id = b.id AND a.status = 'successful'
			), 0),
			COALESCE(BOOL_OR(p.status IN ('created', 'pending', 'requires_action')), FALSE),
			COALESCE(BOOL_OR(p.status = 'failed'), FALSE),
			COALESCE(BOOL_OR(p.status = 'disputed'), FALSE),
			COALESCE(BOOL_OR(p.status IN ('refunded', 'reversed')), FALSE)
		FROM bookings b
		LEFT JOIN payments p ON p.booking_id = b.id
		WHERE b.id = $1
		GROUP BY b.id, b.total_amount_minor, b.deposit_amount_minor
	`
	var totalMinor, depositMinor, grossPaidMinor, adjustmentMinor int64
	var pending, failed, disputed, refunded bool
	if err := tx.QueryRow(ctx, query, bookingID).Scan(
		&totalMinor, &depositMinor, &grossPaidMinor, &adjustmentMinor,
		&pending, &failed, &disputed, &refunded,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLedgerRecordNotFound
		}
		return fmt.Errorf("load booking payment obligations: %w", err)
	}
	netPaidMinor := grossPaidMinor - adjustmentMinor
	if netPaidMinor < 0 {
		netPaidMinor = 0
	}
	state, err := DeriveBookingPaymentState(BookingObligationSummary{
		TotalMinor: totalMinor, DepositMinor: depositMinor, NetPaidMinor: netPaidMinor,
		HasPendingAttempt: pending, HasFailedAttempt: failed, Refunded: refunded, Disputed: disputed,
	})
	if err != nil {
		return fmt.Errorf("derive booking payment state: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE bookings SET payment_status = $2, updated_at = NOW() WHERE id = $1`, bookingID, string(state)); err != nil {
		return fmt.Errorf("update booking payment state: %w", err)
	}
	return nil
}

type FinancialJobParams struct {
	ID               uuid.UUID
	Kind             string
	AggregateType    string
	AggregateID      uuid.UUID
	DeduplicationKey string
	Payload          json.RawMessage
}

func enqueueFinancialJobTx(ctx context.Context, tx pgx.Tx, params FinancialJobParams) error {
	const query = `
		INSERT INTO financial_jobs (
			id, kind, aggregate_type, aggregate_id, deduplication_key, payload,
			status, available_at, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,'pending',NOW(),NOW(),NOW())
		ON CONFLICT (deduplication_key) DO NOTHING
	`
	if _, err := tx.Exec(
		ctx, query, params.ID, params.Kind, params.AggregateType,
		params.AggregateID, params.DeduplicationKey, params.Payload,
	); err != nil {
		return fmt.Errorf("enqueue financial job: %w", err)
	}
	return nil
}
