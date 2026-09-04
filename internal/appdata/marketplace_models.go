package appdata

import (
	"time"

	"booking/go-server/internal/money"

	"github.com/google/uuid"
)

type MarketplaceCategory struct {
	ID            string `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	ImageURL      string `json:"image_url"`
	ProviderCount int    `json:"provider_count"`
}

type MarketplaceRegion struct {
	ID            string `json:"id"`
	ParentID      string `json:"parent_id,omitempty"`
	Level         string `json:"level"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	ProviderCount int    `json:"provider_count"`
}

type MarketplaceProvider struct {
	ID                           string               `json:"id"`
	HandleSlug                   string               `json:"handle_slug"`
	PublicBookingURL             string               `json:"public_booking_url"`
	BusinessName                 string               `json:"business_name"`
	Headline                     string               `json:"headline"`
	CategoryID                   string               `json:"category_id"`
	CategoryName                 string               `json:"category_name"`
	AvatarURL                    string               `json:"avatar_url,omitempty"`
	HeroImageURL                 string               `json:"hero_image_url,omitempty"`
	Verified                     bool                 `json:"verified"`
	ReviewRating                 float64              `json:"review_rating"`
	ReviewCount                  int                  `json:"review_count"`
	LocationLabel                string               `json:"location_label"`
	DistanceMeters               *int                 `json:"distance_meters,omitempty"`
	ServiceID                    string               `json:"service_id"`
	ServiceSlug                  string               `json:"service_slug"`
	ServiceTitle                 string               `json:"service_title"`
	DurationMinutes              int                  `json:"duration_minutes"`
	PriceAmountMinor             money.Minor          `json:"price_amount_minor"`
	CurrencyCode                 string               `json:"currency_code"`
	FulfillmentMode              string               `json:"fulfillment_mode"`
	CompletedBookings            int                  `json:"completed_bookings"`
	NextAvailableAt              *time.Time           `json:"next_available_at,omitempty"`
	MapPoint                     *MarketplaceMapPoint `json:"map_point,omitempty"`
	Badges                       []string             `json:"badges"`
	providerDocumentRevision     int64
	serviceDocumentRevision      int64
	availabilityDocumentRevision int64
}

type MarketplaceMapPoint struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Precision string  `json:"precision"`
}

type MarketplaceLocationContext struct {
	Mode             string `json:"mode"`
	Label            string `json:"label"`
	StateRegionID    string `json:"state_region_id,omitempty"`
	LGARegionID      string `json:"lga_region_id,omitempty"`
	SearchRadiusM    int    `json:"search_radius_meters,omitempty"`
	ResolutionStatus string `json:"resolution_status,omitempty"`
}

type MarketplaceHomeResponse struct {
	Providers []MarketplaceProvider      `json:"providers"`
	Location  MarketplaceLocationContext `json:"location"`
}

type MarketplaceProviderSearchInput struct {
	Query             string
	LocationToken     string
	StateID           *uuid.UUID
	LGAID             *uuid.UUID
	CategoryID        *uuid.UUID
	RadiusMeters      int
	MinimumRating     float64
	MinimumPriceMinor *int64
	MaximumPriceMinor *int64
	FulfillmentMode   string
	AvailableOn       *time.Time
	Sort              string
	Cursor            *MarketplaceProviderSearchCursor
	Limit             int
}

type MarketplaceProviderSearchCursor struct {
	Primary    string `json:"p"`
	Secondary  string `json:"q"`
	Tertiary   string `json:"t"`
	ProviderID string `json:"i"`
}

type MarketplaceProviderSearchResponse struct {
	Items      []MarketplaceProvider      `json:"items"`
	NextCursor string                     `json:"next_cursor,omitempty"`
	Location   MarketplaceLocationContext `json:"location"`
	nextCursor *MarketplaceProviderSearchCursor
}

type MarketplaceReadiness struct {
	Ready           bool     `json:"ready"`
	BlockingReasons []string `json:"blocking_reasons"`
}

