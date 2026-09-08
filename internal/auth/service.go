package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"booking/go-server/internal/authchallenge"
	"booking/go-server/internal/config"
	"booking/go-server/internal/storage"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials    = errors.New("invalid credentials")
	ErrInvalidRefresh        = errors.New("invalid refresh token")
	ErrInvalidAccess         = errors.New("invalid access token")
	ErrInvalidResetToken     = errors.New("invalid password reset token")
	ErrIdentityAlreadyLinked = errors.New("identity is already linked to this account")
	ErrIdentityConflict      = errors.New("identity is already linked")
	ErrWeakPassword          = errors.New("password must be between 8 and 72 characters")
)

type Service struct {
	repo              *Repository
	cfg               config.Config
	storage           *storage.R2Service
	challenges        *authchallenge.Service
	dummyPasswordHash []byte
}

func NewService(repo *Repository, cfg config.Config, storageService *storage.R2Service, challenges *authchallenge.Service) *Service {
	cost := cfg.AuthBcryptCost
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		cost = bcrypt.DefaultCost
	}
	dummyHash, _ := bcrypt.GenerateFromPassword([]byte("tellbook-invalid-password-padding"), cost)
	return &Service{repo: repo, cfg: cfg, storage: storageService, challenges: challenges, dummyPasswordHash: dummyHash}
}

type CodeVerificationResult struct {
	User               User
	Pair               tokenPair
	RefreshToken       string
	IsNewAccount       bool
	OnboardingRequired bool
}

func (s *Service) AuthCapabilities() authchallenge.Capabilities {
	return authchallenge.NewCapabilities(s.challenges, true)
}

func (s *Service) StartCodeChallenge(ctx context.Context, identifier, channel string) (authchallenge.Response, error) {
	if s.challenges == nil {
		return authchallenge.Response{}, authchallenge.ErrUnavailable
	}
	return s.challenges.Start(ctx, authchallenge.StartRequest{
		Realm: authchallenge.RealmProvider, RawIdentifier: identifier,
		Channel: channel, Purpose: authchallenge.PurposeSignIn,
	})
}

func (s *Service) CodeChallengeStatus(ctx context.Context, challengeID uuid.UUID) (authchallenge.Response, error) {
	if s.challenges == nil {
		return authchallenge.Response{}, authchallenge.ErrUnavailable
	}
	return s.challenges.Status(ctx, authchallenge.RealmProvider, challengeID, authchallenge.PurposeSignIn, nil)
}

func (s *Service) ResendCodeChallenge(ctx context.Context, challengeID uuid.UUID) (authchallenge.Response, error) {
	if s.challenges == nil {
		return authchallenge.Response{}, authchallenge.ErrUnavailable
	}
	return s.challenges.Resend(ctx, authchallenge.RealmProvider, challengeID, authchallenge.PurposeSignIn, nil)
}

func (s *Service) StartIdentityLink(ctx context.Context, userID uuid.UUID, rawIdentifier, channel string) (authchallenge.Response, error) {
	if s.challenges == nil {
		return authchallenge.Response{}, authchallenge.ErrUnavailable
	}
	identityType, identifier, normalizedChannel, err := authchallenge.NormalizeIdentifier(rawIdentifier, channel)
	if err != nil {
		return authchallenge.Response{}, err
	}
	owner, ownerErr := s.repo.IdentityOwner(ctx, identityType, identifier)
	if ownerErr == nil {
		if owner != userID {
			return authchallenge.Response{}, ErrIdentityConflict
		}
		return authchallenge.Response{}, ErrIdentityAlreadyLinked
	}
	if !errors.Is(ownerErr, ErrNotFound) {
		return authchallenge.Response{}, ownerErr
	}
	hasType, err := s.repo.UserHasVerifiedIdentityType(ctx, userID, identityType)
	if err != nil {
		return authchallenge.Response{}, err
	}
	if hasType {
		return authchallenge.Response{}, ErrIdentityConflict
	}
	return s.challenges.Start(ctx, authchallenge.StartRequest{
		Realm: authchallenge.RealmProvider, RawIdentifier: identifier, Channel: normalizedChannel,
		Purpose: authchallenge.PurposeLinkIdentity, TargetAccountID: &userID,
	})
}

