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

	"github.com/google/uuid"
)

func TestRecordNotesPersistenceIsolationAndAuthorization(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	full, _, _ := fullSession(t, s)
	business, booking, identity := uuid.New(), uuid.New(), uuid.New()
	run := func(q string, args ...any) {
		t.Helper()
		if _, e := s.db.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	t.Cleanup(func() {
		run(`DELETE FROM admin_business_notes WHERE business_id=$1 OR booking_id=$2 OR contact_id=$3 OR account_id=$3`, business, booking, identity)
		run(`DELETE FROM bookings WHERE id=$1`, booking)
		run(`DELETE FROM customers WHERE id=$1`, identity)
		run(`DELETE FROM marketplace_customers WHERE id=$1`, identity)
		run(`DELETE FROM client_profiles WHERE client_id=$1`, business)
		run(`DELETE FROM client_profile_handles WHERE client_id=$1`, business)
		run(`DELETE FROM clients WHERE id=$1`, business)
	})
	run(`INSERT INTO clients(id,full_name) VALUES($1,'Note business')`, business)
	run(`INSERT INTO client_profile_handles(client_id,handle_slug) VALUES($1,$2)`, business, "notes-"+business.String())
	run(`INSERT INTO client_profiles(client_id,handle_slug,business_name) VALUES($1,$2,'Note business')`, business, "notes-"+business.String())
	run(`INSERT INTO customers(id,client_id,full_name,private_notes) VALUES($1,$2,'Contact','Provider private note')`, identity, business)
	// Same UUID in different identity tables must still be separate note targets.
	run(`INSERT INTO marketplace_customers(id,full_name,email) VALUES($1,'Account',$2)`, identity, identity.String()+"@example.test")
	run(`INSERT INTO bookings(id,client_id,customer_id,title,start_at,end_at,occupied_start_at,occupied_end_at,currency_code,country_code,notes) VALUES($1,$2,$3,'Note booking',now(),now()+interval '1 hour',now(),now()+interval '1 hour','NGN','NG','Customer booking text')`, booking, business, identity)
	targets := []struct {
		kind, path string
		id         uuid.UUID
	}{
		{"business", "/businesses/" + business.String() + "/notes", business},
		{"booking", "/bookings/" + booking.String() + "/notes", booking},
		{"customer_contact", "/customers/contacts/" + identity.String() + "/notes", identity},
		{"marketplace_account", "/customers/accounts/" + identity.String() + "/notes", identity},
	}
	notes := make([]Note, len(targets))
	keys := make([]uuid.UUID, len(targets))
	for i, target := range targets {
		keys[i] = uuid.New()
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, e := s.AddRecordNote(ctx, full, target.kind, target.id, keys[i], "  Investigated "+target.kind+".  ")
				errs <- e
			}()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		page, e := s.RecordNotes(ctx, target.kind, target.id, nil)
		if e != nil || len(page.Items) != 1 {
			t.Fatalf("%s notes: %+v %v", target.kind, page, e)
		}
		notes[i] = page.Items[0]
		if notes[i].Body != "Investigated "+target.kind+"." || notes[i].Author != full.Staff.Name {
			t.Fatal("note text/actor lost")
		}
		var count int
		if e = s.db.QueryRow(ctx, `SELECT count(*) FROM admin_audit_events WHERE entity_type=$1 AND entity_id=$2 AND action=$3 AND details->>'note_id'=$4`, target.kind, target.id, target.kind+".note_added", notes[i].ID.String()).Scan(&count); e != nil || count != 1 {
			t.Fatal("duplicate/missing audit", count, e)
		}
		if _, e = s.AddRecordNote(ctx, full, target.kind, target.id, keys[i], "changed"); e == nil {
			t.Fatal("changed retry accepted")
		}
		if _, e = s.RecordNotes(ctx, target.kind, uuid.New(), nil); e == nil {
			t.Fatal("missing target returned notes")
		}
		if _, e = s.AddRecordNote(ctx, full, target.kind, uuid.New(), uuid.New(), "missing"); e == nil {
			t.Fatal("missing target accepted")
		}
	}
	if _, e := s.AddRecordNote(ctx, full, "marketplace_account", identity, keys[2], notes[2].Body); e == nil {
		t.Fatal("contact request key reused on account with identical UUID")
	}
	if _, e := s.RecordNotes(ctx, "marketplace_account", identity, &notes[2].ID); e == nil {
		t.Fatal("cursor crossed identity namespace")
	}
	for i := range 26 {
		if _, e := s.AddRecordNote(ctx, full, "booking", booking, uuid.New(), "History "+strings.Repeat("x", i)); e != nil {
			t.Fatal(e)
		}
	}
	page, e := s.RecordNotes(ctx, "booking", booking, nil)
	if e != nil || len(page.Items) != 25 || page.NextCursor == "" {
		t.Fatal("page bound", e)
	}
	cursor := uuid.MustParse(page.NextCursor)
	tail, e := s.RecordNotes(ctx, "booking", booking, &cursor)
	if e != nil || len(tail.Items) != 2 || tail.NextCursor != "" {
		t.Fatal("tail page", tail, e)
	}
	seen := map[uuid.UUID]bool{}
	for _, n := range append(page.Items, tail.Items...) {
		if seen[n.ID] {
			t.Fatal("duplicate page note")
		}
		seen[n.ID] = true
	}
	var original string
	if e = s.db.QueryRow(ctx, `SELECT private_notes FROM customers WHERE id=$1`, identity).Scan(&original); e != nil || original != "Provider private note" {
		t.Fatal("provider note changed", e)
	}
	if e = s.db.QueryRow(ctx, `SELECT notes FROM bookings WHERE id=$1`, booking).Scan(&original); e != nil || original != "Customer booking text" {
		t.Fatal("booking text changed", e)
	}
	for _, body := range []string{"   ", strings.Repeat("é", 4001)} {
		if _, e = s.AddRecordNote(ctx, full, "booking", booking, uuid.New(), body); e == nil {
			t.Fatal("invalid body accepted")
		}
	}
	if _, e = s.AddRecordNote(ctx, full, "booking", booking, uuid.Nil, "note"); e == nil {
		t.Fatal("missing request key accepted")
	}
	// The database protects the one-target invariant independently of the handler.
	if _, e = s.db.Exec(ctx, `INSERT INTO admin_business_notes(id,business_id,booking_id,author_id,request_key,body) VALUES($1,$2,$3,$4,$5,'invalid')`, uuid.New(), business, booking, full.Staff.ID, uuid.New()); e == nil {
		t.Fatal("two targets allowed")
	}
	// Simulate an audit write failure: a note must never survive without its event.
	run(`ALTER TABLE admin_audit_events ADD CONSTRAINT admin_note_test_audit_failure CHECK(action <> 'booking.note_added') NOT VALID`)
	t.Cleanup(func() { run(`ALTER TABLE admin_audit_events DROP CONSTRAINT IF EXISTS admin_note_test_audit_failure`) })
	rejectedKey := uuid.New()
	if _, e = s.AddRecordNote(ctx, full, "booking", booking, rejectedKey, "Must roll back"); e == nil {
		t.Fatal("audit failure was ignored")
	}
	var rejectedCount int
	if e = s.db.QueryRow(ctx, `SELECT count(*) FROM admin_business_notes WHERE author_id=$1 AND request_key=$2`, full.Staff.ID, rejectedKey).Scan(&rejectedCount); e != nil || rejectedCount != 0 {
		t.Fatal("note survived failed audit", e)
	}
	run(`ALTER TABLE admin_audit_events DROP CONSTRAINT admin_note_test_audit_failure`)
	h := s.Handler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, role := range []string{"super_admin", "operations", "support", "finance", "analyst"} {
		run(`UPDATE admin_staff SET role=$2 WHERE id=$1`, full.Staff.ID, role)
		for _, target := range targets {
			for _, method := range []string{"GET", "POST"} {
				payload, _ := json.Marshal(map[string]any{"body": "Role investigation", "request_key": uuid.New()})
				req := httptest.NewRequest(method, target.path, strings.NewReader(string(payload)))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+full.Token)
				res := httptest.NewRecorder()
				h.ServeHTTP(res, req)
				want := 200
				if role == "analyst" {
					want = 403
				}
				if res.Code != want {
					t.Fatalf("%s %s %s: %d %s", role, method, target.kind, res.Code, res.Body.String())
				}
			}
		}
	}
	// Even a cached authorized actor must be rechecked inside the write transaction.
	if _, e = s.AddRecordNote(ctx, full, "booking", booking, uuid.New(), "stale permissions"); !errors.Is(e, forbidden) {
		t.Fatalf("stale actor: %v", e)
	}
	run(`UPDATE admin_staff SET role='super_admin' WHERE id=$1`, full.Staff.ID)
	run(`DELETE FROM admin_sessions WHERE id=$1`, full.ID)
	if _, e = s.AddRecordNote(ctx, full, "customer_contact", identity, uuid.New(), "revoked"); e == nil {
		t.Fatal("revoked session wrote a note")
	}
}
