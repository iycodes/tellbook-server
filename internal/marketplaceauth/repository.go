package marketplaceauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/authchallenge"
	"booking/go-server/internal/publictoken"
	"booking/go-server/internal/securityemail"
	"booking/go-server/internal/welcomeemail"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound         = errors.New("not found")
	ErrIdentityConflict = errors.New("identity is already linked")
	ErrInvalidRegion    = errors.New("invalid region selection")
	ErrInvalidLocation  = errors.New("invalid or expired location")
)

type Repository struct {
	additionalEmails bool
	db               *pgxpool.Pool
	welcomeURL       string
}

func (r *Repository) WithAdditionalEmails(enabled bool) *Repository {
	r.additionalEmails = enabled
	return r
}

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) WithWelcomeURL(actionURL string) *Repository {
	r.welcomeURL = actionURL
	return r
}

func (r *Repository) CompleteChallenge(ctx context.Context, challenge authchallenge.Challenge, session Session) (Customer, bool, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Customer{}, false, fmt.Errorf("begin marketplace challenge completion: %w", err)
	}
	defer tx.Rollback(ctx)

	var lockedID uuid.UUID
	err = tx.QueryRow(ctx, `
		UPDATE marketplace_auth_challenges SET consumed_at=NOW()
		WHERE id=$1 AND code_hash=$2 AND consumed_at IS NULL
		  AND purpose='sign_in' AND delivery_accepted_at IS NOT NULL
		  AND verify_expires_at>NOW() AND failed_attempts<6
		RETURNING id
	`, challenge.ID, challenge.CodeHash).Scan(&lockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Customer{}, false, ErrNotFound
	}
	if err != nil {
		return Customer{}, false, fmt.Errorf("consume marketplace challenge: %w", err)
	}

	// Serialize first-time sign-ins for one normalized identity so concurrent code
	// verification cannot create orphaned or duplicate customer records.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, challenge.IdentifierType+":"+challenge.Identifier); err != nil {
		return Customer{}, false, fmt.Errorf("lock marketplace identity: %w", err)
	}
	var customerID uuid.UUID
	newCustomer := false
	err = tx.QueryRow(ctx, `
		SELECT marketplace_customer_id
		FROM marketplace_customer_identities
		WHERE identifier_type=$1 AND normalized_identifier=$2
	`, challenge.IdentifierType, challenge.Identifier).Scan(&customerID)
	if errors.Is(err, pgx.ErrNoRows) {
		customerID = uuid.New()
		newCustomer = true
		now := time.Now().UTC()
		if err = insertCustomerForIdentity(ctx, tx, customerID, challenge.IdentifierType, challenge.Identifier, now); err != nil {
			return Customer{}, false, err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO marketplace_customer_identities (
				marketplace_customer_id, identifier_type, normalized_identifier, verified_at
			) VALUES ($1,$2,$3,$4)
		`, customerID, challenge.IdentifierType, challenge.Identifier, now)
	}
	if err != nil {
		return Customer{}, false, fmt.Errorf("resolve marketplace identity: %w", err)
	}
	if err := updateCustomerIdentityColumn(ctx, tx, customerID, challenge.IdentifierType, challenge.Identifier, time.Now().UTC()); err != nil {
		return Customer{}, false, err
	}

	session.CustomerID = customerID
	_, err = tx.Exec(ctx, `
		INSERT INTO marketplace_auth_sessions (
			id, marketplace_customer_id, token_hash, user_agent, ip_address,
			expires_at, last_used_at, created_at, session_revision
		) SELECT $1,$2,$3,$4,$5,$6,$7,$8,security_revision
		  FROM marketplace_customers WHERE id=$2
	`, session.ID, session.CustomerID, session.TokenHash, session.UserAgent, session.IPAddress,
		session.ExpiresAt, session.LastUsedAt, session.CreatedAt)
	if err != nil {
		return Customer{}, false, fmt.Errorf("create marketplace session: %w", err)
	}

	if _, err = tx.Exec(ctx, `
		INSERT INTO marketplace_notification_preferences (marketplace_customer_id)
		VALUES ($1) ON CONFLICT DO NOTHING
	`, customerID); err != nil {
		return Customer{}, false, fmt.Errorf("create notification preferences: %w", err)
	}
	if newCustomer && challenge.IdentifierType == "email" {
		if _, err := welcomeemail.AssignOptionalTx(ctx, tx, welcomeemail.Assignment{
			Audience:  welcomeemail.AudienceMarketplaceCustomer,
			AccountID: customerID,
			Email:     challenge.Identifier,
			ActionURL: r.welcomeURL,
		}); err != nil {
			return Customer{}, false, fmt.Errorf("assign marketplace welcome email: %w", err)
		}
	}

	customer, err := scanCustomer(tx.QueryRow(ctx, customerSelect+` WHERE c.id=$1`, customerID))
	if err != nil {
		return Customer{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Customer{}, false, fmt.Errorf("commit marketplace login: %w", err)
	}
	return customer, newCustomer, nil
}

func insertCustomerForIdentity(ctx context.Context, tx pgx.Tx, customerID uuid.UUID, identifierType, identifier string, now time.Time) error {
	var err error
	switch identifierType {
	case "email":
		_, err = tx.Exec(ctx, `
			INSERT INTO marketplace_customers (id, email, email_verified_at, created_at, updated_at)
			VALUES ($1,$2,$3,$3,$3)
		`, customerID, identifier, now)
	case "phone":
		_, err = tx.Exec(ctx, `
			INSERT INTO marketplace_customers (id, phone_e164, phone_verified_at, created_at, updated_at)
			VALUES ($1,$2,$3,$3,$3)
		`, customerID, identifier, now)
	default:
		return fmt.Errorf("unsupported identifier type %q", identifierType)
	}
	if err != nil {
		return fmt.Errorf("create marketplace customer: %w", err)
	}
	return nil
}

func (r *Repository) IdentityOwner(ctx context.Context, identifierType, identifier string) (uuid.UUID, error) {
	var customerID uuid.UUID
	err := r.db.QueryRow(ctx, `
		SELECT marketplace_customer_id
		FROM marketplace_customer_identities
		WHERE identifier_type=$1 AND normalized_identifier=$2
	`, identifierType, identifier).Scan(&customerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("load marketplace identity owner: %w", err)
	}
	return customerID, nil
}

func (r *Repository) CustomerIdentity(ctx context.Context, customerID uuid.UUID, identifierType string) (string, error) {
	var identifier string
	err := r.db.QueryRow(ctx, `
		SELECT normalized_identifier
		FROM marketplace_customer_identities
		WHERE marketplace_customer_id=$1 AND identifier_type=$2
	`, customerID, identifierType).Scan(&identifier)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load marketplace customer identity: %w", err)
	}
	return identifier, nil
}

func (r *Repository) CustomerHasVerifiedIdentityType(ctx context.Context, customerID uuid.UUID, identifierType string) (bool, error) {
	var verified bool
	err := r.db.QueryRow(ctx, `
		SELECT CASE $2
			WHEN 'email' THEN email_verified_at IS NOT NULL
			WHEN 'phone' THEN phone_verified_at IS NOT NULL
			ELSE FALSE
		END
		FROM marketplace_customers WHERE id=$1
	`, customerID, identifierType).Scan(&verified)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("load marketplace identity verification: %w", err)
	}
	return verified, nil
}

func (r *Repository) CompleteIdentityLink(ctx context.Context, challenge authchallenge.Challenge) (Customer, [][]byte, error) {
	if challenge.TargetAccountID == nil {
		return Customer{}, nil, ErrNotFound
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Customer{}, nil, fmt.Errorf("begin marketplace identity link: %w", err)
	}
	defer tx.Rollback(ctx)
	var securityBefore securityemail.Account
	if r.additionalEmails {
		securityBefore, err = securityemail.CaptureTx(ctx, tx, "marketplace_customer", *challenge.TargetAccountID)
		if err != nil {
			return Customer{}, nil, err
		}
	}

	var lockedID uuid.UUID
	err = tx.QueryRow(ctx, `
		UPDATE marketplace_auth_challenges SET consumed_at=NOW()
		WHERE id=$1 AND code_hash=$2 AND consumed_at IS NULL
		  AND purpose='link_identity' AND target_customer_id=$3
		  AND delivery_accepted_at IS NOT NULL AND verify_expires_at>NOW() AND failed_attempts<6
		RETURNING id
	`, challenge.ID, challenge.CodeHash, *challenge.TargetAccountID).Scan(&lockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Customer{}, nil, ErrNotFound
	}
	if err != nil {
		return Customer{}, nil, fmt.Errorf("consume identity link challenge: %w", err)
	}

	now := time.Now().UTC()
	result, err := tx.Exec(ctx, `
		INSERT INTO marketplace_customer_identities (
			marketplace_customer_id, identifier_type, normalized_identifier, verified_at
		) VALUES ($1,$2,$3,$4)
		ON CONFLICT DO NOTHING
	`, *challenge.TargetAccountID, challenge.IdentifierType, challenge.Identifier, now)
	if err != nil {
		return Customer{}, nil, fmt.Errorf("link marketplace identity: %w", err)
	}
	if result.RowsAffected() == 0 {
		var ownerID uuid.UUID
		err = tx.QueryRow(ctx, `
			SELECT marketplace_customer_id
			FROM marketplace_customer_identities
			WHERE normalized_identifier=$1 AND identifier_type=$2
		`, challenge.Identifier, challenge.IdentifierType).Scan(&ownerID)
		if err != nil || ownerID != *challenge.TargetAccountID {
			return Customer{}, nil, ErrIdentityConflict
		}
	}
	if err := updateCustomerIdentityColumn(ctx, tx, *challenge.TargetAccountID, challenge.IdentifierType, challenge.Identifier, now); err != nil {
		return Customer{}, nil, err
	}
	if challenge.IdentifierType == "email" {
		if _, err := welcomeemail.AssignOptionalTx(ctx, tx, welcomeemail.Assignment{
			Audience: welcomeemail.AudienceMarketplaceCustomer, AccountID: *challenge.TargetAccountID, ActionURL: r.welcomeURL,
			Email: challenge.Identifier,
		}); err != nil {
			return Customer{}, nil, fmt.Errorf("assign linked marketplace welcome email: %w", err)
		}
	}
	revokedHashes, err := marketplaceSessionHashesTx(ctx, tx, *challenge.TargetAccountID)
	if err != nil {
		return Customer{}, nil, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM marketplace_auth_sessions WHERE marketplace_customer_id=$1`, *challenge.TargetAccountID); err != nil {
		return Customer{}, nil, fmt.Errorf("revoke marketplace sessions after identity link: %w", err)
	}
	customer, err := scanCustomer(tx.QueryRow(ctx, customerSelect+` WHERE c.id=$1`, *challenge.TargetAccountID))
	if err != nil {
		return Customer{}, nil, err
	}
	if r.additionalEmails && result.RowsAffected() > 0 {
		recipient := securityBefore.Email
		if recipient == "" && challenge.IdentifierType == "email" {
			recipient = challenge.Identifier
		}
		details := map[string]string{}
		if challenge.IdentifierType == "phone" && len(challenge.Identifier) >= 4 {
			details["phone_last_four"] = challenge.Identifier[len(challenge.Identifier)-4:]
		}
		if err := securityemail.RecordTx(ctx, tx, "marketplace_customer", *challenge.TargetAccountID, recipient, challenge.IdentifierType+"_linked", details); err != nil {
			return Customer{}, nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Customer{}, nil, fmt.Errorf("commit marketplace identity link: %w", err)
	}
	return customer, revokedHashes, nil
}

func updateCustomerIdentityColumn(ctx context.Context, tx pgx.Tx, customerID uuid.UUID, identifierType, identifier string, verifiedAt time.Time) error {
	var err error
	switch identifierType {
	case "email":
		_, err = tx.Exec(ctx, `
			UPDATE marketplace_customers
			SET email=$2, email_verified_at=$3,
				security_revision=security_revision+1, updated_at=$3
			WHERE id=$1 AND (email IS DISTINCT FROM $2 OR email_verified_at IS NULL)
		`, customerID, identifier, verifiedAt)
	case "phone":
		_, err = tx.Exec(ctx, `
			UPDATE marketplace_customers
			SET phone_e164=$2, phone_verified_at=$3,
				security_revision=security_revision+1, updated_at=$3
			WHERE id=$1 AND (phone_e164 IS DISTINCT FROM $2 OR phone_verified_at IS NULL)
		`, customerID, identifier, verifiedAt)
	default:
		return fmt.Errorf("unsupported identifier type %q", identifierType)
	}
	if err != nil {
		return fmt.Errorf("update marketplace customer identity: %w", err)
	}
	return nil
}

type passwordCandidate struct {
	CustomerID   uuid.UUID
	PasswordHash string
}

func (r *Repository) PasswordCandidates(ctx context.Context, normalizedIdentifier string) ([]passwordCandidate, error) {
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT c.id, c.password_hash
		FROM marketplace_customer_identities identity
		JOIN marketplace_customers c ON c.id=identity.marketplace_customer_id
		WHERE identity.normalized_identifier=$1 AND c.password_hash IS NOT NULL
	`, normalizedIdentifier)
	if err != nil {
		return nil, fmt.Errorf("load password candidates: %w", err)
	}
	defer rows.Close()
	items := make([]passwordCandidate, 0, 2)
	for rows.Next() {
		var item passwordCandidate
		if err := rows.Scan(&item.CustomerID, &item.PasswordHash); err != nil {
			return nil, fmt.Errorf("scan password candidate: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) PasswordResetAccount(ctx context.Context, identifierType, identifier string) (passwordCandidate, error) {
	var candidate passwordCandidate
	err := r.db.QueryRow(ctx, `
		SELECT c.id, c.password_hash
		FROM marketplace_customer_identities identity
		JOIN marketplace_customers c ON c.id=identity.marketplace_customer_id
		WHERE identity.identifier_type=$1 AND identity.normalized_identifier=$2
		  AND c.password_hash IS NOT NULL
	`, identifierType, identifier).Scan(&candidate.CustomerID, &candidate.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return passwordCandidate{}, ErrNotFound
	}
	if err != nil {
		return passwordCandidate{}, fmt.Errorf("load marketplace password reset account: %w", err)
	}
	return candidate, nil
}

func (r *Repository) StorePasswordResetGrant(ctx context.Context, challenge authchallenge.Challenge, grantHash []byte, expiresAt time.Time) error {
	if challenge.TargetAccountID == nil {
		return ErrNotFound
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin marketplace reset verification: %w", err)
	}
	defer tx.Rollback(ctx)
	var consumedID uuid.UUID
	err = tx.QueryRow(ctx, `
		UPDATE marketplace_auth_challenges SET consumed_at=NOW()
		WHERE id=$1 AND code_hash=$2 AND purpose='password_reset'
		  AND target_customer_id=$3 AND consumed_at IS NULL
		  AND delivery_accepted_at IS NOT NULL AND verify_expires_at>NOW() AND failed_attempts<6
		RETURNING id
	`, challenge.ID, challenge.CodeHash, *challenge.TargetAccountID).Scan(&consumedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("consume marketplace reset challenge: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO auth_password_reset_grants (
			id,realm,marketplace_customer_id,marketplace_challenge_id,grant_hash,expires_at
		) VALUES ($1,'marketplace_customer',$2,$3,$4,$5)
	`, uuid.New(), *challenge.TargetAccountID, challenge.ID, grantHash, expiresAt)
	if err != nil {
		return fmt.Errorf("store marketplace password reset grant: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit marketplace reset verification: %w", err)
	}
	return nil
}

