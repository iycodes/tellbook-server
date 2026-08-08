package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/money"
	"booking/go-server/internal/payments/capabilities"

	"github.com/google/uuid"
)

type CheckoutService struct {
	ledger       *LedgerService
	repository   *LedgerRepository
	capabilities *capabilities.Registry
	environment  capabilities.Environment
	providers    map[string]CollectionProvider
}

type CheckoutServiceConfig struct {
	Ledger       *LedgerService
	Repository   *LedgerRepository
	Capabilities *capabilities.Registry
	Environment  capabilities.Environment
	Providers    map[string]CollectionProvider
}

type BookingCheckoutInput struct {
	BookingID         uuid.UUID
	ClientID          uuid.UUID
	CustomerID        uuid.UUID
	BookingToken      string
	CountryCode       string
	CurrencyCode      string
	CustomerName      string
	CustomerEmail     string
	CustomerPhone     string
	ServiceTitle      string
	ReturnURLTemplate string
	IdempotencyKey    string
	Method            string
}

type CheckoutAttempt struct {
	Payment      FinancialPayment
	Session      CheckoutSession
	Resumed      bool
	Initializing bool
}

type CheckoutState struct {
	ObligationSatisfied bool
	AmountMinor         money.Minor
	CurrencyCode        string
	AvailableMethods    []string
	ActivePayment       *FinancialPayment
}

type PaymentMethodConflictError struct {
	Payment FinancialPayment
}

func (e *PaymentMethodConflictError) Error() string {
	return "another payment method is already active for this booking obligation"
}

type CheckoutInitializationError struct {
	Payment   FinancialPayment
	Ambiguous bool
	Cause     error
}

func (e *CheckoutInitializationError) Error() string {
	return "initialize provider checkout: " + e.Cause.Error()
}

func (e *CheckoutInitializationError) Unwrap() error { return e.Cause }

func NewCheckoutService(config CheckoutServiceConfig) (*CheckoutService, error) {
	if config.Ledger == nil || config.Repository == nil || config.Capabilities == nil {
		return nil, errors.New("checkout service dependencies are required")
	}
	if config.Environment != capabilities.EnvironmentTest && config.Environment != capabilities.EnvironmentLive {
		return nil, errors.New("checkout service environment is invalid")
	}
	providers := make(map[string]CollectionProvider, len(config.Providers))
	for name, provider := range config.Providers {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || provider == nil {
			return nil, errors.New("checkout service provider is invalid")
		}
		providers[name] = provider
	}
	return &CheckoutService{
		ledger: config.Ledger, repository: config.Repository, capabilities: config.Capabilities,
		environment: config.Environment, providers: providers,
	}, nil
}

func (s *CheckoutService) Initialize(ctx context.Context, input BookingCheckoutInput) (CheckoutAttempt, error) {
	input.Method = strings.TrimSpace(input.Method)
	if input.Method != PaymentMethodCard && input.Method != PaymentMethodBankTransfer {
		return CheckoutAttempt{}, errors.New("checkout method must be card or bank_transfer")
	}
	return s.initialize(ctx, input)
}

func (s *CheckoutService) GetCheckoutState(
	ctx context.Context,
	bookingID uuid.UUID,
	countryCode string,
	currencyCode string,
) (CheckoutState, error) {
	obligation, err := s.repository.GetOutstandingBookingPaymentObligation(ctx, bookingID)
	if errors.Is(err, ErrPaymentObligationSatisfied) {
		return CheckoutState{ObligationSatisfied: true, CurrencyCode: strings.ToUpper(currencyCode)}, nil
	}
	if err != nil {
		return CheckoutState{}, err
	}
	state := CheckoutState{
		AmountMinor: obligation.AmountMinor, CurrencyCode: obligation.CurrencyCode,
		AvailableMethods: s.capabilities.AvailableRails(
			capabilities.OperationCollection, countryCode, currencyCode, s.environment,
		),
	}
	active, err := s.repository.GetActivePaymentForObligation(ctx, bookingID, obligation.Purpose)
	if err == nil {
		state.ActivePayment = &active
		state.AvailableMethods = []string{active.Method}
		return state, nil
	}
	if !errors.Is(err, ErrLedgerRecordNotFound) {
		return CheckoutState{}, err
	}
	methods := make([]string, 0, 2)
	for _, method := range state.AvailableMethods {
		if method == PaymentMethodCard || method == PaymentMethodBankTransfer {
			methods = append(methods, method)
		}
	}
	state.AvailableMethods = methods
	return state, nil
}

