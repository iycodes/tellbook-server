package aiapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGenerateAgreementDocumentRequestValidation(t *testing.T) {
	request := GenerateAgreementDocumentRequest{
		Source:             AgreementGenerationFromFields,
		SchemaVersion:      AgreementDocumentSchemaVersion,
		ConfirmationMethod: AgreementSignature,
		SupportedVariables: []SupportedAgreementVariable{{Key: "BUSINESS_NAME"}},
		BusinessCategory:   "Beauty",
		ServiceName:        "Lash extensions",
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	request.SupportedVariables = append(request.SupportedVariables, SupportedAgreementVariable{Key: "BUSINESS_NAME"})
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestGenerateAgreementDocumentResponseSchemaIsStrict(t *testing.T) {
	schema := GenerateAgreementDocumentResponseJSONSchema()
	payload, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	text := string(payload)
	for _, expected := range []string{`"document_schema"`, `"additionalProperties":false`, `"paragraph"`, `"variable"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("response schema missing %s: %s", expected, text)
		}
	}
	properties := schema["properties"].(map[string]any)
	warnings := properties["warnings"].(map[string]any)
	if warnings["maxItems"] != 10 {
		t.Fatalf("warning maxItems = %#v", warnings["maxItems"])
	}
}
