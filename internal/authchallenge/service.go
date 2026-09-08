package authchallenge

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"booking/go-server/internal/secure"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	RealmProvider            = "provider"
	RealmMarketplaceCustomer = "marketplace_customer"
	ChannelEmail             = "email"
	ChannelWhatsApp          = "whatsapp"
	PurposeSignIn            = "sign_in"
	PurposeLinkIdentity      = "link_identity"
	PurposePasswordReset     = "password_reset"

	codeLength        = 6
	verificationTTL   = 10 * time.Minute
	deliveryDeadline  = 90 * time.Second
	challengeCooldown = 45 * time.Second
)

var (
	ErrUnavailable       = errors.New("authentication delivery channel is unavailable")
	ErrInvalidIdentifier = errors.New("invalid authentication identifier")
	ErrTooSoon           = errors.New("authentication challenge requested too recently")
	ErrInvalidChallenge  = errors.New("authentication challenge is invalid or expired")
	ErrNotFound          = errors.New("authentication challenge was not found")
)

type Config struct {
	EmailEnabled    bool
	WhatsAppEnabled bool
	EncryptionKeys  string
	ActiveKey       string
	DestinationKey  string
}

type Service struct {
	db              *pgxpool.Pool
	keyring         *secure.Keyring
	destinationKey  []byte
	emailEnabled    bool
	whatsAppEnabled bool
	now             func() time.Time
}

type Challenge struct {
	ID                 uuid.UUID
	Realm              string
	IdentifierType     string
	Identifier         string
	DeliveryChannel    string
	Purpose            string
	TargetAccountID    *uuid.UUID
	CodeHash           []byte
	FailedAttempts     int
	DeliveryDeadline   time.Time
	DeliveryAcceptedAt *time.Time
	VerifyExpiresAt    *time.Time
	ConsumedAt         *time.Time
	CreatedAt          time.Time
}

type Response struct {
	ChallengeID                  uuid.UUID `json:"challenge_id"`
	IdentifierType               string    `json:"identifier_type"`
	DeliveryChannel              string    `json:"delivery_channel"`
	DestinationHint              string    `json:"destination_hint"`
	DeliveryState                string    `json:"delivery_state"`
	DeliveryExpiresInSeconds     int       `json:"delivery_expires_in_seconds"`
	VerificationExpiresInSeconds *int      `json:"verification_expires_in_seconds"`
	ResendAvailableInSeconds     int       `json:"resend_available_in_seconds"`
	NextStatusCheckInSeconds     *int      `json:"next_status_check_in_seconds"`
}

type Capabilities struct {
	Channels                  []string `json:"channels"`
	CodeLength                int      `json:"code_length"`
	PasswordFallbackAvailable bool     `json:"password_fallback_available"`
}

func NewCapabilities(service *Service, passwordFallbackAvailable bool) Capabilities {
	channels := []string{}
	if service != nil {
		channels = service.Capabilities()
	}
	return Capabilities{
		Channels:                  channels,
		CodeLength:                codeLength,
		PasswordFallbackAvailable: passwordFallbackAvailable,
	}
}

type StartRequest struct {
	Realm           string
	RawIdentifier   string
	Channel         string
	Purpose         string
	TargetAccountID *uuid.UUID
}

type deliveryPayload struct {
	Destination string                `json:"destination"`
	Code        string                `json:"code,omitempty"`
	Security    *securityEmailPayload `json:"security,omitempty"`
	Link        *linkEmailPayload     `json:"link,omitempty"`
}

