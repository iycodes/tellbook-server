package marketplaceauth

import (
	"time"

	"github.com/google/uuid"
)

type Customer struct {
	ID                 uuid.UUID  `json:"id"`
	FullName           string     `json:"full_name"`
	Email              string     `json:"email,omitempty"`
	Phone              string     `json:"phone,omitempty"`
	WhatsApp           string     `json:"whatsapp,omitempty"`
	EmailVerifiedAt    *time.Time `json:"email_verified_at,omitempty"`
	PhoneVerifiedAt    *time.Time `json:"phone_verified_at,omitempty"`
	WhatsAppVerifiedAt *time.Time `json:"whatsapp_verified_at,omitempty"`
	Birthday           string     `json:"birthday,omitempty"`
	HasPassword        bool       `json:"has_password"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type Challenge struct {
	ID               uuid.UUID
	IdentifierType   string
	Identifier       string
	DeliveryChannel  string
	Purpose          string
	TargetCustomerID *uuid.UUID
	CodeHash         []byte
	FailedAttempts   int
	ExpiresAt        time.Time
	ConsumedAt       *time.Time
	CreatedAt        time.Time
}

type Session struct {
	ID         uuid.UUID
	CustomerID uuid.UUID
	TokenHash  []byte
	UserAgent  string
	IPAddress  string
	ExpiresAt  time.Time
	LastUsedAt time.Time
	CreatedAt  time.Time
}

// SessionPrincipal is the complete Redis session-cache payload. It deliberately
// excludes profile and contact fields.
type SessionPrincipal struct {
	SessionID        uuid.UUID `json:"session_id"`
	CustomerID       uuid.UUID `json:"customer_id"`
	ExpiresAt        time.Time `json:"expires_at"`
	SecurityRevision int64     `json:"security_revision"`
	SessionRevision  int64     `json:"session_revision"`
}

type Address struct {
	ID            uuid.UUID  `json:"id"`
	Label         string     `json:"label"`
	AddressLine1  string     `json:"address_line_1"`
	AddressLine2  string     `json:"address_line_2"`
	Locality      string     `json:"locality"`
	StateRegionID *uuid.UUID `json:"state_region_id,omitempty"`
	StateName     string     `json:"state_name,omitempty"`
	LGARegionID   *uuid.UUID `json:"lga_region_id,omitempty"`
	LGAName       string     `json:"lga_name,omitempty"`
	CountryCode   string     `json:"country_code"`
	PostalCode    string     `json:"postal_code"`
	Latitude      *float64   `json:"latitude,omitempty"`
	Longitude     *float64   `json:"longitude,omitempty"`
	IsDefault     bool       `json:"is_default"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type AddressInput struct {
	Label         string   `json:"label"`
	AddressLine1  string   `json:"address_line_1"`
	AddressLine2  string   `json:"address_line_2"`
	Locality      string   `json:"locality"`
	StateRegionID string   `json:"state_region_id"`
	LGARegionID   string   `json:"lga_region_id"`
	CountryCode   string   `json:"country_code"`
	PostalCode    string   `json:"postal_code"`
	Latitude      *float64 `json:"latitude"`
	Longitude     *float64 `json:"longitude"`
	LocationToken string   `json:"location_token"`
	IsDefault     bool     `json:"is_default"`
}

type SavedAddressLocation struct {
	LocationToken    string `json:"location_token"`
	FormattedAddress string `json:"formatted_address"`
	ResolutionStatus string `json:"resolution_status"`
	CountryCode      string `json:"country_code,omitempty"`
	StateRegionID    string `json:"state_region_id,omitempty"`
	StateName        string `json:"state_name,omitempty"`
	LGARegionID      string `json:"lga_region_id,omitempty"`
	LGAName          string `json:"lga_name,omitempty"`
	Locality         string `json:"locality,omitempty"`
	ExpiresAt        string `json:"expires_at"`
}

type NotificationPreferences struct {
	BookingEmail    bool      `json:"booking_email"`
	BookingSMS      bool      `json:"booking_sms"`
	BookingWhatsApp bool      `json:"booking_whatsapp"`
	MarketingEmail  bool      `json:"marketing_email"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type NotificationPreferencesInput struct {
	BookingEmail    bool `json:"booking_email"`
	BookingSMS      bool `json:"booking_sms"`
	BookingWhatsApp bool `json:"booking_whatsapp"`
	MarketingEmail  bool `json:"marketing_email"`
}

type ProfileInput struct {
	FullName string `json:"full_name"`
	Birthday string `json:"birthday"`
}
