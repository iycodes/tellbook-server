package admin

import (
	"booking/go-server/internal/mailer"
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestBootstrapRenewalOnlyBeforeAcceptance(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	original, e := s.Bootstrap(ctx, "first@example.test", "First Admin")
	if e != nil {
		t.Fatal(e)
	}
	old, _ := url.Parse(original)
	if _, e = s.db.Exec(ctx, `UPDATE admin_invitations SET expires_at=now()-interval '1 hour'`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RenewBootstrapInvitation(ctx, "different@example.test", "Ticket 123"); e == nil {
		t.Fatal("renewal changed bootstrap identity")
	}
	if _, e = s.RenewBootstrapInvitation(ctx, "first@example.test", ""); e == nil {
		t.Fatal("renewal omitted reason")
	}
	renewed, e := s.RenewBootstrapInvitation(ctx, "FIRST@example.test", "Ticket 123: expired before enrollment")
	if e != nil {
		t.Fatal(e)
	}
	if renewed == original {
		t.Fatal("token was not rotated")
	}
	if _, e = s.Invitation(ctx, old.Query().Get("token")); e == nil {
		t.Fatal("previous invitation remains usable")
	}
	next, _ := url.Parse(renewed)
	p, e := s.Invitation(ctx, next.Query().Get("token"))
	if e != nil || p.Email != "first@example.test" {
		t.Fatalf("renewed invitation: %+v %v", p, e)
	}
	var reason string
	if e = s.db.QueryRow(ctx, `SELECT reason FROM admin_audit_events WHERE action='staff.bootstrap_invitation_renewed'`).Scan(&reason); e != nil || !strings.Contains(reason, "Ticket 123") {
		t.Fatal("renewal not audited", e)
	}
	if _, e = s.Accept(ctx, next.Query().Get("token"), "first-test-password", "test"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RenewBootstrapInvitation(ctx, "first@example.test", "Attempt to bypass MFA"); e == nil {
		t.Fatal("renewal bypassed enrolled staff recovery")
	}
}

type outcomeMailer struct {
	enabled bool
	err     error
	cancel  context.CancelFunc
}

func (m outcomeMailer) Enabled() bool { return m.enabled }
func (m outcomeMailer) Send(context.Context, mailer.Message) error {
	if m.cancel != nil {
		m.cancel()
	}
	return m.err
}
func TestPasswordResetDeliveryOutcomesAndRevision(t *testing.T) {
	s, m := integrationService(t)
	ctx := context.Background()
	full, _, _ := fullSession(t, s)
	if e := s.RequestReset(ctx, full.Staff.Email, "test-reset"); e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(strings.Split(m.messages[0].Text, "\n")[2])
	token := u.Query().Get("token")
	var state string
	var revision int64
	if e := s.db.QueryRow(ctx, `SELECT delivery_state,staff_revision FROM admin_password_resets WHERE token_hash=$1`, hashToken(token)).Scan(&state, &revision); e != nil || state != "sent" || revision != full.Staff.Revision {
		t.Fatalf("delivery: %s %d %v", state, revision, e)
	}
	events, e := s.Audit(ctx, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, event := range events.Items {
		if event.Action == "staff.password_reset_requested" && event.Actor != "Unauthenticated request" {
			t.Fatal("reset request impersonated a staff actor")
		}
	}
	if _, e = s.db.Exec(ctx, `UPDATE admin_staff SET revision=revision+1 WHERE id=$1`, full.Staff.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.ResetPassword(ctx, token, "replacement-password"); e == nil {
		t.Fatal("reset grant survived access revision change")
	}
	for _, tc := range []struct {
		name   string
		sender outcomeMailer
		want   string
	}{{"unconfigured", outcomeMailer{}, "not_sent"}, {"ambiguous", outcomeMailer{enabled: true, err: errors.New("transport response lost")}, "unknown"}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := s.db.Exec(ctx, `DELETE FROM admin_auth_limits`); e != nil {
				t.Fatal(e)
			}
			s.mailer = tc.sender
			if e := s.RequestReset(ctx, full.Staff.Email, tc.name); e != nil {
				t.Fatal(e)
			}
			if e := s.db.QueryRow(ctx, `SELECT delivery_state FROM admin_password_resets ORDER BY created_at DESC LIMIT 1`).Scan(&state); e != nil || state != tc.want {
				t.Fatalf("got %s want %s: %v", state, tc.want, e)
			}
		})
	}
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mailer = outcomeMailer{enabled: true, cancel: cancel}
	if _, e = s.db.Exec(ctx, `DELETE FROM admin_auth_limits`); e != nil {
		t.Fatal(e)
	}
	if e = s.RequestReset(canceled, full.Staff.Email, "disconnect"); e != nil {
		t.Fatal(e)
	}
	if e = s.db.QueryRow(ctx, `SELECT delivery_state FROM admin_password_resets ORDER BY created_at DESC LIMIT 1`).Scan(&state); e != nil || state != "sent" {
		t.Fatalf("disconnect lost outcome: %s %v", state, e)
	}
}
