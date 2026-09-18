package admin

import (
	"booking/go-server/internal/mailer"
	"context"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeMailer struct{ messages []mailer.Message }

func (f *fakeMailer) Enabled() bool { return true }
func (f *fakeMailer) Send(_ context.Context, m mailer.Message) error {
	f.messages = append(f.messages, m)
	return nil
}
func integrationService(t *testing.T) (*Service, *fakeMailer) {
	t.Helper()
	dsn := os.Getenv("ADMIN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ADMIN_TEST_DATABASE_URL required for PostgreSQL integration tests")
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(cfg.ConnConfig.Database, "tellbook_admin_test_") {
		t.Fatal("refusing to reset a non-admin test database")
	}
	p, e := pgxpool.NewWithConfig(context.Background(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p.Close)
	_, e = p.Exec(context.Background(), `TRUNCATE admin_financial_requests,admin_support_changes,admin_support_cases,admin_business_decisions,admin_business_notes,admin_audit_events,admin_auth_limits,admin_password_resets,admin_invitations,admin_recovery_codes,admin_sessions,admin_staff`)
	if e != nil {
		t.Fatal(e)
	}
	m := &fakeMailer{}
	s, e := New(p, Config{PublicURL: "http://localhost:5486", EncryptionKeys: `{"v1":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`, ActiveKey: "v1", BcryptCost: 4}, m)
	if e != nil {
		t.Fatal(e)
	}
	return s, m
}
func fullSession(t *testing.T, s *Service) (Session, string, []string) {
	t.Helper()
	ctx := context.Background()
	link, e := s.Bootstrap(ctx, "owner@example.test", "Admin Test")
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(link)
	p, e := s.Accept(ctx, u.Query().Get("token"), "test-password-123", "integration test")
	if e != nil {
		t.Fatal(e)
	}
	en, e := s.Enrollment(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	full, codes, e := s.MFA(ctx, p, totp(en["secret"], time.Now().Unix()/30), "integration test")
	if e != nil {
		t.Fatal(e)
	}
	return full, en["secret"], codes
}
func TestAdminWorkflowPersistenceAndIsolation(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	full, _, codes := fullSession(t, s)
	if len(codes) != 10 {
		t.Fatal("missing recovery codes")
	}
	if _, e := s.Bootstrap(ctx, "second@example.test", "Second"); e == nil {
		t.Fatal("second bootstrap accepted")
	}
	handler := s.Handler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, token := range []string{"", "provider.jwt.token"} {
		r := httptest.NewRequest("GET", "/businesses", nil)
		r.Header.Set("Cookie", "provider_session=whatever")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("wrong realm: %d", w.Code)
		}
	}
	partial, e := s.Login(ctx, "owner@example.test", "test-password-123", "test", "test")
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("GET", "/businesses", nil)
	r.Header.Set("Authorization", "Bearer "+partial.Token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("password-only session entered app")
	}
	// A recovery code is consumed once, and successful MFA replaces its challenge.
	recovered, _, e := s.MFA(ctx, partial, codes[0], "test")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Session(ctx, partial.Token); e == nil {
		t.Fatal("challenge still active")
	}
	partial, e = s.Login(ctx, "owner@example.test", "test-password-123", "test", "test")
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.MFA(ctx, partial, codes[0], "test"); e == nil {
		t.Fatal("recovery replay accepted")
	}
	id := uuid.New()
	_, e = s.db.Exec(ctx, `INSERT INTO clients(id,full_name,email) VALUES($1,'Integration Business Owner',$2)`, id, id.String()+"@example.test")
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.db.Exec(ctx, `INSERT INTO client_profile_handles(client_id,handle_slug) VALUES($1,$2)`, id, "admin-"+id.String())
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.db.Exec(ctx, `INSERT INTO client_profiles(client_id,business_name,handle_slug,category) VALUES($1,'Admin Integration Studio',$2,'Wellness')`, id, "admin-"+id.String())
	if e != nil {
		t.Fatal(e)
	}
	key := uuid.New()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.AddNote(ctx, full, id, key, "Investigated account readiness.")
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
	notes, e := s.Notes(ctx, id, nil)
	if e != nil || len(notes.Items) != 1 {
		t.Fatalf("notes: %+v %v", notes, e)
	}
	events, e := s.Audit(ctx, &id, nil)
	if e != nil || len(events.Items) != 1 || events.Items[0].Actor != "Admin Test" {
		t.Fatalf("audit: %+v %v", events, e)
	}
	if _, e = s.AddNote(ctx, full, id, key, "Different body"); e == nil {
		t.Fatal("idempotency conflict accepted")
	}
	if e = s.RevokeSession(ctx, recovered, full.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.AddNote(ctx, full, id, uuid.New(), "Must fail after revocation"); e == nil {
		t.Fatal("revoked session mutated data")
	}
	_, e = s.db.Exec(ctx, `UPDATE admin_staff SET role='analyst',revision=revision+1 WHERE id=$1`, full.Staff.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Session(ctx, recovered.Token); e == nil {
		t.Fatal("permission revision did not invalidate session")
	}
	// Reissue a real analyst session to test that private fields never leave the API.
	tx, e := s.db.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	p, e := scanStaff(tx.QueryRow(ctx, `SELECT `+staffColumns+` FROM admin_staff WHERE id=$1`, full.Staff.ID))
	if e != nil {
		t.Fatal(e)
	}
	analyst, e := s.newSession(ctx, tx, p, "full", "test")
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	r = httptest.NewRequest("GET", "/businesses", nil)
	r.Header.Set("Authorization", "Bearer "+analyst.Token)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 || strings.Contains(w.Body.String(), "owner_email") {
		t.Fatalf("analyst leaked private records: %s", w.Body.String())
	}
}
func TestResetPreservesMFAAndStaffAccessGuards(t *testing.T) {
	s, m := integrationService(t)
	ctx := context.Background()
	full, _, _ := fullSession(t, s)
	if e := s.RequestReset(ctx, full.Staff.Email, "test"); e != nil {
		t.Fatal(e)
	}
	parts := strings.Split(m.messages[0].Text, "\n")
	u, e := url.Parse(parts[2])
	if e != nil {
		t.Fatal(e)
	}
	token := u.Query().Get("token")
	if e = s.ResetPassword(ctx, token, "new-test-password-123"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Session(ctx, full.Token); e == nil {
		t.Fatal("password reset retained session")
	}
	p, e := s.Login(ctx, full.Staff.Email, "new-test-password-123", "test", "test")
	if e != nil {
		t.Fatal(e)
	}
	if p.Stage != "login" {
		t.Fatal("reset bypassed existing MFA")
	}
	if e = s.ResetPassword(ctx, token, "another-test-password"); e == nil {
		t.Fatal("reset token reused")
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "password_hash") || strings.Contains(string(raw), "mfa_cipher") {
		t.Fatal("secret leaked in session response")
	}
}

// Opt-in local browser fixture: real handler and isolated persistence, no mail or
// production integrations. This test never runs in normal CI.
func TestBrowserFixture(t *testing.T) {
	if os.Getenv("ADMIN_BROWSER_TEST") != "1" {
		t.Skip("manual browser fixture")
	}
	s, _ := integrationService(t)
	if os.Getenv("ADMIN_BROWSER_FINANCE") == "1" {
		seedFinanceTransfers(t, s, 3)
	}
	credentials := map[string]string{"password": "test-password-123"}
	if os.Getenv("ADMIN_BROWSER_ENROLLMENT") == "1" {
		link, e := s.Bootstrap(context.Background(), "first@example.test", "First Admin")
		if e != nil {
			t.Fatal(e)
		}
		credentials["invitation"] = link
		credentials["email"] = "first@example.test"
	} else {
		full, secret, _ := fullSession(t, s)
		// Allow immediate authenticator sign-in after fixture enrollment.
		if _, e := s.db.Exec(context.Background(), `UPDATE admin_staff SET last_totp_step=-1 WHERE id=$1`, full.Staff.ID); e != nil {
			t.Fatal(e)
		}
		credentials["email"], credentials["secret"] = full.Staff.Email, secret
		if os.Getenv("ADMIN_BROWSER_RELEASE") == "1" {
			seedBrowserRelease(t, s, full, credentials)
		}
		if os.Getenv("ADMIN_BROWSER_APPROVALS") == "1" {
			input := seedFinancialReview(t, s)
			var paymentID uuid.UUID
			if e := s.db.QueryRow(context.Background(), `SELECT payment_id FROM payment_allocations WHERE id=$1`, input.AllocationID).Scan(&paymentID); e != nil {
				t.Fatal(e)
			}
			credentials["approval_payment_id"] = paymentID.String()
			credentials["approval_destination_id"] = input.DestinationID.String()
			reviewer, secret := enrolledReleaseStaff(t, s, full, "reviewer@example.test", "finance")
			if _, e := s.db.Exec(context.Background(), `UPDATE admin_staff SET last_totp_step=-1 WHERE id=$1`, reviewer.Staff.ID); e != nil {
				t.Fatal(e)
			}
			credentials["reviewer_email"], credentials["reviewer_secret"], credentials["reviewer_password"] = reviewer.Staff.Email, secret, "staff-test-password-123"
		}
		if os.Getenv("ADMIN_BROWSER_TEAM") == "1" {
			colleague, colleagueSecret := enrolledReleaseStaff(t, s, full, "support@example.test", "support")
			if _, e := s.Invite(context.Background(), full, "invited@example.test", "Invited QA", "finance"); e != nil {
				t.Fatal(e)
			}
			if _, e := s.db.Exec(context.Background(), `UPDATE admin_staff SET last_totp_step=-1 WHERE id=$1`, colleague.Staff.ID); e != nil {
				t.Fatal(e)
			}
			credentials["colleague_email"], credentials["colleague_secret"], credentials["colleague_password"] = colleague.Staff.Email, colleagueSecret, "staff-test-password-123"
		}

	}
	raw, e := json.Marshal(credentials)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile("/tmp/tellbook-admin-browser-fixture.json", raw, 0600); e != nil {
		t.Fatal(e)
	}
	t.Log("Browser fixture: http://127.0.0.1:5487/v1/admin; credentials written to /tmp/tellbook-admin-browser-fixture.json")
	r := chi.NewRouter()
	r.Use(chimiddleware.RequestID)
	var handler http.Handler = s.Handler(slog.Default())
	if os.Getenv("ADMIN_BROWSER_INTERRUPT") == "1" {
		handler = interruptBrowserResponses(handler)
	}
	r.Mount("/v1/admin", handler)
	srv := &http.Server{Addr: "127.0.0.1:5487", Handler: r, ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { srv.Close() })
	if e = srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		t.Fatal(e)
	}
}

func TestInvitationRevocationMFAAttemptLimitAndStaffRecovery(t *testing.T) {
	s, m := integrationService(t)
	ctx := context.Background()
	owner, _, _ := fullSession(t, s)
	state, e := s.Invite(ctx, owner, "ops@example.test", "Operations Test", "operations")
	if e != nil || state != "sent" {
		t.Fatalf("invite: %s %v", state, e)
	}
	link := strings.Split(m.messages[0].Text, "\n")[2]
	u, _ := url.Parse(link)
	token := u.Query().Get("token")
	p, e := s.Invitation(ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ChangeStaff(ctx, owner, p.ID, StaffChange{Action: "revoke_invitation", Revision: p.Revision, Reason: "Invitation sent in error"}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Accept(ctx, token, "ops-test-password", "test"); e == nil {
		t.Fatal("revoked invitation accepted")
	}
	if _, e = s.ChangeStaff(ctx, owner, p.ID, StaffChange{Action: "resend_invitation", Revision: p.Revision, Reason: "Address checked"}); e != nil {
		t.Fatal(e)
	}
	u, _ = url.Parse(strings.Split(m.messages[1].Text, "\n")[2])
	enrolling, e := s.Accept(ctx, u.Query().Get("token"), "ops-test-password", "test")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AddNote(ctx, enrolling, uuid.New(), uuid.New(), "Cannot enter during enrollment"); e == nil {
		t.Fatal("enrolling staff mutated data")
	}
	for range 5 {
		if _, _, e = s.MFA(ctx, enrolling, "invalid", "test"); e == nil {
			t.Fatal("invalid MFA accepted")
		}
	}
	if _, e = s.Session(ctx, enrolling.Token); e == nil {
		t.Fatal("challenge survived attempt limit")
	}
	enrolling, e = s.Login(ctx, "ops@example.test", "ops-test-password", "test", "test")
	if e != nil {
		t.Fatal(e)
	}
	setup, e := s.Enrollment(ctx, enrolling)
	if e != nil {
		t.Fatal(e)
	}
	ops, _, e := s.MFA(ctx, enrolling, totp(setup["secret"], time.Now().Unix()/30), "test")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ChangeStaff(ctx, ops, owner.Staff.ID, StaffChange{Action: "suspend", Revision: owner.Staff.Revision, Reason: "Forbidden"}); e == nil {
		t.Fatal("operations changed staff access")
	}
	if _, e = s.ChangeStaff(ctx, owner, owner.Staff.ID, StaffChange{Action: "role", Role: "operations", Revision: owner.Staff.Revision, Reason: "Self demotion"}); e == nil {
		t.Fatal("self access change accepted")
	}
	if _, e = s.ChangeStaff(ctx, owner, ops.Staff.ID, StaffChange{Action: "recover_mfa", Revision: ops.Staff.Revision, Reason: "Staff identity verified in person"}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Session(ctx, ops.Token); e == nil {
		t.Fatal("MFA recovery retained old sessions")
	}
	recovered, e := s.Login(ctx, "ops@example.test", "ops-test-password", "test", "test")
	if e != nil || recovered.Stage != "enroll" {
		t.Fatalf("fresh enrollment missing: %+v %v", recovered, e)
	}
	next, e := s.Enrollment(ctx, recovered)
	if e != nil || next["secret"] == setup["secret"] {
		t.Fatalf("MFA secret was not replaced: %v", e)
	}
	if _, e = s.ChangeStaff(ctx, owner, ops.Staff.ID, StaffChange{Action: "suspend", Revision: ops.Staff.Revision, Reason: "Stale decision"}); e == nil {
		t.Fatal("stale staff revision accepted")
	}
}
