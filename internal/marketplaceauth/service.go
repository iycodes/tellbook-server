package marketplaceauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"booking/go-server/internal/config"
	"booking/go-server/internal/mailer"
	"booking/go-server/internal/redisstore"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sync/singleflight"
)

var (
	ErrInvalidChallenge      = errors.New("verification code is invalid or expired")
	ErrDeliveryUnavailable   = errors.New("delivery channel is not available yet")
	ErrChallengeTooSoon      = errors.New("wait before requesting another code")
	ErrInvalidIdentifier     = errors.New("invalid identifier")
	ErrCodeDelivery          = errors.New("code delivery failed")
	ErrInvalidPassword       = errors.New("identifier or password is incorrect")
	ErrWeakPassword          = errors.New("password must be between 8 and 72 characters")
	ErrIdentityAlreadyLinked = errors.New("identity is already linked to this account")
	ErrAuthUnavailable       = errors.New("authentication is temporarily unavailable")
)

const (
	challengeTTL             = 10 * time.Minute
	challengeCooldown        = 45 * time.Second
	maximumChallengeAttempts = 6
)

type Service struct {
	repo              *Repository
	cfg               config.Config
	mailer            mailer.Sender
	dummyPasswordHash []byte
	sessionCache      interface {
		CacheGet(context.Context, string, string, any) error
		CacheSet(context.Context, string, string, any, time.Duration) error
		CacheDelete(context.Context, string, ...string) error
	}
	sessionRepo interface {
		SessionPrincipalByTokenHash(context.Context, []byte) (SessionPrincipal, error)
		ActiveSessionTokenHashesPage(context.Context, uuid.UUID, []byte, int) ([][]byte, error)
	}
	sessionMetrics interface {
		ObserveCacheRequest(string, string)
	}
	sessionFallback chan struct{}
	principalFlight singleflight.Group
}

func NewService(repo *Repository, cfg config.Config, sender mailer.Sender) *Service {
	cost := cfg.AuthBcryptCost
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		cost = bcrypt.DefaultCost
	}
	dummyHash, _ := bcrypt.GenerateFromPassword([]byte("tellbook-invalid-password-padding"), cost)
	return &Service{
		repo: repo, cfg: cfg, mailer: sender, dummyPasswordHash: dummyHash,
		sessionRepo: repo, sessionFallback: make(chan struct{}, 32),
	}
}

func (s *Service) ConfigureSessionCache(
	cache interface {
		CacheGet(context.Context, string, string, any) error
		CacheSet(context.Context, string, string, any, time.Duration) error
		CacheDelete(context.Context, string, ...string) error
	},
	fallbackMaxConcurrency int,
	metrics interface{ ObserveCacheRequest(string, string) },
) {
	if fallbackMaxConcurrency < 1 {
		fallbackMaxConcurrency = 1
	}
	s.sessionCache = cache
	s.sessionMetrics = metrics
	s.sessionFallback = make(chan struct{}, fallbackMaxConcurrency)
}

type StartChallengeResult struct {
	ChallengeID              uuid.UUID `json:"challenge_id"`
	IdentifierType           string    `json:"identifier_type"`
	DeliveryChannel          string    `json:"delivery_channel"`
	DestinationHint          string    `json:"destination_hint"`
	ExpiresInSeconds         int       `json:"expires_in_seconds"`
	ResendAvailableInSeconds int       `json:"resend_available_in_seconds"`
}

func (s *Service) StartChallenge(ctx context.Context, rawIdentifier, requestedChannel string) (StartChallengeResult, error) {
	return s.startChallenge(ctx, rawIdentifier, requestedChannel, "sign_in", nil)
}

