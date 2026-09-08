package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"booking/go-server/internal/markets"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	providerReminderMinutes = 24 * 60
	verificationTTL         = 15 * time.Minute
	verificationMaxAttempts = 5
)

var (
	ErrNotificationFoundationUnavailable       = errors.New("notification contact foundation is unavailable")
	ErrProviderWhatsAppVerificationUnavailable = errors.New("provider WhatsApp verification is unavailable")
	ErrProviderWhatsAppUnverified              = errors.New("provider WhatsApp number is not verified")
	ErrProviderWhatsAppAlreadyVerified         = errors.New("provider WhatsApp number is already verified")
	ErrInvalidWhatsAppDestination              = errors.New("WhatsApp destination is invalid")
	ErrUnsupportedProviderReminderOffset       = errors.New("provider reminder offset is unsupported")
)

type ProviderNotificationPreferences struct {
	BookingEmail                  bool       `json:"booking_email"`
	BookingWhatsApp               bool       `json:"booking_whatsapp"`
	AppointmentReminderEnabled    bool       `json:"appointment_reminder_enabled"`
	AppointmentReminderMinutes    int        `json:"appointment_reminder_minutes"`
	EmailAvailable                bool       `json:"email_available"`
	EmailDeliveryEnabled          bool       `json:"email_delivery_enabled"`
	WhatsAppE164                  string     `json:"whatsapp_e164,omitempty"`
	WhatsAppVerifiedAt            *time.Time `json:"whatsapp_verified_at,omitempty"`
	WhatsAppVerificationAvailable bool       `json:"whatsapp_verification_available"`
	WhatsAppDeliveryEnabled       bool       `json:"whatsapp_delivery_enabled"`
	PreferenceRevision            int64      `json:"preference_revision"`
	UpdatedAt                     time.Time  `json:"updated_at"`
}

type ProviderNotificationPreferencesPatch struct {
	BookingEmail               *bool `json:"booking_email"`
	BookingWhatsApp            *bool `json:"booking_whatsapp"`
	AppointmentReminderEnabled *bool `json:"appointment_reminder_enabled"`
	AppointmentReminderMinutes *int  `json:"appointment_reminder_minutes"`
}

