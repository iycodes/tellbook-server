package payments

import (
	"booking/go-server/internal/emailtest"
	"booking/go-server/internal/mailer"
	"booking/go-server/internal/securityemail"
	"context"
	"errors"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestFinancialRefundEmailLifecycleIntegration(t *testing.T) {
	ctx := context.Background()
	f := emailtest.New(t, true)
	r := NewLedgerRepository(f.DB).WithAdditionalEmails(true, true)
	command, request := uuid.New(), uuid.New()
	f.Exec(t, `INSERT INTO booking_change_commands(id,booking_id,actor_type,actor_id,command,idempotency_key,response_snapshot,request_fingerprint) VALUES($1,$2,'provider',$3,'decline',$1,'{}',repeat('a',64))`, command, f.Booking, f.Client)
	insert := func(commit bool) {
		t.Helper()
		tx, err := f.DB.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if err = securityemail.EnableTx(ctx, tx, false, true); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO booking_refund_requests(id,booking_id,command_id,amount_minor,currency_code) VALUES($1,$2,$3,5000,'NGN')`, request, f.Booking, command); err != nil {
			t.Fatal(err)
		}
		if commit {
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	insert(false)
	var count int
	if err := f.DB.QueryRow(ctx, `SELECT count(*) FROM financial_jobs WHERE aggregate_id=$1`, request).Scan(&count); err != nil || count != 0 {
		t.Fatal("rollback queued mail", err)
	}
	insert(true)
	var revision int64
	var delay float64
	if err := f.DB.QueryRow(ctx, `SELECT count(*),min(EXTRACT(EPOCH FROM available_at-created_at)) FROM financial_jobs WHERE aggregate_id=$1 AND kind='financial_email'`, request).Scan(&count, &delay); err != nil || count != 2 || delay < 299 {
		t.Fatalf("queued jobs %d delay %.0f err %v", count, delay, err)
	}
	f.Exec(t, `UPDATE booking_refund_requests SET updated_at=NOW() WHERE id=$1`, request)
	if err := f.DB.QueryRow(ctx, `SELECT notification_revision FROM booking_refund_requests WHERE id=$1`, request).Scan(&revision); err != nil || revision != 1 {
		t.Fatal("timestamp created revision", err)
	}
	transition := func(status string) {
		t.Helper()
		tx, err := f.DB.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if err = securityemail.EnableTx(ctx, tx, false, true); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE booking_refund_requests SET status=$2,updated_at=NOW() WHERE id=$1`, request, status); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	payment := f.Payment(t, 3000)
	f.Exec(t, `INSERT INTO booking_refund_attempts(id,request_id,payment_id,provider,transaction_reference,amount_minor,payment_amount_minor,currency_code,currency_exponent,status) VALUES($1,$2,$3,'paystack','captured-test',2000,3000,'NGN',2,'successful')`, uuid.New(), request, payment)

	secondPayment := f.Payment(t, 3000)
	f.Exec(t, `INSERT INTO booking_refund_attempts(id,request_id,payment_id,provider,transaction_reference,amount_minor,payment_amount_minor,currency_code,currency_exponent,status) VALUES($1,$2,$3,'paystack','captured-uncertain',3000,3000,'NGN',2,'unknown')`, uuid.New(), request, secondPayment)
	transition("manual_review")
	reconciler := NewBookingRefundWorker(r, nil, nil)
	for range 2 {
		if err := reconciler.reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	transition("manual_review")
	if err := f.DB.QueryRow(ctx, `SELECT count(*),min(EXTRACT(EPOCH FROM available_at-created_at)) FROM financial_jobs WHERE aggregate_id=$1 AND payload->>'status'='manual_review'`, request).Scan(&count, &delay); err != nil || count != 2 || delay < 1799 {
		t.Fatalf("uncertain delay/repeats %d %.0f %v", count, delay, err)
	}
	sender := &emailtest.Sender{}
	w := NewFinancialEmailWorker(r, sender, strings.Repeat("k", 32), "https://provider.example", "https://customer.example", nil)
	f.Exec(t, `UPDATE financial_jobs SET available_at=NOW()-INTERVAL '1 minute' WHERE aggregate_id=$1`, request)
	jobs, err := r.ClaimFinancialJobsByKind(ctx, w.workerID, "financial_email", 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.AggregateID == request {
			w.process(ctx, job)
		}
	}
	if len(sender.Messages) != 2 {
		t.Fatalf("stale notice sent or missing partial notices: %d", len(sender.Messages))
	}
	for _, m := range sender.Messages {
		if !strings.Contains(m.Text, "20.00") || !strings.Contains(m.Text, "50.00") {
			t.Fatalf("partial/request amounts missing: %s", m.Text)
		}
	}
	transition("failed")
	jobs, err = r.ClaimFinancialJobsByKind(ctx, w.workerID, "financial_email", 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.AggregateID != request {
			continue
		}
		if _, err = w.authorize(ctx, job); err != nil {
			t.Fatal(err)
		} // Simulate process loss after dispatch commit, before completion persistence.
		f.Exec(t, `UPDATE financial_jobs SET lease_expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, job.ID)
	}
	w.drain(ctx)
	if err = f.DB.QueryRow(ctx, `SELECT count(*) FROM financial_jobs WHERE aggregate_id=$1 AND status='unknown'`, request).Scan(&count); err != nil || count != 2 {
		t.Fatal("interrupted SMTP not fenced", err, count)
	}
	if len(sender.Messages) != 2 {
		t.Fatal("unknown jobs resent")
	}
	transition("successful")
	if err = f.DB.QueryRow(ctx, `SELECT count(*) FROM financial_jobs WHERE aggregate_id=$1 AND payload->>'status'='successful'`, request).Scan(&count); err != nil || count != 0 {
		t.Fatal("duplicate successful refund notice", err)
	}
}
func TestPayoutEmailAndAccountEventsIntegration(t *testing.T) {
	ctx := context.Background()
	f := emailtest.New(t, true)
	r := NewLedgerRepository(f.DB).WithAdditionalEmails(true, true)
	payment := f.Payment(t, 3000)
	allocation, destination, payout := uuid.New(), uuid.New(), uuid.New()
	f.Exec(t, `INSERT INTO payment_allocations(id,payment_id,client_id,currency_code,gross_amount_minor,business_net_amount_minor,policy_version,calculation_snapshot) VALUES($1,$2,$3,'NGN',3000,3000,'test','{}')`, allocation, payment, f.Client)
	tx, err := f.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = securityemail.EnableTx(ctx, tx, true, true); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO payout_destinations(id,client_id,provider,country_code,currency_code,rail,institution_code,institution_name,masked_identifier,identifier_ciphertext,identifier_nonce,encryption_key_version,resolved_account_name,verification_fingerprint,verified_at) VALUES($1,$2,'paystack','NG','NGN','bank_account','001','Original Bank','******1234','x','y','test','Test Account',repeat('a',64),NOW())`, destination, f.Client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE payout_destinations SET updated_at=NOW() WHERE id=$1`, destination); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE payout_destinations SET is_default=true WHERE id=$1`, destination); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.DB.QueryRow(ctx, `SELECT count(*) FROM account_security_events WHERE provider_client_id=$1`, f.Client).Scan(&count); err != nil || count != 2 {
		t.Fatal("unchanged account save generated notice", err, count)
	}
	f.Exec(t, `INSERT INTO payouts(id,payment_allocation_id,client_id,payout_destination_id,provider,rail,country_code,currency_code,amount_minor,reference,idempotency_key,request_fingerprint,destination_snapshot,status) VALUES($1,$2,$3,$4,'paystack','bank_account','NG','NGN',3000,$1::uuid::text,$1::uuid::text,repeat('a',64),'{"institution_name":"Original Bank","masked_identifier":"******1234"}','created')`, payout, allocation, f.Client, destination)
	p, err := r.TransitionPayout(ctx, payout, 1, PayoutStatusPending, PayoutTransitionUpdate{})
	if err != nil {
		t.Fatal(err)
	}
	f.Exec(t, `UPDATE payout_destinations SET institution_name='Changed Bank',masked_identifier='******9999' WHERE id=$1`, destination)
	p, err = r.TransitionPayout(ctx, p.ID, p.Version, PayoutStatusSuccessful, PayoutTransitionUpdate{})
	if err != nil {
		t.Fatal(err)
	}
	// A same-status reconciliation must neither create another job nor invalidate this one.
	p, err = r.TransitionPayout(ctx, p.ID, p.Version, PayoutStatusSuccessful, PayoutTransitionUpdate{})
	if err != nil {
		t.Fatal(err)
	}
	sender := &emailtest.Sender{Err: &mailer.TransportError{Disposition: mailer.DispositionRetryable, Cause: errors.New("SMTP 451 before acceptance")}}
	w := NewFinancialEmailWorker(r, sender, strings.Repeat("k", 32), "https://provider.example", "https://customer.example", nil)
	f.Exec(t, `UPDATE financial_jobs SET available_at=NOW()-INTERVAL '1 minute' WHERE aggregate_id=$1 AND kind='financial_email'`, payout)
	jobs, err := r.ClaimFinancialJobsByKind(ctx, w.workerID, "financial_email", 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.AggregateID == payout {
			w.process(ctx, job)
		}
	}
	if len(sender.Messages) != 1 || !strings.Contains(sender.Messages[0].Text, "Original Bank") || strings.Contains(sender.Messages[0].Text, "9999") {
		t.Fatalf("payout destination snapshot/obsolete notice: %+v", sender.Messages)
	}
	if err = f.DB.QueryRow(ctx, `SELECT count(*) FROM financial_jobs WHERE aggregate_id=$1 AND kind='financial_email' AND status='failed'`, payout).Scan(&count); err != nil || count != 1 {
		t.Fatal("definite transient not retried", err)
	}
	sender.Err = &mailer.TransportError{Disposition: mailer.DispositionAmbiguous, Cause: errors.New("SMTP ack lost")}
	f.Exec(t, `UPDATE financial_jobs SET available_at=NOW()-INTERVAL '1 minute' WHERE aggregate_id=$1 AND status='failed'`, payout)
	jobs, err = r.ClaimFinancialJobsByKind(ctx, w.workerID, "financial_email", 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.AggregateID == payout {
			w.process(ctx, job)
		}
	}
	if err = f.DB.QueryRow(ctx, `SELECT count(*) FROM financial_jobs WHERE aggregate_id=$1 AND status='unknown'`, payout).Scan(&count); err != nil || count != 1 {
		t.Fatal("ambiguous payout notice not fenced", err)
	}
	if err = r.RevokePayoutDestination(ctx, f.Client, destination); err != nil {
		t.Fatal(err)
	}
	if err = f.DB.QueryRow(ctx, `SELECT count(*) FROM account_security_events WHERE provider_client_id=$1 AND kind='payout_account_removed'`, f.Client).Scan(&count); err != nil || count != 1 {
		t.Fatal("missing removed account notice", err)
	}
}
