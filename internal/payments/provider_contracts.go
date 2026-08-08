package payments

import (
	"context"
	"errors"
	"net/http"
	"time"

	"booking/go-server/internal/money"

	"github.com/google/uuid"
)

var ErrCheckoutRecoveryNotReady = errors.New("checkout recovery is not ready")

type PaymentEvidenceMismatchError struct {
	Reconciliation PaymentReconciliation
	Message        string
}

func (e *PaymentEvidenceMismatchError) Error() string {
	if e == nil || e.Message == "" {
		return "provider payment evidence does not match the stored obligation"
	}
	return e.Message
}

type PaymentSnapshot struct {
	PaymentID        uuid.UUID         `json:"payment_id"`
	Reference        string            `json:"reference"`
	Purpose          PaymentPurpose    `json:"purpose"`
	Provider         string            `json:"provider"`
	Method           string            `json:"method"`
	CountryCode      string            `json:"country_code"`
	CurrencyCode     string            `json:"currency_code"`
	CurrencyExponent uint8             `json:"currency_exponent"`
	AmountMinor      money.Minor       `json:"amount_minor"`
	CustomerName     string            `json:"customer_name"`
	CustomerEmail    string            `json:"customer_email"`
	CustomerPhone    string            `json:"customer_phone"`
	Description      string            `json:"description"`
	ReturnURL        string            `json:"return_url"`
	Metadata         map[string]string `json:"metadata"`
	RequestedAt      time.Time         `json:"requested_at"`
}

const (
	PaymentMethodCard             = "card"
	PaymentMethodBankTransfer     = "bank_transfer"
	PaymentMethodHostedHistorical = "hosted_checkout"
)

type CheckoutFlow string

const (
	CheckoutFlowHostedRedirect CheckoutFlow = "hosted_redirect"
	CheckoutFlowHostedModal    CheckoutFlow = "hosted_modal"
	CheckoutFlowBankTransfer   CheckoutFlow = "bank_transfer"
)

type BankTransferInstructions struct {
	AccountName       string `json:"account_name"`
	AccountNumber     string `json:"account_number"`
	BankName          string `json:"bank_name"`
	TransferReference string `json:"transfer_reference,omitempty"`
}

type CheckoutSession struct {
	Flow              CheckoutFlow              `json:"flow"`
	ProviderReference string                    `json:"provider_reference,omitempty"`
	CheckoutURL       string                    `json:"checkout_url,omitempty"`
	PublicKey         string                    `json:"public_key,omitempty"`
	ExpiresAt         *time.Time                `json:"expires_at,omitempty"`
	Instructions      map[string]string         `json:"instructions,omitempty"`
	BankTransfer      *BankTransferInstructions `json:"bank_transfer,omitempty"`
}

type CheckoutRecord struct {
	Version  int              `json:"version"`
	Snapshot PaymentSnapshot  `json:"snapshot"`
	Session  *CheckoutSession `json:"session,omitempty"`
}

type CheckoutInitializationState string

const (
	CheckoutInitializationPrepared CheckoutInitializationState = "prepared"
	CheckoutInitializationReady    CheckoutInitializationState = "ready"
	CheckoutInitializationUnknown  CheckoutInitializationState = "unknown"
)

type PaymentRecord struct {
	ID                uuid.UUID
	Reference         string
	Provider          string
	ProviderReference string
	Method            string
	Status            PaymentStatus
	CurrencyCode      string
	CurrencyExponent  uint8
	AmountMinor       money.Minor
}

type PaymentReconciliation struct {
	ProviderStatus       string
	ProviderChannel      string
	ReconciliationReason string
	FailureCode          string
	FailureMessage       string
	Status               PaymentStatus
	AmountMinor          money.Minor
	CurrencyCode         string
	PaidAt               *time.Time
}

type CollectionProvider interface {
	InitializeCheckout(context.Context, PaymentSnapshot) (CheckoutSession, error)
	RecoverCheckout(context.Context, PaymentSnapshot) (CheckoutSession, error)
	ReconcilePayment(context.Context, PaymentRecord) (PaymentReconciliation, error)
}

type SettlementQuery struct {
	From time.Time
	To   time.Time
}

type SettlementEvidence struct {
	Provider            string
	SettlementReference string
	PaymentReference    string
	ProviderStatus      string
	SettlementStatus    string
	AmountMinor         money.Minor
	CurrencyCode        string
	AvailableAt         time.Time
}

type SettlementProvider interface {
	ListSettlementEvidence(context.Context, SettlementQuery) ([]SettlementEvidence, error)
}

type VerifiedEvent struct {
	ProviderEventID string
	EventType       string
	Normalized      map[string]any
}

type WebhookVerifier interface {
	VerifyAndDecodeWebhook(rawBody []byte, headers http.Header) (VerifiedEvent, error)
}

type DestinationQuery struct {
	CountryCode  string
	CurrencyCode string
	Rail         string
}

type DestinationOption struct {
	Code string
	Name string
}

type ResolveDestinationInput struct {
	CountryCode     string
	CurrencyCode    string
	Rail            string
	Institution     string
	InstitutionName string
	Identifier      string
}

type ResolvedDestination struct {
	CountryCode     string
	CurrencyCode    string
	Rail            string
	InstitutionCode string
	InstitutionName string
	Identifier      string
	AccountName     string
}

type ProviderRecipient struct {
	ProviderReference string
	CountryCode       string
	CurrencyCode      string
	Rail              string
	InstitutionCode   string
	InstitutionName   string
	Identifier        string
	AccountName       string
}

type DestinationProvider interface {
	ListDestinations(context.Context, DestinationQuery) ([]DestinationOption, error)
	ResolveDestination(context.Context, ResolveDestinationInput) (ResolvedDestination, error)
	CreateProviderRecipient(context.Context, ResolvedDestination) (ProviderRecipient, error)
}

type PayoutSnapshot struct {
	PayoutID         uuid.UUID
	Reference        string
	Provider         string
	Rail             string
	CountryCode      string
	CurrencyCode     string
	CurrencyExponent uint8
	AmountMinor      money.Minor
	Narration        string
}

type PayoutResult struct {
	ProviderReference string
	ProviderStatus    string
	Status            PayoutStatus
}

type PayoutReconciliation struct {
	ProviderStatus string
	Status         PayoutStatus
	AmountMinor    money.Minor
	CurrencyCode   string
	CompletedAt    *time.Time
}

type PayoutRecord struct {
	ID                uuid.UUID
	Reference         string
	Provider          string
	ProviderReference string
	Status            PayoutStatus
	CurrencyCode      string
	CurrencyExponent  uint8
	AmountMinor       money.Minor
	ExpectedRecipient ProviderRecipient
}

type PayoutProvider interface {
	InitiatePayout(context.Context, PayoutSnapshot, ProviderRecipient) (PayoutResult, error)
	ReconcilePayout(context.Context, PayoutRecord) (PayoutReconciliation, error)
}

type PayoutLiquidityProvider interface {
	AvailablePayoutBalance(context.Context, string) (money.Minor, error)
}
