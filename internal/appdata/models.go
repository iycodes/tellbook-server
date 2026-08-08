package appdata

import (
	"time"

	"booking/go-server/internal/money"
	"booking/go-server/internal/payments"
)

type DashboardResponse struct {
	Profile       DashboardProfile       `json:"profile"`
	Stats         DashboardStats         `json:"stats"`
	AttentionItem *NotificationItem      `json:"attention_item,omitempty"`
	TodayBookings []DashboardBookingItem `json:"today_bookings"`
}

type DashboardProfile struct {
	ClientID      string  `json:"client_id"`
	FullName      string  `json:"full_name"`
	BusinessName  string  `json:"business_name"`
	AvatarURL     string  `json:"avatar_url,omitempty"`
	Category      string  `json:"category"`
	Headline      string  `json:"headline"`
	LocationLabel string  `json:"location_label"`
	ReviewRating  float64 `json:"review_rating"`
	ReviewCount   int     `json:"review_count"`
	Verified      bool    `json:"verified"`
}

type DashboardStats struct {
	TodayBookingsCount int         `json:"today_bookings_count"`
	ProjectedRevenue   money.Minor `json:"projected_revenue_minor"`
	CurrencyCode       string      `json:"currency_code"`
}

type DashboardBookingItem struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	StartAt       time.Time `json:"start_at"`
	CustomerName  string    `json:"customer_name"`
	Status        string    `json:"status"`
	PaymentStatus string    `json:"payment_status"`
	IconName      string    `json:"icon_name"`
}

type StatsOverviewResponse struct {
	Range               string      `json:"range"`
	PeriodStart         time.Time   `json:"period_start"`
	PeriodEnd           time.Time   `json:"period_end"`
	CurrencyCode        string      `json:"currency_code"`
	BookedValueMinor    money.Minor `json:"booked_value_minor"`
	TotalBookings       int         `json:"total_bookings"`
	CompletedBookings   int         `json:"completed_bookings"`
	ScheduledBookings   int         `json:"scheduled_bookings"`
	CancelledBookings   int         `json:"cancelled_bookings"`
	SecuredBookings     int         `json:"secured_bookings"`
	UniqueCustomerCount int         `json:"unique_customer_count"`
}

type RevenueOverviewResponse struct {
	Range                  string               `json:"range"`
	PeriodStart            time.Time            `json:"period_start"`
	PeriodEnd              time.Time            `json:"period_end"`
	CurrencyCode           string               `json:"currency_code"`
	WalletBalanceMinor     money.Minor          `json:"wallet_balance_minor"`
	GrossRevenueMinor      money.Minor          `json:"gross_revenue_minor"`
	NetRevenueMinor        money.Minor          `json:"net_revenue_minor"`
	AveragePaymentMinor    money.Minor          `json:"average_payment_minor"`
	PaymentCount           int                  `json:"payment_count"`
	RecentCustomerPayments []RevenuePaymentItem `json:"recent_payments"`
}

type RevenuePaymentItem struct {
	ID           string      `json:"id"`
	CustomerName string      `json:"customer_name"`
	ServiceName  string      `json:"service_name"`
	GrossMinor   money.Minor `json:"gross_amount_minor"`
	NetMinor     money.Minor `json:"net_amount_minor"`
	Method       string      `json:"method"`
	Provider     string      `json:"provider"`
	PaidAt       time.Time   `json:"paid_at"`
}

type BookingItem struct {
	ID              string    `json:"id"`
	CustomerID      string    `json:"customer_id"`
	Title           string    `json:"title"`
	StartAt         time.Time `json:"start_at"`
	EndAt           time.Time `json:"end_at"`
	CustomerName    string    `json:"customer_name"`
	Status          string    `json:"status"`
	PaymentStatus   string    `json:"payment_status"`
	AgreementStatus string    `json:"agreement_status"`
	IconName        string    `json:"icon_name"`
	LocationLabel   string    `json:"location_label"`
	Notes           string    `json:"notes"`
}

