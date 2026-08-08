package appdata

import "booking/go-server/internal/money"

type ServiceSectionItem struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	CoverImageURL string `json:"cover_image_url,omitempty"`
	ServiceCount  int    `json:"service_count"`
	UpdatedLabel  string `json:"updated_label"`
}

type ManagedServiceItem struct {
	ID                          string                    `json:"id"`
	Name                        string                    `json:"name"`
	Description                 string                    `json:"description"`
	CurrencyCode                string                    `json:"currency_code"`
	DurationMinutes             int                       `json:"duration_minutes"`
	DurationLabel               string                    `json:"duration_label"`
	Status                      string                    `json:"status"`
	IsHidden                    bool                      `json:"is_hidden"`
	ImageURL                    string                    `json:"image_url,omitempty"`
	SectionID                   string                    `json:"section_id,omitempty"`
	SectionName                 string                    `json:"section_name,omitempty"`
	Badge                       string                    `json:"badge,omitempty"`
	Pricing                     ServicePricingConfig      `json:"pricing"`
	Fulfillment                 ServiceFulfillmentConfig  `json:"fulfillment"`
	Availability                ServiceAvailabilityConfig `json:"availability"`
	ShortNoticeRules            []ServiceShortNoticeRule  `json:"short_notice_rules"`
	VirtualDelivery             ServiceVirtualDelivery    `json:"virtual_delivery"`
	CancellationPolicy          string                    `json:"cancellation_policy,omitempty"`
	LatenessPolicy              string                    `json:"lateness_policy,omitempty"`
	AgreementTemplateFamilyID   string                    `json:"agreement_template_family_id,omitempty"`
	AgreementTemplateTitle      string                    `json:"agreement_template_title,omitempty"`
	AgreementConfirmationMethod string                    `json:"agreement_confirmation_method,omitempty"`
	AgreementTiming             string                    `json:"agreement_timing,omitempty"`
	StandaloneSignatureRequired bool                      `json:"standalone_signature_required"`
	Instructions                string                    `json:"instructions,omitempty"`
}

type ServicePricingConfig struct {
	PriceAmountMinor        money.Minor `json:"price_amount_minor"`
	ComparePriceAmountMinor money.Minor `json:"compare_price_amount_minor"`
	DepositRequired         bool        `json:"deposit_required"`
	DepositType             string      `json:"deposit_type"`
	DepositAmountMinor      money.Minor `json:"deposit_amount_minor"`
	DepositPercentageBPS    int         `json:"deposit_percentage_bps"`
}

type ServiceFulfillmentConfig struct {
	Mode                    string      `json:"mode"`
	ProviderLocationID      string      `json:"provider_location_id,omitempty"`
	ProviderLocationLabel   string      `json:"provider_location_label,omitempty"`
	TravelFeeMinor          money.Minor `json:"travel_fee_minor"`
	MaxTravelDistanceMeters *int        `json:"max_travel_distance_meters,omitempty"`
}

type ServiceAvailabilityWindow struct {
	ID                  string `json:"id,omitempty"`
	DayOfWeek           int    `json:"day_of_week"`
	StartTime           string `json:"start_time"`
	EndTime             string `json:"end_time"`
	SlotIntervalMinutes int    `json:"slot_interval_minutes"`
}

type ServiceAvailabilityConfig struct {
	Mode                 string                      `json:"mode"`
	MinimumNoticeMinutes int                         `json:"minimum_notice_minutes"`
	MaxBookingsPerDay    int                         `json:"max_bookings_per_day"`
	PrepTimeMinutes      int                         `json:"prep_time_minutes"`
	BufferTimeMinutes    int                         `json:"buffer_time_minutes"`
	CustomWindows        []ServiceAvailabilityWindow `json:"custom_windows"`
}

type ServiceShortNoticeRule struct {
	ID                     string      `json:"id,omitempty"`
	ThresholdMinutes       int         `json:"threshold_minutes"`
	SurchargeType          string      `json:"surcharge_type"`
	SurchargeAmountMinor   money.Minor `json:"surcharge_amount_minor"`
	SurchargePercentageBPS int         `json:"surcharge_percentage_bps"`
}

type ServiceVirtualDelivery struct {
	Label        string `json:"label"`
	JoinURL      string `json:"join_url"`
	Instructions string `json:"instructions"`
}

type CreateServiceSectionInput struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	CoverImageURL string `json:"cover_image_url"`
}

type DeleteServiceSectionInput struct {
	Mode            string `json:"mode"`
	TargetSectionID string `json:"target_section_id"`
}

type UpdateServiceSectionInput struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	CoverImageURL string `json:"cover_image_url"`
}

type ServiceSectionDetailsResponse struct {
	Section  ServiceSectionItem   `json:"section"`
	Services []ManagedServiceItem `json:"services"`
}

type ReorderItemsInput struct {
	OrderedIDs []string `json:"ordered_ids"`
}

type CreateManagedServiceInput struct {
	SectionID                   string                    `json:"section_id"`
	ServiceName                 string                    `json:"service_name"`
	Description                 string                    `json:"description"`
	Badge                       string                    `json:"badge"`
	DurationMinutes             int                       `json:"duration_minutes"`
	Pricing                     ServicePricingConfig      `json:"pricing"`
	Fulfillment                 ServiceFulfillmentConfig  `json:"fulfillment"`
	Availability                ServiceAvailabilityConfig `json:"availability"`
	ShortNoticeRules            []ServiceShortNoticeRule  `json:"short_notice_rules"`
	VirtualDelivery             ServiceVirtualDelivery    `json:"virtual_delivery"`
	CancellationPolicy          string                    `json:"cancellation_policy"`
	LatenessPolicy              string                    `json:"lateness_policy"`
	AgreementTemplateFamilyID   string                    `json:"agreement_template_family_id"`
	AgreementTiming             string                    `json:"agreement_timing"`
	StandaloneSignatureRequired bool                      `json:"standalone_signature_required"`
	Instructions                string                    `json:"instructions"`
	PublishStatus               string                    `json:"publish_status"`
	ImageURL                    string                    `json:"image_url"`
	WizardDraftID               string                    `json:"wizard_draft_id"`
}

type UpdateManagedServiceVisibilityInput struct {
	IsHidden bool `json:"is_hidden"`
}
