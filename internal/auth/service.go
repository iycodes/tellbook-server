package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"mime"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"booking/go-server/internal/config"
	"booking/go-server/internal/mailer"
	"booking/go-server/internal/storage"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials       = errors.New("invalid credentials")
	ErrEmailTaken               = errors.New("email already in use")
	ErrInvalidRefresh           = errors.New("invalid refresh token")
	ErrInvalidAccess            = errors.New("invalid access token")
	ErrInvalidResetToken        = errors.New("invalid password reset token")
	ErrInvalidRegistrationToken = errors.New("invalid registration verification token")
)

type Service struct {
	repo    *Repository
	cfg     config.Config
	storage *storage.R2Service
	mailer  mailer.Sender
}

const (
	verificationCodeTTL          = 15 * time.Minute
	passwordResetCodeMaxAttempts = 5
)

func NewService(repo *Repository, cfg config.Config, storageService *storage.R2Service, mailerSender mailer.Sender) *Service {
	return &Service{repo: repo, cfg: cfg, storage: storageService, mailer: mailerSender}
}

func (s *Service) StartRegistration(ctx context.Context, input registerInput) error {
	if s.mailer == nil || !s.mailer.Enabled() {
		return errors.New("email delivery is not configured")
	}

	input.FullName = strings.TrimSpace(input.FullName)
	input.Bio = strings.TrimSpace(input.Bio)
	input.Email = normalizeEmail(input.Email)
	input.Password = strings.TrimSpace(input.Password)
	input.CoverImageDataURL = strings.TrimSpace(input.CoverImageDataURL)
	input.CoverImageContentType = strings.TrimSpace(input.CoverImageContentType)

	if len(input.FullName) < 2 {
		return fmt.Errorf("full_name must be at least 2 characters")
	}
	if !strings.Contains(input.Email, "@") {
		return fmt.Errorf("email must be valid")
	}
	if len(input.Password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	if input.CoverImageDataURL != "" {
		if _, err := decodeRegistrationCover(input); err != nil {
			return err
		}
	}

	if _, err := s.repo.GetUserByEmail(ctx, input.Email); err == nil {
		return ErrEmailTaken
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(input.Password), s.cfg.AuthBcryptCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	rawToken, tokenHash, err := newSixDigitCode()
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	pending := pendingRegistration{
		ID:                    uuid.New(),
		FullName:              input.FullName,
		Bio:                   input.Bio,
		Email:                 input.Email,
		PasswordHash:          string(passwordHash),
		CoverImageDataURL:     input.CoverImageDataURL,
		CoverImageContentType: input.CoverImageContentType,
		TokenHash:             tokenHash,
		ExpiresAt:             now.Add(verificationCodeTTL),
		CreatedAt:             now,
		UpdatedAt:             now,
	}

	if err := s.repo.UpsertPendingRegistration(ctx, pending); err != nil {
		return err
	}

	if err := s.sendRegistrationVerificationEmail(ctx, pending, rawToken); err != nil {
		_ = s.repo.DeletePendingRegistration(ctx, pending.ID)
		return err
	}

	return nil
}

func (s *Service) ResendRegistrationVerification(ctx context.Context, email string) error {
	if s.mailer == nil || !s.mailer.Enabled() {
		return errors.New("email delivery is not configured")
	}

	pending, err := s.repo.GetPendingRegistrationByEmail(ctx, normalizeEmail(email))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrInvalidRegistrationToken
		}
		return err
	}

	rawToken, tokenHash, err := newSixDigitCode()
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	if err := s.repo.UpdatePendingRegistrationToken(
		ctx,
		pending.ID,
		tokenHash,
		now.Add(verificationCodeTTL),
		now,
	); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrInvalidRegistrationToken
		}
		return err
	}

	return s.sendRegistrationVerificationEmail(ctx, pending, rawToken)
}

