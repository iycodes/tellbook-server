package payments

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"booking/go-server/internal/money"
	"booking/go-server/internal/payments/capabilities"
	"booking/go-server/internal/secure"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLedgerPaymentToPayoutLifecycle(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	defer pool.Close()

	clientID := uuid.New()
	customerID := uuid.New()
	bookingID := uuid.New()
	var webhookEventID uuid.UUID
	email := fmt.Sprintf("ledger-%s@example.test", clientID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO clients (id, full_name, email, password_hash, created_at, updated_at)
		VALUES ($1, 'Ledger Test', $2, 'not-used', NOW(), NOW())
	`, clientID, email); err != nil {
		t.Fatalf("insert client: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO customers (id, client_id, full_name, email, created_at, updated_at)
		VALUES ($1, $2, 'Payment Customer', $3, NOW(), NOW())
	`, customerID, clientID, email); err != nil {
		t.Fatalf("insert customer: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bookings (
			id, client_id, customer_id, title, stylist_name, source, status,
			payment_status, agreement_status, start_at, end_at, timezone,
			base_service_amount_minor, discounted_service_amount_minor,
			total_amount_minor, deposit_amount_minor,
			currency_code, country_code, duration_minutes, notes, location_label,
			occupied_start_at, occupied_end_at,
			created_at, updated_at
		)
		VALUES (
			$1,$2,$3,'Ledger Booking','Ledger Test','test','booked',
			'deposit_pending','not_required',NOW() + INTERVAL '1 day',
			NOW() + INTERVAL '1 day 1 hour','Africa/Lagos',10000,10000,10000,3000,
			'NGN','NG',60,'','Test Studio',NOW() + INTERVAL '1 day',
			NOW() + INTERVAL '1 day 1 hour',NOW(),NOW()
		)
	`, bookingID, clientID, customerID); err != nil {
		t.Fatalf("insert booking: %v", err)
	}
	t.Cleanup(func() {
		if webhookEventID != uuid.Nil {
			_, _ = pool.Exec(ctx, `DELETE FROM financial_jobs WHERE aggregate_id = $1`, webhookEventID)
			_, _ = pool.Exec(ctx, `DELETE FROM provider_webhook_events WHERE id = $1`, webhookEventID)
		}
		_, _ = pool.Exec(ctx, `
			DELETE FROM financial_jobs
			WHERE aggregate_id IN (
				SELECT id FROM payments WHERE client_id = $1
				UNION SELECT id FROM payment_allocations WHERE client_id = $1
				UNION SELECT id FROM payouts WHERE client_id = $1
			)
		`, clientID)
		_, _ = pool.Exec(ctx, `DELETE FROM business_balance_entries WHERE client_id = $1`, clientID)
		_, _ = pool.Exec(ctx, `DELETE FROM payouts WHERE client_id = $1`, clientID)
		_, _ = pool.Exec(ctx, `DELETE FROM payment_adjustments WHERE payment_id IN (SELECT id FROM payments WHERE client_id = $1)`, clientID)
		_, _ = pool.Exec(ctx, `DELETE FROM provider_settlement_evidence WHERE payment_id IN (SELECT id FROM payments WHERE client_id = $1)`, clientID)
		_, _ = pool.Exec(ctx, `DELETE FROM payment_allocations WHERE client_id = $1`, clientID)
		_, _ = pool.Exec(ctx, `DELETE FROM payout_destinations WHERE client_id = $1`, clientID)
		_, _ = pool.Exec(ctx, `DELETE FROM payments WHERE client_id = $1`, clientID)
		_, _ = pool.Exec(ctx, `DELETE FROM bookings WHERE client_id = $1`, clientID)
		_, _ = pool.Exec(ctx, `DELETE FROM customers WHERE client_id = $1`, clientID)
		_, _ = pool.Exec(ctx, `DELETE FROM clients WHERE id = $1`, clientID)
	})

	repository := NewLedgerRepository(pool)
	service, err := NewLedgerService(repository, nil, nil)
	if err != nil {
		t.Fatalf("NewLedgerService() error = %v", err)
	}
	input := CreatePaymentAttemptInput{
		BookingID: bookingID, ClientID: clientID, CustomerID: customerID,
		Purpose: PaymentPurposeDeposit, Provider: "paystack", Method: "card",
		CountryCode: "NG", CurrencyCode: "NGN", AmountMinor: money.Minor(3000),
		PriceSnapshot:  map[string]string{"total_amount_minor": "10000", "deposit_amount_minor": "3000"},
		IdempotencyKey: "integration_payment_123456",
	}
	payment, created, err := service.CreatePaymentAttempt(ctx, input)
	if err != nil || !created {
		t.Fatalf("CreatePaymentAttempt() created=%v error=%v", created, err)
	}
	repeated, created, err := service.CreatePaymentAttempt(ctx, input)
	if err != nil || created || repeated.ID != payment.ID {
		t.Fatalf("repeated payment = %#v, created=%v error=%v", repeated, created, err)
	}
	changed := input
	changed.Method = "bank_transfer"
	if _, _, err := service.CreatePaymentAttempt(ctx, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed idempotent input error = %v", err)
	}
	secondIntent := input
	secondIntent.IdempotencyKey = "integration_payment_654321"
	if _, _, err := service.CreatePaymentAttempt(ctx, secondIntent); err == nil {
		t.Fatal("second active payment unexpectedly succeeded")
	} else {
		var activeErr *ActivePaymentError
		if !errors.As(err, &activeErr) || activeErr.Payment.ID != payment.ID {
			t.Fatalf("second active payment error = %v", err)
		}
	}

	payment, err = repository.TransitionPayment(ctx, payment.ID, payment.Version, PaymentStatusPending, PaymentTransitionUpdate{})
	if err != nil {
		t.Fatalf("TransitionPayment(pending) error = %v", err)
	}
	payment, err = repository.TransitionPayment(ctx, payment.ID, payment.Version, PaymentStatusPaid, PaymentTransitionUpdate{
		ProviderReference: "provider-payment-" + payment.ID.String(), ProviderStatus: "success",
	})
	if err != nil {
		t.Fatalf("TransitionPayment(paid) error = %v", err)
	}
	var bookingPaymentStatus string
	if err := pool.QueryRow(ctx, `SELECT payment_status FROM bookings WHERE id = $1`, bookingID).Scan(&bookingPaymentStatus); err != nil {
		t.Fatalf("load booking payment status: %v", err)
	}
	if bookingPaymentStatus != string(BookingPaymentDepositPaidBalance) {
		t.Fatalf("booking payment status = %q", bookingPaymentStatus)
	}
	balanceInput := input
	balanceInput.Purpose = PaymentPurposeBalance
	balanceInput.AmountMinor = money.Minor(7000)
	balanceInput.IdempotencyKey = "integration_balance_123456"
	balancePayment, created, err := service.CreatePaymentAttempt(ctx, balanceInput)
	if err != nil || !created || balancePayment.Purpose != PaymentPurposeBalance || balancePayment.AmountMinor != 7000 {
		t.Fatalf("balance payment = %#v, created=%v error=%v", balancePayment, created, err)
	}
	claimedPayments, err := repository.ClaimStalePayments(ctx, "integration-payment-reconciler", time.Now().UTC().Add(time.Second), 10, time.Minute)
	if err != nil || !containsPayment(claimedPayments, balancePayment.ID) {
		t.Fatalf("claimed payments = %#v, error=%v", claimedPayments, err)
	}
	claimedAgain, err := repository.ClaimStalePayments(ctx, "second-payment-reconciler", time.Now().UTC().Add(time.Second), 10, time.Minute)
	if err != nil || containsPayment(claimedAgain, balancePayment.ID) {
		t.Fatalf("second payment claim = %#v, error=%v", claimedAgain, err)
	}
	balancePayment, err = repository.TransitionPayment(ctx, balancePayment.ID, balancePayment.Version, PaymentStatusPending, PaymentTransitionUpdate{})
	if err != nil {
		t.Fatalf("TransitionPayment(balance pending) error = %v", err)
	}
	balancePayment, err = repository.TransitionPayment(ctx, balancePayment.ID, balancePayment.Version, PaymentStatusPaid, PaymentTransitionUpdate{
		ProviderReference: "provider-payment-" + balancePayment.ID.String(), ProviderStatus: "success",
	})
	if err != nil {
		t.Fatalf("TransitionPayment(balance paid) error = %v", err)
	}
	if _, err := repository.GetOutstandingBookingPaymentObligation(ctx, bookingID); !errors.Is(err, ErrPaymentObligationSatisfied) {
		t.Fatalf("settled booking obligation error = %v", err)
	}
	settledInput := balanceInput
	settledInput.IdempotencyKey = "integration_settled_123456"
	if _, _, err := service.CreatePaymentAttempt(ctx, settledInput); !errors.Is(err, ErrPaymentObligationSatisfied) {
		t.Fatalf("payment against settled booking error = %v", err)
	}

	allocation, created, err := repository.CreatePaymentAllocation(ctx, CreatePaymentAllocationInput{
		PaymentID: payment.ID, ClientID: clientID, CurrencyCode: "NGN",
		Amounts:       AllocationAmounts{GrossMinor: 3000, ProviderFeeMinor: 50, PlatformFeeMinor: 150, BusinessNetAmountMinor: 2800},
		PolicyVersion: "test-v1", CalculationSnapshot: map[string]string{"policy": "test-v1"},
		SettlementStatus: "pending",
	})
	if err != nil || !created {
		t.Fatalf("CreatePaymentAllocation() created=%v error=%v", created, err)
	}
	availableAt := time.Now().UTC().Add(-time.Minute)
	applied, err := repository.RecordSettlementEvidence(ctx, SettlementEvidence{
		Provider: "paystack", SettlementReference: "settlement-mismatch", PaymentReference: payment.Reference,
		ProviderStatus: "success", SettlementStatus: "available",
		AmountMinor: payment.AmountMinor + 1, CurrencyCode: payment.CurrencyCode,
		AvailableAt: availableAt,
	})
	if err != nil || applied {
		t.Fatalf("mismatched RecordSettlementEvidence() applied=%v error=%v", applied, err)
	}
	allocation, err = repository.GetPaymentAllocation(ctx, clientID, allocation.ID)
	if err != nil || allocation.Status != "pending" || allocation.SettlementStatus != "pending" {
		t.Fatalf("mismatched evidence changed allocation=%#v error=%v", allocation, err)
	}
	applied, err = repository.RecordSettlementEvidence(ctx, SettlementEvidence{
		Provider: "paystack", SettlementReference: "settlement-1", PaymentReference: payment.Reference,
		ProviderStatus: "success", SettlementStatus: "available",
		AmountMinor: payment.AmountMinor, CurrencyCode: payment.CurrencyCode,
		AvailableAt: availableAt,
	})
	if err != nil || !applied {
		t.Fatalf("RecordSettlementEvidence() applied=%v error=%v", applied, err)
	}
	allocation, err = repository.GetPaymentAllocation(ctx, clientID, allocation.ID)
	if err != nil || allocation.Status != "eligible" || allocation.SettlementStatus != "available" || allocation.SettlementReference != "settlement-1" {
		t.Fatalf("settled allocation=%#v error=%v", allocation, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE payment_allocations SET status = 'blocked' WHERE id = $1`, allocation.ID); err != nil {
		t.Fatalf("block allocation for reevaluation: %v", err)
	}
	if err := repository.ReevaluatePaymentAllocation(ctx, allocation.ID); err != nil {
		t.Fatalf("ReevaluatePaymentAllocation() error=%v", err)
	}
	allocation, err = repository.GetPaymentAllocation(ctx, clientID, allocation.ID)
	if err != nil || allocation.Status != "eligible" || allocation.Amounts.BusinessNetAmountMinor != 2800 {
		t.Fatalf("reevaluated allocation=%#v error=%v", allocation, err)
	}

	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	keyring, err := secure.ParseKeyring(fmt.Sprintf(`{"v1":%q}`, key), "v1")
	if err != nil {
		t.Fatalf("ParseKeyring() error = %v", err)
	}
	fingerprinter, err := secure.NewFingerprinter(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)))
	if err != nil {
		t.Fatalf("NewFingerprinter() error = %v", err)
	}
	secureService, err := NewLedgerService(repository, keyring, fingerprinter)
	if err != nil {
		t.Fatalf("NewLedgerService(secure) error = %v", err)
	}
	destination, created, err := secureService.SavePayoutDestination(ctx, SavePayoutDestinationInput{
		ClientID: clientID, Provider: "payaza", CountryCode: "NG", CurrencyCode: "NGN",
		Rail: "bank_account", InstitutionCode: "999", InstitutionName: "Test Bank",
		Identifier: "0123456789", ResolvedAccountName: "LEDGER TEST",
		MakeDefault: true,
	})
	if err != nil || !created {
		t.Fatalf("SavePayoutDestination() created=%v error=%v", created, err)
	}
	revealed, err := secureService.RevealPayoutDestinationIdentifier(ctx, destination.ID)
	if err != nil || revealed != "0123456789" {
		t.Fatalf("RevealPayoutDestinationIdentifier() value=%q error=%v", revealed, err)
	}

	webhookBody := []byte(fmt.Sprintf(`{"event":"payment.success","reference":%q}`, "provider-"+bookingID.String()))
	webhook, created, err := secureService.StoreVerifiedWebhook(ctx, StoreVerifiedWebhookInput{
		Provider: "payaza", ProviderEventID: "event-" + bookingID.String(),
		EventType: "payment.success", RawBody: webhookBody,
		NormalizedEvent: map[string]any{"reference": "provider-test"},
	})
	if err != nil || !created || webhook.ProcessingStatus != "completed" {
		t.Fatalf("StoreVerifiedWebhook() created=%v error=%v", created, err)
	}
	webhookEventID = webhook.ID
	repeatedWebhook, created, err := secureService.StoreVerifiedWebhook(ctx, StoreVerifiedWebhookInput{
		Provider: "payaza", ProviderEventID: "event-" + bookingID.String(),
		EventType: "payment.success", RawBody: webhookBody,
		NormalizedEvent: map[string]any{"reference": "provider-test"},
	})
	if err != nil || created || repeatedWebhook.ID != webhook.ID {
		t.Fatalf("repeated webhook=%#v created=%v error=%v", repeatedWebhook, created, err)
	}
	bodyDuplicate, created, err := secureService.StoreVerifiedWebhook(ctx, StoreVerifiedWebhookInput{
		Provider: "payaza", ProviderEventID: "duplicate-delivery-" + bookingID.String(),
		EventType: "payment.success", RawBody: webhookBody,
		NormalizedEvent: map[string]any{"reference": "provider-test"},
	})
	if err != nil || created || bodyDuplicate.ID != webhook.ID {
		t.Fatalf("body duplicate webhook=%#v created=%v error=%v", bodyDuplicate, created, err)
	}
	loadedWebhook, err := secureService.LoadVerifiedWebhook(ctx, webhook.ID)
	if err != nil || !bytes.Equal(loadedWebhook.RawBody, webhookBody) {
		t.Fatalf("LoadVerifiedWebhook() body=%q error=%v", loadedWebhook.RawBody, err)
	}
	collectionWebhook, created, err := secureService.StoreVerifiedWebhook(ctx, StoreVerifiedWebhookInput{
		Provider: "payaza", ProviderEventID: "collection-" + bookingID.String(),
		EventType: "collection.success", RawBody: []byte(fmt.Sprintf(`{"event":"collection.success","booking":"%s"}`, bookingID)),
		NormalizedEvent: map[string]any{
			"reference":          "provider-generated-reference",
			"merchant_reference": payment.Reference,
		},
	})
	if err != nil || !created {
		t.Fatalf("StoreVerifiedWebhook(collection) created=%v error=%v", created, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE financial_jobs SET available_at = '1970-01-01T00:00:00Z' WHERE aggregate_id = $1`, collectionWebhook.ID); err != nil {
		t.Fatalf("prioritize collection webhook job: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM financial_jobs WHERE aggregate_id = $1`, collectionWebhook.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_webhook_events WHERE id = $1`, collectionWebhook.ID)
	})
	claimed, err := repository.ClaimCollectionWebhookJobs(ctx, "integration-webhook-worker", 10, time.Minute)
	if err != nil {
		t.Fatalf("ClaimCollectionWebhookJobs() error=%v", err)
	}
	if !containsClaimedAggregate(claimed, collectionWebhook.ID) {
		t.Fatalf("claimed collection webhook jobs=%#v", claimed)
	}

	registry, err := capabilities.New([]capabilities.Capability{{
		Provider: capabilities.ProviderPayaza, Operation: capabilities.OperationPayout,
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
		ProviderChannel: "nuban", CurrencyExponent: 2, Configured: true, SandboxVerified: true,
	}})
	if err != nil {
		t.Fatalf("capabilities.New() error = %v", err)
	}
	provider := &integrationPayoutProvider{availableBalance: money.Minor(100000)}
	payoutService, err := NewPayoutService(PayoutServiceConfig{
		Ledger: secureService, Repository: repository, Capabilities: registry,
		Environment: capabilities.EnvironmentTest, Providers: map[string]PayoutProvider{"payaza": provider},
	})
	if err != nil {
		t.Fatalf("NewPayoutService() error = %v", err)
	}
	payout, err := payoutService.Initiate(ctx, InitiatePayoutInput{
		ClientID: clientID, PaymentAllocationID: allocation.ID,
		PayoutDestinationID: destination.ID, IdempotencyKey: "integration_payout_1234567",
	})
	if err != nil || payout.Status != PayoutStatusPending {
		t.Fatalf("Initiate() payout=%#v error=%v", payout, err)
	}
	claimedReconciliations, err := repository.ClaimStalePayouts(ctx, "integration-payout-reconciler", time.Now().UTC().Add(time.Second), 10, time.Minute)
	if err != nil || !containsPayout(claimedReconciliations, payout.ID) {
		t.Fatalf("claimed payouts = %#v, error=%v", claimedReconciliations, err)
	}
	claimedReconciliationsAgain, err := repository.ClaimStalePayouts(ctx, "second-payout-reconciler", time.Now().UTC().Add(time.Second), 10, time.Minute)
	if err != nil || containsPayout(claimedReconciliationsAgain, payout.ID) {
		t.Fatalf("second payout claim = %#v, error=%v", claimedReconciliationsAgain, err)
	}
	if provider.recipient.Identifier != "0123456789" || provider.snapshot.AmountMinor != 2800 || provider.balanceChecks != 1 {
		t.Fatalf("provider initiation snapshot=%#v recipient=%#v", provider.snapshot, provider.recipient)
	}
	payoutWebhook, created, err := secureService.StoreVerifiedWebhook(ctx, StoreVerifiedWebhookInput{
		Provider: "payaza", ProviderEventID: "payout-" + bookingID.String(),
		EventType: "payout.pending", RawBody: []byte(fmt.Sprintf(`{"event":"payout.pending","booking":"%s"}`, bookingID)),
		NormalizedEvent: map[string]any{
			"reference":             "provider-generated-reference",
			"transaction_reference": payout.Reference,
		},
	})
	if err != nil || !created {
		t.Fatalf("StoreVerifiedWebhook(payout) created=%v error=%v", created, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE financial_jobs SET available_at = '1970-01-01T00:00:00Z' WHERE aggregate_id = $1`, payoutWebhook.ID); err != nil {
		t.Fatalf("prioritize payout webhook job: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM financial_jobs WHERE aggregate_id = $1`, payoutWebhook.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_webhook_events WHERE id = $1`, payoutWebhook.ID)
	})
	claimedPayouts, err := repository.ClaimPayoutWebhookJobs(ctx, "integration-payout-webhook-worker", 10, time.Minute)
	if err != nil {
		t.Fatalf("ClaimPayoutWebhookJobs() error=%v", err)
	}
	if !containsClaimedAggregate(claimedPayouts, payoutWebhook.ID) {
		t.Fatalf("claimed payout webhook jobs=%#v", claimedPayouts)
	}
	inFlightAdjustmentInput := RecordPaymentAdjustmentInput{
		PaymentID: payment.ID, Provider: "paystack", ProviderReference: "in-flight-refund-" + payment.ID.String(),
		Kind: "partial_refund", Status: "successful", CurrencyCode: "NGN",
		AmountMinor: 500, AllocationImpact: 500, Reason: "refund while payout is in flight",
	}
	inFlightAdjustment, created, err := repository.RecordPaymentAdjustment(ctx, inFlightAdjustmentInput)
	if err != nil || !created {
		t.Fatalf("RecordPaymentAdjustment(in flight) adjustment=%#v created=%v error=%v", inFlightAdjustment, created, err)
	}
	if _, err := payoutService.Reconcile(ctx, payout); err != nil {
		t.Fatalf("Reconcile(successful) error = %v", err)
	}
	var inFlightDebtStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM business_balance_entries WHERE payment_adjustment_id = $1`, inFlightAdjustment.ID).Scan(&inFlightDebtStatus); err != nil || inFlightDebtStatus != "open" {
		t.Fatalf("in-flight adjustment debt status=%q error=%v", inFlightDebtStatus, err)
	}

	adjustmentInput := RecordPaymentAdjustmentInput{
		PaymentID: payment.ID, Provider: "paystack", ProviderReference: "refund-" + payment.ID.String(),
		Kind: "partial_refund", Status: "pending", CurrencyCode: "NGN",
		AmountMinor: 1000, AllocationImpact: 1000, Reason: "integration test refund",
	}
	adjustment, created, err := repository.RecordPaymentAdjustment(ctx, adjustmentInput)
	if err != nil || !created || adjustment.Status != "pending" {
		t.Fatalf("RecordPaymentAdjustment(pending) adjustment=%#v created=%v error=%v", adjustment, created, err)
	}
	adjustmentInput.Status = "successful"
	adjustment, created, err = repository.RecordPaymentAdjustment(ctx, adjustmentInput)
	if err != nil || created || adjustment.Status != "successful" {
		t.Fatalf("RecordPaymentAdjustment(successful) adjustment=%#v created=%v error=%v", adjustment, created, err)
	}
	var debtStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM business_balance_entries WHERE payment_adjustment_id = $1`, adjustment.ID).Scan(&debtStatus); err != nil || debtStatus != "open" {
		t.Fatalf("post-payout adjustment debt status=%q error=%v", debtStatus, err)
	}
	adjustmentInput.Status = "reversed"
	adjustment, created, err = repository.RecordPaymentAdjustment(ctx, adjustmentInput)
	if err != nil || created || adjustment.Status != "reversed" {
		t.Fatalf("RecordPaymentAdjustment(reversed) adjustment=%#v created=%v error=%v", adjustment, created, err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM business_balance_entries WHERE payment_adjustment_id = $1`, adjustment.ID).Scan(&debtStatus); err != nil || debtStatus != "void" {
		t.Fatalf("reversed adjustment debt status=%q error=%v", debtStatus, err)
	}
}

type integrationPayoutProvider struct {
	snapshot         PayoutSnapshot
	recipient        ProviderRecipient
	availableBalance money.Minor
	balanceChecks    int
}

func (p *integrationPayoutProvider) AvailablePayoutBalance(_ context.Context, _ string) (money.Minor, error) {
	p.balanceChecks++
	return p.availableBalance, nil
}

func (p *integrationPayoutProvider) InitiatePayout(_ context.Context, snapshot PayoutSnapshot, recipient ProviderRecipient) (PayoutResult, error) {
	p.snapshot = snapshot
	p.recipient = recipient
	return PayoutResult{ProviderReference: "provider-" + snapshot.Reference, ProviderStatus: "processing", Status: PayoutStatusPending}, nil
}

func (p *integrationPayoutProvider) ReconcilePayout(_ context.Context, payout PayoutRecord) (PayoutReconciliation, error) {
	now := time.Now().UTC()
	return PayoutReconciliation{
		ProviderStatus: "successful", Status: PayoutStatusSuccessful,
		AmountMinor: payout.AmountMinor, CurrencyCode: payout.CurrencyCode, CompletedAt: &now,
	}, nil
}

func containsClaimedAggregate(jobs []FinancialJob, aggregateID uuid.UUID) bool {
	for _, job := range jobs {
		if job.AggregateID == aggregateID {
			return true
		}
	}
	return false
}

func containsPayment(payments []FinancialPayment, paymentID uuid.UUID) bool {
	for _, payment := range payments {
		if payment.ID == paymentID {
			return true
		}
	}
	return false
}

func containsPayout(payouts []FinancialPayout, payoutID uuid.UUID) bool {
	for _, payout := range payouts {
		if payout.ID == payoutID {
			return true
		}
	}
	return false
}