func (r *Repository) CompletePasswordReset(ctx context.Context, grantHash []byte, passwordHash string, session Session) (Customer, [][]byte, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Customer{}, nil, fmt.Errorf("begin marketplace password reset: %w", err)
	}
	defer tx.Rollback(ctx)
	var customerID uuid.UUID
	err = tx.QueryRow(ctx, `
		UPDATE auth_password_reset_grants SET consumed_at=NOW()
		WHERE realm='marketplace_customer' AND grant_hash=$1 AND consumed_at IS NULL AND expires_at>NOW()
		RETURNING marketplace_customer_id
	`, grantHash).Scan(&customerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Customer{}, nil, ErrNotFound
	}
	if err != nil {
		return Customer{}, nil, fmt.Errorf("consume marketplace password reset grant: %w", err)
	}
	var securityBefore securityemail.Account
	if r.additionalEmails {
		securityBefore, err = securityemail.CaptureTx(ctx, tx, "marketplace_customer", customerID)
		if err != nil {
			return Customer{}, nil, err
		}
	}
	if _, err = tx.Exec(ctx, `
		UPDATE marketplace_customers
		SET password_hash=$2,security_revision=security_revision+1,updated_at=NOW()
		WHERE id=$1
	`, customerID, passwordHash); err != nil {
		return Customer{}, nil, fmt.Errorf("reset marketplace password: %w", err)
	}
	revokedHashes, err := marketplaceSessionHashesTx(ctx, tx, customerID)
	if err != nil {
		return Customer{}, nil, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM marketplace_auth_sessions WHERE marketplace_customer_id=$1`, customerID); err != nil {
		return Customer{}, nil, fmt.Errorf("revoke marketplace sessions after reset: %w", err)
	}
	session.CustomerID = customerID
	if _, err = tx.Exec(ctx, `
		INSERT INTO marketplace_auth_sessions (
			id,marketplace_customer_id,token_hash,user_agent,ip_address,
			expires_at,last_used_at,created_at,session_revision
		) SELECT $1,$2,$3,$4,$5,$6,$7,$8,security_revision
		  FROM marketplace_customers WHERE id=$2
	`, session.ID, session.CustomerID, session.TokenHash, session.UserAgent, session.IPAddress,
		session.ExpiresAt, session.LastUsedAt, session.CreatedAt); err != nil {
		return Customer{}, nil, fmt.Errorf("create marketplace reset session: %w", err)
	}
	customer, err := scanCustomer(tx.QueryRow(ctx, customerSelect+` WHERE c.id=$1`, customerID))
	if err != nil {
		return Customer{}, nil, err
	}
	if r.additionalEmails {
		kind := "password_reset"
		if err := securityemail.RecordTx(ctx, tx, "marketplace_customer", customerID, securityBefore.Email, kind, nil); err != nil {
			return Customer{}, nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Customer{}, nil, fmt.Errorf("commit marketplace password reset: %w", err)
	}
	return customer, revokedHashes, nil
}

func (r *Repository) CreateSession(ctx context.Context, customerID uuid.UUID, session Session) (Customer, error) {
	_, err := r.db.Exec(ctx, `
		INSERT INTO marketplace_auth_sessions (
			id, marketplace_customer_id, token_hash, user_agent, ip_address,
			expires_at, last_used_at, created_at, session_revision
		) SELECT $1,$2,$3,$4,$5,$6,$7,$8,security_revision
		  FROM marketplace_customers WHERE id=$2
	`, session.ID, customerID, session.TokenHash, session.UserAgent, session.IPAddress,
		session.ExpiresAt, session.LastUsedAt, session.CreatedAt)
	if err != nil {
		return Customer{}, fmt.Errorf("create marketplace session: %w", err)
	}
	return scanCustomer(r.db.QueryRow(ctx, customerSelect+` WHERE c.id=$1`, customerID))
}

const customerSelect = `
	SELECT c.id, c.full_name, COALESCE(c.email,''), COALESCE(c.phone_e164,''),
	       c.email_verified_at, c.phone_verified_at,
	       COALESCE(to_char(c.birthday, 'YYYY-MM-DD'), ''), c.password_hash IS NOT NULL,
	       c.created_at, c.updated_at
	FROM marketplace_customers c`

type rowScanner interface{ Scan(...any) error }

func scanCustomer(row rowScanner) (Customer, error) {
	var customer Customer
	var emailVerified, phoneVerified sql.NullTime
	err := row.Scan(&customer.ID, &customer.FullName, &customer.Email, &customer.Phone,
		&emailVerified, &phoneVerified, &customer.Birthday, &customer.HasPassword,
		&customer.CreatedAt, &customer.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Customer{}, ErrNotFound
	}
	if err != nil {
		return Customer{}, fmt.Errorf("scan marketplace customer: %w", err)
	}
	if emailVerified.Valid {
		customer.EmailVerifiedAt = &emailVerified.Time
	}
	if phoneVerified.Valid {
		customer.PhoneVerifiedAt = &phoneVerified.Time
	}
	return customer, nil
}

func (r *Repository) CustomerBySessionTokenHash(ctx context.Context, tokenHash []byte) (Customer, error) {
	return scanCustomer(r.db.QueryRow(ctx, customerSelect+`
		JOIN marketplace_auth_sessions s ON s.marketplace_customer_id=c.id
		WHERE s.token_hash=$1 AND s.expires_at>NOW()
		  AND s.session_revision=c.security_revision
	`, tokenHash))
}

func (r *Repository) SessionPrincipalByTokenHash(ctx context.Context, tokenHash []byte) (SessionPrincipal, error) {
	var principal SessionPrincipal
	err := r.db.QueryRow(ctx, `
		SELECT s.id, s.marketplace_customer_id, s.expires_at,
		       c.security_revision, s.session_revision
		FROM marketplace_auth_sessions s
		JOIN marketplace_customers c ON c.id=s.marketplace_customer_id
		WHERE s.token_hash=$1 AND s.expires_at>NOW()
		  AND s.session_revision=c.security_revision
	`, tokenHash).Scan(
		&principal.SessionID, &principal.CustomerID, &principal.ExpiresAt,
		&principal.SecurityRevision, &principal.SessionRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionPrincipal{}, ErrNotFound
	}
	if err != nil {
		return SessionPrincipal{}, fmt.Errorf("load marketplace session principal: %w", err)
	}
	return principal, nil
}

func (r *Repository) CustomerByID(ctx context.Context, customerID uuid.UUID) (Customer, error) {
	return scanCustomer(r.db.QueryRow(ctx, customerSelect+` WHERE c.id=$1`, customerID))
}

func (r *Repository) ActiveSessionTokenHashesPage(
	ctx context.Context,
	customerID uuid.UUID,
	after []byte,
	limit int,
) ([][]byte, error) {
	if limit < 1 || limit > 256 {
		limit = 256
	}
	rows, err := r.db.Query(ctx, `
		SELECT token_hash
		FROM marketplace_auth_sessions
		WHERE marketplace_customer_id=$1 AND expires_at>NOW()
		  AND ($2::boolean=FALSE OR token_hash>$3)
		ORDER BY token_hash
		LIMIT $4
	`, customerID, len(after) > 0, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list marketplace session token hashes: %w", err)
	}
	defer rows.Close()
	hashes := make([][]byte, 0, limit)
	for rows.Next() {
		var tokenHash []byte
		if err := rows.Scan(&tokenHash); err != nil {
			return nil, fmt.Errorf("scan marketplace session token hash: %w", err)
		}
		hashes = append(hashes, tokenHash)
	}
	return hashes, rows.Err()
}

func (r *Repository) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := r.db.Exec(ctx, `DELETE FROM marketplace_auth_sessions WHERE token_hash=$1`, tokenHash)
	if err != nil {
		return fmt.Errorf("delete marketplace session: %w", err)
	}
	return nil
}

func (r *Repository) UpdateProfile(ctx context.Context, customerID uuid.UUID, input ProfileInput) (Customer, error) {
	_, err := r.db.Exec(ctx, `
		UPDATE marketplace_customers SET full_name=$2, birthday=NULLIF($3,'')::date, updated_at=NOW()
		WHERE id=$1
	`, customerID, input.FullName, input.Birthday)
	if err != nil {
		return Customer{}, fmt.Errorf("update marketplace profile: %w", err)
	}
	return scanCustomer(r.db.QueryRow(ctx, customerSelect+` WHERE c.id=$1`, customerID))
}

func (r *Repository) SetPassword(ctx context.Context, customerID uuid.UUID, passwordHash string) (Customer, [][]byte, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Customer{}, nil, fmt.Errorf("begin marketplace password change: %w", err)
	}
	defer tx.Rollback(ctx)
	var securityBefore securityemail.Account
	if r.additionalEmails {
		securityBefore, err = securityemail.CaptureTx(ctx, tx, "marketplace_customer", customerID)
		if err != nil {
			return Customer{}, nil, err
		}
	}
	result, err := tx.Exec(ctx, `
		UPDATE marketplace_customers
		SET password_hash=$2, security_revision=security_revision+1, updated_at=NOW()
		WHERE id=$1
	`, customerID, passwordHash)
	if err != nil {
		return Customer{}, nil, fmt.Errorf("set marketplace password: %w", err)
	}
	if result.RowsAffected() != 1 {
		return Customer{}, nil, ErrNotFound
	}
	revokedHashes, err := marketplaceSessionHashesTx(ctx, tx, customerID)
	if err != nil {
		return Customer{}, nil, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM marketplace_auth_sessions WHERE marketplace_customer_id=$1`, customerID); err != nil {
		return Customer{}, nil, fmt.Errorf("revoke marketplace sessions after password change: %w", err)
	}
	customer, err := scanCustomer(tx.QueryRow(ctx, customerSelect+` WHERE c.id=$1`, customerID))
	if err != nil {
		return Customer{}, nil, err
	}
	if r.additionalEmails {
		kind := "password_changed"
		if !securityBefore.HasPassword {
			kind = "password_set"
		}
		if err := securityemail.RecordTx(ctx, tx, "marketplace_customer", customerID, securityBefore.Email, kind, nil); err != nil {
			return Customer{}, nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Customer{}, nil, fmt.Errorf("commit marketplace password change: %w", err)
	}
	return customer, revokedHashes, nil
}

