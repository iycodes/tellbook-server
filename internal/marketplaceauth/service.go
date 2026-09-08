package marketplaceauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"booking/go-server/internal/authchallenge"
	"booking/go-server/internal/config"
	"booking/go-server/internal/redisstore"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sync/singleflight"
)

var (
	ErrInvalidChallenge      = authchallenge.ErrInvalidChallenge
	ErrDeliveryUnavailable   = authchallenge.ErrUnavailable
	ErrChallengeTooSoon      = authchallenge.ErrTooSoon
	ErrInvalidIdentifier     = authchallenge.ErrInvalidIdentifier
	ErrInvalidPassword       = errors.New("identifier or password is incorrect")
	ErrWeakPassword          = errors.New("password must be between 8 and 72 characters")
	ErrIdentityAlreadyLinked = errors.New("identity is already linked to this account")
	ErrAuthUnavailable       = errors.New("authentication is temporarily unavailable")
)

type Service struct {
	repo              *Repository
	cfg               config.Config
	challenges        *authchallenge.Service
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

func NewService(repo *Repository, cfg config.Config, challenges *authchallenge.Service) *Service {
	cost := cfg.AuthBcryptCost
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		cost = bcrypt.DefaultCost
	}
	dummyHash, _ := bcrypt.GenerateFromPassword([]byte("tellbook-invalid-password-padding"), cost)
	return &Service{
		repo: repo, cfg: cfg, challenges: challenges, dummyPasswordHash: dummyHash,
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

type StartChallengeResult = authchallenge.Response

func (s *Service) AuthCapabilities() authchallenge.Capabilities {
	return authchallenge.NewCapabilities(s.challenges, true)
}

func (s *Service) StartChallenge(ctx context.Context, rawIdentifier, requestedChannel string) (StartChallengeResult, error) {
	if s.challenges == nil {
		return StartChallengeResult{}, ErrDeliveryUnavailable
	}
	return s.challenges.Start(ctx, authchallenge.StartRequest{
		Realm: authchallenge.RealmMarketplaceCustomer, RawIdentifier: rawIdentifier,
		Channel: requestedChannel, Purpose: authchallenge.PurposeSignIn,
	})
}

func (s *Service) StartIdentityLink(ctx context.Context, customerID uuid.UUID, rawIdentifier, requestedChannel string) (StartChallengeResult, error) {
	identifierType, identifier, channel, err := authchallenge.NormalizeIdentifier(rawIdentifier, requestedChannel)
	if err != nil {
		return StartChallengeResult{}, err
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
		return s.startIdentityLinkChallenge(ctx, identifier, channel, customerID)
	}
	if !errors.Is(ownerErr, ErrNotFound) {
		return StartChallengeResult{}, ownerErr
	}
	if _, identityErr := s.repo.CustomerIdentity(ctx, customerID, identifierType); identityErr == nil {
		return StartChallengeResult{}, ErrIdentityConflict
	} else if !errors.Is(identityErr, ErrNotFound) {
		return StartChallengeResult{}, identityErr
	}
	return s.startIdentityLinkChallenge(ctx, identifier, channel, customerID)
}

func (s *Service) startIdentityLinkChallenge(ctx context.Context, identifier, channel string, customerID uuid.UUID) (StartChallengeResult, error) {
	if s.challenges == nil {
		return StartChallengeResult{}, ErrDeliveryUnavailable
	}
	return s.challenges.Start(ctx, authchallenge.StartRequest{
		Realm: authchallenge.RealmMarketplaceCustomer, RawIdentifier: identifier,
		Channel: channel, Purpose: authchallenge.PurposeLinkIdentity, TargetAccountID: &customerID,
	})
}

func (s *Service) ResendChallenge(ctx context.Context, challengeID uuid.UUID) (StartChallengeResult, error) {
	if s.challenges == nil {
		return StartChallengeResult{}, ErrDeliveryUnavailable
	}
	return s.challenges.Resend(ctx, authchallenge.RealmMarketplaceCustomer, challengeID, authchallenge.PurposeSignIn, nil)
}

func (s *Service) ResendIdentityLink(ctx context.Context, customerID, challengeID uuid.UUID) (StartChallengeResult, error) {
	if s.challenges == nil {
		return StartChallengeResult{}, ErrDeliveryUnavailable
	}
	return s.challenges.Resend(ctx, authchallenge.RealmMarketplaceCustomer, challengeID, authchallenge.PurposeLinkIdentity, &customerID)
}

func (s *Service) ChallengeStatus(ctx context.Context, challengeID uuid.UUID) (StartChallengeResult, error) {
	if s.challenges == nil {
		return StartChallengeResult{}, ErrDeliveryUnavailable
	}
	response, err := s.challenges.Status(ctx, authchallenge.RealmMarketplaceCustomer, challengeID, authchallenge.PurposeSignIn, nil)
	if err != nil {
		return StartChallengeResult{}, ErrInvalidChallenge
	}
	return response, nil
}

func (s *Service) IdentityLinkStatus(ctx context.Context, customerID, challengeID uuid.UUID) (StartChallengeResult, error) {
	if s.challenges == nil {
		return StartChallengeResult{}, ErrDeliveryUnavailable
	}
	response, err := s.challenges.Status(
		ctx, authchallenge.RealmMarketplaceCustomer, challengeID,
		authchallenge.PurposeLinkIdentity, &customerID,
	)
	if err != nil {
		return StartChallengeResult{}, ErrInvalidChallenge
	}
	return response, nil
}

func (s *Service) VerifyChallenge(ctx context.Context, challengeID uuid.UUID, rawCode, userAgent, ipAddress string) (Customer, string, bool, error) {
	if s.challenges == nil {
		return Customer{}, "", false, ErrDeliveryUnavailable
	}
	challenge, err := s.challenges.Verify(ctx, authchallenge.RealmMarketplaceCustomer, challengeID, rawCode, authchallenge.PurposeSignIn, nil)
	if err != nil {
		return Customer{}, "", false, ErrInvalidChallenge
	}
	token, tokenHash, err := newOpaqueToken()
	if err != nil {
		return Customer{}, "", false, err
	}
	now := time.Now().UTC()
	session := Session{ID: uuid.New(), TokenHash: tokenHash, UserAgent: truncate(userAgent, 512),
		IPAddress: truncate(ipAddress, 64), ExpiresAt: now.Add(s.cfg.AuthRefreshTokenTTL), LastUsedAt: now, CreatedAt: now}
	customer, newAccount, err := s.repo.CompleteChallenge(ctx, challenge, session)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Customer{}, "", false, ErrInvalidChallenge
		}
		return Customer{}, "", false, err
	}
	_ = s.invalidateCustomerSessionsAfterCommit(ctx, customer.ID)
	return customer, token, newAccount, nil
}

