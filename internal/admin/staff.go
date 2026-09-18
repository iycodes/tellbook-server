package admin

import (
	"booking/go-server/internal/mailer"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
	"strings"
	"time"
)

// authorizeTx pins identity and session until the mutation commits. Revocation and
// role changes cannot race a command that has already checked its authority.
func (s *Service) authorizeTx(ctx context.Context, tx pgx.Tx, session Session, cap string) error {
	p, e := scanStaff(tx.QueryRow(ctx, `SELECT `+staffColumns+` FROM admin_staff WHERE id=$1 FOR SHARE`, session.Staff.ID))
	if e != nil {
		return unauthorized
	}
	var ok bool
	e = tx.QueryRow(ctx, `SELECT true FROM admin_sessions WHERE id=$1 AND staff_id=$2 AND stage='full' AND expires_at>now() AND staff_revision=$3 FOR SHARE`, session.ID, p.ID, p.Revision).Scan(&ok)
	if e != nil || p.Status != "active" || p.Revision != session.Staff.Revision {
		return unauthorized
	}
	if cap != "" && !allowed(p.Role, cap) {
		return forbidden
	}
	return nil
}

type InvitationStatus struct {
	ExpiresAt     time.Time `json:"expires_at"`
	State         string    `json:"state"`
	DeliveryState string    `json:"delivery_state"`
}
type StaffMember struct {
	Staff
	Invitation *InvitationStatus `json:"invitation"`
}

