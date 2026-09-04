package appdata

import "time"

type TessaPreferences struct {
	IntroductionCompleted      bool       `json:"introduction_completed"`
	IntroductionCompletedAt    *time.Time `json:"introduction_completed_at,omitempty"`
	AcknowledgedNoticeRevision string     `json:"acknowledged_notice_revision"`
	NoticeAcknowledgedAt       *time.Time `json:"notice_acknowledged_at,omitempty"`
	CurrentNoticeRevision      string     `json:"current_notice_revision"`
	NoticeRequired             bool       `json:"notice_required"`
}

type TessaThread struct {
	ID             string     `json:"id"`
	Status         string     `json:"status"`
	LastActivityAt time.Time  `json:"last_activity_at"`
	CreatedAt      time.Time  `json:"created_at"`
	ArchivedAt     *time.Time `json:"archived_at,omitempty"`
}

type TessaMessage struct {
	ID               string                   `json:"id"`
	ThreadID         string                   `json:"thread_id"`
	Sequence         int64                    `json:"sequence"`
	SenderType       string                   `json:"sender_type"`
	SourceChannel    string                   `json:"source_channel"`
	ClientMessageID  string                   `json:"client_message_id,omitempty"`
	Content          string                   `json:"content"`
	Presentation     TessaMessagePresentation `json:"presentation"`
	EntityReferences []TessaEntityReference   `json:"entity_references"`
	RunID            string                   `json:"run_id,omitempty"`
	CreatedAt        time.Time                `json:"created_at"`
}

type TessaNavigationAction struct {
	RouteID  string `json:"route_id"`
	EntityID string `json:"entity_id,omitempty"`
	Label    string `json:"label"`
}

type TessaMessagePresentation struct {
	Kind    string                  `json:"kind,omitempty"`
	Version int                     `json:"version,omitempty"`
	Actions []TessaNavigationAction `json:"actions,omitempty"`
}

type TessaEntityReference struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Label   string `json:"label"`
	RouteID string `json:"route_id"`
}

type TessaRun struct {
	ID               string     `json:"id"`
	ThreadID         string     `json:"thread_id"`
	TriggerMessageID string     `json:"trigger_message_id"`
	Status           string     `json:"status"`
	Stage            string     `json:"stage"`
	ErrorCode        string     `json:"error_code,omitempty"`
	FallbackUsed     bool       `json:"fallback_used"`
	CreatedAt        time.Time  `json:"created_at"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	CancelledAt      *time.Time `json:"cancelled_at,omitempty"`
}

type TessaBootstrapResponse struct {
	Preferences    TessaPreferences `json:"preferences"`
	Thread         *TessaThread     `json:"thread,omitempty"`
	Messages       []TessaMessage   `json:"messages"`
	CurrentRun     *TessaRun        `json:"current_run,omitempty"`
	BeforeSequence string           `json:"before_sequence,omitempty"`
	HasMore        bool             `json:"has_more"`
	RealtimeCursor string           `json:"realtime_cursor"`
}

type TessaMessagePage struct {
	Items          []TessaMessage `json:"items"`
	BeforeSequence string         `json:"before_sequence,omitempty"`
	HasMore        bool           `json:"has_more"`
}

type TessaSendMessageInput struct {
	ClientMessageID string `json:"client_message_id"`
	Content         string `json:"content"`
}

type TessaSendMessageResponse struct {
	Message TessaMessage `json:"message"`
	Run     TessaRun     `json:"run"`
}

type TessaNewThreadInput struct {
	ClientRequestID string `json:"client_request_id"`
}

type TessaIntroductionInput struct {
	NoticeRevision string `json:"notice_revision"`
}

type TessaRealtimeEvent struct {
	Cursor    string        `json:"cursor"`
	Type      string        `json:"type"`
	ThreadID  string        `json:"thread_id"`
	Message   *TessaMessage `json:"message,omitempty"`
	Run       *TessaRun     `json:"run,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
}

type TessaRealtimeReset struct {
	Cursor string `json:"cursor"`
	Reason string `json:"reason"`
}

type TessaEventDrain struct {
	Events       []TessaRealtimeEvent
	Cursor       string
	LatestCursor string
	HasMore      bool
	Reset        bool
}

type TessaBusinessSnapshot struct {
	BusinessName              string `json:"business_name"`
	Category                  string `json:"category,omitempty"`
	CountryCode               string `json:"country_code,omitempty"`
	CurrencyCode              string `json:"currency_code,omitempty"`
	Timezone                  string `json:"timezone,omitempty"`
	MarketConfigured          bool   `json:"market_configured"`
	MarketplaceEnabled        bool   `json:"marketplace_enabled"`
	ConcurrentBookingCapacity int    `json:"concurrent_booking_capacity"`
}