type MarketplaceProfileSettings struct {
	Enabled            bool                  `json:"enabled"`
	CategoryID         string                `json:"category_id,omitempty"`
	LocationVisibility string                `json:"location_visibility"`
	Categories         []MarketplaceCategory `json:"categories"`
	Readiness          MarketplaceReadiness  `json:"readiness"`
}

type UpdateMarketplaceProfileSettingsInput struct {
	Enabled            bool   `json:"enabled"`
	CategoryID         string `json:"category_id"`
	LocationVisibility string `json:"location_visibility"`
}

type MarketplaceBookingStatus string

const (
	MarketplaceBookingUpcoming  MarketplaceBookingStatus = "upcoming"
	MarketplaceBookingPast      MarketplaceBookingStatus = "past"
	MarketplaceBookingCancelled MarketplaceBookingStatus = "cancelled"
)

type MarketplaceBookingListItem struct {
	ID                 string                   `json:"id"`
	StatusGroup        MarketplaceBookingStatus `json:"status_group"`
	Status             string                   `json:"status"`
	ServiceID          string                   `json:"service_id,omitempty"`
	ServiceTitle       string                   `json:"service_title"`
	ServiceImageURL    string                   `json:"service_image_url,omitempty"`
	ProviderID         string                   `json:"provider_id"`
	ProviderName       string                   `json:"provider_name"`
	ProviderHandle     string                   `json:"provider_handle"`
	ProviderAvatarURL  string                   `json:"provider_avatar_url,omitempty"`
	StartsAt           time.Time                `json:"starts_at"`
	EndsAt             time.Time                `json:"ends_at"`
	Timezone           string                   `json:"timezone"`
	DurationMinutes    int                      `json:"duration_minutes"`
	LocationLabel      string                   `json:"location_label"`
	FulfillmentMode    string                   `json:"fulfillment_mode"`
	TotalAmountMinor   money.Minor              `json:"total_amount_minor"`
	NetPaidAmountMinor money.Minor              `json:"net_paid_amount_minor"`
	CurrencyCode       string                   `json:"currency_code"`
	PaymentStatus      string                   `json:"payment_status"`
	AgreementStatus    string                   `json:"agreement_status"`
	RefundStatus       string                   `json:"refund_status,omitempty"`
	ReviewStatus       string                   `json:"review_status,omitempty"`
}

type MarketplaceBookingCounts struct {
	Upcoming  int `json:"upcoming"`
	Past      int `json:"past"`
	Cancelled int `json:"cancelled"`
}

type MarketplaceBookingListResponse struct {
	Items      []MarketplaceBookingListItem `json:"items"`
	Counts     *MarketplaceBookingCounts    `json:"counts,omitempty"`
	NextCursor string                       `json:"next_cursor,omitempty"`
}

type MarketplaceBookingPayment struct {
	Status                 string      `json:"status"`
	TotalAmountMinor       money.Minor `json:"total_amount_minor"`
	NetPaidAmountMinor     money.Minor `json:"net_paid_amount_minor"`
	OutstandingAmountMinor money.Minor `json:"outstanding_amount_minor"`
	CurrencyCode           string      `json:"currency_code"`
	ReceiptAvailable       bool        `json:"receipt_available"`
}

