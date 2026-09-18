package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCustomerDirectoryScopePaginationAndPrivacy(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	full, _, _ := fullSession(t, s)
	business, other, account := uuid.New(), uuid.New(), uuid.New()
	name := "Directory " + uuid.NewString()
	email := account.String() + "@example.test"
	run := func(q string, args ...any) {
		t.Helper()
		if _, e := s.db.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	t.Cleanup(func() {
		for _, b := range []uuid.UUID{business, other} {
			for _, q := range []string{`DELETE FROM customers WHERE client_id=$1`, `DELETE FROM client_profiles WHERE client_id=$1`, `DELETE FROM client_profile_handles WHERE client_id=$1`, `DELETE FROM clients WHERE id=$1`} {
				if _, e := s.db.Exec(ctx, q, b); e != nil {
					t.Error(e)
				}
			}
		}
		if _, e := s.db.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=$1`, account); e != nil {
			t.Error(e)
		}
	})
	for _, b := range []uuid.UUID{business, other} {
		run(`INSERT INTO clients(id,full_name) VALUES($1,'Directory owner')`, b)
		run(`INSERT INTO client_profile_handles(client_id,handle_slug) VALUES($1,$2)`, b, "directory-"+b.String())
		run(`INSERT INTO client_profiles(client_id,handle_slug,business_name) VALUES($1,$2,'Directory business')`, b, "directory-"+b.String())
	}
	var special uuid.UUID
	for i := range 27 {
		id, b, phone := uuid.New(), business, "+2348000000000"
		if i == 26 {
			b = other
		}
		if i == 0 {
			special, phone = id, `qa%_\phone`
		}
		run(`INSERT INTO customers(id,client_id,full_name,email,phone,private_notes) VALUES($1,$2,$3,$4,$5,'PRIVATE_NOTE_SECRET')`, id, b, name, email, phone)
	}
	run(`INSERT INTO marketplace_customers(id,full_name,email,password_hash) VALUES($1,$2,$3,'PASSWORD_SECRET')`, account, name, email)
	f := CustomerFilter{Kind: "contacts", Q: name, BusinessID: &business}
	first, e := s.Customers(ctx, f)
	if e != nil || len(first.Items) != 25 || first.NextCursor == "" {
		t.Fatalf("first page: %+v %v", first, e)
	}
	f.Cursor = first.NextCursor
	second, e := s.Customers(ctx, f)
	if e != nil || len(second.Items) != 1 || second.NextCursor != "" {
		t.Fatalf("second page: %+v %v", second, e)
	}
	seen := map[uuid.UUID]bool{}
	for _, c := range append(first.Items, second.Items...) {
		if seen[c.ID] || c.BusinessID == nil || *c.BusinessID != business || c.Kind != "contacts" {
			t.Fatal("duplicate or wrong-scope contact", c)
		}
		seen[c.ID] = true
	}
	for _, changed := range []CustomerFilter{
		{Kind: "accounts", Q: name, Cursor: first.NextCursor},
		{Kind: "contacts", Q: name, BusinessID: &other, Cursor: first.NextCursor},
		{Kind: "contacts", Q: "different", BusinessID: &business, Cursor: first.NextCursor},
	} {
		if _, err := s.Customers(ctx, changed); err == nil {
			t.Fatal("cursor reused with changed filters", changed)
		}
	}
	for _, q := range []string{`%_\`, special.String()} {
		page, err := s.Customers(ctx, CustomerFilter{Kind: "contacts", Q: q, BusinessID: &business})
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != special {
			t.Fatalf("literal/ID search %q: %+v %v", q, page, err)
		}
	}
	accounts, e := s.Customers(ctx, CustomerFilter{Kind: "accounts", Q: strings.ToUpper(email)})
	if e != nil || len(accounts.Items) != 1 || accounts.Items[0].ID != account || accounts.Items[0].BusinessID != nil {
		t.Fatal("account identity merged or not found", accounts, e)
	}
	contactPage, e := s.Customers(ctx, CustomerFilter{Kind: "contacts", Q: email, BusinessID: &other})
	if e != nil || len(contactPage.Items) != 1 {
		t.Fatal("separate business contact missing", contactPage, e)
	}
	detail, e := s.CustomerRecord(ctx, "accounts", account)
	if e != nil || detail != accounts.Items[0] {
		t.Fatal("list/detail projections diverged", e)
	}
	for _, f := range []CustomerFilter{{Kind: "accounts", BusinessID: &business}, {Kind: "contacts", Q: strings.Repeat("é", 101)}, {Kind: "contacts", Cursor: "broken"}, {Kind: "contacts", Cursor: strings.Repeat("x", 501)}, {Kind: "unknown"}} {
		if _, err := s.Customers(ctx, f); err == nil {
			t.Fatal("invalid query accepted", f)
		}
	}
	for _, page := range []CustomerPage{first, accounts} {
		raw, _ := json.Marshal(page)
		if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "password") || strings.Contains(string(raw), "private_notes") {
			t.Fatal("private fields leaked", string(raw))
		}
	}
	h := s.Handler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, role := range []string{"super_admin", "operations", "support", "finance", "analyst"} {
		run(`UPDATE admin_staff SET role=$2 WHERE id=$1`, full.Staff.ID, role)
		for _, kind := range []string{"contacts", "accounts"} {
			req := httptest.NewRequest("GET", "/customers/"+kind+"?q="+url.QueryEscape(name), nil)
			req.Header.Set("Authorization", "Bearer "+full.Token)
			res := httptest.NewRecorder()
			h.ServeHTTP(res, req)
			want := 200
			if role == "analyst" {
				want = 403
			}
			if res.Code != want {
				t.Fatalf("%s / %s: %d %s", role, kind, res.Code, res.Body.String())
			}
		}
	}
	for _, token := range []string{"", "provider-token-not-a-staff-session"} {
		req := httptest.NewRequest("GET", "/customers/contacts", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != 401 {
			t.Fatalf("non-staff access: %d", res.Code)
		}
	}
}