func (s *Service) CompleteRegistration(
	ctx context.Context,
	email string,
	rawToken string,
	meta sessionMetadata,
) (User, tokenPair, string, error) {
	email = normalizeEmail(email)
	rawToken = strings.TrimSpace(rawToken)
	if email == "" || !isSixDigitCode(rawToken) {
		return User{}, tokenPair{}, "", ErrInvalidRegistrationToken
	}

	pending, err := s.repo.GetPendingRegistrationByToken(ctx, email, hashToken(rawToken))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return User{}, tokenPair{}, "", ErrInvalidRegistrationToken
		}
		return User{}, tokenPair{}, "", err
	}

	now := time.Now().UTC()
	verifiedAt := now
	user := userRecord{
		User: User{
			ID:              pending.ID,
			FullName:        pending.FullName,
			Bio:             pending.Bio,
			Email:           pending.Email,
			EmailVerifiedAt: &verifiedAt,
			CreatedAt:       now,
			UpdatedAt:       now,
		},
		PasswordHash: pending.PasswordHash,
	}

	var uploadedCoverObject storageObject
	if pending.CoverImageDataURL != "" {
		uploadedCoverObject, err = s.uploadRegistrationCover(ctx, user.ID, registerInput{
			CoverImageDataURL:     pending.CoverImageDataURL,
			CoverImageContentType: pending.CoverImageContentType,
		})
		if err != nil {
			return User{}, tokenPair{}, "", err
		}
		user.CoverImageURL = uploadedCoverObject.URL
	}

	pair, refreshToken, refreshSession, err := s.prepareTokens(user.User, meta)
	if err != nil {
		if uploadedCoverObject.Key != "" {
			_ = s.deleteRegistrationCover(ctx, uploadedCoverObject)
		}
		return User{}, tokenPair{}, "", err
	}

	if err := s.repo.PromotePendingRegistration(ctx, pending, user, refreshSession); err != nil {
		if uploadedCoverObject.Key != "" {
			_ = s.deleteRegistrationCover(ctx, uploadedCoverObject)
		}
		if isUniqueViolation(err) {
			return User{}, tokenPair{}, "", ErrEmailTaken
		}
		if errors.Is(err, ErrNotFound) {
			return User{}, tokenPair{}, "", ErrInvalidRegistrationToken
		}
		return User{}, tokenPair{}, "", err
	}

	return user.User, pair, refreshToken, nil
}

func (s *Service) sendRegistrationVerificationEmail(
	ctx context.Context,
	pending pendingRegistration,
	rawToken string,
) error {
	if err := s.mailer.Send(ctx, mailer.Message{
		ToEmail: pending.Email,
		ToName:  pending.FullName,
		Subject: "Verify your email",
		Text: fmt.Sprintf(
			"Hello %s,\n\nUse this code to finish creating your account:\n\n%s\n\nThis code expires in 15 minutes.\n",
			pending.FullName,
			rawToken,
		),
	}); err != nil {
		return fmt.Errorf("send registration verification email: %w", err)
	}

	return nil
}

type registrationCoverPayload struct {
	ContentType string
	Data        []byte
	Extension   string
}

type storageObject struct {
	Key        string
	BucketName string
	URL        string
}

func (s *Service) uploadRegistrationCover(ctx context.Context, userID uuid.UUID, input registerInput) (storageObject, error) {
	if s.storage == nil {
		return storageObject{}, errors.New("image uploads are not configured")
	}

	payload, err := decodeRegistrationCover(input)
	if err != nil {
		return storageObject{}, err
	}

	objectKey := fmt.Sprintf("clients/%s/cover-%d%s", userID.String(), time.Now().UTC().Unix(), payload.Extension)
	bucketName := s.storage.PrivateBucketName()

	objectURL, err := s.storage.Upload(ctx, payload.Data, objectKey, payload.ContentType, bucketName)
	if err != nil {
		return storageObject{}, fmt.Errorf("upload registration cover: %w", err)
	}

	return storageObject{
		Key:        objectKey,
		BucketName: bucketName,
		URL:        objectURL,
	}, nil
}

func (s *Service) deleteRegistrationCover(ctx context.Context, object storageObject) error {
	if s.storage == nil || object.Key == "" {
		return nil
	}
	return s.storage.Delete(ctx, object.Key, object.BucketName)
}

func (s *Service) Login(ctx context.Context, input loginInput, meta sessionMetadata) (User, tokenPair, string, error) {
	record, err := s.repo.GetUserByEmail(ctx, normalizeEmail(input.Email))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return User{}, tokenPair{}, "", ErrInvalidCredentials
		}
		return User{}, tokenPair{}, "", err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(record.PasswordHash), []byte(input.Password)); err != nil {
		return User{}, tokenPair{}, "", ErrInvalidCredentials
	}

	pair, refreshToken, err := s.issueTokens(ctx, record.User, meta)
	if err != nil {
		return User{}, tokenPair{}, "", err
	}

	return record.User, pair, refreshToken, nil
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

	return user, nil
}

func (s *Service) Logout(ctx context.Context, rawRefreshToken string) error {
	if strings.TrimSpace(rawRefreshToken) == "" {
		return nil
	}
	return s.repo.DeleteRefreshSessionByTokenHash(ctx, hashToken(rawRefreshToken))
}

func (s *Service) SendPasswordReset(ctx context.Context, email string) error {
	if s.mailer == nil || !s.mailer.Enabled() {
		return errors.New("email delivery is not configured")
	}

	email = normalizeEmail(email)
	record, err := s.repo.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}

	if err := s.repo.DeletePasswordResetTokensByUserID(ctx, record.ID); err != nil {
		return err
	}

	var rawToken string
	for range passwordResetCodeMaxAttempts {
		code, tokenHash, err := newSixDigitCode()
		if err != nil {
			return err
		}

		err = s.repo.CreatePasswordResetToken(
			ctx,
			record.ID,
			tokenHash,
			time.Now().UTC().Add(verificationCodeTTL),
		)
		if err == nil {
			rawToken = code
			break
		}
		if !isUniqueViolation(err) {
			return err
		}
	}
	if rawToken == "" {
		return errors.New("could not allocate a unique password reset code")
	}

	if err := s.mailer.Send(ctx, mailer.Message{
		ToEmail: record.Email,
		ToName:  record.FullName,
		Subject: "Reset your password",
		Text: fmt.Sprintf(
			"Hello %s,\n\nUse this code to reset your password:\n\n%s\n\nThis code expires in 15 minutes.\n",
			record.FullName,
			rawToken,
		),
	}); err != nil {
		_ = s.repo.DeletePasswordResetTokensByUserID(ctx, record.ID)
		return fmt.Errorf("send password reset email: %w", err)
	}

	return nil
}