type MarketplaceBookingAgreement struct {
	Title        string     `json:"title,omitempty"`
	Status       string     `json:"status"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	PDFAvailable bool       `json:"pdf_available"`
}

type MarketplaceBookingAllowedActions struct {
	AddToCalendar   bool `json:"add_to_calendar"`
	Reschedule      bool `json:"reschedule"`
	Cancel          bool `json:"cancel"`
	PayBalance      bool `json:"pay_balance"`
	DownloadReceipt bool `json:"download_receipt"`
	ViewAgreement   bool `json:"view_agreement"`
	GetDirections   bool `json:"get_directions"`
	Rebook          bool `json:"rebook"`
	Review          bool `json:"review"`
	Message         bool `json:"message"`
	CheckIn         bool `json:"check_in"`
}

type MarketplaceBookingChangeQuote struct {
	QuoteToken          string      `json:"quote_token"`
	BookingID           string      `json:"booking_id"`
	Kind                string      `json:"kind"`
	CurrentStartsAt     time.Time   `json:"current_starts_at"`
	CurrentEndsAt       time.Time   `json:"current_ends_at"`
	ProposedStartsAt    *time.Time  `json:"proposed_starts_at,omitempty"`
	ProposedEndsAt      *time.Time  `json:"proposed_ends_at,omitempty"`
	RefundAmountMinor   money.Minor `json:"refund_amount_minor"`
	RetainedAmountMinor money.Minor `json:"retained_amount_minor"`
	FeeAmountMinor      money.Minor `json:"fee_amount_minor"`
	CurrencyCode        string      `json:"currency_code"`
	PolicyMessage       string      `json:"policy_message"`
	ExpiresAt           time.Time   `json:"expires_at"`
}

type BookingCommandResponse struct {
	BookingID         string      `json:"booking_id"`
	Status            string      `json:"status"`
	StartsAt          time.Time   `json:"starts_at"`
	EndsAt            time.Time   `json:"ends_at"`
	RefundStatus      string      `json:"refund_status,omitempty"`
	RefundAmountMinor money.Minor `json:"refund_amount_minor"`
	CurrencyCode      string      `json:"currency_code"`
}

type MarketplaceBookingDetail struct {
	MarketplaceBookingListItem
	BookingToken         string                           `json:"booking_token"`
	Source               string                           `json:"source"`
	CustomerName         string                           `json:"customer_name"`
	CustomerEmail        string                           `json:"customer_email"`
	CustomerPhone        string                           `json:"customer_phone"`
	Notes                string                           `json:"notes"`
	Preparation          string                           `json:"preparation,omitempty"`
	CancellationPolicy   string                           `json:"cancellation_policy,omitempty"`
	LatenessPolicy       string                           `json:"lateness_policy,omitempty"`
	ProviderLatitude     *float64                         `json:"provider_latitude,omitempty"`
	ProviderLongitude    *float64                         `json:"provider_longitude,omitempty"`
	CustomerLatitude     *float64                         `json:"customer_latitude,omitempty"`
	CustomerLongitude    *float64                         `json:"customer_longitude,omitempty"`
	VirtualDeliveryLabel string                           `json:"virtual_delivery_label,omitempty"`
	VirtualJoinURL       string                           `json:"virtual_join_url,omitempty"`
	VirtualInstructions  string                           `json:"virtual_instructions,omitempty"`
	Payment              MarketplaceBookingPayment        `json:"payment"`
	Agreement            MarketplaceBookingAgreement      `json:"agreement"`
	PaymentHistory       []BookingDetailPaymentItem       `json:"payment_history"`
	RefundHistory        []BookingDetailRefundItem        `json:"refund_history"`
	ChangeHistory        []BookingDetailEvent             `json:"change_history"`
	ConversationID       string                           `json:"conversation_id,omitempty"`
	ReviewID             string                           `json:"review_id,omitempty"`
	AllowedActions       MarketplaceBookingAllowedActions `json:"allowed_actions"`
}

type MarketplaceBookingReceiptPayment struct {
	Reference   string      `json:"reference"`
	Purpose     string      `json:"purpose"`
	Method      string      `json:"method"`
	AmountMinor money.Minor `json:"amount_minor"`
	PaidAt      time.Time   `json:"paid_at"`
}

type MarketplaceBookingReceipt struct {
	ReceiptNumber       string                             `json:"receipt_number"`
	BookingID           string                             `json:"booking_id"`
	ProviderName        string                             `json:"provider_name"`
	CustomerName        string                             `json:"customer_name"`
	CustomerEmail       string                             `json:"customer_email"`
	ServiceTitle        string                             `json:"service_title"`
	StartsAt            time.Time                          `json:"starts_at"`
	Timezone            string                             `json:"timezone"`
	CurrencyCode        string                             `json:"currency_code"`
	TotalAmountMinor    money.Minor                        `json:"total_amount_minor"`
	NetPaidAmountMinor  money.Minor                        `json:"net_paid_amount_minor"`
	RefundedAmountMinor money.Minor                        `json:"refunded_amount_minor"`
	Payments            []MarketplaceBookingReceiptPayment `json:"payments"`
	IssuedAt            time.Time                          `json:"issued_at"`
}
