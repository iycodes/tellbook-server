package aiapi

type GenerateServiceDescriptionRequest struct {
	ServiceID        string                   `json:"service_id,omitempty"`
	BusinessName     string                   `json:"business_name,omitempty"`
	ServiceTitle     string                   `json:"service_title"`
	Category         string                   `json:"category,omitempty"`
	SectionTitle     string                   `json:"section_title,omitempty"`
	LocationType     string                   `json:"location_type,omitempty"`
	DurationMinutes  int                      `json:"duration_minutes,omitempty"`
	PriceAmountMinor MinorAmount              `json:"price_amount_minor,omitempty"`
	CurrencyCode     string                   `json:"currency_code,omitempty"`
	Mode             ContentGenerationMode    `json:"mode,omitempty"`
	ImprovementGoal  string                   `json:"improvement_goal,omitempty"`
	ExistingSummary  string                   `json:"existing_summary,omitempty"`
	ExistingDetails  string                   `json:"existing_details,omitempty"`
	Options          ContentGenerationOptions `json:"options,omitempty"`
}

type GenerateServiceDescriptionResponse struct {
	Description            string    `json:"description"`
	AlternativeDescription string    `json:"alternative_description,omitempty"`
	Warnings               []Warning `json:"warnings,omitempty"`
}

type GenerateShortServiceSummaryRequest struct {
	ServiceID        string                   `json:"service_id,omitempty"`
	BusinessName     string                   `json:"business_name,omitempty"`
	ServiceTitle     string                   `json:"service_title"`
	Category         string                   `json:"category,omitempty"`
	Description      string                   `json:"description,omitempty"`
	DurationMinutes  int                      `json:"duration_minutes,omitempty"`
	PriceAmountMinor MinorAmount              `json:"price_amount_minor,omitempty"`
	CurrencyCode     string                   `json:"currency_code,omitempty"`
	Mode             ContentGenerationMode    `json:"mode,omitempty"`
	ImprovementGoal  string                   `json:"improvement_goal,omitempty"`
	ExistingSummary  string                   `json:"existing_summary,omitempty"`
	Options          ContentGenerationOptions `json:"options,omitempty"`
}

type GenerateShortServiceSummaryResponse struct {
	Summary            string    `json:"summary"`
	AlternativeSummary string    `json:"alternative_summary,omitempty"`
	Warnings           []Warning `json:"warnings,omitempty"`
}

type GeneratePrepAftercareInstructionsRequest struct {
	ServiceID            string                   `json:"service_id,omitempty"`
	BusinessName         string                   `json:"business_name,omitempty"`
	ServiceTitle         string                   `json:"service_title"`
	Category             string                   `json:"category,omitempty"`
	Description          string                   `json:"description,omitempty"`
	LocationType         string                   `json:"location_type,omitempty"`
	DurationMinutes      int                      `json:"duration_minutes,omitempty"`
	Mode                 ContentGenerationMode    `json:"mode,omitempty"`
	ImprovementGoal      string                   `json:"improvement_goal,omitempty"`
	ExistingInstructions string                   `json:"existing_instructions,omitempty"`
	IncludePrep          bool                     `json:"include_prep"`
	IncludeAftercare     bool                     `json:"include_aftercare"`
	Options              ContentGenerationOptions `json:"options,omitempty"`
}

type GeneratePrepAftercareInstructionsResponse struct {
	Instructions            string    `json:"instructions"`
	AlternativeInstructions string    `json:"alternative_instructions,omitempty"`
	Warnings                []Warning `json:"warnings,omitempty"`
}

type ServiceFAQItem struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type GenerateServiceFAQsRequest struct {
	ServiceID          string                   `json:"service_id,omitempty"`
	BusinessName       string                   `json:"business_name,omitempty"`
	ServiceTitle       string                   `json:"service_title"`
	Category           string                   `json:"category,omitempty"`
	Description        string                   `json:"description,omitempty"`
	Instructions       string                   `json:"instructions,omitempty"`
	CancellationPolicy string                   `json:"cancellation_policy,omitempty"`
	LatenessPolicy     string                   `json:"lateness_policy,omitempty"`
	LocationType       string                   `json:"location_type,omitempty"`
	DurationMinutes    int                      `json:"duration_minutes,omitempty"`
	PriceAmountMinor   MinorAmount              `json:"price_amount_minor,omitempty"`
	CurrencyCode       string                   `json:"currency_code,omitempty"`
	FAQCount           int                      `json:"faq_count,omitempty"`
	Mode               ContentGenerationMode    `json:"mode,omitempty"`
	ImprovementGoal    string                   `json:"improvement_goal,omitempty"`
	ExistingFAQs       []ServiceFAQItem         `json:"existing_faqs,omitempty"`
	Options            ContentGenerationOptions `json:"options,omitempty"`
}

type GenerateServiceFAQsResponse struct {
	FAQs     []ServiceFAQItem `json:"faqs"`
	Warnings []Warning        `json:"warnings,omitempty"`
}
