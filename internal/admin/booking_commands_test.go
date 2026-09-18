package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"booking/go-server/internal/appdata"
	"github.com/google/uuid"
)

func TestStaffBookingCommands(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	staff, _, _ := fullSession(t, s)
	business, contact := uuid.New(), uuid.New()
	run := func(q string, args ...any) {
		t.Helper()
		if _, e := s.db.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	t.Cleanup(func() {
		run(`DELETE FROM booking_refund_requests WHERE booking_id IN (SELECT id FROM bookings WHERE client_id=$1)`, business)
		run(`DELETE FROM payments WHERE client_id=$1`, business)
		run(`DELETE FROM bookings WHERE client_id=$1`, business)
		run(`DELETE FROM customers WHERE client_id=$1`, business)
		run(`DELETE FROM client_profiles WHERE client_id=$1`, business)
		run(`DELETE FROM client_profile_handles WHERE client_id=$1`, business)
		run(`DELETE FROM clients WHERE id=$1`, business)
	})
	run(`INSERT INTO clients(id,full_name) VALUES($1,'Command owner')`, business)
	run(`INSERT INTO client_profile_handles(client_id,handle_slug) VALUES($1,$2)`, business, "command-"+business.String())
	run(`INSERT INTO client_profiles(client_id,handle_slug,business_name) VALUES($1,$2,'Command business')`, business, "command-"+business.String())
	run(`INSERT INTO customers(id,client_id,full_name) VALUES($1,$2,'Command customer')`, contact, business)
	makeBooking := func(status, payment, agreement string, endOffset time.Duration) (uuid.UUID, time.Time) {
		t.Helper()
		id := uuid.New()
		end := time.Now().UTC().Add(endOffset)
		title := ""
		if agreement != "not_required" {
			title = "Required agreement"
		}
		run(`INSERT INTO bookings(id,client_id,customer_id,title,status,payment_status,agreement_status,agreement_title_snapshot,start_at,end_at,occupied_start_at,occupied_end_at,currency_code,country_code) VALUES($1,$2,$3,'Command test',$4,$5,$6,$7,$8,$9,$8,$9,'NGN','NG')`, id, business, contact, status, payment, agreement, title, end.Add(-time.Hour), end)
		var version time.Time
		if e := s.db.QueryRow(ctx, `SELECT updated_at FROM bookings WHERE id=$1`, id).Scan(&version); e != nil {
			t.Fatal(e)
		}
		return id, version
	}
	command := func(action string, version time.Time) BookingCommand {
		return BookingCommand{Action: action, Reason: "Reviewed appointment evidence", RequestKey: uuid.New(), ExpectedUpdatedAt: version}
	}
	assertCode := func(e error, code string) {
		t.Helper()
		var p *Problem
		if !errors.As(e, &p) || p.Code != code {
			t.Fatalf("want %s: %v", code, e)
		}
	}
	id, version := makeBooking("booked", "paid_in_full", "accepted", 24*time.Hour)
	input := command("confirm", version)
	results := make(chan BookingCommandResult, 4)
	errs := make(chan error, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); r, e := s.CommandBooking(ctx, staff, id, input); results <- r; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var receipt uuid.UUID
	for r := range results {
		if r.Status != "confirmed" || r.CommandID == uuid.Nil {
			t.Fatal(r)
		}
		if receipt != uuid.Nil && receipt != r.CommandID {
			t.Fatal("duplicate receipt")
		}
		receipt = r.CommandID
	}
	var actorType string
	var actorID uuid.UUID
	if e := s.db.QueryRow(ctx, `SELECT actor_type,actor_id FROM booking_change_commands WHERE id=$1`, receipt).Scan(&actorType, &actorID); e != nil || actorType != "staff" || actorID != staff.Staff.ID {
		t.Fatal("staff impersonated provider", actorType, actorID, e)
	}
	var count int
	if e := s.db.QueryRow(ctx, `SELECT count(*) FROM admin_audit_events WHERE entity_id=$1 AND action='booking.confirm' AND details->>'command_id'=$2`, id, receipt.String()).Scan(&count); e != nil || count != 1 {
		t.Fatal("audit missing or duplicated", count, e)
	}
	changed := input
	changed.Reason = "Different reason"
	_, e := s.CommandBooking(ctx, staff, id, changed)
	assertCode(e, "idempotency_conflict")
	changed = input
	changed.ExpectedUpdatedAt = version.Add(time.Microsecond)
	_, e = s.CommandBooking(ctx, staff, id, changed)
	assertCode(e, "idempotency_conflict")
	_, e = s.CommandBooking(ctx, staff, id, command("confirm", version))
	assertCode(e, "record_changed")
	other, otherVersion := makeBooking("booked", "paid_in_full", "not_required", 24*time.Hour)
	_, e = s.CommandBooking(ctx, staff, other, input)
	assertCode(e, "idempotency_conflict")
	for _, scenario := range []struct {
		action, status, payment, agreement string
		end                                time.Duration
		want                               string
	}{
		{"confirm", "booked", "full_payment_pending", "not_required", 24 * time.Hour, ""},
		{"confirm", "booked", "paid_in_full", "pending", 24 * time.Hour, ""},
		{"confirm", "booked", "paid_in_full", "accepted", -time.Hour, ""},
		{"complete", "confirmed", "paid_in_full", "not_required", 24 * time.Hour, ""},
		{"mark_no_show", "booked", "paid_in_full", "not_required", -time.Hour, ""},
		{"complete", "confirmed", "paid_in_full", "not_required", -time.Hour, "completed"},
		{"mark_no_show", "confirmed", "paid_in_full", "not_required", -time.Hour, "no_show"},
	} {
		b, v := makeBooking(scenario.status, scenario.payment, scenario.agreement, scenario.end)
		r, err := s.CommandBooking(ctx, staff, b, command(scenario.action, v))
		if scenario.want == "" {
			assertCode(err, "action_unavailable")
		} else if err != nil || r.Status != scenario.want {
			t.Fatalf("%s: %+v %v", scenario.action, r, err)
		}
	}
	// Conflicting terminal actions on the same version cannot both commit.
	contested, contestedVersion := makeBooking("confirmed", "paid_in_full", "not_required", -time.Hour)
	competing := make(chan error, 2)
	for _, action := range []string{"complete", "mark_no_show"} {
		wg.Add(1)
		go func(action string) {
			defer wg.Done()
			_, err := s.CommandBooking(ctx, staff, contested, command(action, contestedVersion))
			competing <- err
		}(action)
	}
	wg.Wait()
	close(competing)
	wins := 0
	for err := range competing {
		if err == nil {
			wins++
		} else {
			assertCode(err, "record_changed")
		}
	}
	if wins != 1 {
		t.Fatal("conflicting actions both committed", wins)
	}
	for _, action := range []string{"decline", "cancel", "reschedule", "refund", "unknown"} {
		_, e = s.CommandBooking(ctx, staff, other, command(action, otherVersion))
		assertCode(e, "invalid_action")
	}
	invalid := command("confirm", otherVersion)
	invalid.Reason = "   "
	_, e = s.CommandBooking(ctx, staff, other, invalid)
	assertCode(e, "invalid_reason")
	invalid.Reason = strings.Repeat("é", 1001)
	_, e = s.CommandBooking(ctx, staff, other, invalid)
	assertCode(e, "invalid_reason")
	invalid = command("confirm", time.Time{})
	_, e = s.CommandBooking(ctx, staff, other, invalid)
	assertCode(e, "invalid_command")
	_, e = s.CommandBooking(ctx, staff, uuid.New(), command("confirm", version))
	assertCode(e, "not_found")
	// Restriction must not prevent existing booking obligations from being handled.
	run(`UPDATE client_profiles SET platform_restricted=true WHERE client_id=$1`, business)
	if _, e = s.CommandBooking(ctx, staff, other, command("confirm", otherVersion)); e != nil {
		t.Fatal("existing booking blocked by platform restriction", e)
	}
	// No admin command above can produce a refund, regardless of input shape.
	if e = s.db.QueryRow(ctx, `SELECT count(*) FROM booking_refund_requests WHERE booking_id IN (SELECT id FROM bookings WHERE client_id=$1)`, business).Scan(&count); e != nil || count != 0 {
		t.Fatal("staff created refund", count, e)
	}
	rollbackID, rollbackVersion := makeBooking("booked", "paid_in_full", "not_required", 24*time.Hour)
	run(`ALTER TABLE admin_audit_events ADD CONSTRAINT admin_command_test_audit_failure CHECK(action<>'booking.confirm') NOT VALID`)
	t.Cleanup(func() {
		run(`ALTER TABLE admin_audit_events DROP CONSTRAINT IF EXISTS admin_command_test_audit_failure`)
	})
	_, e = s.CommandBooking(ctx, staff, rollbackID, command("confirm", rollbackVersion))
	if e == nil {
		t.Fatal("ignored audit failure")
	}
	run(`ALTER TABLE admin_audit_events DROP CONSTRAINT admin_command_test_audit_failure`)
	var status string
	if e = s.db.QueryRow(ctx, `SELECT status FROM bookings WHERE id=$1`, rollbackID).Scan(&status); e != nil || status != "booked" {
		t.Fatal("mutation survived audit failure", status, e)
	}
	if e = s.db.QueryRow(ctx, `SELECT count(*) FROM booking_change_commands WHERE booking_id=$1`, rollbackID).Scan(&count); e != nil || count != 0 {
		t.Fatal("receipt survived audit failure", count, e)
	}
	// Provider callers retain their existing actor, replay and refund behavior.
	providerBooking, _ := makeBooking("booked", "paid_in_full", "not_required", 24*time.Hour)
	if _, e = s.bookings.ApplyProviderBookingCommand(ctx, uuid.New(), providerBooking, "confirm", "Provider review", uuid.New()); !errors.Is(e, appdata.ErrNotFound) {
		t.Fatal("provider cross-business scope", e)
	}
	providerKey := uuid.New()
	for range 2 {
		r, err := s.bookings.ApplyProviderBookingCommand(ctx, business, providerBooking, "confirm", "Provider review", providerKey)
		if err != nil || r.Status != "confirmed" {
			t.Fatal("provider confirm regression", r, err)
		}
	}
	refundBooking, _ := makeBooking("booked", "paid_in_full", "not_required", 24*time.Hour)
	paymentID := uuid.New()
	run(`INSERT INTO payments(id,public_token,booking_id,client_id,customer_id,purpose,provider,method,country_code,currency_code,amount_minor,price_snapshot,reference,idempotency_key,request_fingerprint,status,paid_at) VALUES($1,$2,$3,$4,$5,'full','paystack','card','NG','NGN',2500,'{}',$2,$2,$6,'paid',now())`, paymentID, paymentID.String(), refundBooking, business, contact, strings.Repeat("a", 64))
	providerKey = uuid.New()
	for range 2 {
		r, err := s.bookings.ApplyProviderBookingCommand(ctx, business, refundBooking, "decline", "Provider decline", providerKey)
		if err != nil || r.RefundAmountMinor != 2500 || r.RefundStatus != "queued" {
			t.Fatal("provider refund regression", r, err)
		}
	}
	if e = s.db.QueryRow(ctx, `SELECT count(*) FROM booking_refund_requests WHERE booking_id=$1`, refundBooking).Scan(&count); e != nil || count != 1 {
		t.Fatal("provider duplicate refund", count, e)
	}
	// HTTP and transaction checks agree for every fixed role.
	h := s.Handler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, role := range []string{"operations", "support", "finance", "analyst"} {
		run(`UPDATE admin_staff SET role=$2 WHERE id=$1`, staff.Staff.ID, role)
		b, v := makeBooking("booked", "paid_in_full", "not_required", 24*time.Hour)
		body, _ := json.Marshal(command("confirm", v))
		req := httptest.NewRequest("POST", "/bookings/"+b.String()+"/commands", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+staff.Token)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		want := 403
		if role == "operations" {
			want = 200
		}
		if res.Code != want {
			t.Fatalf("%s command: %d %s", role, res.Code, res.Body.String())
		}
		req = httptest.NewRequest("GET", "/bookings/"+b.String(), nil)
		req.Header.Set("Authorization", "Bearer "+staff.Token)
		res = httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if role == "support" || role == "finance" {
			var detail BookingDetail
			if e = json.Unmarshal(res.Body.Bytes(), &detail); e != nil || len(detail.AllowedActions) != 0 {
				t.Fatal("read-only role got actions", role, e)
			}
		}
	}
	_, e = s.CommandBooking(ctx, staff, rollbackID, command("confirm", rollbackVersion))
	if !errors.Is(e, forbidden) {
		t.Fatal("cached role bypassed authorization", e)
	}
	run(`UPDATE admin_staff SET role='super_admin' WHERE id=$1`, staff.Staff.ID)
	run(`DELETE FROM admin_sessions WHERE id=$1`, staff.ID)
	_, e = s.CommandBooking(ctx, staff, id, input)
	if !errors.Is(e, unauthorized) {
		t.Fatal("revoked session replayed command", e)
	}
}
