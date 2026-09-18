package admin

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func seedFinanceTransfers(t *testing.T, s *Service, count int) (uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, time.Time) {
	t.Helper()
	ctx := context.Background()
	business, booking, payments, at := seedFinanceInvestigation(t, s, max(count, 3))
	run := func(q string, args ...any) {
		t.Helper()
		if _, e := s.db.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	var allocation uuid.UUID
	if e := s.db.QueryRow(ctx, `SELECT id FROM payment_allocations WHERE payment_id=$1`, payments[0]).Scan(&allocation); e != nil {
		t.Fatal(e)
	}
	destination := uuid.New()
	run(`INSERT INTO payout_destinations(id,client_id,provider,country_code,currency_code,rail,institution_code,institution_name,masked_identifier,resolved_account_name,verification_fingerprint,verified_at,status) VALUES($1,$2,'paystack','NG','NGN','bank_transfer','001','Current institution','****9999','PRIVATE_ACCOUNT_NAME',$3,now(),'disabled')`, destination, business, strings.Repeat("b", 64))
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM booking_refund_attempts WHERE request_id IN (SELECT id FROM booking_refund_requests WHERE booking_id=$1)`, `DELETE FROM booking_refund_requests WHERE booking_id=$1`, `DELETE FROM booking_change_commands WHERE booking_id=$1`, `DELETE FROM payouts WHERE client_id=(SELECT client_id FROM bookings WHERE id=$1)`, `DELETE FROM payout_destinations WHERE client_id=(SELECT client_id FROM bookings WHERE id=$1)`} {
			if _, e := s.db.Exec(ctx, q, booking); e != nil {
				t.Error(e)
			}
		}
	})
	var firstPayout, firstRefund uuid.UUID
	for i := 0; i < count; i++ {
		payout, request, command := uuid.New(), uuid.New(), uuid.New()
		status := "failed"
		amount := int64(5000)
		refundStatus := "queued"
		if i == 0 {
			firstPayout, firstRefund = payout, request
			status = "unknown"
			amount = 9007199254740393
			refundStatus = "manual_review"
		}
		run(`INSERT INTO payouts(id,payment_allocation_id,client_id,payout_destination_id,provider,rail,country_code,currency_code,amount_minor,fee_minor,reference,provider_reference,idempotency_key,request_fingerprint,destination_snapshot,status,created_at,provider_status,initiated_at,last_reconciled_at) VALUES($1,$2,$3,$4,'paystack','bank_transfer','NG','NGN',$5,10,$6,$7,$8,$9,'{"institution_name":"Historical institution","masked_identifier":"****1234","provider_recipient_id":"PRIVATE_RECIPIENT","account_name":"PRIVATE_ACCOUNT_NAME"}',$10,$11,'processing',$11,$11)`, payout, allocation, business, destination, amount, "QA-PAYOUT-"+payout.String(), "QA-TRANSFER-"+payout.String(), "payout_"+payout.String(), strings.Repeat("c", 64), status, at)
		run(`INSERT INTO booking_change_commands(id,booking_id,actor_type,actor_id,command,idempotency_key,reason,response_snapshot,request_fingerprint) VALUES($1,$2,'provider',$3,'decline',$4,'QA refund request','{}',$5)`, command, booking, business, uuid.New(), strings.Repeat("d", 64))
		run(`INSERT INTO booking_refund_requests(id,booking_id,command_id,amount_minor,currency_code,status,reason,created_at) VALUES($1,$2,$3,100,'NGN',$4,'QA: investigate existing refund operation',$5)`, request, booking, command, refundStatus, at)
	}
	for i, payment := range payments {
		if i == 1 {
			continue
		}
		if i > 27 {
			break
		}
		status := "successful"
		if i == 0 {
			status = "unknown"
		}
		run(`INSERT INTO booking_refund_attempts(id,request_id,payment_id,provider,transaction_reference,provider_reference,amount_minor,payment_amount_minor,currency_code,currency_exponent,status,provider_status,failure_message,created_at) VALUES($1,$2,$3,'paystack',$4,$5,1,250000,'NGN',2,$6,'observed','PRIVATE_PROVIDER_ERROR',$7)`, uuid.New(), firstRefund, payment, "QA-PAYMENT-"+payment.String(), "QA-ATTEMPT-"+payment.String(), status, at.Add(time.Duration(i)*time.Minute))
	}
	return business, booking, firstPayout, firstRefund, at
}
func TestFinanceTransfersReadScopeAndEvidence(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	staff, _, _ := fullSession(t, s)
	business, booking, payout, refund, at := seedFinanceTransfers(t, s, 52)
	f := FinanceFilter{From: at.Format("2006-01-02"), To: at.AddDate(0, 0, 1).Format("2006-01-02"), BusinessID: &business}
	for _, kind := range []string{"payouts", "refunds"} {
		first, e := s.FinanceTransfers(ctx, kind, f)
		if e != nil || len(first.Items) != 50 || first.NextCursor == "" {
			t.Fatal(kind, len(first.Items), e)
		}
		next := f
		next.Cursor = first.NextCursor
		second, e := s.FinanceTransfers(ctx, kind, next)
		if e != nil || len(second.Items) != 2 {
			t.Fatal(kind, "second page", e)
		}
		seen := map[uuid.UUID]bool{}
		for _, p := range append(first.Items, second.Items...) {
			if seen[p.ID] {
				t.Fatal("duplicate row")
			}
			seen[p.ID] = true
		}
		otherKind := "refunds"
		if kind == "refunds" {
			otherKind = "payouts"
		}
		if _, e = s.FinanceTransfers(ctx, otherKind, next); e == nil {
			t.Fatal("cross-kind cursor accepted")
		}
		if _, e = s.FinancePayments(ctx, next); e == nil {
			t.Fatal("transfer cursor accepted in payments")
		}
		for _, change := range []func(*FinanceFilter){func(x *FinanceFilter) { x.Currency = "USD" }, func(x *FinanceFilter) { x.BookingID = &booking }, func(x *FinanceFilter) { x.Q = "QA" }, func(x *FinanceFilter) { x.To = at.AddDate(0, 0, 2).Format("2006-01-02") }} {
			x := next
			change(&x)
			if _, e = s.FinanceTransfers(ctx, kind, x); e == nil {
				t.Fatal("changed-filter cursor")
			}
		}
		other := uuid.New()
		x := f
		x.BusinessID = &other
		empty, e := s.FinanceTransfers(ctx, kind, x)
		if e != nil || len(empty.Items) != 0 {
			t.Fatal("business scope", e)
		}
		x = f
		x.BookingID = &other
		empty, e = s.FinanceTransfers(ctx, kind, x)
		if e != nil || len(empty.Items) != 0 {
			t.Fatal("booking scope", e)
		}
		x = f
		x.Q = "%"
		empty, e = s.FinanceTransfers(ctx, kind, x)
		if e != nil || len(empty.Items) != 0 {
			t.Fatal("wildcard", e)
		}
		for _, x := range []FinanceFilter{{Status: "paid"}, {Currency: "bad!"}, {From: "bad"}, {Cursor: "bad"}, {Q: strings.Repeat("x", 101)}} {
			if _, e = s.FinanceTransfers(ctx, kind, x); e == nil {
				t.Fatal("invalid filter", kind, x)
			}
		}
	}
	p, e := s.FinancePayoutDetail(ctx, payout)
	if e != nil {
		t.Fatal(e)
	}
	if p.Institution != "Historical institution" || p.MaskedIdentifier != "****1234" || p.Status != "unknown" || p.PaymentID == nil || p.AmountMinor != 9007199254740393 {
		t.Fatalf("payout snapshot %+v", p)
	}
	r, e := s.FinanceRefundDetail(ctx, refund)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Attempts) != 25 || !r.MoreAttempts || r.CommandID == uuid.Nil || r.PaymentID != nil || r.Status != "manual_review" {
		t.Fatalf("refund evidence %+v", r)
	}
	raw, _ := json.Marshal([]any{p, r})
	for _, secret := range []string{"PRIVATE_", "destination_snapshot", "identifier_ciphertext", "provider_recipient_id", "request_fingerprint", "idempotency_key", "failure_message"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private evidence leaked", secret)
		}
	}
	x := f
	x.Status = "unknown"
	list, e := s.FinanceTransfers(ctx, "payouts", x)
	if e != nil || len(list.Items) != 1 || list.Items[0].ID != payout {
		t.Fatal("payout status", e)
	}
	x = f
	x.Status = "manual_review"
	list, e = s.FinanceTransfers(ctx, "refunds", x)
	if e != nil || len(list.Items) != 1 || list.Items[0].ID != refund {
		t.Fatal("refund status", e)
	}
	x = f
	x.Q = refund.String()
	list, e = s.FinanceTransfers(ctx, "refunds", x)
	if e != nil || len(list.Items) != 1 {
		t.Fatal("refund ID search", e)
	}
	h := s.Handler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	paths := []string{"/finance/payouts?business=" + business.String(), "/finance/refunds?business=" + business.String(), "/finance/payouts/" + payout.String(), "/finance/refunds/" + refund.String()}
	get := func(method, path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		return res
	}
	for _, role := range []string{"super_admin", "finance", "operations", "support", "analyst"} {
		if _, e = s.db.Exec(ctx, `UPDATE admin_staff SET role=$2 WHERE id=$1`, staff.Staff.ID, role); e != nil {
			t.Fatal(e)
		}
		want := 403
		if role == "super_admin" || role == "finance" {
			want = 200
		}
		for _, path := range paths {
			res := get("GET", path, staff.Token)
			if res.Code != want {
				t.Fatal(role, path, res.Code, res.Body.String())
			}
		}
	}
	for _, token := range []string{"", "provider-token", "customer-token"} {
		for _, path := range paths {
			if get("GET", path, token).Code != 401 {
				t.Fatal("wrong realm")
			}
		}
	}
	if _, e = s.db.Exec(ctx, `UPDATE admin_staff SET role='finance' WHERE id=$1`, staff.Staff.ID); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/finance/payouts/" + uuid.NewString(), "/finance/refunds/" + uuid.NewString(), "/finance/payouts/" + refund.String(), "/finance/refunds/" + payout.String()} {
		if get("GET", path, staff.Token).Code != 404 {
			t.Fatal("missing/wrong resource", path)
		}
	}
	if get("GET", "/finance/refunds?booking=invalid", staff.Token).Code != 422 {
		t.Fatal("bad scope accepted")
	}
	for _, path := range paths {
		if get("POST", path, staff.Token).Code != 405 {
			t.Fatal("mutation exposed")
		}
	}
	if _, e = s.db.Exec(ctx, `DELETE FROM admin_sessions WHERE id=$1`, staff.ID); e != nil {
		t.Fatal(e)
	}
	if get("GET", paths[0], staff.Token).Code != 401 {
		t.Fatal("revocation")
	}
}