func (s *Service) StartIdentityLink(ctx context.Context, customerID uuid.UUID, rawIdentifier, requestedChannel string) (StartChallengeResult, error) {
	identifierType, identifier, channel, err := normalizeIdentifier(rawIdentifier, requestedChannel)
	if err != nil {
		return StartChallengeResult{}, fmt.Errorf("%w: %v", ErrInvalidIdentifier, err)
	}
	owner, ownerErr := s.repo.IdentityOwner(ctx, identifierType, identifier)
	if ownerErr == nil {
		if owner != customerID {
			return StartChallengeResult{}, ErrIdentityConflict
		}
		verified, verificationErr := s.repo.CustomerHasVerifiedIdentityType(ctx, customerID, identifierType)
		if verificationErr != nil {
			return StartChallengeResult{}, verificationErr
		}
		if verified {
			return StartChallengeResult{}, ErrIdentityAlreadyLinked
		}
		// The same phone contact may be verified for the other delivery channel.
		return s.startNormalizedChallenge(ctx, identifierType, identifier, channel, "link_identity", &customerID)
	}
	if !errors.Is(ownerErr, ErrNotFound) {
		return StartChallengeResult{}, ownerErr
	}
	if _, identityErr := s.repo.CustomerIdentity(ctx, customerID, identifierType); identityErr == nil {
		return StartChallengeResult{}, ErrIdentityConflict
	} else if !errors.Is(identityErr, ErrNotFound) {
		return StartChallengeResult{}, identityErr
	}
	return s.startNormalizedChallenge(ctx, identifierType, identifier, channel, "link_identity", &customerID)
}

func (s *Service) startChallenge(ctx context.Context, rawIdentifier, requestedChannel, purpose string, targetCustomerID *uuid.UUID) (StartChallengeResult, error) {
	identifierType, identifier, channel, err := normalizeIdentifier(rawIdentifier, requestedChannel)
	if err != nil {
		return StartChallengeResult{}, fmt.Errorf("%w: %v", ErrInvalidIdentifier, err)
	}
	return s.startNormalizedChallenge(ctx, identifierType, identifier, channel, purpose, targetCustomerID)
}

func (s *Service) startNormalizedChallenge(ctx context.Context, identifierType, identifier, channel, purpose string, targetCustomerID *uuid.UUID) (StartChallengeResult, error) {
	if channel != "email" {
		return StartChallengeResult{}, ErrDeliveryUnavailable
	}
	if s.mailer == nil || !s.mailer.Enabled() {
		return StartChallengeResult{}, ErrCodeDelivery
	}
	if latest, err := s.repo.LatestChallengeCreatedAt(ctx, identifierType, identifier); err == nil && time.Since(latest) < challengeCooldown {
		return StartChallengeResult{}, ErrChallengeTooSoon
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return StartChallengeResult{}, err
	}

	code, codeHash, err := newSixDigitCode()
	if err != nil {
		return StartChallengeResult{}, err
	}
	now := time.Now().UTC()
	challenge := Challenge{ID: uuid.New(), IdentifierType: identifierType, Identifier: identifier,
		DeliveryChannel: channel, Purpose: purpose, TargetCustomerID: targetCustomerID,
		CodeHash: codeHash, ExpiresAt: now.Add(challengeTTL), CreatedAt: now}
	if err := s.repo.CreateChallenge(ctx, challenge); err != nil {
		return StartChallengeResult{}, err
	}
	subject := "Your Tellbook sign-in code"
	if purpose == "link_identity" {
		subject = "Verify a contact for your Tellbook account"
	}
	if err := s.mailer.Send(ctx, mailer.Message{
		ToEmail: identifier,
		Subject: subject,
		Text:    fmt.Sprintf("Use this code to continue to Tellbook:\n\n%s\n\nThis code expires in 10 minutes. If you did not request it, you can ignore this message.", code),
	}); err != nil {
		s.repo.DeleteChallenge(ctx, challenge.ID)
		return StartChallengeResult{}, fmt.Errorf("%w: %v", ErrCodeDelivery, err)
	}
	return StartChallengeResult{ChallengeID: challenge.ID, IdentifierType: identifierType,
		DeliveryChannel: channel, DestinationHint: maskIdentifier(identifierType, identifier),
		ExpiresInSeconds:         int(challengeTTL.Seconds()),
		ResendAvailableInSeconds: int(challengeCooldown.Seconds())}, nil
}

