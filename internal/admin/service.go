// Package admin owns platform staff authorization and admin-specific records.
// Business/provider credentials and identities are deliberately not accepted here.
package admin

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"errors"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"booking/go-server/internal/appdata"
	"booking/go-server/internal/mailer"
	"booking/go-server/internal/secure"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type Config struct {
	PublicURL      string
	EncryptionKeys string
	ActiveKey      string
	BcryptCost     int
}
type Service struct {
	bookings  *appdata.Repository
	db        *pgxpool.Pool
	keys      *secure.Keyring
	mailer    mailer.Sender
	cfg       Config
	now       func() time.Time
	dummyHash []byte
}
type Staff struct {
	ID           uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	Role         string    `json:"role"`
	Status       string    `json:"status"`
	Revision     int64     `json:"revision"`
	Capabilities []string  `json:"capabilities"`
}
type Session struct {
	Staff     Staff     `json:"staff"`
	Stage     string    `json:"stage"`
	ID        uuid.UUID `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
	Token     string    `json:"session_token,omitempty"`
}
type Problem struct {
	Status  int
	Code    string
	Message string
}

func (e *Problem) Error() string                     { return e.Message }
func problem(status int, code, message string) error { return &Problem{status, code, message} }

var unauthorized = problem(401, "unauthorized", "Sign in again to continue.")
var invalidCredentials = problem(401, "invalid_credentials", "The sign-in details could not be verified.")
var conflict = problem(409, "record_changed", "This record changed. Refresh and review it again.")
var forbidden = problem(403, "forbidden", "Your role does not permit this action.")

func New(db *pgxpool.Pool, cfg Config, sender mailer.Sender) (*Service, error) {
	if db == nil {
		return nil, errors.New("admin database is required")
	}
	u, e := url.Parse(cfg.PublicURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
		return nil, errors.New("ADMIN_PUBLIC_URL must use HTTPS (HTTP allowed on loopback)")
	}
	keys, e := secure.ParseKeyring(cfg.EncryptionKeys, cfg.ActiveKey)
	if e != nil {
		return nil, e
	}
	if cfg.BcryptCost == 0 {
		cfg.BcryptCost = bcrypt.DefaultCost
	}
	dummy, e := bcrypt.GenerateFromPassword([]byte(randomToken()), cfg.BcryptCost)
	if e != nil {
		return nil, e
	}
	return &Service{bookings: appdata.NewRepository(db), db: db, keys: keys, mailer: sender, cfg: cfg, now: time.Now, dummyHash: dummy}, nil
}

// audit is transaction-bound so a successful domain change cannot omit its actor.
func audit(ctx context.Context, tx pgx.Tx, actor *uuid.UUID, action, kind string, id *uuid.UUID, reason string, details any) error {
	data, e := json.Marshal(details)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO admin_audit_events(actor_id,action,entity_type,entity_id,reason,details,request_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, actor, action, kind, id, reason, data, chimiddleware.GetReqID(ctx))
	return e
}
func scanStaff(row pgx.Row) (Staff, error) {
	var p Staff
	e := row.Scan(&p.ID, &p.Email, &p.Name, &p.Role, &p.Status, &p.Revision)
	p.Capabilities = capabilities(p.Role)
	return p, e
}

const staffColumns = `id,email,full_name,role,status,revision`

func (s *Service) Session(ctx context.Context, token string) (Session, error) {
	var out Session
	if len(token) < 32 || len(token) > 128 {
		return out, unauthorized
	}
	e := s.db.QueryRow(ctx, `SELECT p.id,p.email,p.full_name,p.role,p.status,p.revision,s.id,s.stage,s.expires_at FROM admin_sessions s JOIN admin_staff p ON p.id=s.staff_id WHERE s.token_hash=$1 AND s.expires_at>now() AND s.staff_revision=p.revision AND s.failed_attempts<5 AND p.status IN ('active','enrolling') AND (s.stage<>'full' OR (p.status='active' AND p.mfa_cipher IS NOT NULL))`, hashToken(token)).Scan(&out.Staff.ID, &out.Staff.Email, &out.Staff.Name, &out.Staff.Role, &out.Staff.Status, &out.Staff.Revision, &out.ID, &out.Stage, &out.ExpiresAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, unauthorized
	}
	out.Staff.Capabilities = capabilities(out.Staff.Role)
	return out, e
}
func (s *Service) newSession(ctx context.Context, tx pgx.Tx, p Staff, stage, userAgent string) (Session, error) {
	token := randomToken()
	ttl := 10 * time.Minute
	if stage == "full" {
		ttl = 8 * time.Hour
	}
	out := Session{Staff: p, Stage: stage, ID: uuid.New(), Token: token, ExpiresAt: time.Time{}}
	e := tx.QueryRow(ctx, `INSERT INTO admin_sessions(id,staff_id,token_hash,stage,staff_revision,user_agent,expires_at) VALUES($1,$2,$3,$4,$5,$6,now()+$7*interval '1 second') RETURNING expires_at`, out.ID, p.ID, hashToken(token), stage, p.Revision, truncate(userAgent, 300), int(ttl.Seconds())).Scan(&out.ExpiresAt)
	return out, e
}
func (s *Service) limit(ctx context.Context, key string, max int) error {
	var n int
	e := s.db.QueryRow(ctx, `INSERT INTO admin_auth_limits(key_hash,attempts,window_end) VALUES($1,1,now()+interval '15 minutes') ON CONFLICT(key_hash) DO UPDATE SET attempts=CASE WHEN admin_auth_limits.window_end<now() THEN 1 ELSE admin_auth_limits.attempts+1 END,window_end=CASE WHEN admin_auth_limits.window_end<now() THEN now()+interval '15 minutes' ELSE admin_auth_limits.window_end END RETURNING attempts`, hashToken(key)).Scan(&n)
	if e != nil {
		return e
	}
	if n > max {
		return problem(429, "rate_limited", "Too many attempts. Try again later.")
	}
	return nil
}
func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	v, e := mail.ParseAddress(email)
	if e != nil || v.Address != email || len(email) > 254 {
		return "", problem(422, "invalid_email", "Enter a valid email address.")
	}
	return email, nil
}
func passwordHash(password string, cost int) (string, error) {
	if len(password) < 8 || len(password) > 72 {
		return "", problem(422, "invalid_password", "Use a password between 8 and 72 bytes.")
	}
	h, e := bcrypt.GenerateFromPassword([]byte(password), cost)
	return string(h), e
}
func truncate(v string, n int) string {
	r := []rune(v)
	if len(r) > n {
		return string(r[:n])
	}
	return v
}
func (s *Service) Login(ctx context.Context, email, password, ip, userAgent string) (Session, error) {
	var out Session
	email = strings.ToLower(strings.TrimSpace(email))
	if e := s.limit(ctx, "login-ip:"+ip, 40); e != nil {
		return out, e
	}
	if e := s.limit(ctx, "login-email:"+email, 10); e != nil {
		return out, e
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	var p Staff
	var hash string
	e = tx.QueryRow(ctx, `SELECT `+staffColumns+`,password_hash FROM admin_staff WHERE email=$1 FOR UPDATE`, email).Scan(&p.ID, &p.Email, &p.Name, &p.Role, &p.Status, &p.Revision, &hash)
	if errors.Is(e, pgx.ErrNoRows) {
		bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password))
		return out, invalidCredentials
	}
	if e != nil {
		return out, e
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || !(p.Status == "active" || p.Status == "enrolling") {
		return out, invalidCredentials
	}
	p.Capabilities = capabilities(p.Role)
	stage := "login"
	if p.Status == "enrolling" {
		stage = "enroll"
	}
	out, e = s.newSession(ctx, tx, p, stage, userAgent)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Service) secret(ctx context.Context, tx pgx.Tx, id uuid.UUID) (string, int64, error) {
	var cipher []byte
	var last int64
	e := tx.QueryRow(ctx, `SELECT mfa_cipher,last_totp_step FROM admin_staff WHERE id=$1 FOR UPDATE`, id).Scan(&cipher, &last)
	if e != nil {
		return "", 0, e
	}
	var value secure.Ciphertext
	if e = json.Unmarshal(cipher, &value); e != nil {
		return "", 0, e
	}
	raw, e := s.keys.Decrypt(value, []byte("admin:mfa:"+id.String()))
	return string(raw), last, e
}
func (s *Service) setSecret(ctx context.Context, tx pgx.Tx, id uuid.UUID) (string, error) {
	secret := newSecret()
	cipher, e := s.keys.Encrypt([]byte(secret), []byte("admin:mfa:"+id.String()))
	if e != nil {
		return "", e
	}
	data, e := json.Marshal(cipher)
	if e != nil {
		return "", e
	}
	_, e = tx.Exec(ctx, `UPDATE admin_staff SET mfa_cipher=$2,last_totp_step=-1 WHERE id=$1`, id, data)
	return secret, e
}
func (s *Service) Enrollment(ctx context.Context, session Session) (map[string]string, error) {
	if session.Stage != "enroll" || session.Staff.Status != "enrolling" {
		return nil, forbidden
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	secret, _, e := s.secret(ctx, tx, session.Staff.ID)
	if e != nil {
		return nil, e
	}
	var valid bool
	e = tx.QueryRow(ctx, `SELECT true FROM admin_sessions a JOIN admin_staff p ON p.id=a.staff_id WHERE a.id=$1 AND a.staff_id=$2 AND a.stage='enroll' AND a.expires_at>now() AND a.staff_revision=p.revision AND p.revision=$3 AND p.status='enrolling' FOR SHARE OF a`, session.ID, session.Staff.ID, session.Staff.Revision).Scan(&valid)
	if e != nil {
		return nil, unauthorized
	}
	return map[string]string{"secret": secret, "otpauth_url": "otpauth://totp/" + url.PathEscape("Tellbook Admin:"+session.Staff.Email) + "?secret=" + secret + "&issuer=Tellbook%20Admin&algorithm=SHA1&digits=6&period=30"}, nil
}
func (s *Service) MFA(ctx context.Context, session Session, code, userAgent string) (Session, []string, error) {
	var out Session
	if session.Stage != "login" && session.Stage != "enroll" {
		return out, nil, forbidden
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return out, nil, e
	}
	defer tx.Rollback(ctx)
	// Follow staff -> session lock order used by access revocation and recovery.
	secret, last, e := s.secret(ctx, tx, session.Staff.ID)
	if e != nil {
		return out, nil, e
	}
	var stage string
	e = tx.QueryRow(ctx, `SELECT stage FROM admin_sessions WHERE id=$1 AND staff_id=$2 AND expires_at>now() AND failed_attempts<5 FOR UPDATE`, session.ID, session.Staff.ID).Scan(&stage)
	if e != nil || stage != session.Stage {
		return out, nil, unauthorized
	}
	p, e := scanStaff(tx.QueryRow(ctx, `SELECT `+staffColumns+` FROM admin_staff WHERE id=$1`, session.Staff.ID))
	if e != nil {
		return out, nil, e
	}
	if p.Revision != session.Staff.Revision || p.Status == "suspended" {
		return out, nil, unauthorized
	}
	step, valid := verifyTOTP(secret, strings.TrimSpace(code), last, s.now())
	usedRecovery := false
	if !valid && stage == "login" {
		tag, err := tx.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE staff_id=$1 AND code_hash=$2`, p.ID, recoveryHash(code))
		if err != nil {
			return out, nil, err
		}
		valid = tag.RowsAffected() == 1
		usedRecovery = valid
	}
	if !valid {
		if _, e = tx.Exec(ctx, `UPDATE admin_sessions SET failed_attempts=failed_attempts+1 WHERE id=$1`, session.ID); e != nil {
			return out, nil, e
		}
		if e = tx.Commit(ctx); e != nil {
			return out, nil, e
		}
		return out, nil, problem(422, "invalid_code", "The code is invalid, expired or already used.")
	}
	if !usedRecovery {
		if _, e = tx.Exec(ctx, `UPDATE admin_staff SET last_totp_step=$2 WHERE id=$1`, p.ID, step); e != nil {
			return out, nil, e
		}
	}
	var codes []string
	if stage == "enroll" {
		p.Status = "active"
		if _, e = tx.Exec(ctx, `UPDATE admin_staff SET status='active',updated_at=now() WHERE id=$1`, p.ID); e != nil {
			return out, nil, e
		}
		codes, e = s.replaceCodes(ctx, tx, p.ID)
		if e != nil {
			return out, nil, e
		}
	}
	if _, e = tx.Exec(ctx, `DELETE FROM admin_sessions WHERE id=$1`, session.ID); e != nil {
		return out, nil, e
	}
	out, e = s.newSession(ctx, tx, p, "full", userAgent)
	if e != nil {
		return out, nil, e
	}
	if e = audit(ctx, tx, &p.ID, "staff.signed_in", "staff", &p.ID, "", map[string]any{"recovery_code": usedRecovery, "enrolled": stage == "enroll"}); e != nil {
		return out, nil, e
	}
	return out, codes, tx.Commit(ctx)
}
func (s *Service) replaceCodes(ctx context.Context, tx pgx.Tx, id uuid.UUID) ([]string, error) {
	if _, e := tx.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE staff_id=$1`, id); e != nil {
		return nil, e
	}
	codes := make([]string, 10)
	for i := range codes {
		b := make([]byte, 10)
		raw := randomToken()
		copy(b, hashToken(raw))
		v := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
		codes[i] = v[:8] + "-" + v[8:]
		if _, e := tx.Exec(ctx, `INSERT INTO admin_recovery_codes(staff_id,code_hash) VALUES($1,$2)`, id, recoveryHash(codes[i])); e != nil {
			return nil, e
		}
	}
	return codes, nil
}
func (s *Service) Logout(ctx context.Context, session Session) error {
	_, e := s.db.Exec(ctx, `DELETE FROM admin_sessions WHERE id=$1`, session.ID)
	return e
}
func (s *Service) Bootstrap(ctx context.Context, email, name string) (string, error) {
	email, e := normalizeEmail(email)
	if e != nil {
		return "", e
	}
	if strings.TrimSpace(name) == "" {
		return "", errors.New("name required")
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(74109151)`); e != nil {
		return "", e
	}
	var n int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM admin_staff`).Scan(&n); e != nil {
		return "", e
	}
	if n > 0 {
		return "", errors.New("bootstrap is only allowed before the first staff account exists")
	}
	id := uuid.New()
	_, e = tx.Exec(ctx, `INSERT INTO admin_staff(id,email,full_name,role) VALUES($1,$2,$3,'super_admin')`, id, email, name)
	if e != nil {
		return "", e
	}
	raw, e := s.inviteToken(ctx, tx, id)
	if e != nil {
		return "", e
	}
	if e = audit(ctx, tx, nil, "staff.bootstrapped", "staff", &id, "Operational bootstrap", map[string]string{}); e != nil {
		return "", e
	}
	return s.cfg.PublicURL + "/accept-invitation?token=" + raw, tx.Commit(ctx)
}
func (s *Service) inviteToken(ctx context.Context, tx pgx.Tx, id uuid.UUID) (string, error) {
	if _, e := tx.Exec(ctx, `UPDATE admin_invitations SET revoked_at=now() WHERE staff_id=$1 AND consumed_at IS NULL AND revoked_at IS NULL`, id); e != nil {
		return "", e
	}
	raw := randomToken()
	_, e := tx.Exec(ctx, `INSERT INTO admin_invitations(id,staff_id,token_hash,expires_at) VALUES($1,$2,$3,now()+interval '72 hours')`, uuid.New(), id, hashToken(raw))
	return raw, e
}
func (s *Service) Invitation(ctx context.Context, token string) (Staff, error) {
	if len(token) < 32 {
		return Staff{}, unauthorized
	}
	p, e := scanStaff(s.db.QueryRow(ctx, `SELECT p.id,p.email,p.full_name,p.role,p.status,p.revision FROM admin_invitations i JOIN admin_staff p ON p.id=i.staff_id WHERE i.token_hash=$1 AND i.consumed_at IS NULL AND i.revoked_at IS NULL AND i.expires_at>now() AND p.status='invited'`, hashToken(token)))
	if errors.Is(e, pgx.ErrNoRows) {
		return p, problem(410, "invitation_inactive", "This invitation is expired, revoked or already used.")
	}
	return p, e
}
func (s *Service) Accept(ctx context.Context, token, password, userAgent string) (Session, error) {
	var out Session
	hash, e := passwordHash(password, s.cfg.BcryptCost)
	if e != nil {
		return out, e
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	var id uuid.UUID
	e = tx.QueryRow(ctx, `SELECT staff_id FROM admin_invitations WHERE token_hash=$1`, hashToken(token)).Scan(&id)
	if e != nil {
		return out, problem(410, "invitation_inactive", "This invitation is expired, revoked or already used.")
	}
	var staffID uuid.UUID
	if e = tx.QueryRow(ctx, `SELECT id FROM admin_staff WHERE id=$1 AND status='invited' FOR UPDATE`, id).Scan(&staffID); e != nil {
		return out, problem(410, "invitation_inactive", "This invitation is expired, revoked or already used.")
	}
	e = tx.QueryRow(ctx, `UPDATE admin_invitations SET consumed_at=now() WHERE token_hash=$1 AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at>now() RETURNING staff_id`, hashToken(token)).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, problem(410, "invitation_inactive", "This invitation is expired, revoked or already used.")
	}
	if e != nil {
		return out, e
	}
	p, e := scanStaff(tx.QueryRow(ctx, `UPDATE admin_staff SET password_hash=$2,status='enrolling',updated_at=now() WHERE id=$1 AND status='invited' RETURNING `+staffColumns, id, hash))
	if e != nil {
		return out, conflict
	}
	if _, e = s.setSecret(ctx, tx, id); e != nil {
		return out, e
	}
	out, e = s.newSession(ctx, tx, p, "enroll", userAgent)
	if e != nil {
		return out, e
	}
	if e = audit(ctx, tx, &id, "staff.invitation_accepted", "staff", &id, "", map[string]string{}); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

// WithBookingRepository shares the domain repository's configured notification
// policies with provider/customer operations. Call during application setup.
func (s *Service) WithBookingRepository(repository *appdata.Repository) *Service {
	if repository != nil {
		s.bookings = repository
	}
	return s
}