func marketplaceSessionHashesTx(ctx context.Context, tx pgx.Tx, customerID uuid.UUID) ([][]byte, error) {
	rows, err := tx.Query(ctx, `
		SELECT token_hash FROM marketplace_auth_sessions
		WHERE marketplace_customer_id=$1
	`, customerID)
	if err != nil {
		return nil, fmt.Errorf("list revoked marketplace session hashes: %w", err)
	}
	defer rows.Close()
	hashes := make([][]byte, 0, 4)
	for rows.Next() {
		var tokenHash []byte
		if err := rows.Scan(&tokenHash); err != nil {
			return nil, fmt.Errorf("scan revoked marketplace session hash: %w", err)
		}
		hashes = append(hashes, tokenHash)
	}
	return hashes, rows.Err()
}

func (r *Repository) PasswordHashByCustomerID(ctx context.Context, customerID uuid.UUID) (string, error) {
	var passwordHash sql.NullString
	err := r.db.QueryRow(ctx, `SELECT password_hash FROM marketplace_customers WHERE id=$1`, customerID).Scan(&passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load marketplace password hash: %w", err)
	}
	return passwordHash.String, nil
}

func (r *Repository) ListAddresses(ctx context.Context, customerID uuid.UUID) ([]Address, error) {
	rows, err := r.db.Query(ctx, addressSelect+` WHERE a.marketplace_customer_id=$1 ORDER BY a.is_default DESC, a.created_at`, customerID)
	if err != nil {
		return nil, fmt.Errorf("list marketplace addresses: %w", err)
	}
	defer rows.Close()
	items := make([]Address, 0)
	for rows.Next() {
		item, err := scanAddress(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) DefaultAddressLocation(ctx context.Context, customerID uuid.UUID) (SavedAddressLocation, error) {
	address, err := scanAddress(r.db.QueryRow(ctx, addressSelect+`
		WHERE a.marketplace_customer_id=$1 AND a.is_default=TRUE
	`, customerID))
	if err != nil {
		return SavedAddressLocation{}, err
	}
	parts := []string{address.AddressLine1, address.AddressLine2, address.Locality}
	formattedParts := parts[:0]
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			formattedParts = append(formattedParts, part)
		}
	}
	formattedAddress := strings.Join(formattedParts, ", ")
	resolutionStatus := "text_only"
	if address.Latitude != nil && address.Longitude != nil {
		resolutionStatus = "coordinates_resolved"
	}
	token, err := publictoken.New()
	if err != nil {
		return SavedAddressLocation{}, fmt.Errorf("create saved-address location token: %w", err)
	}
	expiresAt := time.Now().UTC().Add(30 * time.Minute)
	_, err = r.db.Exec(ctx, `
		INSERT INTO resolved_locations (
			id, public_token, provider, formatted_address, latitude, longitude,
			address_source, resolution_status, country_code, state_region_id,
			lga_region_id, locality, expires_at, created_at
		) VALUES ($1,$2,'manual',$3,$4,$5,'manual',$6,$7,$8,$9,$10,$11,NOW())
	`, uuid.New(), token, formattedAddress, address.Latitude, address.Longitude,
		resolutionStatus, address.CountryCode, address.StateRegionID, address.LGARegionID,
		address.Locality, expiresAt)
	if err != nil {
		return SavedAddressLocation{}, fmt.Errorf("store saved-address location token: %w", err)
	}
	result := SavedAddressLocation{
		LocationToken: token, FormattedAddress: formattedAddress, ResolutionStatus: resolutionStatus,
		CountryCode: address.CountryCode, StateName: address.StateName, LGAName: address.LGAName,
		Locality: address.Locality, ExpiresAt: expiresAt.Format(time.RFC3339),
	}
	if address.StateRegionID != nil {
		result.StateRegionID = address.StateRegionID.String()
	}
	if address.LGARegionID != nil {
		result.LGARegionID = address.LGARegionID.String()
	}
	return result, nil
}

