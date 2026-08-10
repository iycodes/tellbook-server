package aiapi

import "fmt"

type AgreementGenerationSource string

const (
	AgreementGenerationFromFields   AgreementGenerationSource = "fields"
	AgreementGenerationFromDocument AgreementGenerationSource = "document"
)

type SupportedAgreementVariable struct {
	Key          string `json:"key"`
	Label        string `json:"label"`
	Description  string `json:"description"`
	ExampleValue string `json:"example_value"`
}

type GenerateAgreementDocumentRequest struct {
	Source                    AgreementGenerationSource    `json:"source"`
	SchemaVersion             int                          `json:"schema_version"`
	ConfirmationMethod        AgreementConfirmationMethod  `json:"confirmation_method"`
	SupportedVariables        []SupportedAgreementVariable `json:"supported_variables"`
	Context                   []NamedValue                 `json:"context"`
	BusinessCategory          string                       `json:"business_category"`
	ServiceName               string                       `json:"service_name"`
	CustomInstructions        string                       `json:"custom_instructions"`
	AgreementStyle            string                       `json:"agreement_style"`
	TypicalServiceLocation    string                       `json:"typical_service_location"`
	Tone                      string                       `json:"tone"`
	IncludeCancellationPolicy bool                         `json:"include_cancellation_policy"`
	IncludeLatenessPolicy     bool                         `json:"include_lateness_policy"`
	IncludePaymentTerms       bool                         `json:"include_payment_terms"`
	SourceTitle               string                       `json:"source_title"`
	SourceFileName            string                       `json:"source_file_name"`
	RedactedDocumentText      string                       `json:"redacted_document_text"`
}

type GenerateAgreementDocumentResponse struct {
	IsServiceAgreement   bool                     `json:"is_service_agreement"`
	DocumentType         string                   `json:"document_type"`
	Reason               string                   `json:"reason"`
	SuggestedTitle       string                   `json:"suggested_title"`
	SuggestedDescription string                   `json:"suggested_description"`
	DocumentSchema       *GeneratedDocumentSchema `json:"document_schema"`
	Warnings             []Warning                `json:"warnings"`
}

func (r GenerateAgreementDocumentRequest) Validate() error {
	if r.Source != AgreementGenerationFromFields && r.Source != AgreementGenerationFromDocument {
		return fmt.Errorf("unsupported agreement generation source %q", r.Source)
	}
	if r.SchemaVersion != AgreementDocumentSchemaVersion {
		return fmt.Errorf("schema_version must be %d", AgreementDocumentSchemaVersion)
	}
	if r.ConfirmationMethod != AgreementConfirmation && r.ConfirmationMethod != AgreementSignature {
		return fmt.Errorf("unsupported confirmation method %q", r.ConfirmationMethod)
	}
	if len(r.SupportedVariables) == 0 {
		return fmt.Errorf("supported_variables must not be empty")
	}
	seen := make(map[string]struct{}, len(r.SupportedVariables))
	for index, variable := range r.SupportedVariables {
		if !agreementVariableKeyPattern.MatchString(variable.Key) {
			return fmt.Errorf("supported_variables[%d].key is malformed", index)
		}
		if _, exists := seen[variable.Key]; exists {
			return fmt.Errorf("supported_variables[%d].key is duplicated", index)
		}
		seen[variable.Key] = struct{}{}
	}
	if r.Source == AgreementGenerationFromDocument && r.RedactedDocumentText == "" {
		return fmt.Errorf("redacted_document_text is required for document generation")
	}
	if r.Source == AgreementGenerationFromFields && (r.BusinessCategory == "" || r.ServiceName == "") {
		return fmt.Errorf("business_category and service_name are required for field generation")
	}
	return nil
}

func (r GenerateAgreementDocumentRequest) SupportedVariableKeySet() map[string]struct{} {
	result := make(map[string]struct{}, len(r.SupportedVariables))
	for _, variable := range r.SupportedVariables {
		result[variable.Key] = struct{}{}
	}
	return result
}

func GenerateAgreementDocumentResponseJSONSchema() map[string]any {
	documentSchema := GeneratedDocumentJSONSchema()
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"is_service_agreement":  map[string]any{"type": "boolean"},
			"document_type":         map[string]any{"type": "string"},
			"reason":                map[string]any{"type": "string"},
			"suggested_title":       map[string]any{"type": "string"},
			"suggested_description": map[string]any{"type": "string"},
			"document_schema": map[string]any{
				"anyOf": []any{documentSchema, map[string]any{"type": "null"}},
			},
			"warnings": map[string]any{
				"type":     "array",
				"maxItems": 10,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"code":    map[string]any{"type": "string"},
						"message": map[string]any{"type": "string"},
					},
					"required":             []string{"code", "message"},
					"additionalProperties": false,
				},
			},
		},
		"required": []string{
			"is_service_agreement",
			"document_type",
			"reason",
			"suggested_title",
			"suggested_description",
			"document_schema",
			"warnings",
		},
		"additionalProperties": false,
	}
}