type BookingOptimizationItem struct {
	Kind        string `json:"kind"`
	Severity    string `json:"severity"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	ActionLabel string `json:"action_label,omitempty"`
	DayLabel    string `json:"day_label,omitempty"`
	StartLabel  string `json:"start_label,omitempty"`
	EndLabel    string `json:"end_label,omitempty"`
	ServiceID   string `json:"service_id,omitempty"`
	ServiceName string `json:"service_name,omitempty"`
}

type BookingOptimizationInsight struct {
	OptimizationsAvailable bool                      `json:"optimizations_available"`
	Summary                string                    `json:"summary,omitempty"`
	Items                  []BookingOptimizationItem `json:"items"`
}

type BookingDetailsResponse struct {
	ID                     string      `json:"id"`
	Status                 string      `json:"status"`
	Title                  string      `json:"title"`
	Stylist                string      `json:"stylist"`
	DateLabel              string      `json:"date_label"`
	TimeLabel              string      `json:"time_label"`
	BaseServiceAmountMinor money.Minor `json:"base_service_amount_minor"`
	DurationLabel          string      `json:"duration_label"`
	TotalAmountMinor       money.Minor `json:"total_amount_minor"`
	CurrencyCode           string      `json:"currency_code"`
	PaymentStatus          string      `json:"payment_status"`
	AgreementStatus        string      `json:"agreement_status"`
	Notes                  string      `json:"notes"`
	Location               string      `json:"location"`
	ImageURL               string      `json:"image_url,omitempty"`
}

type CustomerItem struct {
	ID                     string     `json:"id"`
	FullName               string     `json:"full_name"`
	Email                  string     `json:"email"`
	Phone                  string     `json:"phone"`
	AvatarURL              string     `json:"avatar_url,omitempty"`
	TierLabel              string     `json:"tier_label"`
	StatusLabel            string     `json:"status_label"`
	BadgeLabel             string     `json:"badge_label"`
	BadgeTone              string     `json:"badge_tone"`
	Tags                   []string   `json:"tags"`
	PrivateNotes           string     `json:"private_notes"`
	LastSeenAt             time.Time  `json:"last_seen_at"`
	HasUpcomingBooking     bool       `json:"has_upcoming_booking"`
	HasCompletedBooking    bool       `json:"has_completed_booking"`
	NextBookingAt          *time.Time `json:"next_booking_at,omitempty"`
	LastCompletedBookingAt *time.Time `json:"last_completed_booking_at,omitempty"`
}

type CustomerBookingHistoryItem struct {
	ID           string      `json:"id"`
	Service      string      `json:"service"`
	Date         string      `json:"date"`
	AmountMinor  money.Minor `json:"amount_minor"`
	CurrencyCode string      `json:"currency_code"`
	Icon         string      `json:"icon"`
}

type CustomerUpcomingBooking struct {
	DateLabel string `json:"date_label"`
	Title     string `json:"title"`
	Duration  string `json:"duration"`
}

type CustomerDetailsResponse struct {
	ID              string                       `json:"id"`
	Name            string                       `json:"name"`
	Tier            string                       `json:"tier"`
	PaymentStatus   string                       `json:"payment_status"`
	AgreementStatus string                       `json:"agreement_status"`
	NextBooking     *CustomerUpcomingBooking     `json:"next_booking,omitempty"`
	History         []CustomerBookingHistoryItem `json:"history"`
	Notes           string                       `json:"notes"`
	ImageURL        string                       `json:"image_url,omitempty"`
	Email           string                       `json:"email"`
	Phone           string                       `json:"phone"`
}

type NotificationItem struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	Severity    string    `json:"severity"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	ActionLabel string    `json:"action_label"`
	ActionRoute string    `json:"action_route"`
	ImageURL    string    `json:"image_url,omitempty"`
	IconName    string    `json:"icon_name,omitempty"`
	IconTone    string    `json:"icon_tone,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type NotificationsResponse struct {
	ActionRequired []NotificationItem `json:"action_required"`
	Today          []NotificationItem `json:"today"`
}

type AutomationSettingItem struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ActionLabel string `json:"action_label"`
	Enabled     bool   `json:"enabled"`
}

type ClientProfileResponse struct {
	FullName           string `json:"full_name"`
	Email              string `json:"email"`
	Bio                string `json:"bio"`
	BusinessName       string `json:"business_name"`
	HandleSlug         string `json:"handle_slug"`
	Category           string `json:"category"`
	Headline           string `json:"headline"`
	ShortBio           string `json:"short_bio"`
	PublicProfileAbout string `json:"public_profile_about"`
	BookingPageIntro   string `json:"booking_page_intro"`
	Location           string `json:"location_label"`
	City               string `json:"city"`
	Region             string `json:"region"`
	Timezone           string `json:"timezone"`
	Locale             string `json:"locale"`
	CountryCode        string `json:"country_code"`
	AvatarURL          string `json:"avatar_url,omitempty"`
	HeroImageURL       string `json:"hero_image_url,omitempty"`
	Verified           bool   `json:"verified"`
	CurrencyCode       string `json:"currency_code"`
	MarketConfigured   bool   `json:"market_configured"`
}

type UpdateClientProfileInput struct {
	BusinessName       string  `json:"business_name"`
	HandleSlug         *string `json:"handle_slug,omitempty"`
	ShortBio           string  `json:"short_bio"`
	Headline           string  `json:"headline"`
	PublicProfileAbout string  `json:"public_profile_about"`
	BookingPageIntro   string  `json:"booking_page_intro"`
	Location           string  `json:"location_label"`
	City               string  `json:"city"`
	Region             string  `json:"region"`
	Category           string  `json:"category"`
	HeroImageURL       string  `json:"hero_image_url,omitempty"`
}

type UpdateClientMarketInput struct {
	CountryCode  string `json:"country_code"`
	CurrencyCode string `json:"currency_code"`
	Timezone     string `json:"timezone"`
	Locale       string `json:"locale"`
}

type UpdateAutomationSettingInput struct {
	Enabled bool `json:"enabled"`
}

type PayoutInputMetadata struct {
	Label             string `json:"label"`
	MinimumLength     int    `json:"minimum_length"`
	MaximumLength     int    `json:"maximum_length"`
	AllowedCharacters string `json:"allowed_characters"`
	ResolutionEnabled bool   `json:"resolution_enabled"`
}

type PayoutInstitutionItem struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type PayoutDestinationItem struct {
	ID                  string    `json:"id"`
	CountryCode         string    `json:"country_code"`
	CurrencyCode        string    `json:"currency_code"`
	Rail                string    `json:"rail"`
	InstitutionCode     string    `json:"institution_code"`
	InstitutionName     string    `json:"institution_name"`
	MaskedIdentifier    string    `json:"masked_identifier"`
	ResolvedAccountName string    `json:"account_name"`
	VerifiedAt          time.Time `json:"verified_at"`
	IsDefault           bool      `json:"is_default"`
	Status              string    `json:"status"`
}

type PayoutSetupResponse struct {
	Available    bool                    `json:"available"`
	CountryCode  string                  `json:"country_code"`
	CurrencyCode string                  `json:"currency_code"`
	Rails        []string                `json:"rails"`
	SelectedRail string                  `json:"selected_rail,omitempty"`
	Input        PayoutInputMetadata     `json:"input"`
	Institutions []PayoutInstitutionItem `json:"institutions"`
	Destinations []PayoutDestinationItem `json:"destinations"`
}

type ResolvePayoutDestinationInput struct {
	Rail            string `json:"rail"`
	InstitutionCode string `json:"institution_code"`
	Identifier      string `json:"identifier"`
}

type ResolvedPayoutDestinationResponse struct {
	CountryCode     string `json:"country_code"`
	CurrencyCode    string `json:"currency_code"`
	Rail            string `json:"rail"`
	InstitutionCode string `json:"institution_code"`
	InstitutionName string `json:"institution_name"`
	AccountName     string `json:"account_name"`
}

type SavePayoutDestinationInput struct {
	Rail                 string `json:"rail"`
	InstitutionCode      string `json:"institution_code"`
	Identifier           string `json:"identifier"`
	ConfirmedAccountName string `json:"confirmed_account_name"`
	MakeDefault          bool   `json:"make_default"`
}

type EligiblePayoutAllocationItem struct {
	ID          string      `json:"id"`
	AmountMinor money.Minor `json:"amount_minor"`
	AvailableAt time.Time   `json:"available_at"`
}

type PayoutItem struct {
	ID               string      `json:"id"`
	AmountMinor      money.Minor `json:"amount_minor"`
	FeeMinor         money.Minor `json:"fee_minor"`
	CurrencyCode     string      `json:"currency_code"`
	Status           string      `json:"status"`
	InstitutionName  string      `json:"institution_name"`
	MaskedIdentifier string      `json:"masked_identifier"`
	AccountName      string      `json:"account_name"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
}