func (s *Service) ResendChallenge(ctx context.Context, challengeID uuid.UUID) (StartChallengeResult, error) {
	challenge, err := s.repo.GetChallenge(ctx, challengeID)
	if err != nil {
		return StartChallengeResult{}, ErrInvalidChallenge
	}
	if challenge.Purpose != "sign_in" {
		return StartChallengeResult{}, ErrInvalidChallenge
	}
	return s.startNormalizedChallenge(ctx, challenge.IdentifierType, challenge.Identifier, challenge.DeliveryChannel, challenge.Purpose, nil)
}

func (s *Service) ResendIdentityLink(ctx context.Context, customerID, challengeID uuid.UUID) (StartChallengeResult, error) {
	challenge, err := s.repo.GetChallenge(ctx, challengeID)
	if err != nil || challenge.Purpose != "link_identity" || challenge.TargetCustomerID == nil || *challenge.TargetCustomerID != customerID {
		return StartChallengeResult{}, ErrInvalidChallenge
	}
	return s.startNormalizedChallenge(ctx, challenge.IdentifierType, challenge.Identifier, challenge.DeliveryChannel, challenge.Purpose, &customerID)
}

func (s *Service) VerifyChallenge(ctx context.Context, challengeID uuid.UUID, rawCode, userAgent, ipAddress string) (Customer, string, error) {
	challenge, err := s.repo.GetChallenge(ctx, challengeID)
	if err != nil || challenge.ConsumedAt != nil || !challenge.ExpiresAt.After(time.Now().UTC()) || challenge.FailedAttempts >= maximumChallengeAttempts {
		return Customer{}, "", ErrInvalidChallenge
	}
	rawCode = strings.TrimSpace(rawCode)
	if !validSixDigitCode(rawCode) || !equalHash(challenge.CodeHash, hashToken(rawCode)) {
		_ = s.repo.RecordFailedChallengeAttempt(ctx, challengeID)
		return Customer{}, "", ErrInvalidChallenge
	}
	token, tokenHash, err := newOpaqueToken()
	if err != nil {
		return Customer{}, "", err
	}
	now := time.Now().UTC()
	session := Session{ID: uuid.New(), TokenHash: tokenHash, UserAgent: truncate(userAgent, 512),
		IPAddress: truncate(ipAddress, 64), ExpiresAt: now.Add(s.cfg.AuthRefreshTokenTTL), LastUsedAt: now, CreatedAt: now}
	customer, err := s.repo.CompleteChallenge(ctx, challenge, session)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Customer{}, "", ErrInvalidChallenge
		}
		return Customer{}, "", err
	}
	_ = s.invalidateCustomerSessionsAfterCommit(ctx, customer.ID)
	return customer, token, nil
}

func (s *Service) VerifyIdentityLink(ctx context.Context, customerID, challengeID uuid.UUID, rawCode string) (Customer, error) {
	challenge, err := s.repo.GetChallenge(ctx, challengeID)
	if err != nil || challenge.Purpose != "link_identity" || challenge.TargetCustomerID == nil ||
		*challenge.TargetCustomerID != customerID || challenge.ConsumedAt != nil ||
		!challenge.ExpiresAt.After(time.Now().UTC()) || challenge.FailedAttempts >= maximumChallengeAttempts {
		return Customer{}, ErrInvalidChallenge
	}
	rawCode = strings.TrimSpace(rawCode)
	if !validSixDigitCode(rawCode) || !equalHash(challenge.CodeHash, hashToken(rawCode)) {
		_ = s.repo.RecordFailedChallengeAttempt(ctx, challengeID)
		return Customer{}, ErrInvalidChallenge
	}
	customer, err := s.repo.CompleteIdentityLink(ctx, challenge)
	if errors.Is(err, ErrNotFound) {
		return Customer{}, ErrInvalidChallenge
	}
	if err == nil {
		_ = s.invalidateCustomerSessionsAfterCommit(ctx, customer.ID)
	}
	return customer, err
}

