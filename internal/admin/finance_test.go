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

// Used only by isolated integration tests and the opt-in browser fixture.
func seedFinanceInvestigation(t *testing.T, s *Service, count int) (uuid.UUID, uuid.UUID, []uuid.UUID, time.Time) {
	t.Helper()
	ctx := context.Background()
	business, contact, booking := uuid.New(), uuid.New(), uuid.New()
	run := func(q string, args ...any) {
		t.Helper()
		if _, e := s.db.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	run(`INSERT INTO clients(id,full_name) VALUES($1,'Finance QA owner')`, business)
	run(`INSERT INTO client_profile_handles(client_id,handle_slug) VALUES($1,$2)`, business, "finance-"+business.String())
	run(`INSERT INTO client_profiles(client_id,handle_slug,business_name) VALUES($1,$2,'Finance QA studio')`, business, "finance-"+business.String())
	run(`INSERT INTO customers(id,client_id,full_name,email,private_notes) VALUES($1,$2,'Finance QA contact','finance@example.test','PRIVATE_CONTACT_NOTE')`, contact, business)
	at := s.now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	run(`INSERT INTO bookings(id,client_id,customer_id,title,status,payment_status,agreement_status,start_at,end_at,occupied_start_at,occupied_end_at,currency_code,country_code,total_amount_minor,discounted_service_amount_minor) VALUES($1,$2,$3,'Finance QA appointment','booked','paid_in_full','not_required',$4,$4::timestamptz+interval '1 hour',$4,$4::timestamptz+interval '1 hour','NGN','NG',250000,250000)`, booking, business, contact, at)
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM payment_exceptions WHERE booking_id IN (SELECT id FROM bookings WHERE client_id=$1)`, `DELETE FROM payment_adjustments WHERE payment_id IN (SELECT id FROM payments WHERE client_id=$1)`, `DELETE FROM payment_allocations WHERE client_id=$1`, `DELETE FROM payments WHERE client_id=$1`, `DELETE FROM bookings WHERE client_id=$1`, `DELETE FROM customers WHERE client_id=$1`, `DELETE FROM client_profiles WHERE client_id=$1`, `DELETE FROM client_profile_handles WHERE client_id=$1`, `DELETE FROM clients WHERE id=$1`} {
			if _, e := s.db.Exec(ctx, q, business); e != nil {
				t.Error(e)
			}
		}
	})
	ids := []uuid.UUID{}
	for i := 0; i < count; i++ {
		id := uuid.New()
		ids = append(ids, id)
		status, currency, amount := "failed", "NGN", int64(250000)
		if i == 0 {
			status = "paid"
			amount = 9007199254740993
		}
		if i == 1 {
			currency = "USD"
		}
		run(`INSERT INTO payments(id,public_token,booking_id,client_id,customer_id,purpose,provider,method,country_code,currency_code,amount_minor,price_snapshot,reference,provider_reference,idempotency_key,request_fingerprint,status,created_at,checkout_url,checkout_details,provider_status,paid_at,last_reconciled_at) VALUES($1,$2,$3,$4,$5,'full','paystack','card','NG',$6,$7,'{}',$8,$9,$10,$11,$12,$13,'https://example.test/PRIVATE_CHECKOUT','{"private":"RAW_PROVIDER_PAYLOAD"}','recorded',$14,$13)`, id, "PRIVATE_TOKEN_"+id.String(), booking, business, contact, currency, amount, "QA-PAY-"+id.String(), "QA-PROVIDER-"+id.String(), "request_"+id.String(), strings.Repeat("a", 64), status, at, func() any {
			if i == 0 {
				return at
			}
			return nil
		}())
	}
	if count > 0 {
		run(`INSERT INTO payment_allocations(id,payment_id,client_id,currency_code,gross_amount_minor,provider_collection_fee_minor,platform_fee_minor,tax_amount_minor,adjustment_amount_minor,business_net_amount_minor,policy_version,calculation_snapshot,status,settlement_status,settlement_reference,available_for_payout_at) VALUES($1,$2,$3,'NGN',9007199254740993,100,200,0,300,9007199254740393,'qa-v1','{}','eligible','available','QA-SETTLEMENT',$4)`, uuid.New(), ids[0], business, at)
		for i := 0; i < 27; i++ {
			run(`INSERT INTO payment_adjustments(id,payment_id,provider,provider_reference,kind,status,currency_code,amount_minor,occurred_at) VALUES($1,$2,'paystack',$3,'partial_refund','successful','NGN',100,$4)`, uuid.New(), ids[0], "QA-REFUND-"+uuid.NewString(), at.Add(time.Duration(i)*time.Minute))
			run(`INSERT INTO payment_exceptions(id,payment_id,booking_id,provider,exception_kind,provider_reference,evidence_source,evidence_reference,observed_amount_minor,currency_code,created_at) VALUES($1,$2,$3,'paystack','amount_mismatch',$4,'webhook',$5,250000,'NGN',$6)`, uuid.New(), ids[0], booking, "QA-PROVIDER-"+ids[0].String(), "QA-EVIDENCE-"+uuid.NewString(), at.Add(time.Duration(i)*time.Minute))
		}
	}
	return business, booking, ids, at
}

func TestFinanceInvestigationScopePrivacyAndPermissions(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	staff, _, _ := fullSession(t, s)
	business, booking, ids, at := seedFinanceInvestigation(t, s, 52)
	f := FinanceFilter{From: at.Format("2006-01-02"), To: at.AddDate(0, 0, 1).Format("2006-01-02"), BusinessID: &business}
	first, e := s.FinancePayments(ctx, f)
	if e != nil || len(first.Items) != 50 || first.NextCursor == "" {
		t.Fatalf("first page %+v %v", first, e)
	}
	f.Cursor = first.NextCursor
	second, e := s.FinancePayments(ctx, f)
	if e != nil || len(second.Items) != 2 || second.NextCursor != "" {
		t.Fatalf("second page %+v %v", second, e)
	}
	seen := map[uuid.UUID]bool{}
	for _, p := range append(first.Items, second.Items...) {
		if seen[p.ID] {
			t.Fatal("duplicate across equal-timestamp pages")
		}
		seen[p.ID] = true
	}
	for _, change := range []func(*FinanceFilter){func(f *FinanceFilter) { f.Currency = "USD" }, func(f *FinanceFilter) { f.Q = "QA" }, func(f *FinanceFilter) { f.Status = "paid" }, func(f *FinanceFilter) { f.BookingID = &booking }, func(f *FinanceFilter) { f.To = at.AddDate(0, 0, 2).Format("2006-01-02") }} {
		x := f
		change(&x)
		if _, e = s.FinancePayments(ctx, x); e == nil {
			t.Fatal("changed filter accepted cursor")
		}
	}
	f.Cursor = ""
	f.Currency = "usd"
	list, e := s.FinancePayments(ctx, f)
	if e != nil || len(list.Items) != 1 || list.Items[0].ID != ids[1] {
		t.Fatal("currency filter", e, list)
	}
	f.Currency = ""
	f.Status = "paid"
	list, e = s.FinancePayments(ctx, f)
	if e != nil || len(list.Items) != 1 || list.Items[0].ID != ids[0] {
		t.Fatal("status filter", e, list)
	}
	f.Status = ""
	f.Q = "%"
	list, e = s.FinancePayments(ctx, f)
	if e != nil || len(list.Items) != 0 {
		t.Fatal("search wildcard expanded", e)
	}
	f.Q = "QA-PROVIDER-" + ids[0].String()
	list, e = s.FinancePayments(ctx, f)
	if e != nil || len(list.Items) != 1 {
		t.Fatal("provider reference search", e)
	}
	other := uuid.New()
	f.Q = ""
	f.BookingID = &other
	list, e = s.FinancePayments(ctx, f)
	if e != nil || len(list.Items) != 0 {
		t.Fatal("booking scope ignored", e)
	}
	f.BookingID = nil
	f.BusinessID = &other
	list, e = s.FinancePayments(ctx, f)
	if e != nil || len(list.Items) != 0 {
		t.Fatal("business scope ignored", e)
	}
	for _, x := range []FinanceFilter{{From: "bad"}, {From: "2026-01-01", To: "2026-03-01"}, {From: "2026-01-02", To: "2026-01-01"}, {Status: "successful"}, {Currency: "N1!"}, {Q: strings.Repeat("a", 101)}, {Cursor: "bad"}} {
		if _, e = s.FinancePayments(ctx, x); e == nil {
			t.Fatal("invalid filter accepted", x)
		}
	}
	detail, e := s.FinancePaymentDetail(ctx, ids[0])
	if e != nil {
		t.Fatal(e)
	}
	if detail.Allocation == nil || detail.Allocation.BusinessNet != 9007199254740393 || len(detail.Adjustments) != 25 || len(detail.Exceptions) != 25 || !detail.MoreAdjustments || !detail.MoreExceptions {
		t.Fatalf("detail evidence %+v", detail)
	}
	if detail.Adjustments[0].OccurredAt.Before(detail.Adjustments[1].OccurredAt) {
		t.Fatal("history order")
	}
	raw, _ := json.Marshal(detail)
	if !strings.Contains(string(raw), `"9007199254740993"`) {
		t.Fatal("lost exact amount")
	}
	for _, secret := range []string{"public_token", "PRIVATE_TOKEN", "checkout_url", "PRIVATE_CHECKOUT", "RAW_PROVIDER_PAYLOAD", "request_fingerprint", "idempotency_key", "PRIVATE_CONTACT_NOTE", "calculation_snapshot"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private field leaked", secret)
		}
	}
	empty, e := s.FinancePaymentDetail(ctx, ids[1])
	if e != nil || empty.Allocation != nil || len(empty.Adjustments) != 0 || empty.Adjustments == nil || empty.Exceptions == nil {
		t.Fatal("empty detail", e)
	}
	if _, e = s.FinancePaymentDetail(ctx, uuid.New()); e == nil {
		t.Fatal("missing payment accepted")
	}
	h := s.Handler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	get := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Cookie", "provider_session=customer-token")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		return res
	}
	paths := []string{"/finance/payments?business=" + business.String(), "/finance/payments/" + ids[0].String()}
	for _, role := range []string{"super_admin", "finance", "operations", "support", "analyst"} {
		if _, e = s.db.Exec(ctx, `UPDATE admin_staff SET role=$2 WHERE id=$1`, staff.Staff.ID, role); e != nil {
			t.Fatal(e)
		}
		want := 403
		if role == "super_admin" || role == "finance" {
			want = 200
		}
		for _, path := range paths {
			res := get(path, staff.Token)
			if res.Code != want {
				t.Fatalf("%s %d %s", role, res.Code, res.Body.String())
			}
			if want == 403 && strings.Contains(res.Body.String(), ids[0].String()) {
				t.Fatal("denial contained private record")
			}
		}
	}
	for _, token := range []string{"", "provider-token", "customer-token"} {
		for _, path := range paths {
			if res := get(path, token); res.Code != 401 {
				t.Fatal("wrong-realm access", res.Code)
			}
		}
	}
	if _, e = s.db.Exec(ctx, `UPDATE admin_staff SET role='finance' WHERE id=$1`, staff.Staff.ID); e != nil {
		t.Fatal(e)
	}
	if get("/finance/payments?business=invalid", staff.Token).Code != 422 || get("/finance/payments/"+uuid.NewString(), staff.Token).Code != 404 {
		t.Fatal("invalid ID/missing record response")
	}
	if _, e = s.db.Exec(ctx, `DELETE FROM admin_sessions WHERE id=$1`, staff.ID); e != nil {
		t.Fatal(e)
	}
	if get(paths[0], staff.Token).Code != 401 {
		t.Fatal("revoked session retained access")
	}
}
