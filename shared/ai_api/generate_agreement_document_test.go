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
	payload, err := json.Marshal(GenerateAgreementDocumentResponseJSONSchema())
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	text := string(payload)
	for _, expected := range []string{`"document_schema"`, `"additionalProperties":false`, `"acceptance"`, `"variable"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("response schema missing %s: %s", expected, text)
		}
	}
}
