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

func TestBookingInvestigationScopePaginationAndPrivacy(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	full, _, _ := fullSession(t, s)
	business, contact, account := uuid.New(), uuid.New(), uuid.New()
	run := func(q string, args ...any) {
		t.Helper()
		if _, e := s.db.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	run(`INSERT INTO clients(id,full_name) VALUES($1,'Investigation owner')`, business)
	run(`INSERT INTO client_profile_handles(client_id,handle_slug) VALUES($1,$2)`, business, "investigation-"+business.String())
	run(`INSERT INTO client_profiles(client_id,handle_slug,business_name) VALUES($1,$2,'Investigation studio')`, business, "investigation-"+business.String())
	run(`INSERT INTO customers(id,client_id,full_name,email,private_notes) VALUES($1,$2,'Same Name','same@example.test','PROVIDER_PRIVATE_SECRET')`, contact, business)
	run(`INSERT INTO marketplace_customers(id,full_name,email,password_hash) VALUES($1,'Same Name','same@example.test','PASSWORD_SECRET')`, account)
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM bookings WHERE client_id=$1`, `DELETE FROM customers WHERE client_id=$1`, `DELETE FROM client_profiles WHERE client_id=$1`, `DELETE FROM client_profile_handles WHERE client_id=$1`, `DELETE FROM clients WHERE id=$1`} {
			if _, e := s.db.Exec(ctx, q, business); e != nil {
				t.Error(e)
			}
		}
		_, _ = s.db.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=$1`, account)
	})
	start := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	ids := []uuid.UUID{}
	for i := range 52 {
		id := uuid.New()
		ids = append(ids, id)
		var linked any
		if i == 0 {
			linked = account
		}
		at := start.Add(time.Duration(i) * time.Minute)
		run(`INSERT INTO bookings(id,client_id,customer_id,marketplace_customer_id,title,status,payment_status,agreement_status,start_at,end_at,occupied_start_at,occupied_end_at,currency_code,country_code,total_amount_minor,discounted_service_amount_minor,customer_email_snapshot) VALUES($1,$2,$3,$4,'Investigation service','booked','unpaid','not_required',$5,$5::timestamptz+interval '1 hour',$5,$5::timestamptz+interval '1 hour','NGN','NG',9007199254740993,9007199254740993,'snapshot@example.test')`, id, business, contact, linked, at)
	}
	f := BookingFilter{From: "2026-09-16", To: "2026-09-17", BusinessID: &business}
	first, e := s.Bookings(ctx, f)
	if e != nil || len(first.Items) != 50 || first.NextCursor == "" {
		t.Fatalf("first page %d %v", len(first.Items), e)
	}
	if first.Items[0].StartAt.Location() != time.UTC {
		t.Fatal("timestamps must match UTC filter and agenda scope")
	}
	f.Cursor = first.NextCursor
	second, e := s.Bookings(ctx, f)
	if e != nil || len(second.Items) != 2 || second.NextCursor != "" {
		t.Fatalf("second page: %+v %v", second, e)
	}
	if first.Items[49].ID == second.Items[0].ID {
		t.Fatal("duplicate pagination row")
	}
	f.Status = "confirmed"
	if _, e = s.Bookings(ctx, f); e == nil {
		t.Fatal("cursor accepted with different filters")
	}
	f.Cursor = ""
	f.Status = ""
	f.AccountID = &account
	linked, e := s.Bookings(ctx, f)
	if e != nil || len(linked.Items) != 1 || linked.Items[0].ID != ids[0] {
		t.Fatal("identity matched by name/email instead of booking ID", e)
	}
	unrelated := uuid.New()
	f.AccountID = nil
	f.BusinessID = &unrelated
	empty, e := s.Bookings(ctx, f)
	if e != nil || len(empty.Items) != 0 {
		t.Fatal("cross-business scope ignored", e)
	}
	b, e := s.BookingDetail(ctx, ids[0])
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(b)
	if !strings.Contains(string(raw), `"9007199254740993"`) || strings.Contains(string(raw), "public_token") {
		t.Fatalf("unsafe detail contract: %s", raw)
	}
	if b.BookingEmail != "snapshot@example.test" || b.AccountID == nil || *b.AccountID != account {
		t.Fatal("booking identity/snapshot lost")
	}
	for _, kind := range []string{"contacts", "accounts"} {
		id := contact
		if kind == "accounts" {
			id = account
		}
		record, e := s.CustomerRecord(ctx, kind, id)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(record)
		if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "private_notes") {
			t.Fatal("private fields leaked")
		}
	}
	if _, e = s.CustomerRecord(ctx, "accounts", contact); e == nil {
		t.Fatal("contact treated as marketplace account")
	}
	if _, e = s.BookingDetail(ctx, uuid.New()); e == nil {
		t.Fatal("missing booking accepted")
	}
	run(`UPDATE admin_staff SET role='analyst' WHERE id=$1`, full.Staff.ID)
	handler := s.Handler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, path := range []string{"/bookings", "/bookings/" + ids[0].String(), "/customers/contacts/" + contact.String(), "/customers/accounts/" + account.String()} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+full.Token)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != 403 {
			t.Fatalf("analyst reached %s: %d", path, res.Code)
		}
	}
}
func TestBookingWindowValidation(t *testing.T) {
	now := time.Date(2026, 9, 16, 23, 30, 0, 0, time.FixedZone("west", -3600))
	start, end, e := bookingWindow(BookingFilter{}, now)
	if e != nil || start.Format("2006-01-02") != "2026-09-17" || end.Sub(start) != 7*24*time.Hour {
		t.Fatal("default UTC window", start, end, e)
	}
	for _, f := range []BookingFilter{{From: "bad"}, {From: "2026-09-17", To: "2026-09-17"}, {From: "2026-09-17", To: "2027-01-01"}, {From: "2026-02-30"}} {
		if _, _, e := bookingWindow(f, now); e == nil {
			t.Fatal("invalid window accepted", f)
		}
	}
}