func (s *CheckoutService) GetCheckoutRecord(ctx context.Context, paymentID uuid.UUID) (StoredCheckoutRecord, error) {
	return s.repository.GetCheckoutRecord(ctx, paymentID)
}

func (s *CheckoutService) initialize(ctx context.Context, input BookingCheckoutInput) (CheckoutAttempt, error) {
	obligation, err := s.repository.GetOutstandingBookingPaymentObligation(ctx, input.BookingID)
	if err != nil {
		return CheckoutAttempt{}, err
	}
	active, activeErr := s.repository.GetActivePaymentForObligation(ctx, input.BookingID, obligation.Purpose)
	if activeErr == nil {
		if active.Method != input.Method {
			return CheckoutAttempt{Payment: active, Resumed: true}, &PaymentMethodConflictError{Payment: active}
		}
		return s.resumeActive(ctx, active)
	}
	if !errors.Is(activeErr, ErrLedgerRecordNotFound) {
		return CheckoutAttempt{}, activeErr
	}
	capability, err := s.capabilities.Lookup(capabilities.Query{
		Operation: capabilities.OperationCollection, CountryCode: input.CountryCode,
		CurrencyCode: input.CurrencyCode, Rail: input.Method, Environment: s.environment,
	})
	if err != nil {
		return CheckoutAttempt{}, err
	}
	provider := s.providers[string(capability.Provider)]
	if provider == nil {
		return CheckoutAttempt{}, capabilities.ErrCapabilityNotReady
	}
	identity, err := newPaymentAttemptIdentity()
	if err != nil {
		return CheckoutAttempt{}, err
	}
	returnURL, err := checkoutReturnURL(input.ReturnURLTemplate, identity.PublicToken)
	if err != nil {
		return CheckoutAttempt{}, err
	}
	requestedAt := time.Now().UTC()
	snapshot := PaymentSnapshot{
		PaymentID: identity.ID, Reference: identity.Reference, Purpose: obligation.Purpose,
		Provider: string(capability.Provider), Method: input.Method, CountryCode: input.CountryCode,
		CurrencyCode: input.CurrencyCode, CurrencyExponent: capability.CurrencyExponent,
		AmountMinor: obligation.AmountMinor, CustomerName: input.CustomerName,
		CustomerEmail: input.CustomerEmail, CustomerPhone: input.CustomerPhone,
		Description: "Payment for " + input.ServiceTitle, ReturnURL: returnURL, RequestedAt: requestedAt,
		Metadata: map[string]string{"booking_token": input.BookingToken, "payment_token": identity.PublicToken},
	}
	recordJSON, err := json.Marshal(CheckoutRecord{Version: 1, Snapshot: snapshot})
	if err != nil {
		return CheckoutAttempt{}, err
	}
	leaseOwner := "checkout-init-" + uuid.NewString()
	leaseExpiresAt := requestedAt.Add(30 * time.Second)
	payment, created, err := s.ledger.CreatePaymentAttempt(ctx, CreatePaymentAttemptInput{
		BookingID: input.BookingID, ClientID: input.ClientID, CustomerID: input.CustomerID,
		Purpose: obligation.Purpose, Provider: string(capability.Provider), Method: input.Method,
		CountryCode: input.CountryCode, CurrencyCode: input.CurrencyCode, AmountMinor: obligation.AmountMinor,
		PriceSnapshot: map[string]string{
			"booking_token": input.BookingToken, "total_amount_minor": fmt.Sprint(obligation.TotalAmountMinor),
			"deposit_amount_minor": fmt.Sprint(obligation.DepositAmountMinor), "payable_amount_minor": fmt.Sprint(obligation.AmountMinor),
			"payment_purpose": string(obligation.Purpose), "currency_code": input.CurrencyCode,
			"currency_exponent": fmt.Sprint(capability.CurrencyExponent),
		},
		IdempotencyKey: input.IdempotencyKey,
		identity:       &identity, checkoutDetails: recordJSON,
		checkoutInitializationState:          CheckoutInitializationPrepared,
		checkoutInitializationLeaseOwner:     leaseOwner,
		checkoutInitializationLeaseExpiresAt: &leaseExpiresAt,
		nextProviderCheckAt:                  &requestedAt,
	})
	if err != nil {
		var activePaymentError *ActivePaymentError
		if errors.As(err, &activePaymentError) {
			if activePaymentError.Payment.Method != input.Method {
				return CheckoutAttempt{Payment: activePaymentError.Payment, Resumed: true},
					&PaymentMethodConflictError{Payment: activePaymentError.Payment}
			}
			return s.resumeActive(ctx, activePaymentError.Payment)
		}
		return CheckoutAttempt{}, err
	}
	if !created {
		return s.resumeActive(ctx, payment)
	}

	session, providerErr := provider.InitializeCheckout(ctx, snapshot)
	if providerErr != nil {
		ambiguous := !isDefinitiveProviderFailure(providerErr)
		status := PaymentStatusFailed
		initializationState := CheckoutInitializationReady
		update := PaymentTransitionUpdate{
			FailureMessage: providerFailureMessage(providerErr), CheckoutInitializationState: initializationState,
			ExpectedInitializationLeaseOwner: leaseOwner,
		}
		if ambiguous {
			status = PaymentStatusPending
			initializationState = CheckoutInitializationUnknown
			nextCheck := requestedAt.Add(providerInitializationRecoveryDelay(payment.Provider, payment.Method))
			update.CheckoutInitializationState = initializationState
			update.NextProviderCheckAt = &nextCheck
			update.ReconciliationReason = "provider initialization outcome is unknown"
			update.ProviderStatus = "initialization_unknown"
			update.ReconciledAt = timePointer(time.Now().UTC())
		}
		transitioned, transitionErr := s.repository.TransitionPayment(ctx, payment.ID, payment.Version, status, update)
		if transitionErr != nil {
			return CheckoutAttempt{}, transitionErr
		}
		return CheckoutAttempt{Payment: transitioned, Initializing: ambiguous}, &CheckoutInitializationError{
			Payment: transitioned, Ambiguous: ambiguous, Cause: providerErr,
		}
	}
	recordJSON, err = json.Marshal(CheckoutRecord{Version: 1, Snapshot: snapshot, Session: &session})
	if err != nil {
		return CheckoutAttempt{}, err
	}
	transitioned, err := s.repository.TransitionPayment(ctx, payment.ID, payment.Version, PaymentStatusPending, PaymentTransitionUpdate{
		ProviderReference: session.ProviderReference, ProviderStatus: "initialized",
		CheckoutURL: session.CheckoutURL, ExpiresAt: session.ExpiresAt, CheckoutDetails: recordJSON,
		CheckoutInitializationState:      CheckoutInitializationReady,
		ExpectedInitializationLeaseOwner: leaseOwner,
	})
	if err != nil {
		return CheckoutAttempt{}, err
	}
	return CheckoutAttempt{Payment: transitioned, Session: session}, nil
}

