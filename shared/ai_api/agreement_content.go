package aiapi

type SummarizeAgreementPlainEnglishRequest struct {
	AgreementID          string                   `json:"agreement_id,omitempty"`
	TemplateID           string                   `json:"template_id,omitempty"`
	AgreementTitle       string                   `json:"agreement_title,omitempty"`
	ServiceCategory      string                   `json:"service_category,omitempty"`
	AgreementText        string                   `json:"agreement_text"`
	ExplainForAudience   string                   `json:"explain_for_audience,omitempty"`
	HighlightClauses     []string                 `json:"highlight_clauses,omitempty"`
	Mode                 ContentGenerationMode    `json:"mode,omitempty"`
	ImprovementGoal      string                   `json:"improvement_goal,omitempty"`
	ExistingSummary      string                   `json:"existing_summary,omitempty"`
	ExistingPlainEnglish string                   `json:"existing_plain_english,omitempty"`
	Options              ContentGenerationOptions `json:"options,omitempty"`
}

type SummarizeAgreementPlainEnglishResponse struct {
	Summary      string    `json:"summary"`
	PlainEnglish string    `json:"plain_english"`
	KeyPoints    []string  `json:"key_points,omitempty"`
	Warnings     []Warning `json:"warnings,omitempty"`
}