type PayoutOverviewResponse struct {
	CurrencyCode                 string                         `json:"currency_code"`
	AvailableAmountMinor         money.Minor                    `json:"available_amount_minor"`
	PendingSettlementAmountMinor money.Minor                    `json:"pending_settlement_amount_minor"`
	PayoutInProgressAmountMinor  money.Minor                    `json:"payout_in_progress_amount_minor"`
	PaidOutAmountMinor           money.Minor                    `json:"paid_out_amount_minor"`
	EligibleAllocations          []EligiblePayoutAllocationItem `json:"eligible_allocations"`
	RecentPayouts                []PayoutItem                   `json:"recent_payouts"`
}

type CreatePayoutInput struct {
	PaymentAllocationID string `json:"payment_allocation_id"`
	PayoutDestinationID string `json:"payout_destination_id"`
	IdempotencyKey      string `json:"idempotency_key"`
}

type PublicProfileResponse struct {
	Profile          PublicProfile         `json:"profile"`
	FeaturedServices []PublicServiceItem   `json:"featured_services"`
	Portfolio        []PublicPortfolioItem `json:"portfolio"`
}

type PublicProfile struct {
	ClientID           string  `json:"client_id"`
	BusinessName       string  `json:"business_name"`
	HandleSlug         string  `json:"handle_slug"`
	Category           string  `json:"category"`
	Headline           string  `json:"headline"`
	ShortBio           string  `json:"short_bio"`
	PublicProfileAbout string  `json:"public_profile_about"`
	BookingPageIntro   string  `json:"booking_page_intro"`
	LocationLabel      string  `json:"location_label"`
	HeroImageURL       string  `json:"hero_image_url,omitempty"`
	AvatarURL          string  `json:"avatar_url,omitempty"`
	Verified           bool    `json:"verified"`
	YearsExperience    int     `json:"years_experience"`
	ReviewRating       float64 `json:"review_rating"`
	ReviewCount        int     `json:"review_count"`
	CountryCode        string  `json:"country_code"`
	CurrencyCode       string  `json:"currency_code"`
	Timezone           string  `json:"timezone"`
	Locale             string  `json:"locale"`
}

