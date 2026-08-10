package aiapi

type GenerateSectionDescriptionRequest struct {
	SectionID           string                   `json:"section_id,omitempty"`
	BusinessName        string                   `json:"business_name,omitempty"`
	SectionTitle        string                   `json:"section_title"`
	ServiceTitles       []string                 `json:"service_titles,omitempty"`
	Mode                ContentGenerationMode    `json:"mode,omitempty"`
	ImprovementGoal     string                   `json:"improvement_goal,omitempty"`
	ExistingDescription string                   `json:"existing_description,omitempty"`
	Options             ContentGenerationOptions `json:"options,omitempty"`
}

type GenerateSectionDescriptionResponse struct {
	Description            string    `json:"description"`
	AlternativeDescription string    `json:"alternative_description,omitempty"`
	Warnings               []Warning `json:"warnings,omitempty"`
}
