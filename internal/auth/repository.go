package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"booking/go-server/internal/authchallenge"
	"booking/go-server/internal/welcomeemail"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("not found")

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) CompleteCodeChallenge(
	ctx context.Context,
	challenge authchallenge.Challenge,
	session RefreshSession,
) (User, bool, bool, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return User{}, false, false, fmt.Errorf("begin provider code verification: %w", err)
	}
	defer tx.Rollback(ctx)

	var consumedID uuid.UUID
	err = tx.QueryRow(ctx, `
		UPDATE provider_auth_challenges SET consumed_at=NOW()
		WHERE id=$1 AND code_hash=$2 AND purpose='sign_in' AND consumed_at IS NULL
		  AND delivery_accepted_at IS NOT NULL AND verify_expires_at>NOW() AND failed_attempts<6
		RETURNING id
	`, challenge.ID, challenge.CodeHash).Scan(&consumedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, false, ErrNotFound
	}
	if err != nil {
		return User{}, false, false, fmt.Errorf("consume provider auth challenge: %w", err)
	}
	lockKey := "provider:" + challenge.IdentifierType + ":" + challenge.Identifier
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return User{}, false, false, fmt.Errorf("lock provider identity: %w", err)
	}

	var clientID uuid.UUID
	newAccount := false
	err = tx.QueryRow(ctx, `
		SELECT client_id FROM provider_auth_identities
		WHERE identity_type=$1 AND normalized_identifier=$2
	`, challenge.IdentifierType, challenge.Identifier).Scan(&clientID)
	if errors.Is(err, pgx.ErrNoRows) {
		clientID = uuid.New()
		newAccount = true
		var email any
		var emailVerifiedAt any
		if challenge.IdentifierType == "email" {
			email = challenge.Identifier
			emailVerifiedAt = time.Now().UTC()
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO clients (id,full_name,email,password_hash,email_verified_at)
			VALUES ($1,'',$2,NULL,$3)
		`, clientID, email, emailVerifiedAt); err != nil {
			return User{}, false, false, fmt.Errorf("create passwordless provider: %w", err)
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO provider_auth_identities (
				client_id,identity_type,normalized_identifier,verified_at
			) VALUES ($1,$2,$3,NOW())
		`, clientID, challenge.IdentifierType, challenge.Identifier); err != nil {
			return User{}, false, false, fmt.Errorf("create provider identity: %w", err)
		}
	} else if err != nil {
		return User{}, false, false, fmt.Errorf("resolve provider identity: %w", err)
	}

	session.UserID = clientID
	if _, err = tx.Exec(ctx, `
		INSERT INTO auth_refresh_sessions (
			id,client_id,token_hash,user_agent,ip_address,expires_at,last_used_at,created_at,session_revision
		) SELECT $1,$2,$3,$4,$5,$6,$7,$8,security_revision FROM clients WHERE id=$2
	`, session.ID, session.UserID, session.TokenHash, session.UserAgent, session.IPAddress,
		session.ExpiresAt, session.LastUsedAt, session.CreatedAt); err != nil {
		return User{}, false, false, fmt.Errorf("create provider code session: %w", err)
	}
	if newAccount && challenge.IdentifierType == "email" {
		if _, err := welcomeemail.AssignOptionalTx(ctx, tx, welcomeemail.Assignment{
			Audience: welcomeemail.AudienceProvider, AccountID: clientID, Email: challenge.Identifier,
		}); err != nil {
			return User{}, false, false, fmt.Errorf("assign provider welcome email: %w", err)
		}
	}

	user, err := scanUser(tx.QueryRow(ctx, `
		SELECT id,full_name,bio,COALESCE(cover_image_url,''),COALESCE(email,''),
			email_verified_at,password_hash IS NOT NULL,security_revision,created_at,updated_at
		FROM clients WHERE id=$1
	`, clientID))
	if err != nil {
		return User{}, false, false, fmt.Errorf("load verified provider: %w", err)
	}
	var onboardingComplete bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM client_profiles WHERE client_id=$1 AND market_configured_at IS NOT NULL)
	`, clientID).Scan(&onboardingComplete); err != nil {
		return User{}, false, false, fmt.Errorf("load provider onboarding state: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, false, false, fmt.Errorf("commit provider code verification: %w", err)
	}
	return user, newAccount, !onboardingComplete, nil
}

type passwordCandidate struct {
	UserID       uuid.UUID
	PasswordHash string
}

func (r *Repository) IdentityOwner(ctx context.Context, identityType, identifier string) (uuid.UUID, error) {
	var userID uuid.UUID
	err := r.db.QueryRow(ctx, `
		SELECT client_id FROM provider_auth_identities
		WHERE identity_type=$1 AND normalized_identifier=$2
	`, identityType, identifier).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("load provider identity owner: %w", err)
	}
	return userID, nil
}

func (r *Repository) UserHasVerifiedIdentityType(ctx context.Context, userID uuid.UUID, identityType string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM provider_auth_identities
			WHERE client_id=$1 AND identity_type=$2
		)
	`, userID, identityType).Scan(&exists)
	return exists, err
}

