package admin

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func enrolledReleaseStaff(t *testing.T, s *Service, owner Session, email, role string) (Session, string) {
	t.Helper()
	ctx := context.Background()
	if _, e := s.Invite(ctx, owner, email, "Support QA", role); e != nil {
		t.Fatal(e)
	}
	m := s.mailer.(*fakeMailer)
	u, e := url.Parse(strings.Split(m.messages[len(m.messages)-1].Text, "\n")[2])
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.Accept(ctx, u.Query().Get("token"), "staff-test-password-123", "release QA")
	if e != nil {
		t.Fatal(e)
	}
	en, e := s.Enrollment(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	full, _, e := s.MFA(ctx, p, totp(en["secret"], time.Now().Unix()/30), "release QA")
	if e != nil {
		t.Fatal(e)
	}
	return full, en["secret"]
}
func TestConcurrentSuperAdminChangesKeepAnActiveOwner(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	owner, _, _ := fullSession(t, s)
	other, _ := enrolledReleaseStaff(t, s, owner, "second@example.test", "super_admin")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, e := s.ChangeStaff(ctx, owner, other.Staff.ID, StaffChange{Action: "suspend", Revision: other.Staff.Revision, Reason: "Concurrent access review"})
		errs <- e
	}()
	go func() {
		defer wg.Done()
		_, e := s.ChangeStaff(ctx, other, owner.Staff.ID, StaffChange{Action: "suspend", Revision: owner.Staff.Revision, Reason: "Concurrent access review"})
		errs <- e
	}()
	wg.Wait()
	close(errs)
	wins := 0
	for e := range errs {
		if e == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatal("competing decisions must have one winner", wins)
	}
	var n int
	if e := s.db.QueryRow(ctx, `SELECT count(*) FROM admin_staff WHERE role='super_admin' AND status='active'`).Scan(&n); e != nil || n != 1 {
		t.Fatal("lost last active owner", n, e)
	}
	if e := s.db.QueryRow(ctx, `SELECT count(*) FROM admin_audit_events WHERE action='staff.suspend'`).Scan(&n); e != nil || n != 1 {
		t.Fatal("suspension audit", n, e)
	}
}
func TestStaffSuspensionRestoreRequiresFreshEnrollment(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	owner, _, _ := fullSession(t, s)
	other, oldSecret := enrolledReleaseStaff(t, s, owner, "support@example.test", "support")
	if _, e := s.ChangeStaff(ctx, owner, other.Staff.ID, StaffChange{Action: "suspend", Revision: other.Staff.Revision, Reason: "Access review"}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Session(ctx, other.Token); e == nil {
		t.Fatal("suspended session survived")
	}
	if _, e := s.Login(ctx, other.Staff.Email, "staff-test-password-123", "test", "test"); e == nil {
		t.Fatal("suspended login accepted")
	}
	members, e := s.StaffList(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var revision int64
	for _, m := range members {
		if m.ID == other.Staff.ID {
			revision = m.Revision
		}
	}
	if _, e = s.ChangeStaff(ctx, owner, other.Staff.ID, StaffChange{Action: "restore", Revision: revision, Reason: "Review completed"}); e != nil {
		t.Fatal(e)
	}
	p, e := s.Login(ctx, other.Staff.Email, "staff-test-password-123", "test", "test")
	if e != nil || p.Stage != "enroll" {
		t.Fatal("restore bypassed enrollment", p.Stage, e)
	}
	en, e := s.Enrollment(ctx, p)
	if e != nil || en["secret"] == oldSecret {
		t.Fatal("old authenticator retained", e)
	}
	var n int
	if e = s.db.QueryRow(ctx, `SELECT count(*) FROM admin_recovery_codes WHERE staff_id=$1`, other.Staff.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal("recovery codes survived", n, e)
	}
	if _, _, e = s.MFA(ctx, p, totp(en["secret"], time.Now().Unix()/30), "test"); e != nil {
		t.Fatal(e)
	}
}
