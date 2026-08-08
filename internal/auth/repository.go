package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

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

func (r *Repository) UpsertPendingRegistration(ctx context.Context, pending pendingRegistration) error {
	const query = `
		INSERT INTO auth_pending_registrations (
			id,
			full_name,
			bio,
			email,
			password_hash,
			cover_image_data_url,
			cover_image_content_type,
			token_hash,
			expires_at,
			created_at,
			updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (email) DO UPDATE
		SET id = EXCLUDED.id,
			full_name = EXCLUDED.full_name,
			bio = EXCLUDED.bio,
			password_hash = EXCLUDED.password_hash,
			cover_image_data_url = EXCLUDED.cover_image_data_url,
			cover_image_content_type = EXCLUDED.cover_image_content_type,
			token_hash = EXCLUDED.token_hash,
			expires_at = EXCLUDED.expires_at,
			created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at
	`

	if _, err := r.db.Exec(
		ctx,
		query,
		pending.ID,
		pending.FullName,
		pending.Bio,
		pending.Email,
		pending.PasswordHash,
		pending.CoverImageDataURL,
		pending.CoverImageContentType,
		pending.TokenHash,
		pending.ExpiresAt,
		pending.CreatedAt,
		pending.UpdatedAt,
	); err != nil {
		return fmt.Errorf("upsert pending registration: %w", err)
	}

	return nil
}

func (r *Repository) GetPendingRegistrationByEmail(ctx context.Context, email string) (pendingRegistration, error) {
	const query = `
		SELECT
			id,
			full_name,
			bio,
			email,
			password_hash,
			cover_image_data_url,
			cover_image_content_type,
			token_hash,
			expires_at,
			created_at,
			updated_at
		FROM auth_pending_registrations
		WHERE email = $1
		  AND expires_at > NOW()
	`

	var pending pendingRegistration
	if err := r.db.QueryRow(ctx, query, email).Scan(
		&pending.ID,
		&pending.FullName,
		&pending.Bio,
		&pending.Email,
		&pending.PasswordHash,
		&pending.CoverImageDataURL,
		&pending.CoverImageContentType,
		&pending.TokenHash,
		&pending.ExpiresAt,
		&pending.CreatedAt,
		&pending.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pendingRegistration{}, ErrNotFound
		}
		return pendingRegistration{}, fmt.Errorf("select pending registration: %w", err)
	}

	return pending, nil
}

func (r *Repository) GetPendingRegistrationByToken(
	ctx context.Context,
	email string,
	tokenHash []byte,
) (pendingRegistration, error) {
	const query = `
		SELECT
			id,
			full_name,
			bio,
			email,
			password_hash,
			cover_image_data_url,
			cover_image_content_type,
			token_hash,
			expires_at,
			created_at,
			updated_at
		FROM auth_pending_registrations
		WHERE email = $1
		  AND token_hash = $2
		  AND expires_at > NOW()
	`

	var pending pendingRegistration
	if err := r.db.QueryRow(ctx, query, email, tokenHash).Scan(
		&pending.ID,
		&pending.FullName,
		&pending.Bio,
		&pending.Email,
		&pending.PasswordHash,
		&pending.CoverImageDataURL,
		&pending.CoverImageContentType,
		&pending.TokenHash,
		&pending.ExpiresAt,
		&pending.CreatedAt,
		&pending.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pendingRegistration{}, ErrNotFound
		}
		return pendingRegistration{}, fmt.Errorf("select pending registration by token: %w", err)
	}

	return pending, nil
}