func (s *Service) IdentityLinkStatus(ctx context.Context, userID, challengeID uuid.UUID) (authchallenge.Response, error) {
	if s.challenges == nil {
		return authchallenge.Response{}, authchallenge.ErrUnavailable
	}
	return s.challenges.Status(ctx, authchallenge.RealmProvider, challengeID, authchallenge.PurposeLinkIdentity, &userID)
}

func (s *Service) ResendIdentityLink(ctx context.Context, userID, challengeID uuid.UUID) (authchallenge.Response, error) {
	if s.challenges == nil {
		return authchallenge.Response{}, authchallenge.ErrUnavailable
	}
	return s.challenges.Resend(ctx, authchallenge.RealmProvider, challengeID, authchallenge.PurposeLinkIdentity, &userID)
}

func (s *Service) VerifyIdentityLink(ctx context.Context, userID, challengeID uuid.UUID, rawCode string) (User, error) {
	if s.challenges == nil {
		return User{}, authchallenge.ErrInvalidChallenge
	}
	challenge, err := s.challenges.Verify(ctx, authchallenge.RealmProvider, challengeID, rawCode, authchallenge.PurposeLinkIdentity, &userID)
	if err != nil {
		return User{}, authchallenge.ErrInvalidChallenge
	}
	user, err := s.repo.CompleteIdentityLink(ctx, challenge)
	if errors.Is(err, ErrNotFound) {
		return User{}, authchallenge.ErrInvalidChallenge
	}
	return user, err
}

func (s *Service) VerifyCodeChallenge(ctx context.Context, challengeID uuid.UUID, rawCode string, meta sessionMetadata) (CodeVerificationResult, error) {
	if s.challenges == nil {
		return CodeVerificationResult{}, authchallenge.ErrUnavailable
	}
	challenge, err := s.challenges.Verify(ctx, authchallenge.RealmProvider, challengeID, rawCode, authchallenge.PurposeSignIn, nil)
	if err != nil {
		return CodeVerificationResult{}, err
	}
	refreshToken, refreshHash, err := newOpaqueToken()
	if err != nil {
		return CodeVerificationResult{}, err
	}
	now := time.Now().UTC()
	session := RefreshSession{
		ID: uuid.New(), TokenHash: refreshHash, UserAgent: truncate(meta.UserAgent, 512),
		IPAddress: truncate(meta.IPAddress, 64), ExpiresAt: now.Add(s.cfg.AuthRefreshTokenTTL),
		LastUsedAt: now, CreatedAt: now,
	}
	user, isNewAccount, onboardingRequired, err := s.repo.CompleteCodeChallenge(ctx, challenge, session)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return CodeVerificationResult{}, authchallenge.ErrInvalidChallenge
		}
		return CodeVerificationResult{}, err
	}
	accessToken, err := s.signAccessToken(user, now)
	if err != nil {
		return CodeVerificationResult{}, err
	}
	return CodeVerificationResult{
		User: user, Pair: tokenPair{AccessToken: accessToken}, RefreshToken: refreshToken,
		IsNewAccount: isNewAccount, OnboardingRequired: onboardingRequired,
	}, nil
}

