package whatsapp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrCustomerContactConflict   = errors.New("customer contact changed; reload before saving")
	ErrCustomerContactUnverified = errors.New("verify the customer contact before sharing it")
	ErrCustomerContactNotFound   = errors.New("business profile not found")
	ErrCustomerContactInvalid    = errors.New("invalid customer contact update")
)

type CustomerContact struct {
	Phone                   string     `json:"phone"`
	VerifiedAt              *time.Time `json:"verified_at,omitempty"`
	AllowBookingContact     bool       `json:"allow_booking_contact"`
	ShowOnPublicProfile     bool       `json:"show_on_public_profile"`
	Revision                int64      `json:"revision"`
	VerificationAvailable   bool       `json:"verification_available"`
	ReusableVerifiedNumbers []string   `json:"reusable_verified_numbers"`
}

type CustomerContactPatch struct {
	Revision            int64 `json:"revision"`
	AllowBookingContact *bool `json:"allow_booking_contact,omitempty"`
	ShowOnPublicProfile *bool `json:"show_on_public_profile,omitempty"`
}

func scanCustomerContact(row pgx.Row) (CustomerContact, error) {
	var item CustomerContact
	err := row.Scan(&item.Phone, &item.VerifiedAt, &item.AllowBookingContact, &item.ShowOnPublicProfile, &item.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrCustomerContactNotFound
	}
	item.ReusableVerifiedNumbers = []string{}
	return item, err
}

const customerContactSelect = `SELECT COALESCE(customer_contact_phone,''),customer_contact_verified_at,
 allow_booking_contact,show_contact_on_public_profile,customer_contact_revision
 FROM client_profiles WHERE client_id=$1`

func (r *ContactFoundationRepository) GetCustomerContact(ctx context.Context, clientID uuid.UUID) (CustomerContact, error) {
	item, err := scanCustomerContact(r.db.QueryRow(ctx, customerContactSelect, clientID))
	if err != nil {
		return item, err
	}
	item.VerificationAvailable = r.verificationAvailable
	rows, err := r.db.Query(ctx, `
 SELECT whatsapp_e164 FROM provider_notification_preferences
 WHERE client_id=$1 AND whatsapp_verified_at IS NOT NULL
 UNION SELECT normalized_identifier FROM provider_auth_identities
 WHERE client_id=$1 AND identity_type='phone' ORDER BY 1`, clientID)
	if err != nil {
		return item, err
	}
	defer rows.Close()
	for rows.Next() {
		var phone string
		if err := rows.Scan(&phone); err != nil {
			return item, err
		}
		item.ReusableVerifiedNumbers = append(item.ReusableVerifiedNumbers, phone)
	}
	return item, rows.Err()
}

// Every mutation locks the profile before its challenges. Revision checks stop a
// stale settings screen from publishing a newly changed number accidentally.
func (r *ContactFoundationRepository) lockCustomerContact(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, revision int64) (CustomerContact, error) {
	item, err := scanCustomerContact(tx.QueryRow(ctx, customerContactSelect+` FOR UPDATE`, clientID))
	if err == nil && (revision < 1 || item.Revision != revision) {
		err = ErrCustomerContactConflict
	}
	return item, err
}

func replanCustomerContactTx(ctx context.Context, tx pgx.Tx, clientID uuid.UUID) error {
	var revision int64
	err := tx.QueryRow(ctx, `INSERT INTO provider_notification_preferences(client_id) VALUES ($1)
 ON CONFLICT(client_id) DO UPDATE SET preference_revision=provider_notification_preferences.preference_revision+1,updated_at=NOW()
 RETURNING preference_revision`, clientID).Scan(&revision)
	if err != nil {
		return err
	}
	return enqueueScopeReplanTx(ctx, tx, clientID, revision)
}