const addressSelect = `
	SELECT a.id, a.label, a.address_line_1, a.address_line_2, a.locality,
	       a.state_region_id, COALESCE(state.name,''), a.lga_region_id, COALESCE(lga.name,''),
	       a.country_code, a.postal_code, a.latitude, a.longitude, a.is_default,
	       a.created_at, a.updated_at
	FROM marketplace_customer_addresses a
	LEFT JOIN administrative_regions state ON state.id=a.state_region_id
	LEFT JOIN administrative_regions lga ON lga.id=a.lga_region_id`

func scanAddress(row rowScanner) (Address, error) {
	var item Address
	var stateID, lgaID uuid.NullUUID
	var latitude, longitude sql.NullFloat64
	err := row.Scan(&item.ID, &item.Label, &item.AddressLine1, &item.AddressLine2, &item.Locality,
		&stateID, &item.StateName, &lgaID, &item.LGAName, &item.CountryCode, &item.PostalCode,
		&latitude, &longitude, &item.IsDefault, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Address{}, ErrNotFound
	}
	if err != nil {
		return Address{}, fmt.Errorf("scan marketplace address: %w", err)
	}
	if stateID.Valid {
		item.StateRegionID = &stateID.UUID
	}
	if lgaID.Valid {
		item.LGARegionID = &lgaID.UUID
	}
	if latitude.Valid {
		item.Latitude = &latitude.Float64
	}
	if longitude.Valid {
		item.Longitude = &longitude.Float64
	}
	return item, nil
}

