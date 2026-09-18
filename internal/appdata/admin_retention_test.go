package appdata

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAdminRetentionBoundaries(t *testing.T) {
	dsn := os.Getenv("ADMIN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ADMIN_TEST_DATABASE_URL is required")
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(cfg.ConnConfig.Database, "tellbook_admin_test_") {
		t.Fatal("requires isolated admin test database")
	}
	ctx := context.Background()
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	// Real layouts, connection-local fixtures: no shared data with other test packages.
	for _, table := range []string{"admin_staff", "admin_sessions", "admin_auth_limits", "admin_password_resets", "admin_invitations", "admin_audit_events", "admin_business_notes"} {
		if _, e = tx.Exec(ctx, `CREATE TEMP TABLE `+table+` (LIKE public.`+table+` INCLUDING DEFAULTS INCLUDING CONSTRAINTS) ON COMMIT DROP`); e != nil {
			t.Fatal(e)
		}
	}
	staff := uuid.New()
	now := time.Now().UTC()
	liveSession := uuid.New()
	latestInvite := uuid.New()
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO admin_staff(id,email,full_name,role,status) VALUES($1,'retention@example.test','Retention Test','super_admin','invited')`, []any{staff}},
		{`INSERT INTO admin_sessions(id,staff_id,token_hash,stage,staff_revision,expires_at) VALUES($1,$2,'expired','full',1,$3::timestamptz-interval '1 day'),($4,$2,'live','full',1,$3::timestamptz+interval '1 day')`, []any{uuid.New(), staff, now, liveSession}},
		{`INSERT INTO admin_auth_limits(key_hash,attempts,window_end) VALUES('expired',1,$1::timestamptz-interval '1 day'),('live',1,$1::timestamptz+interval '1 day')`, []any{now}},
		{`INSERT INTO admin_password_resets(token_hash,staff_id,expires_at,delivery_state,delivery_updated_at) VALUES('old',$1,$2::timestamptz-interval '31 days','sent',$2::timestamptz-interval '31 days'),('keep',$1,$2::timestamptz-interval '1 day','sent',$2::timestamptz-interval '1 day'),('interrupted',$1,$2::timestamptz+interval '1 hour','sending',$2::timestamptz-interval '11 minutes')`, []any{staff, now}},
		{`INSERT INTO admin_invitations(id,staff_id,token_hash,expires_at,created_at) VALUES($1,$2,'old',$3::timestamptz-interval '92 days',$3::timestamptz-interval '95 days'),($4,$2,'latest',$3::timestamptz-interval '91 days',$3::timestamptz-interval '94 days')`, []any{uuid.New(), staff, now, latestInvite}},
		{`INSERT INTO admin_audit_events(action,entity_type,entity_id,created_at) VALUES('staff.bootstrapped','staff',$1,$2::timestamptz-interval '1 year')`, []any{staff, now}},
		{`INSERT INTO admin_business_notes(id,business_id,author_id,body,request_key,created_at) VALUES($1,$2,$2,'Keep this note',$3,$4::timestamptz-interval '1 year')`, []any{uuid.New(), staff, uuid.New(), now}},
	}
	for _, v := range statements {
		if _, e = tx.Exec(ctx, v.sql, v.args...); e != nil {
			t.Fatal(e)
		}
	}
	for _, task := range adminDataMaintenanceTasks {
		if _, e = pruneMaintenanceTask(ctx, tx, task, now, 1); e != nil {
			t.Fatal(e)
		}
	}
	for table, want := range map[string]int{"admin_staff": 1, "admin_sessions": 1, "admin_auth_limits": 1, "admin_password_resets": 2, "admin_invitations": 1, "admin_audit_events": 2, "admin_business_notes": 1} {
		var n int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); e != nil || n != want {
			t.Fatalf("%s count %d want %d: %v", table, n, want, e)
		}
	}
	var state string
	if e = tx.QueryRow(ctx, `SELECT delivery_state FROM admin_password_resets WHERE token_hash='interrupted'`).Scan(&state); e != nil || state != "unknown" {
		t.Fatal("interrupted send not classified unknown", e)
	}
	var id uuid.UUID
	if e = tx.QueryRow(ctx, `SELECT id FROM admin_invitations`).Scan(&id); e != nil || id != latestInvite {
		t.Fatal("last expired invitation lost", e)
	}
	for _, task := range adminDataMaintenanceTasks {
		if _, e = pruneMaintenanceTask(ctx, tx, task, now, 1); e != nil {
			t.Fatal(e)
		}
	}
	var audits int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM admin_audit_events`).Scan(&audits); e != nil || audits != 2 {
		t.Fatal("maintenance duplicated outcome audit", e)
	}
}
func TestAdminRetentionOptIn(t *testing.T) {
	w := NewDataMaintenanceWorker(nil, nil)
	base := len(w.tasks)
	w.WithAdminRetention(true)
	if len(w.tasks) != base+len(adminDataMaintenanceTasks) {
		t.Fatal("admin retention missing")
	}
	w.WithAdminRetention(false)
	if len(w.tasks) != base || len(dataMaintenanceTasks) != base {
		t.Fatal("admin retention changed shared defaults")
	}
}