type PublicServiceItem struct {
	ID                          string      `json:"id"`
	Title                       string      `json:"title"`
	Slug                        string      `json:"slug"`
	Description                 string      `json:"description"`
	Category                    string      `json:"category"`
	IconName                    string      `json:"icon_name"`
	ImageURL                    string      `json:"image_url,omitempty"`
	DurationMinutes             int         `json:"duration_minutes"`
	StartingPriceAmountMinor    money.Minor `json:"starting_price_amount_minor"`
	CurrencyCode                string      `json:"currency_code"`
	Status                      string      `json:"status"`
	IsBookable                  bool        `json:"is_bookable"`
	CancellationPolicy          string      `json:"cancellation_policy,omitempty"`
	LatenessPolicy              string      `json:"lateness_policy,omitempty"`
	AgreementTiming             string      `json:"agreement_timing,omitempty"`
	AgreementConfirmationMethod string      `json:"agreement_confirmation_method,omitempty"`
	AgreementTemplateTitle      string      `json:"agreement_template_title,omitempty"`
	StandaloneSignatureRequired bool        `json:"standalone_signature_required"`
	FulfillmentMode             string      `json:"fulfillment_mode"`
	FulfillmentLabel            string      `json:"fulfillment_label"`
	ProviderLocationLabel       string      `json:"provider_location_label,omitempty"`
	VirtualDeliveryLabel        string      `json:"virtual_delivery_label,omitempty"`
	MinimumNoticeMinutes        int         `json:"minimum_notice_minutes"`
	HasShortNoticePricing       bool        `json:"has_short_notice_pricing"`
}

