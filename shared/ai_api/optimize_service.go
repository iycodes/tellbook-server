package aiapi

type OptimizeServiceRequest struct {
	ServiceID               string       `json:"service_id,omitempty"`
	BusinessName            string       `json:"business_name,omitempty"`
	Category                string       `json:"category,omitempty"`
	Title                   string       `json:"title"`
	Description             string       `json:"description,omitempty"`
	DurationMinutes         int          `json:"duration_minutes,omitempty"`
	PriceAmountMinor        MinorAmount  `json:"price_amount_minor,omitempty"`
	ComparePriceAmountMinor MinorAmount  `json:"compare_price_amount_minor,omitempty"`
	CurrencyCode            string       `json:"currency_code,omitempty"`
	DepositRequired         bool         `json:"deposit_required"`
	DepositType             string       `json:"deposit_type,omitempty"`
	DepositValue            string       `json:"deposit_value,omitempty"`
	PrepTime                string       `json:"prep_time,omitempty"`
	BufferTime              string       `json:"buffer_time,omitempty"`
	LeadTime                string       `json:"lead_time,omitempty"`
	LastBooking             string       `json:"last_booking,omitempty"`
	MaxPerDay               string       `json:"max_per_day,omitempty"`
	LocationType            string       `json:"location_type,omitempty"`
	TravelFee               string       `json:"travel_fee,omitempty"`
	ServiceRadius           int          `json:"service_radius,omitempty"`
	CancellationPolicy      string       `json:"cancellation_policy,omitempty"`
	LatenessPolicy          string       `json:"lateness_policy,omitempty"`
	Instructions            string       `json:"instructions,omitempty"`
	AgreementRequired       bool         `json:"agreement_required"`
	AgreementTiming         string       `json:"agreement_timing,omitempty"`
	Context                 []NamedValue `json:"context,omitempty"`
}

type ServiceOptimizationSuggestion struct {
	Area           string  `json:"area"`
	Issue          string  `json:"issue"`
	Recommendation string  `json:"recommendation"`
	Reason         string  `json:"reason,omitempty"`
	Confidence     float64 `json:"confidence"`
}

type OptimizeServiceResponse struct {
	Score                       int                             `json:"score,omitempty"`
	Summary                     string                          `json:"summary,omitempty"`
	SuggestedTitle              string                          `json:"suggested_title,omitempty"`
	SuggestedDescription        string                          `json:"suggested_description,omitempty"`
	SuggestedInstructions       string                          `json:"suggested_instructions,omitempty"`
	SuggestedCancellationPolicy string                          `json:"suggested_cancellation_policy,omitempty"`
	SuggestedLatenessPolicy     string                          `json:"suggested_lateness_policy,omitempty"`
	Suggestions                 []ServiceOptimizationSuggestion `json:"suggestions,omitempty"`
	Warnings                    []Warning                       `json:"warnings,omitempty"`
}
