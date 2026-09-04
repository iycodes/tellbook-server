package aiapi

import "time"

const (
	InboxReplyDraftMaxCharacters = 4000
	InboxReplyDraftPromptVersion = 1
)

type InboxReplyDraftBooking struct {
	ServiceName        string    `json:"service_name"`
	Status             string    `json:"status"`
	PaymentStatus      string    `json:"payment_status,omitempty"`
	StartsAt           time.Time `json:"starts_at"`
	EndsAt             time.Time `json:"ends_at"`
	Timezone           string    `json:"timezone"`
	FulfillmentMode    string    `json:"fulfillment_mode,omitempty"`
	Location           string    `json:"location,omitempty"`
	CancellationPolicy string    `json:"cancellation_policy,omitempty"`
	LatenessPolicy     string    `json:"lateness_policy,omitempty"`
}

type InboxReplyDraftService struct {
	ServiceName        string `json:"service_name"`
	Category           string `json:"category,omitempty"`
	Description        string `json:"description,omitempty"`
	DurationMinutes    int    `json:"duration_minutes,omitempty"`
	DisplayPrice       string `json:"display_price,omitempty"`
	FulfillmentMode    string `json:"fulfillment_mode,omitempty"`
	CancellationPolicy string `json:"cancellation_policy,omitempty"`
	LatenessPolicy     string `json:"lateness_policy,omitempty"`
}

type InboxReplyDraftRequest struct {
	BusinessName     string                   `json:"business_name"`
	BusinessSummary  string                   `json:"business_summary,omitempty"`
	BusinessTimezone string                   `json:"business_timezone,omitempty"`
	CustomerName     string                   `json:"customer_name"`
	GeneratedAt      time.Time                `json:"generated_at"`
	Bookings         []InboxReplyDraftBooking `json:"bookings,omitempty"`
	Services         []InboxReplyDraftService `json:"published_services,omitempty"`
	Messages         []MessageTurn            `json:"messages,omitempty"`
}

type InboxReplyDraftResponse struct {
	Draft              string    `json:"draft"`
	NeedsProviderInput bool      `json:"needs_provider_input"`
	Warnings           []Warning `json:"warnings"`
}