type PublicAvailabilitySlot struct {
	StartAt                    string      `json:"start_at"`
	Label                      string      `json:"label"`
	BasePriceAmountMinor       money.Minor `json:"base_price_amount_minor"`
	ShortNoticeFeeAmountMinor  money.Minor `json:"short_notice_fee_amount_minor"`
	EstimatedTotalBeforeTravel money.Minor `json:"estimated_total_before_travel_minor"`
	ShortNoticeApplies         bool        `json:"short_notice_applies"`
}

type PublicAvailabilityResponse struct {
	ServiceID       string                   `json:"service_id"`
	Date            string                   `json:"date"`
	Timezone        string                   `json:"timezone"`
	CurrencyCode    string                   `json:"currency_code"`
	DurationMinutes int                      `json:"duration_minutes"`
	LocationLabel   string                   `json:"location_label"`
	Slots           []PublicAvailabilitySlot `json:"slots"`
}

type ResolvePublicLocationInput struct {
	Source          string   `json:"source"`
	Address         string   `json:"address"`
	ProviderPlaceID string   `json:"provider_place_id"`
	Latitude        *float64 `json:"latitude"`
	Longitude       *float64 `json:"longitude"`
}

type ResolvedPublicLocationResponse struct {
	LocationToken    string `json:"location_token"`
	FormattedAddress string `json:"formatted_address"`
	ResolutionStatus string `json:"resolution_status"`
	ExpiresAt        string `json:"expires_at"`
}

type CreatePublicBookingQuoteInput struct {
	ServiceID             string `json:"service_id"`
	StartsAt              string `json:"starts_at"`
	CustomerName          string `json:"customer_name"`
	CustomerEmail         string `json:"customer_email"`
	CustomerPhone         string `json:"customer_phone"`
	BookingNotes          string `json:"booking_notes"`
	DiscountCode          string `json:"discount_code"`
	CustomerLocationToken string `json:"customer_location_token"`
}

type PublicBookingQuoteResponse struct {
	QuoteToken                   string                          `json:"quote_token"`
	ExpiresAt                    string                          `json:"expires_at"`
	ServiceID                    string                          `json:"service_id"`
	ServiceTitle                 string                          `json:"service_title"`
	StartsAt                     string                          `json:"starts_at"`
	EndsAt                       string                          `json:"ends_at"`
	LocationLabel                string                          `json:"location_label"`
	FulfillmentMode              string                          `json:"fulfillment_mode"`
	BaseServiceAmountMinor       money.Minor                     `json:"base_service_amount_minor"`
	DiscountAmountMinor          money.Minor                     `json:"discount_amount_minor"`
	DiscountName                 string                          `json:"discount_name,omitempty"`
	DiscountCode                 string                          `json:"discount_code,omitempty"`
	ShortNoticeFeeMinor          money.Minor                     `json:"short_notice_fee_minor"`
	ShortNoticeLabel             string                          `json:"short_notice_label,omitempty"`
	TravelFeeMinor               money.Minor                     `json:"travel_fee_minor"`
	TravelDistanceMeters         *int                            `json:"travel_distance_meters,omitempty"`
	DiscountedServiceAmountMinor money.Minor                     `json:"discounted_service_amount_minor"`
	TotalAmountMinor             money.Minor                     `json:"total_amount_minor"`
	DepositAmountMinor           money.Minor                     `json:"deposit_amount_minor"`
	RemainingAmountMinor         money.Minor                     `json:"remaining_amount_minor"`
	CountryCode                  string                          `json:"country_code"`
	CurrencyCode                 string                          `json:"currency_code"`
	Timezone                     string                          `json:"timezone"`
	Locale                       string                          `json:"locale"`
	Agreement                    *PublicBookingAgreementSnapshot `json:"agreement,omitempty"`
	StandaloneSignatureRequired  bool                            `json:"standalone_signature_required"`
}

