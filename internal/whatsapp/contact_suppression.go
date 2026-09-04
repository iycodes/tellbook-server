package whatsapp

import (
	"context"
	"errors"
	"strings"
	"unicode"
)

const (
	SuppressionChannelEmail    = "email"
	SuppressionChannelWhatsApp = "whatsapp"
)

func (repository *ContactFoundationRepository) SetDestinationSuppression(
	ctx context.Context,
	channel, destination, reason, source string,
) error {
	fingerprint, err := repository.suppressionFingerprint(channel, destination)
	if err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	source = strings.TrimSpace(source)
	if !validSuppressionReason(reason) || !validSuppressionSource(source) {
		return errors.New("notification suppression reason or source is invalid")
	}
	_, err = repository.db.Exec(ctx, `
		INSERT INTO notification_contact_suppressions (
			channel, destination_hmac, reason, source, created_at, updated_at
		) VALUES ($1,$2,$3,$4,NOW(),NOW())
		ON CONFLICT (channel, destination_hmac) DO UPDATE SET
			reason=EXCLUDED.reason, source=EXCLUDED.source, updated_at=NOW()
		WHERE CASE EXCLUDED.reason
			WHEN 'manual' THEN 4 WHEN 'hard_bounce' THEN 3
			WHEN 'invalid_address' THEN 2 ELSE 1 END
		>= CASE notification_contact_suppressions.reason
			WHEN 'manual' THEN 4 WHEN 'hard_bounce' THEN 3
			WHEN 'invalid_address' THEN 2 ELSE 1 END
	`, channel, fingerprint, reason, source)
	return err
}

func (repository *ContactFoundationRepository) ClearDestinationSuppression(
	ctx context.Context,
	channel, destination string,
) error {
	fingerprint, err := repository.suppressionFingerprint(channel, destination)
	if err != nil {
		return err
	}
	_, err = repository.db.Exec(ctx, `
		DELETE FROM notification_contact_suppressions
		WHERE channel=$1 AND destination_hmac=$2
	`, channel, fingerprint)
	return err
}

func (repository *ContactFoundationRepository) DestinationSuppressed(
	ctx context.Context,
	channel, destination string,
) (bool, error) {
	fingerprint, err := repository.suppressionFingerprint(channel, destination)
	if err != nil {
		return false, err
	}
	var suppressed bool
	err = repository.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM notification_contact_suppressions
			WHERE channel=$1 AND destination_hmac=$2
		)
	`, channel, fingerprint).Scan(&suppressed)
	return suppressed, err
}

func (repository *ContactFoundationRepository) suppressionFingerprint(
	channel, destination string,
) ([]byte, error) {
	if repository == nil || repository.db == nil || len(repository.destinationHMACKey) < 32 {
		return nil, ErrNotificationFoundationUnavailable
	}
	channel = strings.TrimSpace(channel)
	switch channel {
	case SuppressionChannelEmail:
		destination = normalizeEmailDestination(destination)
		if destination == "" {
			return nil, errors.New("email destination is invalid")
		}
	case SuppressionChannelWhatsApp:
		var err error
		destination, err = normalizeInternationalE164(destination)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("notification suppression channel is invalid")
	}
	return destinationFingerprint(repository.destinationHMACKey, channel, destination), nil
}

func normalizeEmailDestination(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) == 0 || len(value) > 320 || strings.Count(value, "@") != 1 {
		return ""
	}
	parts := strings.SplitN(value, "@", 2)
	if parts[0] == "" || parts[1] == "" || strings.HasPrefix(parts[1], ".") ||
		strings.HasSuffix(parts[1], ".") || !strings.Contains(parts[1], ".") {
		return ""
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return ""
		}
	}
	return value
}

func validSuppressionReason(value string) bool {
	switch value {
	case "user_opt_out", "invalid_address", "hard_bounce", "manual":
		return true
	default:
		return false
	}
}

func validSuppressionSource(value string) bool {
	switch value {
	case "inbound_control", "customer_preference", "provider_response", "admin":
		return true
	default:
		return false
	}
}