func (s *CheckoutService) resumeActive(ctx context.Context, payment FinancialPayment) (CheckoutAttempt, error) {
	stored, err := s.repository.GetCheckoutRecord(ctx, payment.ID)
	if err != nil {
		return CheckoutAttempt{}, err
	}
	if stored.State != CheckoutInitializationReady || stored.Record.Session == nil {
		return CheckoutAttempt{Payment: payment, Resumed: true, Initializing: true}, nil
	}
	return CheckoutAttempt{Payment: payment, Session: *stored.Record.Session, Resumed: true}, nil
}

func providerInitializationRecoveryDelay(provider, method string) time.Duration {
	if provider == "paystack" && method == PaymentMethodBankTransfer {
		return 10 * time.Second
	}
	return 5 * time.Second
}

func (s *CheckoutService) Reconcile(ctx context.Context, payment FinancialPayment) (FinancialPayment, error) {
	provider := s.providers[payment.Provider]
	if provider == nil {
		return FinancialPayment{}, capabilities.ErrCapabilityNotReady
	}
	capability, err := s.capabilities.LookupProviderSupport(capabilities.Query{
		Operation: capabilities.OperationCollection, CountryCode: payment.CountryCode,
		CurrencyCode: payment.CurrencyCode, Rail: payment.Method, Environment: s.environment,
	}, capabilities.Provider(payment.Provider))
	if err != nil {
		return FinancialPayment{}, err
	}
	reconciled, err := provider.ReconcilePayment(ctx, PaymentRecord{
		ID: payment.ID, Reference: payment.Reference, Provider: payment.Provider,
		ProviderReference: payment.ProviderReference, Method: payment.Method, Status: payment.Status,
		CurrencyCode: payment.CurrencyCode, CurrencyExponent: capability.CurrencyExponent, AmountMinor: payment.AmountMinor,
	})
	if err != nil {
		var mismatch *PaymentEvidenceMismatchError
		if !errors.As(err, &mismatch) {
			return FinancialPayment{}, err
		}
		evidenceReference := strings.TrimSpace(payment.ProviderReference)
		if evidenceReference == "" {
			evidenceReference = payment.Reference
		}
		observedCurrency := strings.ToUpper(strings.TrimSpace(mismatch.Reconciliation.CurrencyCode))
		if len(observedCurrency) != 3 {
			observedCurrency = payment.CurrencyCode
		}
		if recordErr := s.repository.RecordPaymentException(ctx, PaymentExceptionInput{
			PaymentID: payment.ID, BookingID: payment.BookingID, Provider: payment.Provider,
			Kind: "amount_mismatch", ProviderReference: evidenceReference,
			EvidenceSource: "provider_reconciliation", EvidenceReference: payment.Reference,
			ObservedAmountMinor: mismatch.Reconciliation.AmountMinor,
			CurrencyCode:        observedCurrency,
		}); recordErr != nil {
			return FinancialPayment{}, recordErr
		}
		if isTerminalPaymentStatus(payment.Status) {
			return payment, nil
		}
		now := time.Now().UTC()
		return s.repository.TransitionPayment(ctx, payment.ID, payment.Version, PaymentStatusFailed, PaymentTransitionUpdate{
			ProviderStatus:       mismatch.Reconciliation.ProviderStatus,
			ReconciliationReason: "provider payment evidence quarantined for amount or currency mismatch",
			FailureCode:          "payment_evidence_mismatch",
			FailureMessage:       "The provider reported payment details that do not match this booking.",
			ReconciledAt:         &now,
		})
	}
	if isTerminalPaymentStatus(payment.Status) {
		if reconciled.Status == PaymentStatusPaid && payment.Status != PaymentStatusPaid &&
			payment.Status != PaymentStatusPartiallyRefunded && payment.Status != PaymentStatusRefunded {
			evidenceReference := strings.TrimSpace(payment.ProviderReference)
			if evidenceReference == "" {
				evidenceReference = payment.Reference
			}
			if err := s.repository.RecordPaymentException(ctx, PaymentExceptionInput{
				PaymentID: payment.ID, BookingID: payment.BookingID, Provider: payment.Provider,
				Kind: "late_success", ProviderReference: evidenceReference,
				EvidenceSource: "provider_reconciliation", EvidenceReference: payment.Reference,
				ObservedAmountMinor: reconciled.AmountMinor, CurrencyCode: reconciled.CurrencyCode,
			}); err != nil {
				return FinancialPayment{}, err
			}
		}
		return payment, nil
	}
	now := time.Now().UTC()
	return s.repository.TransitionPayment(ctx, payment.ID, payment.Version, reconciled.Status, PaymentTransitionUpdate{
		ProviderStatus: reconciled.ProviderStatus, ProviderChannel: reconciled.ProviderChannel,
		ReconciliationReason: reconciled.ReconciliationReason,
		FailureCode:          reconciled.FailureCode, FailureMessage: reconciled.FailureMessage,
		PaidAt: reconciled.PaidAt, ReconciledAt: &now,
	})
}

