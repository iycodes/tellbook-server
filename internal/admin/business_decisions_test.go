package admin

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"sync"
	"testing"
)

func TestBusinessDecisionsPermissionsConcurrencyAndHistory(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	full, _, _ := fullSession(t, s)
	id := uuid.New()
	slug := "decision-" + id.String()
	for _, q := range []string{`INSERT INTO clients(id,full_name) VALUES($1,'Decision test')`, `INSERT INTO client_profile_handles(client_id,handle_slug) VALUES($1,$2)`, `INSERT INTO client_profiles(client_id,handle_slug,business_name,marketplace_enabled) VALUES($1,$2,'Decision business',false)`} {
		args := []any{id}
		if q != `INSERT INTO clients(id,full_name) VALUES($1,'Decision test')` {
			args = append(args, slug)
		}
		if _, e := s.db.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	b, e := s.BusinessDetail(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	if b.Readiness.Ready || len(b.Readiness.BlockingReasons) == 0 {
		t.Fatal("missing canonical readiness")
	}
	input := BusinessDecision{Action: "verify", Reason: "Registration reviewed", Evidence: "Case TEST-1", RequestKey: uuid.New(), ExpectedUpdatedAt: b.UpdatedAt}
	missing := input
	missing.Evidence = ""
	if e = s.DecideBusiness(ctx, full, id, missing); e == nil {
		t.Fatal("verification without evidence")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.DecideBusiness(ctx, full, id, input) }()
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	var count int
	if e = s.db.QueryRow(ctx, `SELECT count(*) FROM admin_business_decisions WHERE business_id=$1`, id).Scan(&count); e != nil || count != 1 {
		t.Fatalf("duplicate decisions: %d %v", count, e)
	}
	b, e = s.BusinessDetail(ctx, id)
	if e != nil || !b.Verified {
		t.Fatal("verification not persisted", e)
	}
	changed := input
	changed.Reason = "different"
	if e = s.DecideBusiness(ctx, full, id, changed); e == nil {
		t.Fatal("request key reused with different content")
	}
	stale := input
	stale.RequestKey = uuid.New()
	stale.Action = "reject"
	if e = s.DecideBusiness(ctx, full, id, stale); !errors.Is(e, conflict) {
		t.Fatalf("stale decision: %v", e)
	}
	for _, action := range []string{"restrict", "restore", "reject"} {
		b, e = s.BusinessDetail(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		input = BusinessDecision{Action: action, Reason: "Policy review", Evidence: "Case TEST-2", RequestKey: uuid.New(), ExpectedUpdatedAt: b.UpdatedAt}
		if e = s.DecideBusiness(ctx, full, id, input); e != nil {
			t.Fatal(e)
		}
		b, e = s.BusinessDetail(ctx, id)
		if e != nil || b.MarketplaceEnabled || b.PlatformRestricted != (action == "restrict") {
			t.Fatalf("decision altered listing preference: %+v %v", b, e)
		}
	}
	history, e := s.Audit(ctx, &id, nil)
	if e != nil || len(history.Items) != 4 {
		t.Fatalf("decision history %+v %v", history, e)
	}
	for _, v := range history.Items {
		if v.Actor != full.Staff.Name || v.Reason == "" || len(v.Details) == 0 {
			t.Fatal("incomplete decision audit")
		}
	}
	for _, role := range []string{"support", "finance", "analyst"} {
		if _, e = s.db.Exec(ctx, `UPDATE admin_staff SET role=$2 WHERE id=$1`, full.Staff.ID, role); e != nil {
			t.Fatal(e)
		}
		p, e := s.Session(ctx, full.Token)
		if e != nil {
			t.Fatal(e)
		}
		input.RequestKey = uuid.New()
		input.ExpectedUpdatedAt = b.UpdatedAt
		if e = s.DecideBusiness(ctx, p, id, input); !errors.Is(e, forbidden) {
			t.Fatalf("%s decision not denied: %v", role, e)
		}
	}
	if _, e = s.db.Exec(ctx, `UPDATE admin_staff SET role='operations' WHERE id=$1`, full.Staff.ID); e != nil {
		t.Fatal(e)
	}
	ops, e := s.Session(ctx, full.Token)
	if e != nil {
		t.Fatal(e)
	}
	input.Action = "verify"
	input.RequestKey = uuid.New()
	if e = s.DecideBusiness(ctx, ops, id, input); e != nil {
		t.Fatal("operations decision", e)
	}
}