type TessaBookingSummary struct {
	BookingID      string `json:"booking_id"`
	ServiceID      string `json:"service_id,omitempty"`
	ServiceTitle   string `json:"service_title"`
	CustomerName   string `json:"customer_name,omitempty"`
	StartsAt       string `json:"starts_at"`
	EndsAt         string `json:"ends_at"`
	Timezone       string `json:"timezone"`
	Status         string `json:"booking_status"`
	PaymentStatus  string `json:"payment_status"`
	AgreementState string `json:"agreement_status"`
	LocationLabel  string `json:"location_label,omitempty"`
}

type TessaBookingSearchResult struct {
	From     string                `json:"from"`
	To       string                `json:"to"`
	Timezone string                `json:"timezone"`
	Items    []TessaBookingSummary `json:"items"`
	HasMore  bool                  `json:"has_more"`
}

type TessaBookingDetail struct {
	TessaBookingSummary
	DurationMinutes  int              `json:"duration_minutes"`
	TotalAmountMinor int64            `json:"total_amount_minor"`
	CurrencyCode     string           `json:"currency_code"`
	Lifecycle        BookingLifecycle `json:"lifecycle"`
}

type TessaBookingAttentionSummary struct {
	From                         string                `json:"from"`
	To                           string                `json:"to"`
	Timezone                     string                `json:"timezone"`
	Total                        int                   `json:"total"`
	AwaitingPayment              int                   `json:"awaiting_payment"`
	AwaitingAgreement            int                   `json:"awaiting_agreement"`
	AwaitingProviderConfirmation int                   `json:"awaiting_provider_confirmation"`
	Examples                     []TessaBookingSummary `json:"examples"`
}

type TessaAvailabilitySlot struct {
	StartsAt string `json:"starts_at"`
	Label    string `json:"label"`
}

type TessaAvailabilityDay struct {
	Date  string                  `json:"date"`
	Slots []TessaAvailabilitySlot `json:"slots"`
}

type TessaServiceAvailability struct {
	ServiceID       string                 `json:"service_id"`
	ServiceTitle    string                 `json:"service_title"`
	DurationMinutes int                    `json:"duration_minutes"`
	Dates           []TessaAvailabilityDay `json:"dates"`
}

type TessaAvailabilityResult struct {
	From     string                     `json:"from"`
	To       string                     `json:"to"`
	Timezone string                     `json:"timezone"`
	Services []TessaServiceAvailability `json:"services"`
	HasMore  bool                       `json:"has_more"`
}

type TessaServiceSummary struct {
	ServiceID        string `json:"service_id"`
	Name             string `json:"name"`
	Status           string `json:"status"`
	Hidden           bool   `json:"hidden"`
	DurationMinutes  int    `json:"duration_minutes"`
	PriceAmountMinor int64  `json:"price_amount_minor"`
	CurrencyCode     string `json:"currency_code"`
	FulfillmentMode  string `json:"fulfillment_mode"`
	SectionName      string `json:"section_name,omitempty"`
}

type TessaServiceSearchResult struct {
	Items   []TessaServiceSummary `json:"items"`
	HasMore bool                  `json:"has_more"`
}

type TessaServiceDetail struct {
	TessaServiceSummary
	Description          string `json:"description,omitempty"`
	DepositRequired      bool   `json:"deposit_required"`
	DepositType          string `json:"deposit_type,omitempty"`
	DepositAmountMinor   int64  `json:"deposit_amount_minor"`
	DepositPercentageBPS int    `json:"deposit_percentage_bps"`
	AvailabilityMode     string `json:"availability_mode"`
	MinimumNoticeMinutes int    `json:"minimum_notice_minutes"`
	MaxBookingsPerDay    int    `json:"max_bookings_per_day"`
	LocationLabel        string `json:"location_label,omitempty"`
}

type TessaCustomerSummary struct {
	CustomerID             string `json:"customer_id"`
	Name                   string `json:"name"`
	Tier                   string `json:"tier,omitempty"`
	Status                 string `json:"status,omitempty"`
	HasUpcomingBooking     bool   `json:"has_upcoming_booking"`
	HasCompletedBooking    bool   `json:"has_completed_booking"`
	NextBookingAt          string `json:"next_booking_at,omitempty"`
	LastCompletedBookingAt string `json:"last_completed_booking_at,omitempty"`
}

type TessaCustomerSearchResult struct {
	Items   []TessaCustomerSummary `json:"items"`
	HasMore bool                   `json:"has_more"`
}

type TessaCustomerBookingSummary struct {
	Customer          TessaCustomerSummary  `json:"customer"`
	TotalBookings     int                   `json:"total_bookings"`
	UpcomingBookings  int                   `json:"upcoming_bookings"`
	CompletedBookings int                   `json:"completed_bookings"`
	CancelledBookings int                   `json:"cancelled_bookings"`
	RecentBookings    []TessaBookingSummary `json:"recent_bookings"`
}

