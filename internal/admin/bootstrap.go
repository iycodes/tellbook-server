package admin

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// RenewBootstrapInvitation is an operator-only repair of the original, never
// accepted invitation. It cannot create identities or recover enrolled staff.
func (s *Service) RenewBootstrapInvitation(ctx context.Context, email, reason string) (string, error) {
	email, e := normalizeEmail(email)
	if e != nil {
		return "", e
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 1000 {
		return "", errors.New("renewal requires a reason up to 1,000 characters")
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(74109151)`); e != nil {
		return "", e
	}
	var count int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM admin_staff`).Scan(&count); e != nil {
		return "", e
	}
	if count != 1 {
		return "", errors.New("renewal requires exactly the original unaccepted Super Admin")
	}
	p, e := scanStaff(tx.QueryRow(ctx, `SELECT `+staffColumns+` FROM admin_staff WHERE email=$1 AND role='super_admin' AND status='invited' AND password_hash='' AND revision=1 FOR UPDATE`, email))
	if errors.Is(e, pgx.ErrNoRows) {
		return "", errors.New("the original invitation has been accepted or the staff record changed; use staff recovery")
	}
	if e != nil {
		return "", e
	}
	var original bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_audit_events WHERE entity_id=$1 AND action='staff.bootstrapped') AND NOT EXISTS(SELECT 1 FROM admin_invitations WHERE staff_id=$1 AND consumed_at IS NOT NULL)`, p.ID).Scan(&original); e != nil {
		return "", e
	}
	if !original {
		return "", errors.New("only the original bootstrap invitation can be renewed")
	}
	raw, e := s.inviteToken(ctx, tx, p.ID)
	if e != nil {
		return "", e
	}
	if e = audit(ctx, tx, nil, "staff.bootstrap_invitation_renewed", "staff", &p.ID, reason, map[string]string{"source": "admin-bootstrap"}); e != nil {
		return "", e
	}
	if e = tx.Commit(ctx); e != nil {
		return "", e
	}
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/accept-invitation?token=" + raw, nil
}