type ProviderWhatsAppVerification struct {
	VerificationURL string    `json:"verification_url"`
	Destination     string    `json:"destination"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type ContactFoundationRepository struct {
	db                      *pgxpool.Pool
	destinationHMACKey      []byte
	businessWhatsAppE164    string
	verificationAvailable   bool
	emailDeliveryEnabled    bool
	whatsAppDeliveryEnabled bool
	now                     func() time.Time
}

func NewContactFoundationRepository(
	db *pgxpool.Pool,
	destinationHMACKey, businessWhatsAppE164 string,
	verificationAvailable, emailDeliveryEnabled, whatsAppDeliveryEnabled bool,
) (*ContactFoundationRepository, error) {
	if db == nil || len(destinationHMACKey) < 32 {
		return nil, ErrNotificationFoundationUnavailable
	}
	normalizedBusinessNumber, err := normalizeInternationalE164(businessWhatsAppE164)
	if err != nil {
		return nil, fmt.Errorf("business WhatsApp number: %w", err)
	}
	return &ContactFoundationRepository{
		db: db, destinationHMACKey: []byte(destinationHMACKey),
		businessWhatsAppE164:    normalizedBusinessNumber,
		verificationAvailable:   verificationAvailable,
		emailDeliveryEnabled:    emailDeliveryEnabled,
		whatsAppDeliveryEnabled: whatsAppDeliveryEnabled,
		now:                     func() time.Time { return time.Now().UTC() },
	}, nil
}

func (repository *ContactFoundationRepository) GetProviderPreferences(
	ctx context.Context,
	clientID uuid.UUID,
) (ProviderNotificationPreferences, error) {
	if repository == nil || repository.db == nil || clientID == uuid.Nil {
		return ProviderNotificationPreferences{}, ErrNotificationFoundationUnavailable
	}
	item, err := scanProviderNotificationPreferences(repository.db.QueryRow(ctx, `
		SELECT
			COALESCE(preference.booking_email, TRUE),
			COALESCE(preference.booking_whatsapp, FALSE),
			COALESCE(preference.appointment_reminder_enabled, TRUE),
			COALESCE(preference.appointment_reminder_minutes, 1440),
			client.email_verified_at IS NOT NULL,
			COALESCE(preference.whatsapp_e164, ''),
			preference.whatsapp_verified_at,
			COALESCE(preference.preference_revision, 1),
			COALESCE(preference.updated_at, client.updated_at)
		FROM clients client
		LEFT JOIN provider_notification_preferences preference
			ON preference.client_id=client.id
		WHERE client.id=$1
	`, clientID))
	item.WhatsAppVerificationAvailable = repository.verificationAvailable
	item.EmailDeliveryEnabled = repository.emailDeliveryEnabled
	item.WhatsAppDeliveryEnabled = repository.whatsAppDeliveryEnabled
	return item, err
}

func (repository *ContactFoundationRepository) UpdateProviderPreferences(
	ctx context.Context,
	clientID uuid.UUID,
	patch ProviderNotificationPreferencesPatch,
) (ProviderNotificationPreferences, error) {
	if repository == nil || repository.db == nil || clientID == uuid.Nil {
		return ProviderNotificationPreferences{}, ErrNotificationFoundationUnavailable
	}
	if patch.AppointmentReminderMinutes != nil && *patch.AppointmentReminderMinutes != providerReminderMinutes {
		return ProviderNotificationPreferences{}, fmt.Errorf(
			"%w: appointment_reminder_minutes currently supports only %d",
			ErrUnsupportedProviderReminderOffset, providerReminderMinutes,
		)
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProviderNotificationPreferences{}, fmt.Errorf("begin provider notification preference update: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_notification_preferences (client_id)
		VALUES ($1) ON CONFLICT (client_id) DO NOTHING
	`, clientID); err != nil {
		return ProviderNotificationPreferences{}, fmt.Errorf("ensure provider notification preferences: %w", err)
	}
	current, err := scanProviderNotificationPreferences(tx.QueryRow(ctx, `
		SELECT preference.booking_email, preference.booking_whatsapp,
			preference.appointment_reminder_enabled, preference.appointment_reminder_minutes,
			client.email_verified_at IS NOT NULL, COALESCE(preference.whatsapp_e164, ''),
			preference.whatsapp_verified_at, preference.preference_revision, preference.updated_at
		FROM provider_notification_preferences preference
		JOIN clients client ON client.id=preference.client_id
		WHERE preference.client_id=$1
		FOR UPDATE OF preference
	`, clientID))
	if err != nil {
		return ProviderNotificationPreferences{}, err
	}
	current.WhatsAppVerificationAvailable = repository.verificationAvailable
	current.EmailDeliveryEnabled = repository.emailDeliveryEnabled
	current.WhatsAppDeliveryEnabled = repository.whatsAppDeliveryEnabled
	next := current
	if patch.BookingEmail != nil {
		next.BookingEmail = *patch.BookingEmail
	}
	if patch.BookingWhatsApp != nil {
		next.BookingWhatsApp = *patch.BookingWhatsApp
	}
	if patch.AppointmentReminderEnabled != nil {
		next.AppointmentReminderEnabled = *patch.AppointmentReminderEnabled
	}
	if patch.AppointmentReminderMinutes != nil {
		next.AppointmentReminderMinutes = *patch.AppointmentReminderMinutes
	}
	if next.BookingWhatsApp && current.WhatsAppVerifiedAt == nil {
		return ProviderNotificationPreferences{}, ErrProviderWhatsAppUnverified
	}
	changed := next.BookingEmail != current.BookingEmail ||
		next.BookingWhatsApp != current.BookingWhatsApp ||
		next.AppointmentReminderEnabled != current.AppointmentReminderEnabled ||
		next.AppointmentReminderMinutes != current.AppointmentReminderMinutes
	if changed {
		if err := tx.QueryRow(ctx, `
			UPDATE provider_notification_preferences
			SET booking_email=$2, booking_whatsapp=$3,
				appointment_reminder_enabled=$4, appointment_reminder_minutes=$5,
				preference_revision=preference_revision+1, updated_at=NOW()
			WHERE client_id=$1
			RETURNING preference_revision, updated_at
		`, clientID, next.BookingEmail, next.BookingWhatsApp,
			next.AppointmentReminderEnabled, next.AppointmentReminderMinutes,
		).Scan(&next.PreferenceRevision, &next.UpdatedAt); err != nil {
			return ProviderNotificationPreferences{}, fmt.Errorf("update provider notification preferences: %w", err)
		}
		if err := enqueueScopeReplanTx(ctx, tx, clientID, next.PreferenceRevision); err != nil {
			return ProviderNotificationPreferences{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ProviderNotificationPreferences{}, fmt.Errorf("commit provider notification preference update: %w", err)
	}
	return next, nil
}

func (repository *ContactFoundationRepository) StartProviderWhatsAppVerification(
	ctx context.Context,
	clientID uuid.UUID,
	destination string,
) (ProviderWhatsAppVerification, error) {
	if repository == nil || repository.db == nil || clientID == uuid.Nil {
		return ProviderWhatsAppVerification{}, ErrNotificationFoundationUnavailable
	}
	if !repository.verificationAvailable {
		return ProviderWhatsAppVerification{}, ErrProviderWhatsAppVerificationUnavailable
	}
	var countryCode string
	if err := repository.db.QueryRow(ctx, `
		SELECT profile.country_code
		FROM client_profiles profile
		WHERE profile.client_id=$1 AND profile.market_configured_at IS NOT NULL
	`, clientID).Scan(&countryCode); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderWhatsAppVerification{}, ErrInvalidWhatsAppDestination
		}
		return ProviderWhatsAppVerification{}, fmt.Errorf("load provider market: %w", err)
	}
	normalizedDestination, err := NormalizeE164ForCountry(destination, countryCode)
	if err != nil {
		return ProviderWhatsAppVerification{}, err
	}
	rawToken, tokenHash, err := newVerificationToken()
	if err != nil {
		return ProviderWhatsAppVerification{}, fmt.Errorf("generate provider WhatsApp verification token: %w", err)
	}
	now := repository.now()
	expiresAt := now.Add(verificationTTL)
	destinationHMAC := destinationFingerprint(repository.destinationHMACKey, "whatsapp", normalizedDestination)

	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProviderWhatsAppVerification{}, fmt.Errorf("begin provider WhatsApp verification: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_notification_preferences (client_id)
		VALUES ($1) ON CONFLICT (client_id) DO NOTHING
	`, clientID); err != nil {
		return ProviderWhatsAppVerification{}, fmt.Errorf("ensure provider notification preferences: %w", err)
	}
	var currentDestination string
	var verifiedAt *time.Time
	var preferenceRevision, verificationRevision int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(whatsapp_e164, ''), whatsapp_verified_at,
			preference_revision, whatsapp_verification_revision
		FROM provider_notification_preferences
		WHERE client_id=$1 FOR UPDATE
	`, clientID).Scan(&currentDestination, &verifiedAt, &preferenceRevision, &verificationRevision); err != nil {
		return ProviderWhatsAppVerification{}, fmt.Errorf("lock provider notification preferences: %w", err)
	}
	if currentDestination == normalizedDestination && verifiedAt != nil {
		return ProviderWhatsAppVerification{}, ErrProviderWhatsAppAlreadyVerified
	}
	if _, err := tx.Exec(ctx, `
		UPDATE provider_whatsapp_verification_challenges
		SET consumed_at=$2
		WHERE client_id=$1 AND consumed_at IS NULL
	`, clientID, now); err != nil {
		return ProviderWhatsAppVerification{}, fmt.Errorf("expire previous provider WhatsApp challenge: %w", err)
	}
	numberChanged := currentDestination != normalizedDestination
	verificationRevision++
	if numberChanged {
		preferenceRevision++
	}
	if _, err := tx.Exec(ctx, `
		UPDATE provider_notification_preferences
		SET whatsapp_e164=$2, whatsapp_verified_at=NULL,
			whatsapp_verification_method='', booking_whatsapp=FALSE,
			whatsapp_verification_revision=$3, preference_revision=$4, updated_at=$5
		WHERE client_id=$1
	`, clientID, normalizedDestination, verificationRevision, preferenceRevision, now); err != nil {
		return ProviderWhatsAppVerification{}, fmt.Errorf("prepare provider WhatsApp verification: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_whatsapp_verification_challenges (
			id, client_id, token_hash, destination_hmac, verification_revision,
			expires_at, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7)
	`, uuid.New(), clientID, tokenHash, destinationHMAC, verificationRevision, expiresAt, now); err != nil {
		return ProviderWhatsAppVerification{}, fmt.Errorf("create provider WhatsApp challenge: %w", err)
	}
	if numberChanged {
		if err := enqueueScopeReplanTx(ctx, tx, clientID, preferenceRevision); err != nil {
			return ProviderWhatsAppVerification{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ProviderWhatsAppVerification{}, fmt.Errorf("commit provider WhatsApp verification: %w", err)
	}
	verificationURL := "https://wa.me/" + strings.TrimPrefix(repository.businessWhatsAppE164, "+") +
		"?text=" + url.QueryEscape("VERIFY "+rawToken)
	return ProviderWhatsAppVerification{
		VerificationURL: verificationURL,
		Destination:     maskE164(normalizedDestination),
		ExpiresAt:       expiresAt,
	}, nil
}

func (repository *ContactFoundationRepository) applyInboundControlTx(
	ctx context.Context,
	tx pgx.Tx,
	control inboundControl,
) error {
	if control.kind != "stop" && control.kind != "start" && control.kind != "verify" {
		return nil
	}
	normalizedSender, err := normalizeInternationalE164(control.sender)
	if err != nil {
		return nil
	}
	senderHMAC := destinationFingerprint(repository.destinationHMACKey, "whatsapp", normalizedSender)
	switch control.kind {
	case "stop":
		_, err = tx.Exec(ctx, `
			INSERT INTO notification_contact_suppressions (
				channel, destination_hmac, reason, source, created_at, updated_at
			) VALUES ('whatsapp',$1,'user_opt_out','inbound_control',NOW(),NOW())
			ON CONFLICT (channel, destination_hmac) DO NOTHING
		`, senderHMAC)
	case "start":
		_, err = tx.Exec(ctx, `
			DELETE FROM notification_contact_suppressions
			WHERE channel='whatsapp' AND destination_hmac=$1
				AND reason='user_opt_out' AND source='inbound_control'
		`, senderHMAC)
	case "verify":
		err = repository.consumeVerificationTx(ctx, tx, senderHMAC, control.token)
		if err == nil {
			err = repository.consumeCustomerContactVerificationTx(ctx, tx, senderHMAC, control.token)
		}
	}
	if err != nil {
		return fmt.Errorf("apply inbound WhatsApp control: %w", err)
	}
	return nil
}

func (repository *ContactFoundationRepository) consumeVerificationTx(
	ctx context.Context,
	tx pgx.Tx,
	senderHMAC []byte,
	rawToken string,
) error {
	tokenHash := sha256.Sum256([]byte(rawToken))
	var challengeID, clientID uuid.UUID
	var expectedHMAC []byte
	var verificationRevision int64
	var attemptCount int
	var expiresAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT id, client_id, destination_hmac, verification_revision, attempt_count, expires_at
		FROM provider_whatsapp_verification_challenges
		WHERE token_hash=$1 AND consumed_at IS NULL
		FOR UPDATE
	`, tokenHash[:]).Scan(
		&challengeID, &clientID, &expectedHMAC, &verificationRevision, &attemptCount, &expiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !expiresAt.After(repository.now()) {
		_, err = tx.Exec(ctx, `
			UPDATE provider_whatsapp_verification_challenges SET consumed_at=NOW() WHERE id=$1
		`, challengeID)
		return err
	}
	if len(expectedHMAC) != sha256.Size || subtle.ConstantTimeCompare(expectedHMAC, senderHMAC) != 1 {
		attemptCount++
		_, err = tx.Exec(ctx, `
			UPDATE provider_whatsapp_verification_challenges
			SET attempt_count=$2::integer,
				consumed_at=CASE WHEN $2::integer >= $3::integer THEN NOW() ELSE NULL END
			WHERE id=$1
		`, challengeID, attemptCount, verificationMaxAttempts)
		return err
	}
	var preferenceRevision int64
	err = tx.QueryRow(ctx, `
		UPDATE provider_notification_preferences
		SET whatsapp_verified_at=NOW(), whatsapp_verification_method='inbound_challenge',
			preference_revision=preference_revision+1, updated_at=NOW()
		WHERE client_id=$1 AND whatsapp_verification_revision=$2
			AND whatsapp_e164 IS NOT NULL
			AND whatsapp_verified_at IS NULL
		RETURNING preference_revision
	`, clientID, verificationRevision).Scan(&preferenceRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `
			UPDATE provider_whatsapp_verification_challenges SET consumed_at=NOW() WHERE id=$1
		`, challengeID)
		return err
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE provider_whatsapp_verification_challenges SET consumed_at=NOW() WHERE id=$1
	`, challengeID); err != nil {
		return err
	}
	return enqueueScopeReplanTx(ctx, tx, clientID, preferenceRevision)
}

func enqueueScopeReplanTx(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, revision int64) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO notification_scope_replan_jobs (client_id, preference_revision)
		VALUES ($1,$2) ON CONFLICT (client_id, preference_revision) DO NOTHING
	`, clientID, revision); err != nil {
		return fmt.Errorf("enqueue notification scope replan: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify('tellbook_worker_core', 'notification_scope_replan')`); err != nil {
		return fmt.Errorf("notify notification scope replan: %w", err)
	}
	return nil
}

