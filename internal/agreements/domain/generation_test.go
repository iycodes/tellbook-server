package domain

import (
	"testing"
)

func TestGenerationInputsRoundTripStrictly(t *testing.T) {
	input := FieldsGenerationInput{BusinessCategory: "Beauty", ServiceName: "Lash extensions"}
	payload, err := EncodeGenerationInput(GenerationInputFields, input)
	if err != nil {
		t.Fatalf("EncodeGenerationInput() error = %v", err)
	}
	decoded, err := DecodeFieldsGenerationInput(payload)
	if err != nil {
		t.Fatalf("DecodeFieldsGenerationInput() error = %v", err)
	}
	if decoded.ServiceName != input.ServiceName {
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