type TessaPaymentSummary struct {
	Configured          bool   `json:"configured"`
	From                string `json:"from"`
	To                  string `json:"to"`
	CurrencyCode        string `json:"currency_code"`
	WalletBalanceMinor  int64  `json:"wallet_balance_minor"`
	GrossRevenueMinor   int64  `json:"gross_revenue_minor"`
	NetRevenueMinor     int64  `json:"net_revenue_minor"`
	AveragePaymentMinor int64  `json:"average_payment_minor"`
	PaymentCount        int    `json:"payment_count"`
}

type TessaBookingPaymentStatus struct {
	BookingID        string `json:"booking_id"`
	ServiceTitle     string `json:"service_title"`
	CustomerName     string `json:"customer_name,omitempty"`
	PaymentStatus    string `json:"payment_status"`
	TotalAmountMinor int64  `json:"total_amount_minor"`
	CurrencyCode     string `json:"currency_code"`
}

type TessaPayoutSummary struct {
	MarketConfigured             bool   `json:"market_configured"`
	DestinationConfigured        bool   `json:"destination_configured"`
	ActiveDestinationCount       int    `json:"active_destination_count"`
	CurrencyCode                 string `json:"currency_code"`
	From                         string `json:"from"`
	To                           string `json:"to"`
	BalanceAsOf                  string `json:"balance_as_of"`
	AvailableAmountMinor         int64  `json:"available_amount_minor"`
	PendingSettlementAmountMinor int64  `json:"pending_settlement_amount_minor"`
	PayoutInProgressAmountMinor  int64  `json:"payout_in_progress_amount_minor"`
	PeriodPaidOutAmountMinor     int64  `json:"period_paid_out_amount_minor"`
	EligibleAllocationCount      int    `json:"eligible_allocation_count"`
	PeriodPayoutCount            int    `json:"period_payout_count"`
}

type TessaBookingMetricSet struct {
	TotalBookings     int   `json:"total_bookings"`
	CompletedBookings int   `json:"completed_bookings"`
	ScheduledBookings int   `json:"scheduled_bookings"`
	CancelledBookings int   `json:"cancelled_bookings"`
	SecuredBookings   int   `json:"secured_bookings"`
	UniqueCustomers   int   `json:"unique_customers"`
	BookedValueMinor  int64 `json:"booked_value_minor"`
}

type TessaBookingMetricPeriod struct {
	From    string                `json:"from"`
	To      string                `json:"to"`
	Metrics TessaBookingMetricSet `json:"metrics"`
}

type TessaBookingMetricDelta struct {
	TotalBookings     int   `json:"total_bookings"`
	CompletedBookings int   `json:"completed_bookings"`
	CancelledBookings int   `json:"cancelled_bookings"`
	UniqueCustomers   int   `json:"unique_customers"`
	BookedValueMinor  int64 `json:"booked_value_minor"`
}

type TessaBookingMetrics struct {
	Configured   bool                      `json:"configured"`
	CurrencyCode string                    `json:"currency_code"`
	Current      TessaBookingMetricPeriod  `json:"current"`
	Previous     *TessaBookingMetricPeriod `json:"previous,omitempty"`
	Delta        *TessaBookingMetricDelta  `json:"delta,omitempty"`
}

type TessaInboxConversationSummary struct {
	ConversationID string `json:"conversation_id"`
	CustomerName   string `json:"customer_name"`
	UnreadCount    int    `json:"unread_count"`
	HandoffNeeded  bool   `json:"handoff_needed"`
	LastMessageAt  string `json:"last_message_at,omitempty"`
}

type TessaInboxSummary struct {
	OpenConversations   int                             `json:"open_conversations"`
	UnreadConversations int                             `json:"unread_conversations"`
	UnreadMessages      int                             `json:"unread_messages"`
	HandoffRequests     int                             `json:"handoff_requests"`
	Recent              []TessaInboxConversationSummary `json:"recent"`
}

type TessaReviewItem struct {
	ReviewID      string `json:"review_id"`
	AuthorName    string `json:"author_name"`
	ServiceTitle  string `json:"service_title,omitempty"`
	Rating        int    `json:"rating"`
	ReviewExcerpt string `json:"review_excerpt,omitempty"`
	CreatedAt     string `json:"created_at"`
}

type TessaReviewSummary struct {
	From      string            `json:"from"`
	To        string            `json:"to"`
	Rating    float64           `json:"rating"`
	Count     int               `json:"count"`
	Breakdown map[int]int       `json:"breakdown"`
	Recent    []TessaReviewItem `json:"recent"`
}

type TessaPublicProfileStatus struct {
	BusinessName       string   `json:"business_name"`
	HandleSlug         string   `json:"handle_slug,omitempty"`
	PublicPath         string   `json:"public_path,omitempty"`
	MarketplaceEnabled bool     `json:"marketplace_enabled"`
	MarketConfigured   bool     `json:"market_configured"`
	PublishedServices  int      `json:"published_services"`
	BusinessLocations  int      `json:"business_locations"`
	Verified           bool     `json:"verified"`
	MissingFields      []string `json:"missing_fields"`
}