func (r *Repository) CompleteIdentityLink(ctx context.Context, challenge authchallenge.Challenge) (User, error) {
	if challenge.TargetAccountID == nil {
		return User{}, ErrNotFound
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return User{}, fmt.Errorf("begin provider identity link: %w", err)
	}
	defer tx.Rollback(ctx)
	var consumedID uuid.UUID
	err = tx.QueryRow(ctx, `
		UPDATE provider_auth_challenges SET consumed_at=NOW()
		WHERE id=$1 AND code_hash=$2 AND consumed_at IS NULL
		  AND purpose='link_identity' AND target_client_id=$3
		  AND delivery_accepted_at IS NOT NULL AND verify_expires_at>NOW() AND failed_attempts<6
		RETURNING id
	`, challenge.ID, challenge.CodeHash, *challenge.TargetAccountID).Scan(&consumedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("consume provider identity link challenge: %w", err)
	}
	now := time.Now().UTC()
	result, err := tx.Exec(ctx, `
		INSERT INTO provider_auth_identities (client_id,identity_type,normalized_identifier,verified_at)
		VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING
	`, *challenge.TargetAccountID, challenge.IdentifierType, challenge.Identifier, now)
	if err != nil {
		return User{}, fmt.Errorf("link provider identity: %w", err)
	}
	if result.RowsAffected() == 0 {
		var ownerID uuid.UUID
		err = tx.QueryRow(ctx, `
			SELECT client_id FROM provider_auth_identities
			WHERE identity_type=$1 AND normalized_identifier=$2
		`, challenge.IdentifierType, challenge.Identifier).Scan(&ownerID)
		if err != nil || ownerID != *challenge.TargetAccountID {
			return User{}, ErrIdentityConflict
		}
	}
	if challenge.IdentifierType == "email" {
		if _, err = tx.Exec(ctx, `
			UPDATE clients SET email=$2,email_verified_at=$3,security_revision=security_revision+1,updated_at=$3
			WHERE id=$1
		`, *challenge.TargetAccountID, challenge.Identifier, now); err != nil {
			return User{}, fmt.Errorf("update provider email identity: %w", err)
		}
		if _, err := welcomeemail.AssignOptionalTx(ctx, tx, welcomeemail.Assignment{
			Audience: welcomeemail.AudienceProvider, AccountID: *challenge.TargetAccountID, Email: challenge.Identifier,
		}); err != nil {
			return User{}, fmt.Errorf("assign linked provider welcome email: %w", err)
		}
	} else {
		if _, err = tx.Exec(ctx, `
			UPDATE clients SET security_revision=security_revision+1,updated_at=$2 WHERE id=$1
		`, *challenge.TargetAccountID, now); err != nil {
			return User{}, fmt.Errorf("advance provider security revision: %w", err)
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM auth_refresh_sessions WHERE client_id=$1`, *challenge.TargetAccountID); err != nil {
		return User{}, fmt.Errorf("revoke provider sessions after identity link: %w", err)
	}
	user, err := scanUser(tx.QueryRow(ctx, `
		SELECT id,full_name,bio,COALESCE(cover_image_url,''),COALESCE(email,''),email_verified_at,
		       password_hash IS NOT NULL,security_revision,created_at,updated_at
		FROM clients WHERE id=$1
	`, *challenge.TargetAccountID))
	if err != nil {
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit provider identity link: %w", err)
	}
	return user, nil
}

func (r *Repository) PasswordCandidates(ctx context.Context, normalizedIdentifier string) ([]passwordCandidate, error) {
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT c.id, c.password_hash
		FROM provider_auth_identities identity
		JOIN clients c ON c.id=identity.client_id
		WHERE identity.normalized_identifier=$1 AND c.password_hash IS NOT NULL
	`, normalizedIdentifier)
	if err != nil {
		return nil, fmt.Errorf("load provider password candidates: %w", err)
	}
	defer rows.Close()
	items := make([]passwordCandidate, 0, 1)
	for rows.Next() {
		var item passwordCandidate
		if err := rows.Scan(&item.UserID, &item.PasswordHash); err != nil {
			return nil, fmt.Errorf("scan provider password candidate: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) PasswordResetAccount(ctx context.Context, identifierType, identifier string) (passwordCandidate, error) {
	var candidate passwordCandidate
	err := r.db.QueryRow(ctx, `
		SELECT c.id, c.password_hash
		FROM provider_auth_identities identity
		JOIN clients c ON c.id=identity.client_id
		WHERE identity.identity_type=$1 AND identity.normalized_identifier=$2
		  AND c.password_hash IS NOT NULL
	`, identifierType, identifier).Scan(&candidate.UserID, &candidate.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return passwordCandidate{}, ErrNotFound
	}
	if err != nil {
		return passwordCandidate{}, fmt.Errorf("load provider password reset account: %w", err)
	}
	return candidate, nil
}

func (r *Repository) GetUserByID(ctx context.Context, id uuid.UUID) (User, error) {
	const query = `
		SELECT id, full_name, bio, COALESCE(cover_image_url, ''), COALESCE(email, ''),
		       email_verified_at, password_hash IS NOT NULL, security_revision, created_at, updated_at
		FROM clients
		WHERE id = $1
	`

	user, err := scanUser(r.db.QueryRow(ctx, query, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("select user by id: %w", err)
	}

	return user, nil
}

func (r *Repository) StorePasswordResetGrant(ctx context.Context, challenge authchallenge.Challenge, grantHash []byte, expiresAt time.Time) error {
	if challenge.TargetAccountID == nil {
		return ErrNotFound
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin provider reset verification: %w", err)
	}
	defer tx.Rollback(ctx)
	var consumedID uuid.UUID
	err = tx.QueryRow(ctx, `
		UPDATE provider_auth_challenges SET consumed_at=NOW()
		WHERE id=$1 AND code_hash=$2 AND purpose='password_reset'
		  AND target_client_id=$3 AND consumed_at IS NULL
		  AND delivery_accepted_at IS NOT NULL AND verify_expires_at>NOW() AND failed_attempts<6
		RETURNING id
	`, challenge.ID, challenge.CodeHash, *challenge.TargetAccountID).Scan(&consumedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("consume provider reset challenge: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO auth_password_reset_grants (
			id,realm,provider_client_id,provider_challenge_id,grant_hash,expires_at
		) VALUES ($1,'provider',$2,$3,$4,$5)
	`, uuid.New(), *challenge.TargetAccountID, challenge.ID, grantHash, expiresAt)
	if err != nil {
		return fmt.Errorf("store provider password reset grant: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit provider reset verification: %w", err)
	}
	return nil
}

func (r *Repository) CompletePasswordReset(ctx context.Context, grantHash []byte, passwordHash string, session RefreshSession) (User, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return User{}, fmt.Errorf("begin provider password reset: %w", err)
	}
	defer tx.Rollback(ctx)
	var userID uuid.UUID
	err = tx.QueryRow(ctx, `
		UPDATE auth_password_reset_grants SET consumed_at=NOW()
		WHERE realm='provider' AND grant_hash=$1 AND consumed_at IS NULL AND expires_at>NOW()
		RETURNING provider_client_id
	`, grantHash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("consume provider password reset grant: %w", err)
	}
	if _, err = tx.Exec(ctx, `
		UPDATE clients SET password_hash=$2,security_revision=security_revision+1,updated_at=NOW()
		WHERE id=$1
	`, userID, passwordHash); err != nil {
		return User{}, fmt.Errorf("reset provider password: %w", err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM auth_refresh_sessions WHERE client_id=$1`, userID); err != nil {
		return User{}, fmt.Errorf("revoke provider sessions after reset: %w", err)
	}
	session.UserID = userID
	if _, err = tx.Exec(ctx, `
		INSERT INTO auth_refresh_sessions (
			id,client_id,token_hash,user_agent,ip_address,expires_at,last_used_at,created_at,session_revision
		) SELECT $1,$2,$3,$4,$5,$6,$7,$8,security_revision FROM clients WHERE id=$2
	`, session.ID, session.UserID, session.TokenHash, session.UserAgent, session.IPAddress,
		session.ExpiresAt, session.LastUsedAt, session.CreatedAt); err != nil {
		return User{}, fmt.Errorf("create provider reset session: %w", err)
	}
	user, err := scanUser(tx.QueryRow(ctx, `
		SELECT id,full_name,bio,COALESCE(cover_image_url,''),COALESCE(email,''),email_verified_at,
		       password_hash IS NOT NULL,security_revision,created_at,updated_at
		FROM clients WHERE id=$1
	`, userID))
	if err != nil {
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit provider password reset: %w", err)
	}
	return user, nil
}

func (r *Repository) PasswordHashByUserID(ctx context.Context, userID uuid.UUID) (string, error) {
	var passwordHash sql.NullString
	err := r.db.QueryRow(ctx, `SELECT password_hash FROM clients WHERE id=$1`, userID).Scan(&passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load provider password hash: %w", err)
	}
	return passwordHash.String, nil
}

func (r *Repository) ChangePassword(ctx context.Context, userID uuid.UUID, passwordHash string) (User, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return User{}, fmt.Errorf("begin provider password change: %w", err)
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `
		UPDATE clients SET password_hash=$2,security_revision=security_revision+1,updated_at=NOW()
		WHERE id=$1
	`, userID, passwordHash)
	if err != nil {
		return User{}, fmt.Errorf("change provider password: %w", err)
	}
	if result.RowsAffected() != 1 {
		return User{}, ErrNotFound
	}
	if _, err = tx.Exec(ctx, `DELETE FROM auth_refresh_sessions WHERE client_id=$1`, userID); err != nil {
		return User{}, fmt.Errorf("revoke provider sessions after password change: %w", err)
	}
	user, err := scanUser(tx.QueryRow(ctx, `
		SELECT id,full_name,bio,COALESCE(cover_image_url,''),COALESCE(email,''),email_verified_at,
		       password_hash IS NOT NULL,security_revision,created_at,updated_at
		FROM clients WHERE id=$1
	`, userID))
	if err != nil {
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit provider password change: %w", err)
	}
	return user, nil
}

func (r *Repository) CreateRefreshSession(ctx context.Context, session RefreshSession) error {
	const query = `
		INSERT INTO auth_refresh_sessions (
			id,
			client_id,
			token_hash,
			user_agent,
			ip_address,
			expires_at,
			last_used_at,
			created_at,
			session_revision
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`

	_, err := r.db.Exec(
		ctx,
		query,
		session.ID,
		session.UserID,
		session.TokenHash,
		session.UserAgent,
		session.IPAddress,
		session.ExpiresAt,
		session.LastUsedAt,
		session.CreatedAt,
		session.SessionRevision,
	)
	if err != nil {
		return fmt.Errorf("insert refresh session: %w", err)
	}

	return nil
}

func (r *Repository) GetRefreshSessionByTokenHash(ctx context.Context, tokenHash []byte) (refreshSessionRecord, error) {
	const query = `
		SELECT
			s.id,
			s.client_id,
			s.token_hash,
			s.user_agent,
			COALESCE(s.ip_address, ''),
			s.expires_at,
			s.last_used_at,
			s.created_at,
			s.session_revision,
			u.id,
			u.full_name,
			u.bio,
			COALESCE(u.cover_image_url, ''),
			COALESCE(u.email, ''),
			u.email_verified_at,
			u.security_revision,
			u.created_at,
			u.updated_at
		FROM auth_refresh_sessions s
		JOIN clients u ON u.id = s.client_id
		WHERE s.token_hash = $1
		  AND s.expires_at > NOW()
		  AND s.session_revision = u.security_revision
	`

	var record refreshSessionRecord
	var verifiedAt sql.NullTime
	err := r.db.QueryRow(ctx, query, tokenHash).Scan(
		&record.ID,
		&record.UserID,
		&record.TokenHash,
		&record.UserAgent,
		&record.IPAddress,
		&record.ExpiresAt,
		&record.LastUsedAt,
		&record.CreatedAt,
		&record.SessionRevision,
		&record.User.ID,
		&record.User.FullName,
		&record.User.Bio,
		&record.User.CoverImageURL,
		&record.User.Email,
		&verifiedAt,
		&record.User.SecurityRevision,
		&record.User.CreatedAt,
		&record.User.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return refreshSessionRecord{}, ErrNotFound
		}
		return refreshSessionRecord{}, fmt.Errorf("select refresh session: %w", err)
	}

	record.User.EmailVerifiedAt = nullableTimePtr(verifiedAt)
	return record, nil
}

func (r *Repository) RotateRefreshSession(
	ctx context.Context,
	sessionID uuid.UUID,
	currentTokenHash []byte,
	nextTokenHash []byte,
	expiresAt time.Time,
	usedAt time.Time,
	userAgent string,
	ipAddress string,
) error {
	const query = `
			UPDATE auth_refresh_sessions
			SET token_hash = $3,
				expires_at = $4,
				last_used_at = $5,
				user_agent = $6,
				ip_address = $7
			WHERE id = $1 AND token_hash = $2
		`

	result, err := r.db.Exec(ctx, query, sessionID, currentTokenHash, nextTokenHash, expiresAt, usedAt, userAgent, ipAddress)
	if err != nil {
		return fmt.Errorf("rotate refresh session: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrInvalidRefresh
	}

	return nil
}

func (r *Repository) DeleteRefreshSessionByTokenHash(ctx context.Context, tokenHash []byte) error {
	const query = `DELETE FROM auth_refresh_sessions WHERE token_hash = $1`
	if _, err := r.db.Exec(ctx, query, tokenHash); err != nil {
		return fmt.Errorf("delete refresh session: %w", err)
	}
	return nil
}

type userScanner interface {
	Scan(dest ...any) error
}

func scanUser(scanner userScanner) (User, error) {
	var user User
	var verifiedAt sql.NullTime

	if err := scanner.Scan(
		&user.ID,
		&user.FullName,
		&user.Bio,
		&user.CoverImageURL,
		&user.Email,
		&verifiedAt,
		&user.HasPassword,
		&user.SecurityRevision,
		&user.CreatedAt,
		&user.UpdatedAt,
	); err != nil {
		return User{}, err
	}

	user.EmailVerifiedAt = nullableTimePtr(verifiedAt)
	return user, nil
}

func nullableTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}

	verifiedAt := value.Time
	return &verifiedAt
}