func (r *ContactFoundationRepository) UpdateCustomerContact(ctx context.Context, clientID uuid.UUID, patch CustomerContactPatch) (CustomerContact, error) {
	if patch.AllowBookingContact == nil && patch.ShowOnPublicProfile == nil {
		return CustomerContact{}, ErrCustomerContactInvalid
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return CustomerContact{}, err
	}
	defer tx.Rollback(ctx)
	item, err := r.lockCustomerContact(ctx, tx, clientID, patch.Revision)
	if err != nil {
		return item, err
	}
	previousBooking, previousPublic := item.AllowBookingContact, item.ShowOnPublicProfile
	if patch.AllowBookingContact != nil {
		item.AllowBookingContact = *patch.AllowBookingContact
	}
	if patch.ShowOnPublicProfile != nil {
		item.ShowOnPublicProfile = *patch.ShowOnPublicProfile
	}
	if (item.AllowBookingContact || item.ShowOnPublicProfile) && item.VerifiedAt == nil {
		return item, ErrCustomerContactUnverified
	}
	if previousBooking == item.AllowBookingContact && previousPublic == item.ShowOnPublicProfile {
		if err := tx.Rollback(ctx); err != nil {
			return item, err
		}
		return r.GetCustomerContact(ctx, clientID)
	}
	_, err = tx.Exec(ctx, `UPDATE client_profiles SET allow_booking_contact=$2,show_contact_on_public_profile=$3,
 customer_contact_revision=customer_contact_revision+1,updated_at=NOW() WHERE client_id=$1`, clientID, item.AllowBookingContact, item.ShowOnPublicProfile)
	if err != nil {
		return item, err
	}
	if previousBooking != item.AllowBookingContact {
		if err := replanCustomerContactTx(ctx, tx, clientID); err != nil {
			return item, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return item, err
	}
	return r.GetCustomerContact(ctx, clientID)
}

func (r *ContactFoundationRepository) ReuseCustomerContact(ctx context.Context, clientID uuid.UUID, phone string, revision int64) (CustomerContact, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return CustomerContact{}, err
	}
	defer tx.Rollback(ctx)
	item, err := r.lockCustomerContact(ctx, tx, clientID, revision)
	if err != nil {
		return item, err
	}
	// Match the exact number selected by the provider, never silently choose a
	// different destination if the notification/account number has changed.
	var verifiedAt time.Time
	err = tx.QueryRow(ctx, `SELECT verified_at FROM (
 SELECT whatsapp_verified_at AS verified_at FROM provider_notification_preferences
 WHERE client_id=$1 AND whatsapp_e164=$2 AND whatsapp_verified_at IS NOT NULL
 UNION ALL SELECT verified_at FROM provider_auth_identities
 WHERE client_id=$1 AND identity_type='phone' AND normalized_identifier=$2
 ) evidence ORDER BY verified_at DESC LIMIT 1`, clientID, phone).Scan(&verifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, ErrCustomerContactUnverified
	}
	if err != nil {
		return item, err
	}
	if item.Phone == phone && item.VerifiedAt != nil {
		if err := tx.Commit(ctx); err != nil {
			return item, err
		}
		return r.GetCustomerContact(ctx, clientID)
	}
	if err := replaceCustomerContactTx(ctx, tx, clientID, phone, &verifiedAt, item.AllowBookingContact); err != nil {
		return item, err
	}
	if err := tx.Commit(ctx); err != nil {
		return item, err
	}
	return r.GetCustomerContact(ctx, clientID)
}