func (s *Service) Login(ctx context.Context, input loginInput, meta sessionMetadata) (User, tokenPair, string, error) {
	identifier := input.Identifier
	if strings.TrimSpace(identifier) == "" {
		identifier = input.Email
	}
	_, normalizedIdentifier, _, err := authchallenge.NormalizeIdentifier(identifier, inferredPasswordChannel(identifier))
	if err != nil {
		_ = bcrypt.CompareHashAndPassword(s.dummyPasswordHash, []byte(input.Password))
		return User{}, tokenPair{}, "", ErrInvalidCredentials
	}
	candidates, err := s.repo.PasswordCandidates(ctx, normalizedIdentifier)
	if err != nil {
		return User{}, tokenPair{}, "", err
	}
	var userID uuid.UUID
	compared := false
	for _, candidate := range candidates {
		compared = true
		if bcrypt.CompareHashAndPassword([]byte(candidate.PasswordHash), []byte(input.Password)) == nil {
			userID = candidate.UserID
			break
		}
	}
	if userID == uuid.Nil {
		if !compared {
			_ = bcrypt.CompareHashAndPassword(s.dummyPasswordHash, []byte(input.Password))
		}
		return User{}, tokenPair{}, "", ErrInvalidCredentials
	}
	user, err := s.repo.GetUserByID(ctx, userID)
	if err != nil {
		return User{}, tokenPair{}, "", err
	}
	pair, refreshToken, err := s.issueTokens(ctx, user, meta)
	if err != nil {
		return User{}, tokenPair{}, "", err
	}
	return user, pair, refreshToken, nil
}