func (s *CheckoutService) ReconcileByPublicToken(ctx context.Context, token string) (FinancialPayment, error) {
	payment, err := s.repository.GetPaymentByPublicToken(ctx, token)
	if err != nil {
		return FinancialPayment{}, err
	}
	stored, err := s.repository.GetCheckoutRecord(ctx, payment.ID)
	if err != nil {
		return FinancialPayment{}, err
	}
	if stored.State != CheckoutInitializationReady {
		return s.recoverInitialization(ctx, payment, stored)
	}
	if payment.Provider == "paystack" && payment.Method == PaymentMethodBankTransfer {
		notBefore := stored.Record.Snapshot.RequestedAt.Add(10 * time.Second)
		if time.Now().UTC().Before(notBefore) {
			return payment, nil
		}
	}
	refreshed, err := s.repository.WithPaymentReconciliationLock(ctx, payment.ID, func() (FinancialPayment, error) {
		current, loadErr := s.repository.GetPaymentByPublicToken(ctx, token)
		if loadErr != nil {
			return FinancialPayment{}, loadErr
		}
		if isTerminalPaymentStatus(current.Status) {
			return current, nil
		}
		result, reconcileErr := s.Reconcile(ctx, current)
		if errors.Is(reconcileErr, ErrConcurrentUpdate) {
			return s.repository.GetPaymentByPublicToken(ctx, token)
		}
		if reconcileErr != nil || isTerminalPaymentStatus(result.Status) || result.Method != PaymentMethodBankTransfer ||
			result.ExpiresAt == nil || time.Now().UTC().Before(*result.ExpiresAt) {
			return result, reconcileErr
		}
		now := time.Now().UTC()
		expired, expireErr := s.repository.TransitionPayment(ctx, result.ID, result.Version, PaymentStatusExpired, PaymentTransitionUpdate{
			ProviderStatus: result.ProviderStatus, ProviderChannel: result.ProviderChannel,
			ReconciliationReason: "bank-transfer account window expired after final provider verification",
			FailureCode:          "account_expired", FailureMessage: "The bank-transfer account window expired before payment was confirmed.",
			ReconciledAt: &now,
		})
		if errors.Is(expireErr, ErrConcurrentUpdate) {
			return s.repository.GetPaymentByPublicToken(ctx, token)
		}
		return expired, expireErr
	})
	if err != nil {
		return FinancialPayment{}, err
	}
	return refreshed, nil
}

