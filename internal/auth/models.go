package auth

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type User struct {
	ID              uuid.UUID  `json:"id"`
	FullName        string     `json:"full_name"`
	Bio             string     `json:"bio"`
	CoverImageURL   string     `json:"cover_image_url,omitempty"`
	Email           string     `json:"email"`
	EmailVerifiedAt *time.Time `json:"email_verified_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type userRecord struct {
	User
	PasswordHash string
}

type pendingRegistration struct {
	ID                    uuid.UUID
	FullName              string
	Bio                   string
	Email                 string
	PasswordHash          string
	CoverImageDataURL     string
	CoverImageContentType string
	TokenHash             []byte
	ExpiresAt             time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type RefreshSession struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	TokenHash  []byte
	UserAgent  string
	IPAddress  string
	ExpiresAt  time.Time
	LastUsedAt time.Time
	CreatedAt  time.Time
}

type refreshSessionRecord struct {
	RefreshSession
	User User
}

type AccessTokenClaims struct {
	Email    string `json:"email"`
	FullName string `json:"full_name"`
	jwt.RegisteredClaims
}

type registerInput struct {
	FullName              string `json:"full_name"`
	Bio                   string `json:"bio"`
	Email                 string `json:"email"`
	Password              string `json:"password"`
	CoverImageDataURL     string `json:"cover_image_data_url"`
	CoverImageContentType string `json:"cover_image_content_type"`
}

type loginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type forgotPasswordInput struct {
	Email string `json:"email"`
}

type resetPasswordInput struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

type verifyRegistrationInput struct {
	Email string `json:"email"`
	Token string `json:"token"`
}

type resendRegistrationInput struct {
	Email string `json:"email"`
}

type tokenPair struct {
	AccessToken string
}

type sessionMetadata struct {
	UserAgent string
	IPAddress string
}
