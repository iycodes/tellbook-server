package payments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"booking/go-server/internal/money"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrPayoutAllocationIneligible = errors.New("payment allocation is not eligible for payout")
	ErrPayoutDestinationMismatch  = errors.New("payout destination does not match allocation")
)

// PayoutReview is safe for authorized staff. Destination secrets remain in the
// ledger; their digest binds the review without exposing the underlying values.
type PayoutReview struct {
	BusinessID           uuid.UUID   `json:"business_id"`
	PaymentID            uuid.UUID   `json:"payment_id"`
	AllocationID         uuid.UUID   `json:"allocation_id"`
	DestinationID        uuid.UUID   `json:"destination_id"`
	AmountMinor          money.Minor `json:"amount_minor"`
	CurrencyCode         string      `json:"currency_code"`
	Provider             string      `json:"provider"`
	Rail                 string      `json:"rail"`
	CountryCode          string      `json:"country_code"`
	InstitutionName      string      `json:"institution_name"`
	MaskedIdentifier     string      `json:"masked_identifier"`
	AllocationUpdatedAt  time.Time   `json:"allocation_updated_at"`
	DestinationUpdatedAt time.Time   `json:"destination_updated_at"`
	Fingerprint          string      `json:"fingerprint"`
}

// ReviewPayoutTx neither reserves funds nor creates a worker-visible payout.
// It shares eligibility and row-lock order with the existing creation path.
func (r *LedgerRepository) ReviewPayoutTx(ctx context.Context, tx pgx.Tx, input CreateFinancialPayoutInput) (PayoutReview, error) {
	a, d, err := r.lockPayoutInputs(ctx, tx, input)
	if err != nil {
		return PayoutReview{}, err
	}
	if active, err := getActivePayoutByAllocationTx(ctx, tx, a.ID); err == nil {
		return PayoutReview{}, &ActivePayoutError{Payout: active}
	} else if !errors.Is(err, ErrLedgerRecordNotFound) {
		return PayoutReview{}, err
	}
	return payoutReview(a, d)
}

func payoutReview(a PaymentAllocation, d PayoutDestination) (PayoutReview, error) {
	review := PayoutReview{BusinessID: a.ClientID, PaymentID: a.PaymentID, AllocationID: a.ID, DestinationID: d.ID,
		AmountMinor: money.Minor(a.Amounts.BusinessNetAmountMinor), CurrencyCode: a.CurrencyCode, Provider: d.Provider, Rail: d.Rail,
		CountryCode: d.CountryCode, InstitutionName: d.InstitutionName, MaskedIdentifier: d.MaskedIdentifier,
		AllocationUpdatedAt: a.UpdatedAt, DestinationUpdatedAt: d.UpdatedAt}
	// Canonical JSON includes all ledger allocation/destination inputs, including
	// recipient identity and evidence. A timestamp-only comparison is insufficient.
	raw, err := json.Marshal(struct {
		Allocation  PaymentAllocation
		Destination PayoutDestination
	}{a, d})
	if err != nil {
		return PayoutReview{}, err
	}
	digest := sha256.Sum256(raw)
	review.Fingerprint = hex.EncodeToString(digest[:])
	return review, nil
}

var ErrReviewedPayoutChanged = errors.New("reviewed payout terms changed")

// CreateReviewedPayoutTx revalidates exact terms while holding the same locks as
// provider payout creation. It reserves the allocation in the caller's transaction.
// It performs no provider call. A committed created payout is worker-visible;
// callers must authorize and persist approval consumption/audit before committing.
func (r *LedgerRepository) CreateReviewedPayoutTx(ctx context.Context, tx pgx.Tx, input CreateFinancialPayoutInput, fingerprint string) (FinancialPayout, error) {
	decoded, err := hex.DecodeString(fingerprint)
	if tx == nil || input.ClientID == uuid.Nil || input.PaymentAllocationID == uuid.Nil || input.PayoutDestinationID == uuid.Nil || !idempotencyKeyPattern.MatchString(input.IdempotencyKey) || err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != fingerprint {
		return FinancialPayout{}, errors.New("invalid reviewed payout")
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "reviewed-payout:"+input.ClientID.String()+":"+input.IdempotencyKey); err != nil {
		return FinancialPayout{}, err
	}
	reference, err := newProviderReference(payoutReferencePrefix)
	if err != nil {
		return FinancialPayout{}, err
	}
	return r.createPayoutAttemptTx(ctx, tx, input, reference, fingerprint)
}

func (s *PayoutService) recipientFromDestination(d PayoutDestination, includeIdentifier bool) (ProviderRecipient, error) {
	recipient := ProviderRecipient{ProviderReference: d.ProviderRecipientID, CountryCode: d.CountryCode, CurrencyCode: d.CurrencyCode, Rail: d.Rail, InstitutionCode: d.InstitutionCode, InstitutionName: d.InstitutionName, AccountName: d.ResolvedAccountName}
	if recipient.ProviderReference == "" || includeIdentifier {
		identifier, err := s.ledger.revealPayoutDestination(d)
		if err != nil {
			return ProviderRecipient{}, err
		}
		recipient.Identifier = identifier
	}
	return recipient, nil
}