func (s *CheckoutService) recoverInitialization(
	ctx context.Context,
	payment FinancialPayment,
	stored StoredCheckoutRecord,
) (FinancialPayment, error) {
	if stored.NextCheckAt != nil && time.Now().UTC().Before(*stored.NextCheckAt) {
		return payment, nil
	}
	leaseOwner := "checkout-recovery-" + uuid.NewString()
	claimed, ok, err := s.repository.ClaimCheckoutInitialization(
		ctx, payment.ID, leaseOwner, time.Now().UTC().Add(30*time.Second),
	)
	if err != nil || !ok {
		return payment, err
	}
	provider := s.providers[claimed.Provider]
	if provider == nil {
		nextCheck := time.Now().UTC().Add(providerInitializationRecoveryDelay(claimed.Provider, claimed.Method))
		updated, updateErr := s.repository.TransitionPayment(ctx, claimed.ID, claimed.Version, claimed.Status, PaymentTransitionUpdate{
			CheckoutInitializationState: CheckoutInitializationUnknown, ExpectedInitializationLeaseOwner: leaseOwner,
			NextProviderCheckAt: &nextCheck, ReconciliationReason: "provider checkout recovery is unavailable",
			ReconciledAt: timePointer(time.Now().UTC()),
		})
		if updateErr != nil {
			return FinancialPayment{}, updateErr
		}
		return updated, capabilities.ErrCapabilityNotReady
	}
	session, recoverErr := provider.RecoverCheckout(ctx, stored.Record.Snapshot)
	if recoverErr != nil {
		nextCheck := time.Now().UTC().Add(providerInitializationRecoveryDelay(claimed.Provider, claimed.Method))
		updated, updateErr := s.repository.TransitionPayment(ctx, claimed.ID, claimed.Version, claimed.Status, PaymentTransitionUpdate{
			CheckoutInitializationState:      CheckoutInitializationUnknown,
			ExpectedInitializationLeaseOwner: leaseOwner, NextProviderCheckAt: &nextCheck,
			ReconciliationReason: "provider checkout recovery pending", ReconciledAt: timePointer(time.Now().UTC()),
		})
		if updateErr != nil {
			return FinancialPayment{}, updateErr
		}
		if errors.Is(recoverErr, ErrCheckoutRecoveryNotReady) {
			return updated, nil
		}
		return updated, recoverErr
	}
	stored.Record.Session = &session
	recordJSON, err := json.Marshal(stored.Record)
	if err != nil {
		return FinancialPayment{}, err
	}
	return s.repository.TransitionPayment(ctx, claimed.ID, claimed.Version, PaymentStatusPending, PaymentTransitionUpdate{
		ProviderReference: session.ProviderReference, ProviderStatus: "initialized",
		CheckoutURL: session.CheckoutURL, ExpiresAt: session.ExpiresAt, CheckoutDetails: recordJSON,
		CheckoutInitializationState:      CheckoutInitializationReady,
		ExpectedInitializationLeaseOwner: leaseOwner,
	})
}

