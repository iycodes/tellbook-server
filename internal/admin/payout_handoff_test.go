package admin

import (
	"booking/go-server/internal/payments"
	paycaps "booking/go-server/internal/payments/capabilities"
	"booking/go-server/internal/secure"
	"context"
	"errors"
	"github.com/google/uuid"
	"strings"
	"sync"
	"testing"
)

// Exercises the ledger primitive with the same transactional audit used by admin.
// There is deliberately no runtime execution route at this checkpoint.
func TestReviewedPayoutLedgerHandoff(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	owner, _, _ := fullSession(t, s)
	input := seedFinancialReview(t, s)
	encrypted, err := s.keys.Encrypt([]byte("0123456789"), []byte("payout-destination:"+input.BusinessID.String()+":"+input.DestinationID.String()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(ctx, `UPDATE payout_destinations SET identifier_ciphertext=$2,identifier_nonce=$3,encryption_key_version=$4 WHERE id=$1`, input.DestinationID, encrypted.Data, encrypted.Nonce, encrypted.KeyVersion); err != nil {
		t.Fatal(err)
	}
	draft := financialDraft(t, s, owner, input)
	ledger := payments.NewLedgerRepository(s.db)
	command := payments.CreateFinancialPayoutInput{ClientID: input.BusinessID, PaymentAllocationID: input.AllocationID, PayoutDestinationID: input.DestinationID, IdempotencyKey: "reviewed_" + uuid.NewString()}
	t.Cleanup(func() {
		if _, e := s.db.Exec(ctx, `DELETE FROM payouts WHERE client_id=$1`, input.BusinessID); e != nil {
			t.Error(e)
		}
	})
	execute := func(fingerprint string, commit bool) (payments.FinancialPayout, error) {
		tx, e := s.db.Begin(ctx)
		if e != nil {
			return payments.FinancialPayout{}, e
		}
		defer tx.Rollback(ctx)
		payout, e := ledger.CreateReviewedPayoutTx(ctx, tx, command, fingerprint)
		if e != nil {
			return payout, e
		}
		if e = audit(ctx, tx, &owner.Staff.ID, "finance.handoff_test", "payout", &payout.ID, "Transaction verification", nil); e != nil {
			return payout, e
		}
		if commit {
			e = tx.Commit(ctx)
		}
		return payout, e
	}
	if _, e := execute(strings.Repeat("f", 64), true); !errors.Is(e, payments.ErrReviewedPayoutChanged) {
		t.Fatal("stale terms", e)
	}
	rolled, e := execute(draft.ExpectedFingerprint, false)
	if e != nil {
		t.Fatal(e)
	}
	noFinancialExecution(t, s, input)
	var n int
	if e = s.db.QueryRow(ctx, `SELECT count(*) FROM admin_audit_events WHERE entity_id=$1`, rolled.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal("audit rollback", n, e)
	}
	// Audit failure must roll back the worker-visible payout and reservation too.
	if _, e = s.db.Exec(ctx, `ALTER TABLE admin_audit_events ADD CONSTRAINT handoff_audit_test CHECK(action<>'finance.handoff_test') NOT VALID`); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.db.Exec(ctx, `ALTER TABLE admin_audit_events DROP CONSTRAINT IF EXISTS handoff_audit_test`) })
	if _, e = execute(draft.ExpectedFingerprint, true); e == nil {
		t.Fatal("payout saved despite failed audit")
	}
	noFinancialExecution(t, s, input)
	if _, e = s.db.Exec(ctx, `ALTER TABLE admin_audit_events DROP CONSTRAINT handoff_audit_test`); e != nil {
		t.Fatal(e)
	}
	// Concurrent retries return the same command, even after reservation changed
	// the allocation's current status and timestamp.
	var wg sync.WaitGroup
	out := make(chan payments.FinancialPayout, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, e := s.db.Begin(ctx)
			if e != nil {
				errs <- e
				return
			}
			defer tx.Rollback(ctx)
			p, e := ledger.CreateReviewedPayoutTx(ctx, tx, command, draft.ExpectedFingerprint)
			if e == nil {
				e = tx.Commit(ctx)
			}
			out <- p
			errs <- e
		}()
	}
	wg.Wait()
	if e = <-errs; e != nil {
		t.Fatal(e)
	}
	if e = <-errs; e != nil {
		t.Fatal(e)
	}
	first, second := <-out, <-out
	if first.ID != second.ID || first.Status != payments.PayoutStatusCreated || first.AmountMinor != 9007199254740393 {
		t.Fatal("duplicate/inexact payout", first.ID, second.ID)
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = ledger.CreateReviewedPayoutTx(ctx, tx, command, strings.Repeat("e", 64)); !errors.Is(e, payments.ErrIdempotencyConflict) {
		t.Fatal("changed replay accepted", e)
	}
	var status string
	if e = s.db.QueryRow(ctx, `SELECT status FROM payment_allocations WHERE id=$1`, input.AllocationID).Scan(&status); e != nil || status != "reserved" {
		t.Fatal("reservation", status, e)
	}
	detail, e := s.FinancePayoutDetail(ctx, first.ID)
	if e != nil || detail.MaskedIdentifier != "****9999" {
		t.Fatal("safe detail projection", e)
	}
	// Dispatch and reconciliation must both ignore later live destination edits.
	if _, e = s.db.Exec(ctx, `UPDATE payout_destinations SET provider_recipient_id='CHANGED_RECIPIENT',resolved_account_name='CHANGED_OWNER',status='disabled' WHERE id=$1`, input.DestinationID); e != nil {
		t.Fatal(e)
	}
	fingerprinter, e := secure.NewFingerprinter("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if e != nil {
		t.Fatal(e)
	}
	secureLedger, e := payments.NewLedgerService(ledger, s.keys, fingerprinter)
	if e != nil {
		t.Fatal(e)
	}
	registry, e := paycaps.New([]paycaps.Capability{{Provider: paycaps.ProviderPaystack, Operation: paycaps.OperationPayout, CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_transfer", ProviderChannel: "nuban", CurrencyExponent: 2, Configured: true, SandboxVerified: true}})
	if e != nil {
		t.Fatal(e)
	}
	provider := &reviewedRecipientProvider{}
	payoutService, e := payments.NewPayoutService(payments.PayoutServiceConfig{Ledger: secureLedger, Repository: ledger, Capabilities: registry, Environment: paycaps.EnvironmentTest, Providers: map[string]payments.PayoutProvider{"paystack": provider}})
	if e != nil {
		t.Fatal(e)
	}
	pending, e := payoutService.RetryCreated(ctx, first)
	if e != nil || pending.Status != payments.PayoutStatusPending || provider.sent.ProviderReference != "PRIVATE_RECIPIENT" || provider.sent.AccountName != "PRIVATE_ACCOUNT_NAME" {
		t.Fatal("dispatch used changed destination", e)
	}
	settled, e := payoutService.Reconcile(ctx, pending)
	if e != nil || settled.Status != payments.PayoutStatusSuccessful || provider.checked.ProviderReference != "PRIVATE_RECIPIENT" || provider.checked.Identifier != "0123456789" {
		t.Fatal("reconciliation used changed destination", e)
	}

}

type reviewedRecipientProvider struct{ sent, checked payments.ProviderRecipient }

func (p *reviewedRecipientProvider) InitiatePayout(_ context.Context, _ payments.PayoutSnapshot, r payments.ProviderRecipient) (payments.PayoutResult, error) {
	p.sent = r
	return payments.PayoutResult{Status: payments.PayoutStatusPending, ProviderReference: "local-test-only"}, nil
}
func (p *reviewedRecipientProvider) ReconcilePayout(_ context.Context, r payments.PayoutRecord) (payments.PayoutReconciliation, error) {
	p.checked = r.ExpectedRecipient
	return payments.PayoutReconciliation{Status: payments.PayoutStatusSuccessful, AmountMinor: r.AmountMinor, CurrencyCode: r.CurrencyCode}, nil
}