func (s *Service) PasswordLogin(ctx context.Context, rawIdentifier, password, userAgent, ipAddress string) (Customer, string, error) {
	_, normalizedIdentifier, _, err := normalizeIdentifier(rawIdentifier, "")
	if err != nil {
		return Customer{}, "", ErrInvalidPassword
	}
	candidates, err := s.repo.PasswordCandidates(ctx, normalizedIdentifier)
	if err != nil {
		return Customer{}, "", err
	}
	var customerID uuid.UUID
	for _, candidate := range candidates {
		if bcrypt.CompareHashAndPassword([]byte(candidate.PasswordHash), []byte(password)) == nil {
			customerID = candidate.CustomerID
			break
		}
	}
	if customerID == uuid.Nil {
		// Keep the no-account path close to the cost of a real password comparison.
		_ = bcrypt.CompareHashAndPassword(s.dummyPasswordHash, []byte(password))
		return Customer{}, "", ErrInvalidPassword
	}
	token, tokenHash, err := newOpaqueToken()
	if err != nil {
		return Customer{}, "", err
	}
	now := time.Now().UTC()
	session := Session{ID: uuid.New(), TokenHash: tokenHash, UserAgent: truncate(userAgent, 512),
		IPAddress: truncate(ipAddress, 64), ExpiresAt: now.Add(s.cfg.AuthRefreshTokenTTL), LastUsedAt: now, CreatedAt: now}
	customer, err := s.repo.CreateSession(ctx, customerID, session)
	if err != nil {
		return Customer{}, "", err
	}
	return customer, token, nil
}

func (s *Service) Authenticate(ctx context.Context, rawToken string) (Customer, error) {
	if strings.TrimSpace(rawToken) == "" {
		return Customer{}, ErrNotFound
	}
	tokenHash := hashToken(rawToken)
	customer, err := s.repo.CustomerBySessionTokenHash(ctx, tokenHash)
	if err != nil {
		return Customer{}, err
	}
	return customer, nil
}

func (s *Service) AuthenticatePrincipal(ctx context.Context, rawToken string, forceFresh bool) (SessionPrincipal, error) {
	if strings.TrimSpace(rawToken) == "" {
		return SessionPrincipal{}, ErrNotFound
	}
	tokenHash := hashToken(rawToken)
	cacheIdentity := hex.EncodeToString(tokenHash)
	if !forceFresh && s.sessionCache != nil {
		var cached SessionPrincipal
		err := s.sessionCache.CacheGet(ctx, "marketplace_session", cacheIdentity, &cached)
		if err == nil && validSessionPrincipal(cached, time.Now().UTC()) {
			s.observeSessionCache("hit")
			return cached, nil
		}
		if err == nil {
			_ = s.sessionCache.CacheDelete(context.WithoutCancel(ctx), "marketplace_session", cacheIdentity)
			s.observeSessionCache("invalid")
		} else if errors.Is(err, redisstore.ErrCacheMiss) {
			s.observeSessionCache("miss")
		} else {
			s.observeSessionCache("error")
		}
	} else if forceFresh {
		s.observeSessionCache("fresh")
	} else {
		s.observeSessionCache("disabled")
	}

	result := s.principalFlight.DoChan(cacheIdentity, func() (any, error) {
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		principal, err := s.loadSessionPrincipal(loadCtx, tokenHash)
		if err != nil {
			return SessionPrincipal{}, err
		}
		if s.sessionCache != nil {
			ttl := redisstore.JitterTTL(2*time.Minute, 5*time.Minute)
			if remaining := time.Until(principal.ExpiresAt); remaining < ttl {
				ttl = remaining
			}
			if ttl > 0 {
				_ = s.sessionCache.CacheSet(loadCtx, "marketplace_session", cacheIdentity, principal, ttl)
			}
		}
		return principal, nil
	})
	select {
	case <-ctx.Done():
		return SessionPrincipal{}, ctx.Err()
	case loaded := <-result:
		if loaded.Err != nil {
			return SessionPrincipal{}, loaded.Err
		}
		principal, ok := loaded.Val.(SessionPrincipal)
		if !ok {
			return SessionPrincipal{}, ErrAuthUnavailable
		}
		return principal, nil
	}
}