func scanProviderNotificationPreferences(row pgx.Row) (ProviderNotificationPreferences, error) {
	var item ProviderNotificationPreferences
	if err := row.Scan(
		&item.BookingEmail, &item.BookingWhatsApp,
		&item.AppointmentReminderEnabled, &item.AppointmentReminderMinutes,
		&item.EmailAvailable, &item.WhatsAppE164, &item.WhatsAppVerifiedAt,
		&item.PreferenceRevision, &item.UpdatedAt,
	); err != nil {
		return ProviderNotificationPreferences{}, fmt.Errorf("load provider notification preferences: %w", err)
	}
	return item, nil
}

func NormalizeE164ForCountry(value, countryCode string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "00") {
		return normalizeInternationalE164(value)
	}
	market, ok := markets.DefaultCatalog().Lookup(countryCode)
	if !ok {
		return "", ErrInvalidWhatsAppDestination
	}
	digits, ok := phoneDigits(value)
	if !ok {
		return "", ErrInvalidWhatsAppDestination
	}
	digits = strings.TrimPrefix(digits, "0")
	dialingCode := strings.TrimPrefix(market.DialingCode, "+")
	if strings.HasPrefix(digits, dialingCode) {
		return normalizeInternationalE164("+" + digits)
	}
	return normalizeInternationalE164("+" + dialingCode + digits)
}