func (r *Repository) UpdatePendingRegistrationToken(
	ctx context.Context,
	pendingID uuid.UUID,
	tokenHash []byte,
	expiresAt time.Time,
	updatedAt time.Time,
) error {
	const query = `
		UPDATE auth_pending_registrations
		SET token_hash = $2,
			expires_at = $3,
			updated_at = $4
		WHERE id = $1
	`

	result, err := r.db.Exec(ctx, query, pendingID, tokenHash, expiresAt, updatedAt)
	if err != nil {
		return fmt.Errorf("update pending registration token: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}

func (r *Repository) DeletePendingRegistration(ctx context.Context, pendingID uuid.UUID) error {
	const query = `DELETE FROM auth_pending_registrations WHERE id = $1`
	if _, err := r.db.Exec(ctx, query, pendingID); err != nil {
		return fmt.Errorf("delete pending registration: %w", err)
	}
	return nil
}

func (r *Repository) PromotePendingRegistration(
	ctx context.Context,
	pending pendingRegistration,
	user userRecord,
	session RefreshSession,
) error {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin pending registration promotion: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	const lockQuery = `
		SELECT id
		FROM auth_pending_registrations
		WHERE id = $1
		  AND email = $2
		  AND token_hash = $3
		  AND expires_at > NOW()
		FOR UPDATE
	`

	var pendingID uuid.UUID
	if err := tx.QueryRow(ctx, lockQuery, pending.ID, pending.Email, pending.TokenHash).Scan(&pendingID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock pending registration: %w", err)
	}

	const createUserQuery = `
		INSERT INTO clients (
			id,
			full_name,
			bio,
			cover_image_url,
			email,
			password_hash,
			email_verified_at,
			created_at,
			updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`

	if _, err := tx.Exec(
		ctx,
		createUserQuery,
		user.ID,
		user.FullName,
		user.Bio,
		nullIfEmpty(user.CoverImageURL),
		user.Email,
		user.PasswordHash,
		user.EmailVerifiedAt,
		user.CreatedAt,
		user.UpdatedAt,
	); err != nil {
		return fmt.Errorf("insert verified user: %w", err)
	}

	const createSessionQuery = `
		INSERT INTO auth_refresh_sessions (
			id,
			client_id,
			token_hash,
			user_agent,
			ip_address,
			expires_at,
			last_used_at,
			created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`

	if _, err := tx.Exec(
		ctx,
		createSessionQuery,
		session.ID,
		session.UserID,
		session.TokenHash,
		session.UserAgent,
		session.IPAddress,
		session.ExpiresAt,
		session.LastUsedAt,
		session.CreatedAt,
	); err != nil {
		return fmt.Errorf("insert registration refresh session: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM auth_pending_registrations WHERE id = $1`, pendingID); err != nil {
		return fmt.Errorf("consume pending registration: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit pending registration promotion: %w", err)
	}

	return nil
}

func (r *Repository) GetUserByEmail(ctx context.Context, email string) (userRecord, error) {
	const query = `
		SELECT id, full_name, bio, COALESCE(cover_image_url, ''), email, email_verified_at, password_hash, created_at, updated_at
		FROM clients
		WHERE email = $1
	`

	var record userRecord
	var verifiedAt sql.NullTime
	err := r.db.QueryRow(ctx, query, email).Scan(
		&record.ID,
		&record.FullName,
		&record.Bio,
		&record.CoverImageURL,
		&record.Email,
		&verifiedAt,
		&record.PasswordHash,
		&record.CreatedAt,
		&record.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return userRecord{}, ErrNotFound
		}
		return userRecord{}, fmt.Errorf("select user by email: %w", err)
	}

	record.EmailVerifiedAt = nullableTimePtr(verifiedAt)

	return record, nil
}

func (r *Repository) GetUserByID(ctx context.Context, id uuid.UUID) (User, error) {
	const query = `
		SELECT id, full_name, bio, COALESCE(cover_image_url, ''), email, email_verified_at, created_at, updated_at
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

func (r *Repository) UpdateUserPassword(ctx context.Context, userID uuid.UUID, passwordHash string, updatedAt time.Time) error {
	const query = `
		UPDATE clients
		SET password_hash = $2, updated_at = $3
		WHERE id = $1
	`

	if _, err := r.db.Exec(ctx, query, userID, passwordHash, updatedAt); err != nil {
		return fmt.Errorf("update user password: %w", err)
	}

	return nil
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
			created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
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
			u.id,
			u.full_name,
			u.bio,
			COALESCE(u.cover_image_url, ''),
			u.email,
			u.email_verified_at,
			u.created_at,
			u.updated_at
		FROM auth_refresh_sessions s
		JOIN clients u ON u.id = s.client_id
		WHERE s.token_hash = $1
		  AND s.expires_at > NOW()
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
		&record.User.ID,
		&record.User.FullName,
		&record.User.Bio,
		&record.User.CoverImageURL,
		&record.User.Email,
		&verifiedAt,
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

func (r *Repository) DeleteRefreshSessionsByUserID(ctx context.Context, userID uuid.UUID) error {
	const query = `DELETE FROM auth_refresh_sessions WHERE client_id = $1`
	if _, err := r.db.Exec(ctx, query, userID); err != nil {
		return fmt.Errorf("delete refresh sessions by user: %w", err)
	}
	return nil
}

func (r *Repository) CreatePasswordResetToken(ctx context.Context, userID uuid.UUID, tokenHash []byte, expiresAt time.Time) error {
	const query = `
		INSERT INTO auth_password_reset_tokens (id, client_id, token_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`
	if _, err := r.db.Exec(ctx, query, uuid.New(), userID, tokenHash, expiresAt, time.Now().UTC()); err != nil {
		return fmt.Errorf("insert password reset token: %w", err)
	}
	return nil
}

func (r *Repository) GetPasswordResetTokenUserID(ctx context.Context, tokenHash []byte) (uuid.UUID, error) {
	const query = `
		SELECT client_id
		FROM auth_password_reset_tokens
		WHERE token_hash = $1
		  AND expires_at > NOW()
	`
	var userID uuid.UUID
	if err := r.db.QueryRow(ctx, query, tokenHash).Scan(&userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrNotFound
		}
		return uuid.Nil, fmt.Errorf("select password reset token: %w", err)
	}
	return userID, nil
}

func (r *Repository) DeletePasswordResetTokensByUserID(ctx context.Context, userID uuid.UUID) error {
	const query = `DELETE FROM auth_password_reset_tokens WHERE client_id = $1`
	if _, err := r.db.Exec(ctx, query, userID); err != nil {
		return fmt.Errorf("delete password reset tokens: %w", err)
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

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