type PublicBookingAgreementSnapshot struct {
	Title              string `json:"title"`
	RenderedHTML       string `json:"rendered_html"`
	ConfirmationMethod string `json:"confirmation_method"`
	Timing             string `json:"timing"`
	ResolvedTermsHash  string `json:"resolved_terms_hash"`
}

type CreatePublicBookingInput struct {
	QuoteToken                string `json:"quote_token"`
	FullName                  string `json:"full_name"`
	Email                     string `json:"email"`
	Phone                     string `json:"phone"`
	Notes                     string `json:"notes"`
	AgreementAccepted         bool   `json:"agreement_accepted"`
	AgreementFullName         string `json:"agreement_full_name"`
	AgreementSignatureDataURL string `json:"agreement_signature_data_url"`
}

type PublicBookingSummaryResponse struct {
	BookingID                    string      `json:"-"`
	BookingToken                 string      `json:"booking_token"`
	ServiceTitle                 string      `json:"service_title"`
	ServiceImageURL              string      `json:"service_image_url,omitempty"`
	DurationLabel                string      `json:"duration_label"`
	DateLabel                    string      `json:"date_label"`
	TimeLabel                    string      `json:"time_label"`
	StartsAt                     string      `json:"starts_at"`
	EndsAt                       string      `json:"ends_at"`
	LocationLabel                string      `json:"location_label"`
	FulfillmentMode              string      `json:"fulfillment_mode"`
	ProviderLocationLabel        string      `json:"provider_location_label,omitempty"`
	CustomerLocationLabel        string      `json:"customer_location_label,omitempty"`
	TravelDistanceMeters         *int        `json:"travel_distance_meters,omitempty"`
	VirtualDeliveryLabel         string      `json:"virtual_delivery_label,omitempty"`
	VirtualJoinURL               string      `json:"virtual_join_url,omitempty"`
	VirtualInstructions          string      `json:"virtual_instructions,omitempty"`
	CancellationPolicy           string      `json:"cancellation_policy,omitempty"`
	LatenessPolicy               string      `json:"lateness_policy,omitempty"`
	OriginalAmountMinor          money.Minor `json:"original_amount_minor"`
	DiscountApplied              bool        `json:"discount_applied"`
	DiscountName                 string      `json:"discount_name,omitempty"`
	DiscountSource               string      `json:"discount_source,omitempty"`
	DiscountCode                 string      `json:"discount_code,omitempty"`
	DiscountType                 string      `json:"discount_type,omitempty"`
	DiscountPercentageBPS        int64       `json:"discount_percentage_bps,omitempty"`
	DiscountValueMinor           money.Minor `json:"discount_value_minor,omitempty"`
	DiscountAmountMinor          money.Minor `json:"discount_amount_minor"`
	DiscountedServiceAmountMinor money.Minor `json:"discounted_service_amount_minor"`
	ShortNoticeFeeMinor          money.Minor `json:"short_notice_fee_minor"`
	TravelFeeMinor               money.Minor `json:"travel_fee_minor"`
	TotalAmountMinor             money.Minor `json:"total_amount_minor"`
	DepositAmountMinor           money.Minor `json:"deposit_amount_minor"`
	RemainingAmountMinor         money.Minor `json:"remaining_amount_minor"`
	CountryCode                  string      `json:"country_code"`
	CurrencyCode                 string      `json:"currency_code"`
	Timezone                     string      `json:"timezone"`
	Locale                       string      `json:"locale"`
	Status                       string      `json:"status"`
	PaymentStatus                string      `json:"payment_status"`
	AgreementStatus              string      `json:"agreement_status"`
	PaymentToken                 string      `json:"payment_token,omitempty"`
	PaymentProvider              string      `json:"payment_provider,omitempty"`
	PaymentReference             string      `json:"payment_reference,omitempty"`
	AgreementTiming              string      `json:"agreement_timing,omitempty"`
	AgreementConfirmationMethod  string      `json:"agreement_confirmation_method,omitempty"`
	AgreementTemplateTitle       string      `json:"agreement_template_title,omitempty"`
	StandaloneSignatureRequired  bool        `json:"standalone_signature_required"`
}