func (s *Service) StaffList(ctx context.Context) ([]StaffMember, error) {
	rows, e := s.db.Query(ctx, `SELECT p.id,p.email,p.full_name,p.role,p.status,p.revision,
 (SELECT jsonb_build_object('expires_at',i.expires_at,'delivery_state',i.delivery_state,'state',CASE WHEN i.consumed_at IS NOT NULL THEN 'accepted' WHEN i.revoked_at IS NOT NULL THEN 'revoked' WHEN i.expires_at<=now() THEN 'expired' ELSE 'pending' END) FROM admin_invitations i WHERE i.staff_id=p.id ORDER BY i.created_at DESC,i.id DESC LIMIT 1)
 FROM admin_staff p ORDER BY p.created_at,p.id LIMIT 500`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []StaffMember{}
	for rows.Next() {
		var p StaffMember
		var invitation []byte
		if e = rows.Scan(&p.ID, &p.Email, &p.Name, &p.Role, &p.Status, &p.Revision, &invitation); e != nil {
			return nil, e
		}
		p.Capabilities = capabilities(p.Role)
		if len(invitation) > 0 {
			if e = json.Unmarshal(invitation, &p.Invitation); e != nil {
				return nil, e
			}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Service) sendLink(ctx context.Context, p Staff, path, raw, subject string) string {
	if s.mailer == nil || !s.mailer.Enabled() {
		return "not_sent"
	}
	err := s.mailer.Send(ctx, mailer.Message{ToEmail: p.Email, ToName: p.Name, Subject: subject, Text: subject + "\n\n" + strings.TrimRight(s.cfg.PublicURL, "/") + path + "?token=" + raw + "\n\nIf you did not expect this email, contact your Tellbook administrator."})
	if err == nil {
		return "sent"
	}
	disposition, _ := mailer.ClassifyTransportError(err)
	if disposition == mailer.DispositionAmbiguous {
		return "unknown"
	}
	return "failed"
}
func (s *Service) Invite(ctx context.Context, session Session, email, name, role string) (string, error) {
	email, e := normalizeEmail(email)
	if e != nil {
		return "", e
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 120 {
		return "", problem(422, "invalid_name", "Enter a name up to 120 characters.")
	}
	if _, ok := roleCapabilities[role]; !ok {
		return "", problem(422, "invalid_role", "Choose a supported role.")
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	if e = s.authorizeTx(ctx, tx, session, "staff.manage"); e != nil {
		return "", e
	}
	p := Staff{ID: uuid.New(), Email: email, Name: name, Role: role, Status: "invited"}
	_, e = tx.Exec(ctx, `INSERT INTO admin_staff(id,email,full_name,role) VALUES($1,$2,$3,$4)`, p.ID, email, name, role)
	if e != nil {
		var pe *pgconn.PgError
		if errors.As(e, &pe) && pe.Code == "23505" {
			return "", problem(409, "staff_exists", "A staff account already uses this email.")
		}
		return "", e
	}
	raw, e := s.inviteToken(ctx, tx, p.ID)
	if e != nil {
		return "", e
	}
	if e = audit(ctx, tx, &session.Staff.ID, "staff.invited", "staff", &p.ID, "", map[string]string{"role": role}); e != nil {
		return "", e
	}
	if e = tx.Commit(ctx); e != nil {
		return "", e
	}
	state := s.sendLink(ctx, p, "/accept-invitation", raw, "Your Tellbook Admin invitation")
	_, e = s.db.Exec(ctx, `UPDATE admin_invitations SET delivery_state=$2 WHERE token_hash=$1`, hashToken(raw), state)
	return state, e
}

type StaffChange struct {
	Action   string `json:"action"`
	Role     string `json:"role"`
	Revision int64  `json:"revision"`
	Reason   string `json:"reason"`
}

func (s *Service) ChangeStaff(ctx context.Context, session Session, id uuid.UUID, input StaffChange) (string, error) {
	if strings.TrimSpace(input.Reason) == "" || len([]rune(input.Reason)) > 1000 {
		return "", problem(422, "invalid_reason", "Provide a reason up to 1,000 characters.")
	}
	if id == session.Staff.ID {
		return "", problem(409, "self_change", "Another Super Admin must change your access.")
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	// Staff-access decisions serialize only with each other, including last-admin checks.
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(74109151)`); e != nil {
		return "", e
	}
	if e = s.authorizeTx(ctx, tx, session, "staff.manage"); e != nil {
		return "", e
	}
	p, e := scanStaff(tx.QueryRow(ctx, `SELECT `+staffColumns+` FROM admin_staff WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(e, pgx.ErrNoRows) {
		return "", problem(404, "not_found", "Staff member not found.")
	}
	if e != nil {
		return "", e
	}
	if p.Revision != input.Revision {
		return "", conflict
	}
	if p.Role == "super_admin" && p.Status == "active" && (input.Action == "suspend" || input.Action == "recover_mfa" || (input.Action == "role" && input.Role != "super_admin")) {
		var n int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM admin_staff WHERE role='super_admin' AND status='active'`).Scan(&n); e != nil {
			return "", e
		}
		if n <= 1 {
			return "", problem(409, "last_admin", "Keep at least one active Super Admin.")
		}
	}
	raw := ""
	switch input.Action {
	case "role":
		if _, ok := roleCapabilities[input.Role]; !ok {
			return "", problem(422, "invalid_role", "Choose a supported role.")
		}
		_, e = tx.Exec(ctx, `UPDATE admin_staff SET role=$2,revision=revision+1,updated_at=now() WHERE id=$1`, id, input.Role)
	case "suspend":
		_, e = tx.Exec(ctx, `UPDATE admin_staff SET status='suspended',revision=revision+1,updated_at=now() WHERE id=$1`, id)
	case "restore":
		if p.Status != "suspended" || p.Role == "" {
			return "", conflict
		}
		_, e = tx.Exec(ctx, `UPDATE admin_staff SET status=CASE WHEN password_hash='' THEN 'invited' ELSE 'enrolling' END,revision=revision+1,updated_at=now() WHERE id=$1`, id)
		if e == nil && p.Status == "suspended" {
			_, e = s.setSecret(ctx, tx, id)
		}
	case "recover_mfa":
		if p.Status != "active" {
			return "", conflict
		}
		_, e = tx.Exec(ctx, `UPDATE admin_staff SET status='enrolling',revision=revision+1,updated_at=now() WHERE id=$1`, id)
		if e == nil {
			_, e = s.setSecret(ctx, tx, id)
		}
	case "resend_invitation":
		if p.Status != "invited" {
			return "", conflict
		}
		raw, e = s.inviteToken(ctx, tx, id)
	case "revoke_invitation":
		if p.Status != "invited" {
			return "", conflict
		}
		_, e = tx.Exec(ctx, `UPDATE admin_invitations SET revoked_at=now() WHERE staff_id=$1 AND consumed_at IS NULL`, id)
	default:
		return "", problem(422, "invalid_action", "Unsupported staff action.")
	}
	if e != nil {
		return "", e
	}
	if input.Action != "resend_invitation" && input.Action != "revoke_invitation" {
		if _, e = tx.Exec(ctx, `DELETE FROM admin_sessions WHERE staff_id=$1`, id); e != nil {
			return "", e
		}
		if input.Action != "role" {
			if _, e = tx.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE staff_id=$1`, id); e != nil {
				return "", e
			}
			if _, e = tx.Exec(ctx, `UPDATE admin_invitations SET revoked_at=now() WHERE staff_id=$1 AND consumed_at IS NULL`, id); e != nil {
				return "", e
			}
		}
	}
	if e = audit(ctx, tx, &session.Staff.ID, "staff."+input.Action, "staff", &id, input.Reason, map[string]string{"role": input.Role}); e != nil {
		return "", e
	}
	if e = tx.Commit(ctx); e != nil {
		return "", e
	}
	if raw != "" {
		state := s.sendLink(ctx, p, "/accept-invitation", raw, "Your Tellbook Admin invitation")
		_, e = s.db.Exec(ctx, `UPDATE admin_invitations SET delivery_state=$2 WHERE token_hash=$1`, hashToken(raw), state)
		return state, e
	}
	return "saved", nil
}
func (s *Service) RequestReset(ctx context.Context, email, ip string) error {
	if e := s.limit(ctx, "reset-ip:"+ip, 20); e != nil {
		return e
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if e := s.limit(ctx, "reset-email:"+email, 3); e != nil {
		return e
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	p, e := scanStaff(tx.QueryRow(ctx, `SELECT `+staffColumns+` FROM admin_staff WHERE email=$1 AND status IN ('active','enrolling') FOR SHARE`, email))
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	raw := randomToken()
	id := uuid.New()
	_, e = tx.Exec(ctx, `INSERT INTO admin_password_resets(id,token_hash,staff_id,staff_revision,expires_at,delivery_state,delivery_updated_at) VALUES($1,$2,$3,$4,now()+interval '30 minutes','sending',now())`, id, hashToken(raw), p.ID, p.Revision)
	if e != nil {
		return e
	}
	// The requester is not authenticated yet; do not attribute this request to staff.
	if e = audit(ctx, tx, nil, "staff.password_reset_requested", "staff", &p.ID, "Password recovery requested", map[string]any{"delivery_id": id}); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	state := s.sendLink(ctx, p, "/reset-password", raw, "Reset your Tellbook Admin password")
	// Persist the transport outcome even when the browser request disconnected.
	// A crash before this write leaves 'sending', later classified as unknown.
	deliveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, e = s.db.Begin(deliveryCtx)
	if e != nil {
		return e
	}
	defer tx.Rollback(deliveryCtx)
	if _, e = tx.Exec(deliveryCtx, `UPDATE admin_password_resets SET delivery_state=$2,delivery_updated_at=now() WHERE id=$1`, id, state); e != nil {
		return e
	}
	if e = audit(deliveryCtx, tx, nil, "staff.password_reset_email_"+state, "staff", &p.ID, "Password recovery email outcome", map[string]any{"delivery_id": id, "delivery_state": state}); e != nil {
		return e
	}
	return tx.Commit(deliveryCtx)
}
func (s *Service) ResetPassword(ctx context.Context, token, password string) error {
	hash, e := passwordHash(password, s.cfg.BcryptCost)
	if e != nil {
		return e
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var id uuid.UUID
	var revision *int64
	e = tx.QueryRow(ctx, `SELECT staff_id,staff_revision FROM admin_password_resets WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>now()`, hashToken(token)).Scan(&id, &revision)
	if e != nil {
		return problem(410, "reset_inactive", "This reset link is no longer active.")
	}
	p, e := scanStaff(tx.QueryRow(ctx, `SELECT `+staffColumns+` FROM admin_staff WHERE id=$1 FOR UPDATE`, id))
	if e != nil {
		return e
	}
	if (p.Status != "active" && p.Status != "enrolling") || revision == nil || *revision != p.Revision {
		return unauthorized
	}
	tag, e := tx.Exec(ctx, `UPDATE admin_password_resets SET consumed_at=now() WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>now()`, hashToken(token))
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return conflict
	}
	if _, e = tx.Exec(ctx, `UPDATE admin_staff SET password_hash=$2,revision=revision+1,updated_at=now() WHERE id=$1`, id, hash); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM admin_sessions WHERE staff_id=$1`, id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE admin_password_resets SET consumed_at=coalesce(consumed_at,now()) WHERE staff_id=$1`, id); e != nil {
		return e
	}
	if e = audit(ctx, tx, &id, "staff.password_reset", "staff", &id, "", map[string]string{}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type SessionInfo struct {
	ID        uuid.UUID `json:"id"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Current   bool      `json:"current"`
}

func (s *Service) Security(ctx context.Context, session Session) ([]SessionInfo, error) {
	rows, e := s.db.Query(ctx, `SELECT id,user_agent,created_at,expires_at FROM admin_sessions WHERE staff_id=$1 AND stage='full' AND expires_at>now() ORDER BY created_at DESC`, session.Staff.ID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []SessionInfo{}
	for rows.Next() {
		var p SessionInfo
		if e = rows.Scan(&p.ID, &p.UserAgent, &p.CreatedAt, &p.ExpiresAt); e != nil {
			return nil, e
		}
		p.Current = p.ID == session.ID
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Service) RevokeSession(ctx context.Context, session Session, id uuid.UUID) error {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = s.authorizeTx(ctx, tx, session, ""); e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `DELETE FROM admin_sessions WHERE id=$1 AND staff_id=$2`, id, session.Staff.ID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return problem(404, "not_found", "Session not found.")
	}
	if e = audit(ctx, tx, &session.Staff.ID, "staff.session_revoked", "staff", &session.Staff.ID, "", map[string]any{"session_id": id}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) RotateRecovery(ctx context.Context, session Session, password, code string) ([]string, error) {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	secret, last, e := s.secret(ctx, tx, session.Staff.ID)
	if e != nil {
		return nil, e
	}
	if e = s.authorizeTx(ctx, tx, session, ""); e != nil {
		return nil, e
	}
	var hash string
	if e = tx.QueryRow(ctx, `SELECT password_hash FROM admin_staff WHERE id=$1`, session.Staff.ID).Scan(&hash); e != nil {
		return nil, e
	}
	step, ok := verifyTOTP(secret, code, last, s.now())
	if !ok || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, invalidCredentials
	}
	if _, e = tx.Exec(ctx, `UPDATE admin_staff SET last_totp_step=$2 WHERE id=$1`, session.Staff.ID, step); e != nil {
		return nil, e
	}
	codes, e := s.replaceCodes(ctx, tx, session.Staff.ID)
	if e != nil {
		return nil, e
	}
	if e = audit(ctx, tx, &session.Staff.ID, "staff.recovery_codes_rotated", "staff", &session.Staff.ID, "", map[string]string{}); e != nil {
		return nil, e
	}
	return codes, tx.Commit(ctx)
}

// Assignment is an explicit read projection, not access to the Team roster.
// Reuse staff identities without exposing email, invitations or security state.
type StaffAssignee struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

func (s *Service) SupportAssignees(ctx context.Context) ([]StaffAssignee, error) {
	out := []StaffAssignee{}
	rows, e := s.db.Query(ctx, `SELECT id,full_name FROM admin_staff WHERE status='active' AND role IN ('super_admin','support') ORDER BY full_name,id`)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var a StaffAssignee
		if e = rows.Scan(&a.ID, &a.Name); e != nil {
			return out, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