func (r *Repository) SaveAddress(ctx context.Context, customerID uuid.UUID, addressID *uuid.UUID, input AddressInput) (Address, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Address{}, err
	}
	defer tx.Rollback(ctx)
	if input.LocationToken != "" {
		if err := applyResolvedLocationToAddress(ctx, tx, &input); err != nil {
			return Address{}, err
		}
	}
	stateID, err := optionalUUID(input.StateRegionID)
	if err != nil {
		return Address{}, err
	}
	lgaID, err := optionalUUID(input.LGARegionID)
	if err != nil {
		return Address{}, err
	}
	if err := validateAddressRegions(ctx, tx, input.CountryCode, stateID, lgaID); err != nil {
		return Address{}, err
	}
	if input.IsDefault {
		if _, err := tx.Exec(ctx, `UPDATE marketplace_customer_addresses SET is_default=FALSE, updated_at=NOW() WHERE marketplace_customer_id=$1`, customerID); err != nil {
			return Address{}, fmt.Errorf("clear default marketplace address: %w", err)
		}
	}
	id := uuid.New()
	if addressID == nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO marketplace_customer_addresses (
				id, marketplace_customer_id, label, address_line_1, address_line_2, locality,
				state_region_id, lga_region_id, country_code, postal_code, latitude, longitude, is_default
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		`, id, customerID, input.Label, input.AddressLine1, input.AddressLine2, input.Locality,
			stateID, lgaID, input.CountryCode, input.PostalCode, input.Latitude, input.Longitude, input.IsDefault)
	} else {
		id = *addressID
		result, updateErr := tx.Exec(ctx, `
			UPDATE marketplace_customer_addresses SET label=$3, address_line_1=$4, address_line_2=$5,
			locality=$6, state_region_id=$7, lga_region_id=$8, country_code=$9, postal_code=$10,
			latitude=$11, longitude=$12, is_default=$13, updated_at=NOW()
			WHERE id=$1 AND marketplace_customer_id=$2
		`, id, customerID, input.Label, input.AddressLine1, input.AddressLine2, input.Locality,
			stateID, lgaID, input.CountryCode, input.PostalCode, input.Latitude, input.Longitude, input.IsDefault)
		err = updateErr
		if err == nil && result.RowsAffected() == 0 {
			return Address{}, ErrNotFound
		}
	}
	if err != nil {
		return Address{}, fmt.Errorf("save marketplace address: %w", err)
	}
	item, err := scanAddress(tx.QueryRow(ctx, addressSelect+` WHERE a.id=$1 AND a.marketplace_customer_id=$2`, id, customerID))
	if err != nil {
		return Address{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Address{}, err
	}
	return item, nil
}

func applyResolvedLocationToAddress(ctx context.Context, tx pgx.Tx, input *AddressInput) error {
	var formattedAddress, countryCode, locality string
	var stateID, lgaID uuid.NullUUID
	var latitude, longitude sql.NullFloat64
	err := tx.QueryRow(ctx, `
		SELECT formatted_address, COALESCE(country_code, ''), state_region_id, lga_region_id,
		       locality, latitude::double precision, longitude::double precision
		FROM resolved_locations
		WHERE public_token=$1 AND expires_at>NOW()
	`, input.LocationToken).Scan(&formattedAddress, &countryCode, &stateID, &lgaID, &locality, &latitude, &longitude)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidLocation
	}
	if err != nil {
		return fmt.Errorf("load saved-address location: %w", err)
	}
	if formattedAddress != "" {
		input.AddressLine1 = formattedAddress
	}
	if countryCode != "" {
		input.CountryCode = countryCode
	}
	if locality != "" {
		input.Locality = locality
	}
	if stateID.Valid {
		input.StateRegionID = stateID.UUID.String()
	}
	if lgaID.Valid {
		input.LGARegionID = lgaID.UUID.String()
	}
	if latitude.Valid && longitude.Valid {
		input.Latitude = &latitude.Float64
		input.Longitude = &longitude.Float64
	}
	return nil
}

func validateAddressRegions(ctx context.Context, tx pgx.Tx, countryCode string, stateID, lgaID *uuid.UUID) error {
	if countryCode != "NG" {
		return ErrInvalidRegion
	}
	if lgaID != nil && stateID == nil {
		return ErrInvalidRegion
	}
	if stateID == nil {
		return nil
	}
	var valid bool
	if lgaID == nil {
		err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM administrative_regions
				WHERE id=$1 AND level='state' AND country_code=$2
			)
		`, *stateID, countryCode).Scan(&valid)
		if err != nil {
			return fmt.Errorf("validate address state: %w", err)
		}
	} else {
		err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM administrative_regions lga
				JOIN administrative_regions state ON state.id=lga.parent_id
				WHERE state.id=$1 AND state.level='state' AND state.country_code=$3
				  AND lga.id=$2 AND lga.level='lga' AND lga.country_code=$3
			)
		`, *stateID, *lgaID, countryCode).Scan(&valid)
		if err != nil {
			return fmt.Errorf("validate address LGA: %w", err)
		}
	}
	if !valid {
		return ErrInvalidRegion
	}
	return nil
}

func optionalUUID(value string) (*uuid.UUID, error) {
	if value == "" {
		return nil, nil
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("region id must be a UUID")
	}
	return &id, nil
}

func (r *Repository) DeleteAddress(ctx context.Context, customerID, addressID uuid.UUID) error {
	result, err := r.db.Exec(ctx, `DELETE FROM marketplace_customer_addresses WHERE id=$1 AND marketplace_customer_id=$2`, addressID, customerID)
	if err != nil {
		return fmt.Errorf("delete marketplace address: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) GetPreferences(ctx context.Context, customerID uuid.UUID) (NotificationPreferences, error) {
	var item NotificationPreferences
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(preference.booking_email,TRUE),COALESCE(preference.booking_sms,FALSE),
			COALESCE(preference.booking_whatsapp,FALSE),COALESCE(preference.marketing_email,FALSE),
			customer.email_verified_at IS NOT NULL,customer.phone_verified_at IS NOT NULL,
			COALESCE(preference.updated_at,customer.updated_at)
		FROM marketplace_customers customer
		LEFT JOIN marketplace_notification_preferences preference
			ON preference.marketplace_customer_id=customer.id
		WHERE customer.id=$1
	`, customerID).Scan(
		&item.BookingEmail, &item.BookingSMS, &item.BookingWhatsApp, &item.MarketingEmail,
		&item.EmailAvailable, &item.WhatsAppAvailable, &item.UpdatedAt,
	)
	if err != nil {
		return item, fmt.Errorf("load marketplace preferences: %w", err)
	}
	return item, nil
}

func (r *Repository) UpdatePreferences(ctx context.Context, customerID uuid.UUID, input NotificationPreferencesInput) (NotificationPreferences, error) {
	_, err := r.db.Exec(ctx, `
		INSERT INTO marketplace_notification_preferences (
			marketplace_customer_id, booking_email, booking_sms, booking_whatsapp, marketing_email, updated_at
		) VALUES ($1,$2,$3,$4,$5,NOW())
		ON CONFLICT (marketplace_customer_id) DO UPDATE SET
			booking_email=EXCLUDED.booking_email, booking_sms=EXCLUDED.booking_sms,
			booking_whatsapp=EXCLUDED.booking_whatsapp, marketing_email=EXCLUDED.marketing_email,
			updated_at=NOW()
	`, customerID, input.BookingEmail, input.BookingSMS, input.BookingWhatsApp, input.MarketingEmail)
	if err != nil {
		return NotificationPreferences{}, fmt.Errorf("update marketplace preferences: %w", err)
	}
	return r.GetPreferences(ctx, customerID)
}