func NewService(db *pgxpool.Pool, cfg Config) (*Service, error) {
	if db == nil {
		return nil, errors.New("auth challenge database is required")
	}
	if !cfg.EmailEnabled && !cfg.WhatsAppEnabled {
		return &Service{db: db, now: func() time.Time { return time.Now().UTC() }}, nil
	}
	keyring, err := secure.ParseKeyring(cfg.EncryptionKeys, cfg.ActiveKey)
	if err != nil {
		return nil, fmt.Errorf("configure auth delivery encryption: %w", err)
	}
	if len(cfg.DestinationKey) < 32 {
		return nil, errors.New("auth destination HMAC key must be at least 32 bytes")
	}
	return &Service{
		db: db, keyring: keyring, destinationKey: []byte(cfg.DestinationKey),
		emailEnabled: cfg.EmailEnabled, whatsAppEnabled: cfg.WhatsAppEnabled,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *Service) Capabilities() []string {
	channels := make([]string, 0, 2)
	if s != nil && s.emailEnabled {
		channels = append(channels, ChannelEmail)
	}
	if s != nil && s.whatsAppEnabled {
		channels = append(channels, ChannelWhatsApp)
	}
	return channels
}

func NormalizeIdentifier(rawIdentifier, requestedChannel string) (string, string, string, error) {
	rawIdentifier = strings.TrimSpace(rawIdentifier)
	requestedChannel = strings.ToLower(strings.TrimSpace(requestedChannel))
	if strings.Contains(rawIdentifier, "@") {
		if requestedChannel != ChannelEmail {
			return "", "", "", fmt.Errorf("%w: email identifiers require email delivery", ErrInvalidIdentifier)
		}
		parsed, err := mail.ParseAddress(rawIdentifier)
		if err != nil || !strings.EqualFold(parsed.Address, rawIdentifier) || len(parsed.Address) > 320 {
			return "", "", "", fmt.Errorf("%w: enter a valid email address", ErrInvalidIdentifier)
		}
		return "email", strings.ToLower(parsed.Address), ChannelEmail, nil
	}
	if requestedChannel != ChannelWhatsApp {
		return "", "", "", fmt.Errorf("%w: phone identifiers require WhatsApp delivery", ErrInvalidIdentifier)
	}
	phone, err := normalizePhone(rawIdentifier)
	if err != nil {
		return "", "", "", fmt.Errorf("%w: %v", ErrInvalidIdentifier, err)
	}
	return "phone", phone, ChannelWhatsApp, nil
}

func (s *Service) Start(ctx context.Context, request StartRequest) (Response, error) {
	if s == nil || s.db == nil {
		return Response{}, ErrUnavailable
	}
	identifierType, identifier, channel, err := NormalizeIdentifier(request.RawIdentifier, request.Channel)
	if err != nil {
		return Response{}, err
	}
	if !s.channelEnabled(channel) || s.keyring == nil || len(s.destinationKey) < 32 {
		return Response{}, ErrUnavailable
	}
	if err := validateScope(request.Realm, request.Purpose, request.TargetAccountID); err != nil {
		return Response{}, err
	}
	code, err := generateCode()
	if err != nil {
		return Response{}, err
	}
	now := s.now()
	challenge := Challenge{
		ID: uuid.New(), Realm: request.Realm, IdentifierType: identifierType, Identifier: identifier,
		DeliveryChannel: channel, Purpose: request.Purpose, TargetAccountID: request.TargetAccountID,
		CodeHash: hashCode(code), DeliveryDeadline: now.Add(deliveryDeadline), CreatedAt: now,
	}
	jobID := uuid.New()
	payload, err := json.Marshal(deliveryPayload{Destination: identifier, Code: code})
	if err != nil {
		return Response{}, err
	}
	ciphertext, err := s.keyring.Encrypt(payload, deliveryAAD(jobID))
	if err != nil {
		return Response{}, fmt.Errorf("encrypt auth delivery: %w", err)
	}
	if err := s.insertChallengeAndDelivery(ctx, challenge, jobID, ciphertext); err != nil {
		return Response{}, err
	}
	return queuedResponse(challenge, now), nil
}

// StartSyntheticPasswordReset creates a challenge-shaped response without an
// outbound delivery job. It keeps reset initiation indistinguishable when the
// identifier is unknown or belongs to an account without a password.
func (s *Service) StartSyntheticPasswordReset(ctx context.Context, realm, rawIdentifier, channel string) (Response, error) {
	if s == nil || s.db == nil {
		return Response{}, ErrUnavailable
	}
	identifierType, identifier, normalizedChannel, err := NormalizeIdentifier(rawIdentifier, channel)
	if err != nil {
		return Response{}, err
	}
	if !s.channelEnabled(normalizedChannel) {
		return Response{}, ErrUnavailable
	}
	now := s.now()
	challenge := Challenge{
		ID: uuid.New(), Realm: realm, IdentifierType: identifierType, Identifier: identifier,
		DeliveryChannel: normalizedChannel, Purpose: PurposePasswordReset,
		CodeHash: hashCode(uuid.NewString()), DeliveryDeadline: now.Add(deliveryDeadline), CreatedAt: now,
	}
	if err := validateScope(realm, PurposePasswordReset, nil); err != nil {
		return Response{}, err
	}
	if err := s.insertSyntheticChallenge(ctx, challenge); err != nil {
		return Response{}, err
	}
	return queuedResponse(challenge, now), nil
}

// VerifyPasswordReset deliberately derives the target from the stored
// challenge. Callers cannot choose an account, and synthetic challenges fail
// through the same public error as an invalid code.
func (s *Service) VerifyPasswordReset(ctx context.Context, realm string, challengeID uuid.UUID, rawCode string) (Challenge, error) {
	challenge, deliveryState, err := s.load(ctx, realm, challengeID)
	if err != nil || challenge.Purpose != PurposePasswordReset || challenge.TargetAccountID == nil ||
		challenge.ConsumedAt != nil || challenge.DeliveryAcceptedAt == nil || challenge.VerifyExpiresAt == nil ||
		!challenge.VerifyExpiresAt.After(s.now()) || challenge.FailedAttempts >= 6 ||
		(deliveryState != "accepted" && deliveryState != "sent" && deliveryState != "delivered") {
		return Challenge{}, ErrInvalidChallenge
	}
	rawCode = strings.TrimSpace(rawCode)
	if !validCode(rawCode) || !hmac.Equal(challenge.CodeHash, hashCode(rawCode)) {
		_ = s.recordFailedAttempt(ctx, challenge)
		return Challenge{}, ErrInvalidChallenge
	}
	return challenge, nil
}

func (s *Service) Resend(ctx context.Context, realm string, challengeID uuid.UUID, purpose string, targetAccountID *uuid.UUID) (Response, error) {
	challenge, _, err := s.load(ctx, realm, challengeID)
	if err != nil || challenge.Purpose != purpose || challenge.ConsumedAt != nil || !sameOptionalUUID(challenge.TargetAccountID, targetAccountID) {
		return Response{}, ErrInvalidChallenge
	}
	return s.Start(ctx, StartRequest{
		Realm: realm, RawIdentifier: challenge.Identifier, Channel: challenge.DeliveryChannel,
		Purpose: purpose, TargetAccountID: targetAccountID,
	})
}

func (s *Service) Status(ctx context.Context, realm string, challengeID uuid.UUID, purpose string, targetAccountID *uuid.UUID) (Response, error) {
	challenge, deliveryState, err := s.load(ctx, realm, challengeID)
	if err != nil || challenge.Purpose != purpose || challenge.ConsumedAt != nil || !sameOptionalUUID(challenge.TargetAccountID, targetAccountID) {
		return Response{}, ErrInvalidChallenge
	}
	now := s.now()
	if challenge.VerifyExpiresAt != nil && !challenge.VerifyExpiresAt.After(now) {
		deliveryState = "expired"
	}
	response := responseFor(challenge, deliveryState, now)
	return response, nil
}

func (s *Service) Verify(ctx context.Context, realm string, challengeID uuid.UUID, rawCode, purpose string, targetAccountID *uuid.UUID) (Challenge, error) {
	challenge, deliveryState, err := s.load(ctx, realm, challengeID)
	if err != nil || challenge.Purpose != purpose || !sameOptionalUUID(challenge.TargetAccountID, targetAccountID) ||
		challenge.ConsumedAt != nil || challenge.DeliveryAcceptedAt == nil || challenge.VerifyExpiresAt == nil ||
		!challenge.VerifyExpiresAt.After(s.now()) || challenge.FailedAttempts >= 6 ||
		(deliveryState != "accepted" && deliveryState != "sent" && deliveryState != "delivered") {
		return Challenge{}, ErrInvalidChallenge
	}
	rawCode = strings.TrimSpace(rawCode)
	if !validCode(rawCode) || !hmac.Equal(challenge.CodeHash, hashCode(rawCode)) {
		_ = s.recordFailedAttempt(ctx, challenge)
		return Challenge{}, ErrInvalidChallenge
	}
	return challenge, nil
}

func (s *Service) channelEnabled(channel string) bool {
	return channel == ChannelEmail && s.emailEnabled || channel == ChannelWhatsApp && s.whatsAppEnabled
}

func queuedResponse(challenge Challenge, now time.Time) Response {
	next := 3
	return Response{
		ChallengeID: challenge.ID, IdentifierType: challenge.IdentifierType,
		DeliveryChannel: challenge.DeliveryChannel, DestinationHint: MaskIdentifier(challenge.IdentifierType, challenge.Identifier),
		DeliveryState: "queued", DeliveryExpiresInSeconds: secondsRemaining(challenge.DeliveryDeadline, now),
		ResendAvailableInSeconds: int(challengeCooldown.Seconds()), NextStatusCheckInSeconds: &next,
	}
}

func responseFor(challenge Challenge, deliveryState string, now time.Time) Response {
	deliveryState = publicDeliveryState(deliveryState)
	if challenge.DeliveryAcceptedAt == nil && !challenge.DeliveryDeadline.After(now) {
		deliveryState = "expired"
	}
	response := queuedResponse(challenge, now)
	response.DeliveryState = deliveryState
	response.ResendAvailableInSeconds = max(secondsRemaining(challenge.CreatedAt.Add(challengeCooldown), now), 0)
	if challenge.VerifyExpiresAt != nil {
		remaining := secondsRemaining(*challenge.VerifyExpiresAt, now)
		response.VerificationExpiresInSeconds = &remaining
	}
	if deliveryState != "queued" && deliveryState != "unknown" {
		response.NextStatusCheckInSeconds = nil
	}
	if deliveryState == "accepted" || deliveryState == "sent" || deliveryState == "delivered" {
		response.DeliveryExpiresInSeconds = 0
	}
	return response
}

func publicDeliveryState(state string) string {
	switch state {
	case "pending", "processing", "retry", "queued":
		return "queued"
	case "accepted", "sent", "delivered", "unknown", "failed", "expired":
		return state
	default:
		return "unknown"
	}
}

func MaskIdentifier(identifierType, value string) string {
	if identifierType == "email" {
		parts := strings.SplitN(value, "@", 2)
		if len(parts) != 2 {
			return "•••"
		}
		prefix := []rune(parts[0])
		visible := ""
		if len(prefix) > 0 {
			visible = string(prefix[0])
		}
		return visible + "•••@" + parts[1]
	}
	digits := []rune(value)
	if len(digits) <= 4 {
		return "••••"
	}
	return "+••••••" + string(digits[len(digits)-4:])
}

var nonDigits = regexp.MustCompile(`\D`)

func normalizePhone(value string) (string, error) {
	digits := nonDigits.ReplaceAllString(strings.TrimSpace(value), "")
	digits = strings.TrimPrefix(digits, "00")
	if strings.HasPrefix(digits, "0") {
		digits = "234" + strings.TrimPrefix(digits, "0")
	}
	if len(digits) < 10 || len(digits) > 15 || digits[0] == '0' {
		return "", errors.New("enter a valid phone number with country code")
	}
	return "+" + digits, nil
}

func generateCode() (string, error) {
	limit := big.NewInt(1_000_000)
	value, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", fmt.Errorf("generate authentication code: %w", err)
	}
	return fmt.Sprintf("%06d", value.Int64()), nil
}