func (s *Service) ResetPassword(ctx context.Context, rawToken, newPassword string) error {
	rawToken = strings.TrimSpace(rawToken)
	if !isSixDigitCode(rawToken) {
		return ErrInvalidResetToken
	}

	newPassword = strings.TrimSpace(newPassword)
	if len(newPassword) < 8 {
		return fmt.Errorf("new_password must be at least 8 characters")
	}

	userID, err := s.repo.GetPasswordResetTokenUserID(ctx, hashToken(rawToken))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrInvalidResetToken
		}
		return err
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), s.cfg.AuthBcryptCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	now := time.Now().UTC()
	if err := s.repo.UpdateUserPassword(ctx, userID, string(passwordHash), now); err != nil {
		return err
	}
	if err := s.repo.DeletePasswordResetTokensByUserID(ctx, userID); err != nil {
		return err
	}
	if err := s.repo.DeleteRefreshSessionsByUserID(ctx, userID); err != nil {
		return err
	}

	return nil
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
		ID:         uuid.New(),
		UserID:     user.ID,
		TokenHash:  refreshHash,
		UserAgent:  truncate(meta.UserAgent, 512),
		IPAddress:  truncate(meta.IPAddress, 64),
		ExpiresAt:  now.Add(s.cfg.AuthRefreshTokenTTL),
		LastUsedAt: now,
		CreatedAt:  now,
	}

	return tokenPair{
		AccessToken: accessToken,
	}, refreshToken, refreshSession, nil
}

func (s *Service) signAccessToken(user User, issuedAt time.Time) (string, error) {
	claims := AccessTokenClaims{
		Email:    user.Email,
		FullName: user.FullName,
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

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func decodeRegistrationCover(input registerInput) (registrationCoverPayload, error) {
	contentType := input.CoverImageContentType
	rawBase64 := input.CoverImageDataURL

	if strings.HasPrefix(rawBase64, "data:") {
		parts := strings.SplitN(rawBase64, ",", 2)
		if len(parts) != 2 {
			return registrationCoverPayload{}, errors.New("cover_image_data_url must be a valid data URL")
		}

		metadata := strings.TrimPrefix(parts[0], "data:")
		rawBase64 = parts[1]
		if contentType == "" {
			contentType = strings.TrimSuffix(metadata, ";base64")
		}
	}

	contentType = strings.TrimSpace(contentType)
	switch contentType {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
	default:
		return registrationCoverPayload{}, errors.New("cover image must be jpeg, png, webp, or gif")
	}

	data, err := base64.StdEncoding.DecodeString(rawBase64)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(rawBase64)
	}
	if err != nil {
		return registrationCoverPayload{}, errors.New("cover image must be valid base64")
	}

	if len(data) == 0 {
		return registrationCoverPayload{}, errors.New("cover image cannot be empty")
	}
	if len(data) > 4<<20 {
		return registrationCoverPayload{}, errors.New("cover image must be 4 MB or smaller")
	}

	detectedContentType := http.DetectContentType(data)
	if detectedContentType != contentType {
		if contentType == "image/jpeg" && detectedContentType == "image/jpg" {
			detectedContentType = contentType
		}
		if !strings.HasPrefix(detectedContentType, "image/") {
			return registrationCoverPayload{}, errors.New("cover image content type is invalid")
		}
		contentType = detectedContentType
	}

	extensions, _ := mime.ExtensionsByType(contentType)
	extension := ".bin"
	if len(extensions) > 0 {
		extension = extensions[0]
	}

	return registrationCoverPayload{
		ContentType: contentType,
		Data:        data,
		Extension:   filepath.Clean(extension),
	}, nil
}

func newOpaqueToken() (string, []byte, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", nil, fmt.Errorf("generate token: %w", err)
	}

	rawToken := base64.RawURLEncoding.EncodeToString(randomBytes)
	return rawToken, hashToken(rawToken), nil
}

func newSixDigitCode() (string, []byte, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", nil, fmt.Errorf("generate verification code: %w", err)
	}

	code := fmt.Sprintf("%06d", value.Int64())
	return code, hashToken(code), nil
}

func isSixDigitCode(value string) bool {
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
	for _, header := range []string{"CF-Connecting-IP", "X-Forwarded-For", "X-Real-IP"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			if header == "X-Forwarded-For" {
				value = strings.TrimSpace(strings.Split(value, ",")[0])
			}
			return value
		}
	}

	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}

	return host
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505"
}
