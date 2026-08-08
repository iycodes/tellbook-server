package aiapi

type PublicPageContentBlock struct {
	Headline     string `json:"headline,omitempty"`
	Bio          string `json:"bio,omitempty"`
	About        string `json:"about,omitempty"`
	BookingIntro string `json:"booking_intro,omitempty"`
}

type GeneratePublicPageContentRequest struct {
	ClientID          string                   `json:"client_id,omitempty"`
	BusinessName      string                   `json:"business_name"`
	BusinessType      string                   `json:"business_type,omitempty"`
	Specialties       []string                 `json:"specialties,omitempty"`
	Location          string                   `json:"location,omitempty"`
	ServiceTitles     []string                 `json:"service_titles,omitempty"`
	ServiceCategories []string                 `json:"service_categories,omitempty"`
	Mode              ContentGenerationMode    `json:"mode,omitempty"`
	ImprovementGoal   string                   `json:"improvement_goal,omitempty"`
	FieldsToImprove   []string                 `json:"fields_to_improve,omitempty"`
	ExistingContent   PublicPageContentBlock   `json:"existing_content,omitempty"`
	Options           ContentGenerationOptions `json:"options,omitempty"`
}

type GeneratePublicPageContentResponse struct {
	Content  PublicPageContentBlock `json:"content"`
	Warnings []Warning              `json:"warnings,omitempty"`
}