type CreatePublicBookingCheckoutInput struct {
	IdempotencyKey string `json:"idempotency_key"`
	Method         string `json:"method"`
}

type PublicBookingCheckoutStateResponse struct {
	BookingToken        string                       `json:"booking_token"`
	ObligationSatisfied bool                         `json:"obligation_satisfied"`
	AmountMinor         money.Minor                  `json:"amount_minor"`
	CurrencyCode        string                       `json:"currency_code"`
	AvailableMethods    []string                     `json:"available_methods"`
	ActivePayment       *PublicPaymentStatusResponse `json:"active_payment,omitempty"`
}

type PublicBankTransferInstructions struct {
	AccountName       string `json:"account_name"`
	AccountNumber     string `json:"account_number"`
	BankName          string `json:"bank_name"`
	TransferReference string `json:"transfer_reference,omitempty"`
}

type PublicBankTransferResponse struct {
	BookingToken string                         `json:"booking_token"`
	Payment      PublicPaymentStatusResponse    `json:"payment"`
	ExpiresAt    string                         `json:"expires_at,omitempty"`
	Instructions PublicBankTransferInstructions `json:"instructions"`
}

type PublicBookingCheckoutResponse struct {
	BookingToken     string                          `json:"booking_token"`
	Provider         string                          `json:"provider"`
	Method           string                          `json:"method"`
	PaymentReference string                          `json:"payment_reference"`
	PaymentToken     string                          `json:"payment_token"`
	Status           string                          `json:"status"`
	RecoveryAction   string                          `json:"recovery_action"`
	ProviderChannel  string                          `json:"provider_channel,omitempty"`
	AmountMinor      money.Minor                     `json:"amount_minor"`
	CurrencyCode     string                          `json:"currency_code"`
	RedirectURL      string                          `json:"redirect_url,omitempty"`
	ExpiresAt        string                          `json:"expires_at,omitempty"`
	Flow             payments.CheckoutFlow           `json:"flow,omitempty"`
	PublicKey        string                          `json:"public_key,omitempty"`
	Instructions     map[string]string               `json:"instructions,omitempty"`
	BankTransfer     *PublicBankTransferInstructions `json:"bank_transfer,omitempty"`
}

type PublicPaymentStatusResponse struct {
	PaymentToken     string                 `json:"payment_token"`
	PaymentReference string                 `json:"payment_reference"`
	BookingToken     string                 `json:"booking_token"`
	Status           payments.PaymentStatus `json:"status"`
	RecoveryAction   string                 `json:"recovery_action"`
	ProviderChannel  string                 `json:"provider_channel,omitempty"`
	AmountMinor      money.Minor            `json:"amount_minor"`
	CurrencyCode     string                 `json:"currency_code"`
	Method           string                 `json:"method"`
	FailureCode      string                 `json:"failure_code,omitempty"`
	FailureMessage   string                 `json:"failure_message,omitempty"`
	Version          int64                  `json:"version"`
	CheckoutReady    bool                   `json:"checkout_ready"`
	StatusUpdatedAt  string                 `json:"status_updated_at"`
}

type PublicPortfolioItem struct {
	ID       string `json:"id"`
	ImageURL string `json:"image_url"`
	Caption  string `json:"caption"`
}

type PromotionTargetRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type PromotionListItem struct {
	ID                        string               `json:"id"`
	Name                      string               `json:"name"`
	PromotionType             string               `json:"promotion_type"`
	Code                      string               `json:"code,omitempty"`
	DiscountType              string               `json:"discount_type"`
	DiscountPercentageBPS     int64                `json:"discount_percentage_bps"`
	DiscountValueMinor        money.Minor          `json:"discount_value_minor"`
	ScopeType                 string               `json:"scope_type"`
	IsActive                  bool                 `json:"is_active"`
	StartsAt                  time.Time            `json:"starts_at"`
	EndsAt                    *time.Time           `json:"ends_at,omitempty"`
	MaxRedemptions            int                  `json:"max_redemptions,omitempty"`
	MaxRedemptionsPerCustomer int                  `json:"max_redemptions_per_customer,omitempty"`
	MinimumSpendMinor         money.Minor          `json:"minimum_spend_minor,omitempty"`
	CurrencyCode              string               `json:"currency_code"`
	FirstTimeCustomersOnly    bool                 `json:"first_time_customers_only"`
	AppliesToDeposit          bool                 `json:"applies_to_deposit"`
	StackWithAutomatic        bool                 `json:"stack_with_automatic_discounts"`
	RedemptionCount           int                  `json:"redemption_count"`
	ServiceTargets            []PromotionTargetRef `json:"service_targets,omitempty"`
	SectionTargets            []PromotionTargetRef `json:"section_targets,omitempty"`
	CreatedAt                 time.Time            `json:"created_at"`
	UpdatedAt                 time.Time            `json:"updated_at"`
}

type CreatePromotionInput struct {
	Name                      string      `json:"name"`
	PromotionType             string      `json:"promotion_type"`
	Code                      string      `json:"code"`
	DiscountType              string      `json:"discount_type"`
	DiscountPercentageBPS     int64       `json:"discount_percentage_bps"`
	DiscountValueMinor        money.Minor `json:"discount_value_minor"`
	ScopeType                 string      `json:"scope_type"`
	StartsAt                  time.Time   `json:"starts_at"`
	EndsAt                    *time.Time  `json:"ends_at,omitempty"`
	IsActive                  bool        `json:"is_active"`
	MaxRedemptions            int         `json:"max_redemptions"`
	MaxRedemptionsPerCustomer int         `json:"max_redemptions_per_customer"`
	MinimumSpendMinor         money.Minor `json:"minimum_spend_minor"`
	FirstTimeCustomersOnly    bool        `json:"first_time_customers_only"`
	AppliesToDeposit          bool        `json:"applies_to_deposit"`
	StackWithAutomatic        bool        `json:"stack_with_automatic_discounts"`
	ServiceIDs                []string    `json:"service_ids"`
	SectionIDs                []string    `json:"section_ids"`
}

type UpdatePromotionStatusInput struct {
	IsActive bool `json:"is_active"`
}

type PromotionRedemptionItem struct {
	ID                  string      `json:"id"`
	BookingID           string      `json:"booking_id,omitempty"`
	CustomerID          string      `json:"customer_id,omitempty"`
	CustomerEmail       string      `json:"customer_email,omitempty"`
	CodeUsed            string      `json:"code_used,omitempty"`
	DiscountAmountMinor money.Minor `json:"discount_amount_minor"`
	CurrencyCode        string      `json:"currency_code"`
	CreatedAt           time.Time   `json:"created_at"`
}

type PublicAgreementResponse struct {
	ID                 string     `json:"id"`
	Title              string     `json:"title"`
	Status             string     `json:"status"`
	ConfirmationMethod string     `json:"confirmation_method"`
	RenderedHTML       string     `json:"rendered_html"`
	ResolvedTermsHash  string     `json:"resolved_terms_hash"`
	CustomerName       string     `json:"customer_name,omitempty"`
	BusinessName       string     `json:"business_name,omitempty"`
	SignerName         string     `json:"signer_name,omitempty"`
	SignaturePresent   bool       `json:"signature_present"`
	SignatureSHA256    string     `json:"signature_sha256,omitempty"`
	AcceptedAt         *time.Time `json:"accepted_at,omitempty"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
	PDFStatus          string     `json:"pdf_status"`
}

type PublicAgreementAcceptInput struct {
	Accepted         bool   `json:"accepted"`
	FullName         string `json:"full_name"`
	SignatureDataURL string `json:"signature_data_url"`
}

type PublicBookingAgreementResponse struct {
	AgreementID        string `json:"agreement_id"`
	PublicToken        string `json:"public_token"`
	Title              string `json:"title"`
	Status             string `json:"status"`
	ConfirmationMethod string `json:"confirmation_method"`
	SentToEmail        string `json:"sent_to_email,omitempty"`
}