func (s *Service) loadSessionPrincipal(ctx context.Context, tokenHash []byte) (SessionPrincipal, error) {
	select {
	case s.sessionFallback <- struct{}{}:
		defer func() { <-s.sessionFallback }()
	default:
		s.observeSessionCache("fallback_shed")
		return SessionPrincipal{}, ErrAuthUnavailable
	}
	principal, err := s.sessionRepo.SessionPrincipalByTokenHash(ctx, tokenHash)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return SessionPrincipal{}, fmt.Errorf("%w: %v", ErrAuthUnavailable, err)
		}
		return SessionPrincipal{}, err
	}
	if !validSessionPrincipal(principal, time.Now().UTC()) {
		return SessionPrincipal{}, ErrNotFound
	}
	return principal, nil
}

func validSessionPrincipal(principal SessionPrincipal, now time.Time) bool {
	return principal.SessionID != uuid.Nil && principal.CustomerID != uuid.Nil &&
		principal.SecurityRevision > 0 && principal.SessionRevision > 0 &&
		principal.ExpiresAt.After(now)
}

func (s *Service) Logout(ctx context.Context, rawToken string) error {
	if strings.TrimSpace(rawToken) == "" {
		return nil
	}
	tokenHash := hashToken(rawToken)
	if err := s.repo.DeleteSession(ctx, tokenHash); err != nil {
		return err
	}
	if s.sessionCache != nil {
		invalidateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = s.sessionCache.CacheDelete(invalidateCtx, "marketplace_session", hex.EncodeToString(tokenHash))
	}
	return nil
}

func (s *Service) SetPassword(ctx context.Context, customerID uuid.UUID, password string) (Customer, error) {
	if len(password) < 8 || len(password) > 72 {
		return Customer{}, ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.cfg.AuthBcryptCost)
	if err != nil {
		return Customer{}, fmt.Errorf("hash marketplace password: %w", err)
	}
	customer, err := s.repo.SetPassword(ctx, customerID, string(hash))
	if err == nil {
		_ = s.invalidateCustomerSessionsAfterCommit(ctx, customerID)
	}
	return customer, err
}

func (s *Service) UpdateProfile(ctx context.Context, customerID uuid.UUID, input ProfileInput) (Customer, error) {
	customer, err := s.repo.UpdateProfile(ctx, customerID, input)
	if err == nil {
		_ = s.invalidateCustomerSessionsAfterCommit(ctx, customerID)
	}
	return customer, err
}

func (s *Service) invalidateCustomerSessions(ctx context.Context, customerID uuid.UUID) error {
	if s.sessionCache == nil || customerID == uuid.Nil {
		return nil
	}
	var after []byte
	for {
		hashes, err := s.sessionRepo.ActiveSessionTokenHashesPage(ctx, customerID, after, 256)
		if err != nil {
			return err
		}
		if len(hashes) == 0 {
			return nil
		}
		identities := make([]string, len(hashes))
		for index, tokenHash := range hashes {
			identities[index] = hex.EncodeToString(tokenHash)
		}
		if err := s.sessionCache.CacheDelete(ctx, "marketplace_session", identities...); err != nil {
			return err
		}
		if len(hashes) < 256 {
			return nil
		}
		after = hashes[len(hashes)-1]
	}
}

