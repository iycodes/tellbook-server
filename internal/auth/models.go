package auth

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type User struct {
	ID               uuid.UUID  `json:"id"`
	FullName         string     `json:"full_name"`
	Bio              string     `json:"bio"`
	CoverImageURL    string     `json:"cover_image_url,omitempty"`
	Email            string     `json:"email,omitempty"`
	EmailVerifiedAt  *time.Time `json:"email_verified_at,omitempty"`
	HasPassword      bool       `json:"has_password"`
	SecurityRevision int64      `json:"-"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type userRecord struct {
	User
	PasswordHash string
}

type RefreshSession struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	TokenHash       []byte
	UserAgent       string
	IPAddress       string
	ExpiresAt       time.Time
	LastUsedAt      time.Time
	CreatedAt       time.Time
	SessionRevision int64
}

type refreshSessionRecord struct {
	RefreshSession
	User User
}

type AccessTokenClaims struct {
	Email            string `json:"email,omitempty"`
	FullName         string `json:"full_name"`
	SecurityRevision int64  `json:"security_revision"`
	jwt.RegisteredClaims
}

type loginInput struct {
	Identifier string `json:"identifier"`
	Email      string `json:"email,omitempty"`
	Password   string `json:"password"`
}

type tokenPair struct {
	AccessToken string
}

type sessionMetadata struct {
	UserAgent string
	IPAddress string
}