type PasswordResetVerification struct {
	ResetGrant       string `json:"reset_grant"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
}

type PasswordResetResult struct {
	User         User
	Pair         tokenPair
	RefreshToken string
}

const passwordResetGrantTTL = 10 * time.Minute

func (s *Service) StartPasswordReset(ctx context.Context, identifier, channel string) (authchallenge.Response, error) {
	if s.challenges == nil {
		return authchallenge.Response{}, authchallenge.ErrUnavailable
	}
	identifierType, normalized, normalizedChannel, err := authchallenge.NormalizeIdentifier(identifier, channel)
	if err != nil {
		return authchallenge.Response{}, err
	}
	// Apply the same bounded password work regardless of account existence.
	_ = bcrypt.CompareHashAndPassword(s.dummyPasswordHash, []byte("password-reset-padding"))
	candidate, err := s.repo.PasswordResetAccount(ctx, identifierType, normalized)
	if err == nil {
		return s.challenges.Start(ctx, authchallenge.StartRequest{
			Realm: authchallenge.RealmProvider, RawIdentifier: normalized, Channel: normalizedChannel,
			Purpose: authchallenge.PurposePasswordReset, TargetAccountID: &candidate.UserID,
		})
	}
	if !errors.Is(err, ErrNotFound) {
		return authchallenge.Response{}, err
	}
	return s.challenges.StartSyntheticPasswordReset(ctx, authchallenge.RealmProvider, normalized, normalizedChannel)
}

func (s *Service) VerifyPasswordReset(ctx context.Context, challengeID uuid.UUID, rawCode string) (PasswordResetVerification, error) {
	if s.challenges == nil {
		return PasswordResetVerification{}, authchallenge.ErrInvalidChallenge
	}
	challenge, err := s.challenges.VerifyPasswordReset(ctx, authchallenge.RealmProvider, challengeID, rawCode)
	if err != nil {
		return PasswordResetVerification{}, authchallenge.ErrInvalidChallenge
	}
	rawGrant, grantHash, err := newOpaqueToken()
	if err != nil {
		return PasswordResetVerification{}, err
	}
	if err := s.repo.StorePasswordResetGrant(ctx, challenge, grantHash, time.Now().UTC().Add(passwordResetGrantTTL)); err != nil {
		if errors.Is(err, ErrNotFound) {
			return PasswordResetVerification{}, authchallenge.ErrInvalidChallenge
		}
		return PasswordResetVerification{}, err
	}
	return PasswordResetVerification{ResetGrant: rawGrant, ExpiresInSeconds: int(passwordResetGrantTTL.Seconds())}, nil
}

func (s *Service) CompletePasswordReset(ctx context.Context, rawGrant, newPassword string, meta sessionMetadata) (PasswordResetResult, error) {
	if len(newPassword) < 8 || len(newPassword) > 72 || strings.TrimSpace(rawGrant) == "" {
		return PasswordResetResult{}, ErrInvalidResetToken
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), s.cfg.AuthBcryptCost)
	if err != nil {
		return PasswordResetResult{}, fmt.Errorf("hash provider password: %w", err)
	}
	refreshToken, refreshHash, err := newOpaqueToken()
	if err != nil {
		return PasswordResetResult{}, err
	}
	now := time.Now().UTC()
	session := RefreshSession{
		ID: uuid.New(), TokenHash: refreshHash, UserAgent: truncate(meta.UserAgent, 512),
		IPAddress: truncate(meta.IPAddress, 64), ExpiresAt: now.Add(s.cfg.AuthRefreshTokenTTL),
		LastUsedAt: now, CreatedAt: now,
	}
	user, err := s.repo.CompletePasswordReset(ctx, hashToken(rawGrant), string(passwordHash), session)
	if errors.Is(err, ErrNotFound) {
		return PasswordResetResult{}, ErrInvalidResetToken
	}
	if err != nil {
		return PasswordResetResult{}, err
	}
	accessToken, err := s.signAccessToken(user, now)
	if err != nil {
		return PasswordResetResult{}, err
	}
	return PasswordResetResult{User: user, Pair: tokenPair{AccessToken: accessToken}, RefreshToken: refreshToken}, nil
}

func (s *Service) UpdatePassword(ctx context.Context, userID uuid.UUID, currentPassword, newPassword string) (User, error) {
	if len(newPassword) < 8 || len(newPassword) > 72 {
		return User{}, ErrWeakPassword
	}
	currentHash, err := s.repo.PasswordHashByUserID(ctx, userID)
	if err != nil {
		return User{}, err
	}
	if currentHash != "" && bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(currentPassword)) != nil {
		return User{}, ErrInvalidCredentials
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), s.cfg.AuthBcryptCost)
	if err != nil {
		return User{}, fmt.Errorf("hash provider password: %w", err)
	}
	return s.repo.ChangePassword(ctx, userID, string(passwordHash))
}

func inferredPasswordChannel(identifier string) string {
	if strings.Contains(strings.TrimSpace(identifier), "@") {
		return authchallenge.ChannelEmail
	}
	return authchallenge.ChannelWhatsApp
}

func (s *Service) Refresh(ctx context.Context, rawRefreshToken string, meta sessionMetadata) (User, tokenPair, string, error) {
	currentTokenHash := hashToken(rawRefreshToken)
	record, err := s.repo.GetRefreshSessionByTokenHash(ctx, currentTokenHash)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return User{}, tokenPair{}, "", ErrInvalidRefresh
		}
		return User{}, tokenPair{}, "", err
	}
	if record.SessionRevision != record.User.SecurityRevision {
		return User{}, tokenPair{}, "", ErrInvalidRefresh
	}

	nextRefreshToken, nextRefreshHash, err := newOpaqueToken()
	if err != nil {
		return User{}, tokenPair{}, "", err
	}

	now := time.Now().UTC()
	if err := s.repo.RotateRefreshSession(
		ctx,
		record.ID,
		currentTokenHash,
		nextRefreshHash,
		now.Add(s.cfg.AuthRefreshTokenTTL),
		now,
		truncate(meta.UserAgent, 512),
		truncate(meta.IPAddress, 64),
	); err != nil {
		return User{}, tokenPair{}, "", err
	}

	accessToken, err := s.signAccessToken(record.User, now)
	if err != nil {
		return User{}, tokenPair{}, "", err
	}

	return record.User, tokenPair{
		AccessToken: accessToken,
	}, nextRefreshToken, nil
}

func (s *Service) AuthenticateAccessToken(ctx context.Context, rawAccessToken string) (User, error) {
	claims := &AccessTokenClaims{}
	token, err := jwt.ParseWithClaims(
		rawAccessToken,
		claims,
		func(token *jwt.Token) (any, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method %q", token.Method.Alg())
			}
			return []byte(s.cfg.AuthAccessTokenSecret), nil
		},
		jwt.WithIssuer(s.cfg.AuthIssuer),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithLeeway(15*time.Second),
	)
	if err != nil || !token.Valid {
		return User{}, ErrInvalidAccess
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return User{}, ErrInvalidAccess
	}

	user, err := s.repo.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return User{}, ErrInvalidAccess
		}
		return User{}, err
	}
	if claims.SecurityRevision < 1 || claims.SecurityRevision != user.SecurityRevision {
		return User{}, ErrInvalidAccess
	}

	return user, nil
}

func (s *Service) Logout(ctx context.Context, rawRefreshToken string) error {
	if strings.TrimSpace(rawRefreshToken) == "" {
		return nil
	}
	return s.repo.DeleteRefreshSessionByTokenHash(ctx, hashToken(rawRefreshToken))
}

func (s *Service) issueTokens(ctx context.Context, user User, meta sessionMetadata) (tokenPair, string, error) {
	pair, refreshToken, refreshSession, err := s.prepareTokens(user, meta)
	if err != nil {
		return tokenPair{}, "", err
	}

	if err := s.repo.CreateRefreshSession(ctx, refreshSession); err != nil {
		return tokenPair{}, "", err
	}

	return pair, refreshToken, nil
}

func (s *Service) prepareTokens(
	user User,
	meta sessionMetadata,
) (tokenPair, string, RefreshSession, error) {
	accessIssuedAt := time.Now().UTC()
	accessToken, err := s.signAccessToken(user, accessIssuedAt)
	if err != nil {
		return tokenPair{}, "", RefreshSession{}, err
	}

	refreshToken, refreshHash, err := newOpaqueToken()
	if err != nil {
		return tokenPair{}, "", RefreshSession{}, err
	}

	now := time.Now().UTC()
	refreshSession := RefreshSession{
		ID:              uuid.New(),
		UserID:          user.ID,
		TokenHash:       refreshHash,
		UserAgent:       truncate(meta.UserAgent, 512),
		IPAddress:       truncate(meta.IPAddress, 64),
		ExpiresAt:       now.Add(s.cfg.AuthRefreshTokenTTL),
		LastUsedAt:      now,
		CreatedAt:       now,
		SessionRevision: user.SecurityRevision,
	}

	return tokenPair{
		AccessToken: accessToken,
	}, refreshToken, refreshSession, nil
}

func (s *Service) signAccessToken(user User, issuedAt time.Time) (string, error) {
	claims := AccessTokenClaims{
		Email:            user.Email,
		FullName:         user.FullName,
		SecurityRevision: user.SecurityRevision,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID.String(),
			Issuer:    s.cfg.AuthIssuer,
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			NotBefore: jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(issuedAt.Add(s.cfg.AuthAccessTokenTTL)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(s.cfg.AuthAccessTokenSecret))
	if err != nil {
		return "", fmt.Errorf("sign access token: %w", err)
	}

	return signed, nil
}

func MetadataFromRequest(r *http.Request) sessionMetadata {
	return sessionMetadata{
		UserAgent: strings.TrimSpace(r.UserAgent()),
		IPAddress: clientIP(r),
	}
}

func newOpaqueToken() (string, []byte, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", nil, fmt.Errorf("generate token: %w", err)
	}

	rawToken := base64.RawURLEncoding.EncodeToString(randomBytes)
	return rawToken, hashToken(rawToken), nil
}

func hashToken(rawToken string) []byte {
	sum := sha256.Sum256([]byte(rawToken))
	return sum[:]
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}

	return host
}