func replaceCustomerContactTx(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, phone string, verifiedAt *time.Time, wasSharedWithBookings bool) error {
	if _, err := tx.Exec(ctx, `UPDATE provider_customer_contact_challenges SET consumed_at=NOW()
 WHERE client_id=$1 AND consumed_at IS NULL`, clientID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE client_profiles SET customer_contact_phone=NULLIF($2,''),customer_contact_verified_at=$3,
 allow_booking_contact=FALSE,show_contact_on_public_profile=FALSE,
 customer_contact_revision=customer_contact_revision+1,updated_at=NOW() WHERE client_id=$1`, clientID, phone, verifiedAt); err != nil {
		return err
	}
	if wasSharedWithBookings {
		return replanCustomerContactTx(ctx, tx, clientID)
	}
	return nil
}

func (r *ContactFoundationRepository) RemoveCustomerContact(ctx context.Context, clientID uuid.UUID, revision int64) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	item, err := r.lockCustomerContact(ctx, tx, clientID, revision)
	if err != nil {
		return err
	}
	if err := replaceCustomerContactTx(ctx, tx, clientID, "", nil, item.AllowBookingContact); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *ContactFoundationRepository) StartCustomerContactVerification(ctx context.Context, clientID uuid.UUID, phone string, revision int64) (ProviderWhatsAppVerification, error) {
	if !r.verificationAvailable {
		return ProviderWhatsAppVerification{}, ErrProviderWhatsAppVerificationUnavailable
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return ProviderWhatsAppVerification{}, err
	}
	defer tx.Rollback(ctx)
	item, err := r.lockCustomerContact(ctx, tx, clientID, revision)
	if err != nil {
		return ProviderWhatsAppVerification{}, err
	}
	var country string
	if err := tx.QueryRow(ctx, `SELECT country_code FROM client_profiles WHERE client_id=$1 AND market_configured_at IS NOT NULL`, clientID).Scan(&country); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderWhatsAppVerification{}, ErrInvalidWhatsAppDestination
		}
		return ProviderWhatsAppVerification{}, err
	}
	normalized, err := NormalizeE164ForCountry(phone, country)
	if err != nil {
		return ProviderWhatsAppVerification{}, err
	}
	if item.Phone == normalized && item.VerifiedAt != nil {
		return ProviderWhatsAppVerification{}, ErrProviderWhatsAppAlreadyVerified
	}
	token, hash, err := newVerificationToken()
	if err != nil {
		return ProviderWhatsAppVerification{}, err
	}
	if err := replaceCustomerContactTx(ctx, tx, clientID, normalized, nil, item.AllowBookingContact); err != nil {
		return ProviderWhatsAppVerification{}, err
	}
	expiresAt := r.now().Add(verificationTTL)
	if _, err := tx.Exec(ctx, `INSERT INTO provider_customer_contact_challenges
 (id,client_id,token_hash,destination_hmac,contact_revision,expires_at,created_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7)`, uuid.New(), clientID, hash,
		destinationFingerprint(r.destinationHMACKey, "whatsapp", normalized), item.Revision+1, expiresAt, r.now()); err != nil {
		return ProviderWhatsAppVerification{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProviderWhatsAppVerification{}, err
	}
	return ProviderWhatsAppVerification{
		VerificationURL: "https://wa.me/" + strings.TrimPrefix(r.businessWhatsAppE164, "+") + "?text=" + url.QueryEscape("VERIFY "+token),
		Destination:     maskE164(normalized), ExpiresAt: expiresAt,
	}, nil
}

func (r *ContactFoundationRepository) consumeCustomerContactVerificationTx(ctx context.Context, tx pgx.Tx, senderHMAC []byte, rawToken string) error {
	hash := sha256.Sum256([]byte(rawToken))
	var clientID uuid.UUID
	err := tx.QueryRow(ctx, `SELECT client_id FROM provider_customer_contact_challenges WHERE token_hash=$1 AND consumed_at IS NULL`, hash[:]).Scan(&clientID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	item, err := scanCustomerContact(tx.QueryRow(ctx, customerContactSelect+` FOR UPDATE`, clientID))
	if err != nil {
		return err
	}
	var id uuid.UUID
	var expected []byte
	var revision int64
	var attempts int
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `SELECT id,destination_hmac,contact_revision,attempt_count,expires_at
 FROM provider_customer_contact_challenges WHERE token_hash=$1 AND consumed_at IS NULL FOR UPDATE`, hash[:]).Scan(&id, &expected, &revision, &attempts, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if revision != item.Revision || !expiresAt.After(r.now()) {
		_, err = tx.Exec(ctx, `UPDATE provider_customer_contact_challenges SET consumed_at=NOW() WHERE id=$1`, id)
		return err
	}
	if subtle.ConstantTimeCompare(expected, senderHMAC) != 1 {
		_, err = tx.Exec(ctx, `UPDATE provider_customer_contact_challenges SET attempt_count=$2::integer,
  consumed_at=CASE WHEN $2::integer>=5 THEN NOW() END WHERE id=$1`, id, attempts+1)
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE client_profiles SET customer_contact_verified_at=NOW(),
 customer_contact_revision=customer_contact_revision+1,updated_at=NOW() WHERE client_id=$1`, clientID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE provider_customer_contact_challenges SET consumed_at=NOW() WHERE id=$1`, id)
	return err
}