func (s *Service) VerifyIdentityLink(ctx context.Context, customerID, challengeID uuid.UUID, rawCode string) (Customer, error) {
	if s.challenges == nil {
		return Customer{}, ErrInvalidChallenge
	}
	challenge, err := s.challenges.Verify(ctx, authchallenge.RealmMarketplaceCustomer, challengeID, rawCode, authchallenge.PurposeLinkIdentity, &customerID)
	if err != nil {
		return Customer{}, ErrInvalidChallenge
	}
	customer, revokedHashes, err := s.repo.CompleteIdentityLink(ctx, challenge)
	if errors.Is(err, ErrNotFound) {
		return Customer{}, ErrInvalidChallenge
	}
	if err == nil {
		_ = s.invalidateTokenHashesAfterCommit(ctx, revokedHashes)
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
	compared := false
	for _, candidate := range candidates {
		compared = true
		if bcrypt.CompareHashAndPassword([]byte(candidate.PasswordHash), []byte(password)) == nil {
			customerID = candidate.CustomerID
			break
		}
	}
	if customerID == uuid.Nil {
		// Keep the no-account path close to the cost of a real password comparison.
		if !compared {
			_ = bcrypt.CompareHashAndPassword(s.dummyPasswordHash, []byte(password))
		}
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

type PasswordResetVerification struct {
	ResetGrant       string `json:"reset_grant"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
}

const passwordResetGrantTTL = 10 * time.Minute

func (s *Service) StartPasswordReset(ctx context.Context, rawIdentifier, requestedChannel string) (StartChallengeResult, error) {
	if s.challenges == nil {
		return StartChallengeResult{}, ErrDeliveryUnavailable
	}
	identifierType, identifier, channel, err := authchallenge.NormalizeIdentifier(rawIdentifier, requestedChannel)
	if err != nil {
		return StartChallengeResult{}, err
	}
	// Apply the same bounded password work regardless of account existence.
	_ = bcrypt.CompareHashAndPassword(s.dummyPasswordHash, []byte("password-reset-padding"))
	candidate, err := s.repo.PasswordResetAccount(ctx, identifierType, identifier)
	if err == nil {
		return s.challenges.Start(ctx, authchallenge.StartRequest{
			Realm: authchallenge.RealmMarketplaceCustomer, RawIdentifier: identifier,
			Channel: channel, Purpose: authchallenge.PurposePasswordReset,
			TargetAccountID: &candidate.CustomerID,
		})
	}
	if !errors.Is(err, ErrNotFound) {
		return StartChallengeResult{}, err
	}
	return s.challenges.StartSyntheticPasswordReset(ctx, authchallenge.RealmMarketplaceCustomer, identifier, channel)
}

func (s *Service) VerifyPasswordReset(ctx context.Context, challengeID uuid.UUID, rawCode string) (PasswordResetVerification, error) {
	if s.challenges == nil {
		return PasswordResetVerification{}, ErrInvalidChallenge
	}
	challenge, err := s.challenges.VerifyPasswordReset(ctx, authchallenge.RealmMarketplaceCustomer, challengeID, rawCode)
	if err != nil {
		return PasswordResetVerification{}, ErrInvalidChallenge
	}
	rawGrant, grantHash, err := newOpaqueToken()
	if err != nil {
		return PasswordResetVerification{}, err
	}
	if err := s.repo.StorePasswordResetGrant(ctx, challenge, grantHash, time.Now().UTC().Add(passwordResetGrantTTL)); err != nil {
		if errors.Is(err, ErrNotFound) {
			return PasswordResetVerification{}, ErrInvalidChallenge
		}
		return PasswordResetVerification{}, err
	}
	return PasswordResetVerification{ResetGrant: rawGrant, ExpiresInSeconds: int(passwordResetGrantTTL.Seconds())}, nil
}

func (s *Service) CompletePasswordReset(ctx context.Context, rawGrant, newPassword, userAgent, ipAddress string) (Customer, string, error) {
	if len(newPassword) < 8 || len(newPassword) > 72 || strings.TrimSpace(rawGrant) == "" {
		return Customer{}, "", ErrInvalidChallenge
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), s.cfg.AuthBcryptCost)
	if err != nil {
		return Customer{}, "", fmt.Errorf("hash marketplace password: %w", err)
	}
	token, tokenHash, err := newOpaqueToken()
	if err != nil {
		return Customer{}, "", err
	}
	now := time.Now().UTC()
	session := Session{
		ID: uuid.New(), TokenHash: tokenHash, UserAgent: truncate(userAgent, 512),
		IPAddress: truncate(ipAddress, 64), ExpiresAt: now.Add(s.cfg.AuthRefreshTokenTTL),
		LastUsedAt: now, CreatedAt: now,
	}
	customer, revokedHashes, err := s.repo.CompletePasswordReset(ctx, hashToken(rawGrant), string(passwordHash), session)
	if errors.Is(err, ErrNotFound) {
		return Customer{}, "", ErrInvalidChallenge
	}
	if err != nil {
		return Customer{}, "", err
	}
	_ = s.invalidateTokenHashesAfterCommit(ctx, revokedHashes)
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
		principal.SecurityRevision == principal.SessionRevision &&
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

func (s *Service) SetPassword(ctx context.Context, customerID uuid.UUID, currentPassword, newPassword string) (Customer, error) {
	if len(newPassword) < 8 || len(newPassword) > 72 {
		return Customer{}, ErrWeakPassword
	}
	currentHash, err := s.repo.PasswordHashByCustomerID(ctx, customerID)
	if err != nil {
		return Customer{}, err
	}
	if currentHash != "" && bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(currentPassword)) != nil {
		return Customer{}, ErrInvalidPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), s.cfg.AuthBcryptCost)
	if err != nil {
		return Customer{}, fmt.Errorf("hash marketplace password: %w", err)
	}
	customer, revokedHashes, err := s.repo.SetPassword(ctx, customerID, string(hash))
	if err == nil {
		_ = s.invalidateTokenHashesAfterCommit(ctx, revokedHashes)
	}
	return customer, err
}

func (s *Service) invalidateTokenHashesAfterCommit(ctx context.Context, hashes [][]byte) error {
	if s.sessionCache == nil || len(hashes) == 0 {
		return nil
	}
	identities := make([]string, len(hashes))
	for index, tokenHash := range hashes {
		identities[index] = hex.EncodeToString(tokenHash)
	}
	invalidateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return s.sessionCache.CacheDelete(invalidateCtx, "marketplace_session", identities...)
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
	case "":
		return "phone", normalizedPhone, "", nil
	case "whatsapp":
		return "phone", normalizedPhone, "whatsapp", nil
	default:
		return "", "", "", errors.New("phone identifiers must use WhatsApp delivery")
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
	return customer.PhoneVerifiedAt != nil && normalizedPhone == customer.Phone
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
func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
