package payments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"booking/go-server/internal/money"
	"booking/go-server/internal/publictoken"
	"booking/go-server/internal/secure"

	"github.com/google/uuid"
)

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

type CreatePaymentAttemptInput struct {
	BookingID                            uuid.UUID
	ClientID                             uuid.UUID
	CustomerID                           uuid.UUID
	Purpose                              PaymentPurpose
	Provider                             string
	Method                               string
	CountryCode                          string
	CurrencyCode                         string
	AmountMinor                          money.Minor
	PriceSnapshot                        map[string]string
	IdempotencyKey                       string
	identity                             *paymentAttemptIdentity
	checkoutDetails                      json.RawMessage
	checkoutInitializationState          CheckoutInitializationState
	checkoutInitializationLeaseOwner     string
	checkoutInitializationLeaseExpiresAt *time.Time
	nextProviderCheckAt                  *time.Time
}

type paymentAttemptIdentity struct {
	ID          uuid.UUID
	PublicToken string
	Reference   string
}

type LedgerService struct {
	repository    *LedgerRepository
	keyring       *secure.Keyring
	fingerprinter *secure.Fingerprinter
}

func NewLedgerService(
	repository *LedgerRepository,
	keyring *secure.Keyring,
	fingerprinter *secure.Fingerprinter,
) (*LedgerService, error) {
	if repository == nil {
		return nil, errors.New("financial ledger repository is required")
	}
	if (keyring == nil) != (fingerprinter == nil) {
		return nil, errors.New("financial keyring and fingerprinter must be configured together")
	}
	return &LedgerService{repository: repository, keyring: keyring, fingerprinter: fingerprinter}, nil
}

func (s *LedgerService) CreatePaymentAttempt(ctx context.Context, input CreatePaymentAttemptInput) (FinancialPayment, bool, error) {
	if err := validateCreatePaymentAttempt(input); err != nil {
		return FinancialPayment{}, false, err
	}
	snapshot, err := json.Marshal(input.PriceSnapshot)
	if err != nil {
		return FinancialPayment{}, false, fmt.Errorf("encode payment price snapshot: %w", err)
	}
	fingerprint, err := paymentRequestFingerprint(input, snapshot)
	if err != nil {
		return FinancialPayment{}, false, err
	}
	identity := input.identity
	if identity == nil {
		generated, identityErr := newPaymentAttemptIdentity()
		if identityErr != nil {
			return FinancialPayment{}, false, identityErr
		}
		identity = &generated
	}

	return s.repository.CreatePayment(ctx, CreateFinancialPaymentParams{
		ID:                                   identity.ID,
		PublicToken:                          identity.PublicToken,
		BookingID:                            input.BookingID,
		ClientID:                             input.ClientID,
		CustomerID:                           input.CustomerID,
		Purpose:                              input.Purpose,
		Provider:                             strings.ToLower(strings.TrimSpace(input.Provider)),
		Method:                               strings.TrimSpace(input.Method),
		CountryCode:                          strings.ToUpper(strings.TrimSpace(input.CountryCode)),
		CurrencyCode:                         strings.ToUpper(strings.TrimSpace(input.CurrencyCode)),
		AmountMinor:                          input.AmountMinor,
		PriceSnapshot:                        snapshot,
		Reference:                            identity.Reference,
		IdempotencyKey:                       strings.TrimSpace(input.IdempotencyKey),
		RequestFingerprint:                   fingerprint,
		CheckoutDetails:                      input.checkoutDetails,
		CheckoutInitializationState:          input.checkoutInitializationState,
		CheckoutInitializationLeaseOwner:     input.checkoutInitializationLeaseOwner,
		CheckoutInitializationLeaseExpiresAt: input.checkoutInitializationLeaseExpiresAt,
		NextProviderCheckAt:                  input.nextProviderCheckAt,
	})
}

func newPaymentAttemptIdentity() (paymentAttemptIdentity, error) {
	publicToken, err := publictoken.New()
	if err != nil {
		return paymentAttemptIdentity{}, err
	}
	reference, err := newProviderReference(paymentReferencePrefix)
	if err != nil {
		return paymentAttemptIdentity{}, err
	}
	return paymentAttemptIdentity{ID: uuid.New(), PublicToken: publicToken, Reference: reference}, nil
}

func validateCreatePaymentAttempt(input CreatePaymentAttemptInput) error {
	if input.BookingID == uuid.Nil || input.ClientID == uuid.Nil || input.CustomerID == uuid.Nil {
		return errors.New("booking, client, and customer are required")
	}
	if !input.Purpose.Valid() {
		return errors.New("invalid payment purpose")
	}
	provider := strings.ToLower(strings.TrimSpace(input.Provider))
	if provider != "payaza" && provider != "paystack" {
		return errors.New("invalid payment provider")
	}
	if strings.TrimSpace(input.Method) == "" {
		return errors.New("payment method is required")
	}
	if !isUpperASCII(strings.ToUpper(strings.TrimSpace(input.CountryCode)), 2) ||
		!isUpperASCII(strings.ToUpper(strings.TrimSpace(input.CurrencyCode)), 3) {
		return errors.New("invalid payment country or currency")
	}
	if input.AmountMinor <= 0 {
		return errors.New("payment amount must be positive")
	}
	if !idempotencyKeyPattern.MatchString(strings.TrimSpace(input.IdempotencyKey)) {
		return errors.New("invalid idempotency key")
	}
	if len(input.PriceSnapshot) == 0 {
		return errors.New("payment price snapshot is required")
	}
	return nil
}

func paymentRequestFingerprint(input CreatePaymentAttemptInput, snapshot []byte) (string, error) {
	payload := struct {
		BookingID     string          `json:"booking_id"`
		Purpose       PaymentPurpose  `json:"purpose"`
		Provider      string          `json:"provider"`
		Method        string          `json:"method"`
		CountryCode   string          `json:"country_code"`
		CurrencyCode  string          `json:"currency_code"`
		AmountMinor   string          `json:"amount_minor"`
		PriceSnapshot json.RawMessage `json:"price_snapshot"`
	}{
		BookingID: input.BookingID.String(), Purpose: input.Purpose,
		Provider: strings.ToLower(strings.TrimSpace(input.Provider)), Method: strings.TrimSpace(input.Method),
		CountryCode:  strings.ToUpper(strings.TrimSpace(input.CountryCode)),
		CurrencyCode: strings.ToUpper(strings.TrimSpace(input.CurrencyCode)),
		AmountMinor:  fmt.Sprintf("%d", input.AmountMinor), PriceSnapshot: snapshot,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode payment request fingerprint: %w", err)
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
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
