package domain

import (
	"testing"

	aiapi "booking/go-server/shared/ai_api"
)

func TestGenerationInputsRoundTripStrictly(t *testing.T) {
	input := FieldsGenerationInput{
		BusinessCategory: "Beauty",
		ServiceName:      "Lash extensions",
		Context:          []aiapi.NamedValue{{Key: "service_duration", Value: "90 minutes"}},
	}
	payload, err := EncodeGenerationInput(GenerationInputFields, input)
	if err != nil {
		t.Fatalf("EncodeGenerationInput() error = %v", err)
	}
	decoded, err := DecodeFieldsGenerationInput(payload)
	if err != nil {
		t.Fatalf("DecodeFieldsGenerationInput() error = %v", err)
	}
	if decoded.ServiceName != input.ServiceName || len(decoded.Context) != 1 || decoded.Context[0].Value != "90 minutes" {
		t.Fatalf("decoded = %+v", decoded)
	}
	if _, err := DecodeFieldsGenerationInput(append(payload[:len(payload)-1], []byte(`,"unknown":true}`)...)); err == nil {
		t.Fatal("DecodeFieldsGenerationInput() accepted unknown field")
	}
}

func TestUploadGenerationInputStoresReferenceNotDocumentText(t *testing.T) {
	input := UploadGenerationInput{SourcePDFR2Key: "clients/one/agreement.pdf", SourcePDFFileName: "agreement.pdf"}
	payload, err := EncodeGenerationInput(GenerationInputUpload, input)
	if err != nil {
		t.Fatalf("EncodeGenerationInput() error = %v", err)
	}
	if _, err := DecodeUploadGenerationInput(payload); err != nil {
		t.Fatalf("DecodeUploadGenerationInput() error = %v", err)
	}
}