func (s *Service) invalidateCustomerSessionsAfterCommit(ctx context.Context, customerID uuid.UUID) error {
	invalidateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return s.invalidateCustomerSessions(invalidateCtx, customerID)
}

func (s *Service) observeSessionCache(outcome string) {
	if s.sessionMetrics != nil {
		s.sessionMetrics.ObserveCacheRequest("marketplace_session", outcome)
	}
}

func normalizeIdentifier(rawIdentifier, requestedChannel string) (identifierType, identifier, channel string, err error) {
	rawIdentifier = strings.TrimSpace(rawIdentifier)
	requestedChannel = strings.ToLower(strings.TrimSpace(requestedChannel))
	if strings.Contains(rawIdentifier, "@") {
		if requestedChannel != "" && requestedChannel != "email" {
			return "", "", "", errors.New("email identifiers must use email delivery")
		}
		parsed, parseErr := mail.ParseAddress(rawIdentifier)
		if parseErr != nil || !strings.EqualFold(parsed.Address, rawIdentifier) {
			return "", "", "", errors.New("enter a valid email address")
		}
		return "email", strings.ToLower(parsed.Address), "email", nil
	}
	normalizedPhone, parseErr := normalizePhone(rawIdentifier)
	if parseErr != nil {
		return "", "", "", parseErr
	}
	switch requestedChannel {
	case "", "sms":
		return "phone", normalizedPhone, "sms", nil
	case "whatsapp":
		return "whatsapp", normalizedPhone, "whatsapp", nil
	default:
		return "", "", "", errors.New("delivery_channel must be email, sms, or whatsapp")
	}
}

var nonDigits = regexp.MustCompile(`\D`)

func normalizePhone(value string) (string, error) {
	value = strings.TrimSpace(value)
	digits := nonDigits.ReplaceAllString(value, "")
	if strings.HasPrefix(digits, "00") {
		digits = strings.TrimPrefix(digits, "00")
	}
	if strings.HasPrefix(digits, "0") {
		digits = "234" + strings.TrimPrefix(digits, "0")
	}
	if len(digits) < 10 || len(digits) > 15 {
		return "", errors.New("enter a valid phone number with country code")
	}
	return "+" + digits, nil
}

// CustomerMatchesBookingContact prevents an authenticated customer from silently
// attaching a booking made for an unrelated recipient to their account.
func CustomerMatchesBookingContact(customer Customer, email, phone string) bool {
	if customer.EmailVerifiedAt != nil && strings.EqualFold(strings.TrimSpace(email), customer.Email) {
		return true
	}
	normalizedPhone, err := normalizePhone(phone)
	if err != nil {
		return false
	}
	return (customer.PhoneVerifiedAt != nil && normalizedPhone == customer.Phone) ||
		(customer.WhatsAppVerifiedAt != nil && normalizedPhone == customer.WhatsApp)
}

func maskIdentifier(identifierType, value string) string {
	if identifierType == "email" {
		parts := strings.SplitN(value, "@", 2)
		if len(parts[0]) <= 2 {
			return parts[0][:1] + "…@" + parts[1]
		}
		return parts[0][:2] + "…@" + parts[1]
	}
	if len(value) <= 6 {
		return value
	}
	return value[:4] + "•••" + value[len(value)-3:]
}

func newSixDigitCode() (string, []byte, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", nil, err
	}
	code := fmt.Sprintf("%06d", value.Int64())
	return code, hashToken(code), nil
}

func newOpaqueToken() (string, []byte, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", nil, err
	}
	raw := base64.RawURLEncoding.EncodeToString(randomBytes)
	return raw, hashToken(raw), nil
}

func hashToken(value string) []byte { sum := sha256.Sum256([]byte(value)); return sum[:] }
func equalHash(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}
func validSixDigitCode(value string) bool {
	if len(value) != 6 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