func hashCode(value string) []byte { sum := sha256.Sum256([]byte(value)); return sum[:] }
func validCode(value string) bool {
	if len(value) != codeLength {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func secondsRemaining(deadline, now time.Time) int {
	if !deadline.After(now) {
		return 0
	}
	return int(deadline.Sub(now).Seconds())
}

func sameOptionalUUID(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validateScope(realm, purpose string, target *uuid.UUID) error {
	if realm != RealmProvider && realm != RealmMarketplaceCustomer {
		return errors.New("invalid authentication realm")
	}
	if purpose == PurposeSignIn && target == nil || purpose == PurposeLinkIdentity && target != nil || purpose == PurposePasswordReset {
		return nil
	}
	return errors.New("invalid authentication challenge scope")
}

func (s *Service) insertSyntheticChallenge(ctx context.Context, challenge Challenge) error {
	table, _, err := challengeTable(challenge.Realm)
	if err != nil {
		return err
	}
	targetColumn := "target_client_id"
	if challenge.Realm == RealmMarketplaceCustomer {
		targetColumn = "target_customer_id"
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin synthetic auth challenge: %w", err)
	}
	defer tx.Rollback(ctx)
	lockKey := strings.Join([]string{challenge.Realm, challenge.IdentifierType, challenge.Identifier, challenge.Purpose, ""}, ":")
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return fmt.Errorf("lock synthetic auth challenge: %w", err)
	}
	var latest time.Time
	query := fmt.Sprintf(`SELECT created_at FROM %s WHERE identifier_type=$1 AND identifier=$2 AND purpose=$3 AND %s IS NULL ORDER BY created_at DESC LIMIT 1`, table, targetColumn)
	err = tx.QueryRow(ctx, query, challenge.IdentifierType, challenge.Identifier, challenge.Purpose).Scan(&latest)
	if err == nil && challenge.CreatedAt.Sub(latest) < challengeCooldown {
		return ErrTooSoon
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load latest synthetic auth challenge: %w", err)
	}
	consumeQuery := fmt.Sprintf(`UPDATE %s SET consumed_at=$4 WHERE identifier_type=$1 AND identifier=$2 AND purpose=$3 AND %s IS NULL AND consumed_at IS NULL`, table, targetColumn)
	if _, err = tx.Exec(ctx, consumeQuery, challenge.IdentifierType, challenge.Identifier, challenge.Purpose, challenge.CreatedAt); err != nil {
		return fmt.Errorf("supersede synthetic auth challenge: %w", err)
	}
	insertQuery := fmt.Sprintf(`INSERT INTO %s (id,identifier_type,identifier,delivery_channel,purpose,%s,code_hash,delivery_deadline,created_at) VALUES ($1,$2,$3,$4,$5,NULL,$6,$7,$8)`, table, targetColumn)
	if _, err = tx.Exec(ctx, insertQuery, challenge.ID, challenge.IdentifierType, challenge.Identifier, challenge.DeliveryChannel, challenge.Purpose, challenge.CodeHash, challenge.DeliveryDeadline, challenge.CreatedAt); err != nil {
		return fmt.Errorf("insert synthetic auth challenge: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit synthetic auth challenge: %w", err)
	}
	return nil
}

func deliveryAAD(jobID uuid.UUID) []byte { return []byte("auth-code-delivery:" + jobID.String()) }

func (s *Service) destinationFingerprint(channel, destination string) []byte {
	mac := hmac.New(sha256.New, s.destinationKey)
	_, _ = mac.Write([]byte(channel))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(strings.ToLower(strings.TrimSpace(destination))))
	return mac.Sum(nil)
}

func challengeTable(realm string) (string, string, error) {
	switch realm {
	case RealmProvider:
		return "provider_auth_challenges", "provider_challenge_id", nil
	case RealmMarketplaceCustomer:
		return "marketplace_auth_challenges", "marketplace_challenge_id", nil
	default:
		return "", "", errors.New("invalid authentication realm")
	}
}

func (s *Service) insertChallengeAndDelivery(ctx context.Context, challenge Challenge, jobID uuid.UUID, ciphertext secure.Ciphertext) error {
	table, challengeColumn, err := challengeTable(challenge.Realm)
	if err != nil {
		return err
	}
	targetColumn := "target_client_id"
	if challenge.Realm == RealmMarketplaceCustomer {
		targetColumn = "target_customer_id"
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin auth challenge creation: %w", err)
	}
	defer tx.Rollback(ctx)
	lockKey := strings.Join([]string{challenge.Realm, challenge.IdentifierType, challenge.Identifier, challenge.Purpose, optionalUUIDString(challenge.TargetAccountID)}, ":")
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return fmt.Errorf("lock auth challenge identity: %w", err)
	}
	var latest time.Time
	query := fmt.Sprintf(`SELECT created_at FROM %s WHERE identifier_type=$1 AND identifier=$2 AND purpose=$3 AND %s IS NOT DISTINCT FROM $4 ORDER BY created_at DESC LIMIT 1`, table, targetColumn)
	err = tx.QueryRow(ctx, query, challenge.IdentifierType, challenge.Identifier, challenge.Purpose, challenge.TargetAccountID).Scan(&latest)
	if err == nil && challenge.CreatedAt.Sub(latest) < challengeCooldown {
		return ErrTooSoon
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load latest auth challenge: %w", err)
	}
	consumeQuery := fmt.Sprintf(`UPDATE %s SET consumed_at=$5 WHERE identifier_type=$1 AND identifier=$2 AND purpose=$3 AND %s IS NOT DISTINCT FROM $4 AND consumed_at IS NULL`, table, targetColumn)
	if _, err = tx.Exec(ctx, consumeQuery, challenge.IdentifierType, challenge.Identifier, challenge.Purpose, challenge.TargetAccountID, challenge.CreatedAt); err != nil {
		return fmt.Errorf("supersede auth challenge: %w", err)
	}
	insertQuery := fmt.Sprintf(`INSERT INTO %s (id,identifier_type,identifier,delivery_channel,purpose,%s,code_hash,delivery_deadline,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, table, targetColumn)
	if _, err = tx.Exec(ctx, insertQuery, challenge.ID, challenge.IdentifierType, challenge.Identifier, challenge.DeliveryChannel, challenge.Purpose, challenge.TargetAccountID, challenge.CodeHash, challenge.DeliveryDeadline, challenge.CreatedAt); err != nil {
		return fmt.Errorf("insert auth challenge: %w", err)
	}
	templateKey := "auth_code_email"
	if challenge.DeliveryChannel == ChannelWhatsApp {
		templateKey = "v_c_x"
	}
	jobQuery := fmt.Sprintf(`INSERT INTO auth_code_delivery_jobs (id,realm,channel,template_key,%s,payload_ciphertext,payload_nonce,payload_key_version,destination_fingerprint,delivery_deadline,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)`, challengeColumn)
	if _, err = tx.Exec(ctx, jobQuery, jobID, challenge.Realm, challenge.DeliveryChannel, templateKey, challenge.ID, ciphertext.Data, ciphertext.Nonce, ciphertext.KeyVersion, s.destinationFingerprint(challenge.DeliveryChannel, challenge.Identifier), challenge.DeliveryDeadline, challenge.CreatedAt); err != nil {
		return fmt.Errorf("insert auth code delivery: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit auth challenge: %w", err)
	}
	return nil
}

func optionalUUIDString(value *uuid.UUID) string {
	if value == nil {
		return ""
	}
	return value.String()
}

func (s *Service) load(ctx context.Context, realm string, id uuid.UUID) (Challenge, string, error) {
	table, challengeColumn, err := challengeTable(realm)
	if err != nil {
		return Challenge{}, "", err
	}
	targetColumn := "target_client_id"
	if realm == RealmMarketplaceCustomer {
		targetColumn = "target_customer_id"
	}
	query := fmt.Sprintf(`SELECT challenge.id,challenge.identifier_type,challenge.identifier,challenge.delivery_channel,challenge.purpose,challenge.%s,challenge.code_hash,challenge.failed_attempts,challenge.delivery_deadline,challenge.delivery_accepted_at,challenge.verify_expires_at,challenge.consumed_at,challenge.created_at,COALESCE(job.status,'synthetic') FROM %s challenge LEFT JOIN auth_code_delivery_jobs job ON job.%s=challenge.id WHERE challenge.id=$1`, targetColumn, table, challengeColumn)
	var challenge Challenge
	var target uuid.NullUUID
	var acceptedAt, verifyExpiresAt, consumedAt *time.Time
	var state string
	err = s.db.QueryRow(ctx, query, id).Scan(&challenge.ID, &challenge.IdentifierType, &challenge.Identifier, &challenge.DeliveryChannel, &challenge.Purpose, &target, &challenge.CodeHash, &challenge.FailedAttempts, &challenge.DeliveryDeadline, &acceptedAt, &verifyExpiresAt, &consumedAt, &challenge.CreatedAt, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return Challenge{}, "", ErrNotFound
	}
	if err != nil {
		return Challenge{}, "", fmt.Errorf("load auth challenge: %w", err)
	}
	challenge.Realm, challenge.DeliveryAcceptedAt, challenge.VerifyExpiresAt, challenge.ConsumedAt = realm, acceptedAt, verifyExpiresAt, consumedAt
	if target.Valid {
		id := target.UUID
		challenge.TargetAccountID = &id
	}
	if (state == "pending" || state == "processing" || state == "retry") && !challenge.DeliveryDeadline.After(s.now()) {
		state = "expired"
	}
	if state == "pending" || state == "processing" || state == "retry" {
		state = "queued"
	}
	return challenge, state, nil
}

func (s *Service) recordFailedAttempt(ctx context.Context, challenge Challenge) error {
	table, _, err := challengeTable(challenge.Realm)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`UPDATE %s SET failed_attempts=LEAST(failed_attempts+1,6) WHERE id=$1 AND consumed_at IS NULL AND verify_expires_at>NOW()`, table)
	_, err = s.db.Exec(ctx, query, challenge.ID)
	return err
}