func (s *CheckoutService) ResumeByPublicToken(ctx context.Context, token string) (CheckoutAttempt, error) {
	refreshed, err := s.ReconcileByPublicToken(ctx, token)
	if err != nil {
		return CheckoutAttempt{}, err
	}
	return s.resumeActive(ctx, refreshed)
}

func (s *CheckoutService) GetPaymentByPublicToken(ctx context.Context, token string) (FinancialPayment, error) {
	return s.repository.GetPaymentByPublicToken(ctx, token)
}

func isTerminalPaymentStatus(status PaymentStatus) bool {
	switch status {
	case PaymentStatusCreated, PaymentStatusPending, PaymentStatusRequiresAction:
		return false
	default:
		return true
	}
}

type httpStatusError interface{ HTTPStatusCode() int }

func isDefinitiveProviderFailure(err error) bool {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "reference already exist") || strings.Contains(message, "already generated") {
		return false
	}
	var statusError httpStatusError
	return errors.As(err, &statusError) && statusError.HTTPStatusCode() >= 400 && statusError.HTTPStatusCode() < 500
}

func providerFailureMessage(err error) string {
	var statusError httpStatusError
	if errors.As(err, &statusError) {
		return fmt.Sprintf("provider request failed with HTTP %d", statusError.HTTPStatusCode())
	}
	return "provider request outcome is unknown"
}

func timePointer(value time.Time) *time.Time { return &value }

func checkoutReturnURL(template, paymentToken string) (string, error) {
	if strings.Count(template, "{payment_token}") != 1 || strings.TrimSpace(paymentToken) == "" {
		return "", errors.New("payment return URL template is invalid")
	}
	return strings.Replace(template, "{payment_token}", paymentToken, 1), nil
}