func normalizeInternationalE164(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "00") {
		value = "+" + strings.TrimPrefix(value, "00")
	}
	if !strings.HasPrefix(value, "+") {
		value = "+" + value
	}
	digits, ok := phoneDigits(strings.TrimPrefix(value, "+"))
	if !ok || len(digits) < 8 || len(digits) > 15 || digits[0] == '0' {
		return "", ErrInvalidWhatsAppDestination
	}
	return "+" + digits, nil
}

func phoneDigits(value string) (string, bool) {
	var builder strings.Builder
	for _, character := range value {
		switch {
		case character >= '0' && character <= '9':
			builder.WriteRune(character)
		case character == ' ' || character == '-' || character == '(' || character == ')' || character == '.':
		default:
			return "", false
		}
	}
	return builder.String(), builder.Len() > 0
}

func destinationFingerprint(key []byte, channel, destination string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(channel))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(destination))
	return mac.Sum(nil)
}

func newVerificationToken() (string, []byte, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", nil, err
	}
	raw := base64.RawURLEncoding.EncodeToString(randomBytes)
	hash := sha256.Sum256([]byte(raw))
	return raw, hash[:], nil
}

func maskE164(value string) string {
	if len(value) <= 7 {
		return value
	}
	return value[:4] + strings.Repeat("•", len(value)-7) + value[len(value)-3:]
}
