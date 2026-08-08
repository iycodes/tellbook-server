package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"booking/go-server/internal/agreements/domain"
	aiapi "booking/shared/ai_api"
)

type uploadPreparerFake struct {
	preparation UploadPreparation
}

func (p uploadPreparerFake) PrepareAgreementUpload(context.Context, domain.TemplateGenerationJob, domain.UploadGenerationInput) (UploadPreparation, error) {
	return p.preparation, nil
}

func TestStoredGenerationRequestBuilderKeepsUploadTextOutOfStoredInput(t *testing.T) {
	input := domain.UploadGenerationInput{SourcePDFR2Key: "clients/one/source.pdf", SourcePDFFileName: "source.pdf"}
	payload, err := domain.EncodeGenerationInput(domain.GenerationInputUpload, input)
	if err != nil {
		t.Fatalf("EncodeGenerationInput() error = %v", err)
	}
	if strings.Contains(string(payload), "original agreement terms") {
		t.Fatal("stored input unexpectedly contains extracted source text")
	}
	builder, err := NewStoredGenerationRequestBuilder(uploadPreparerFake{preparation: UploadPreparation{
		RedactedDocumentText: "original agreement terms after redaction",
		ProhibitedLiterals:   []string{"Ada Okafor"},
	}})
	if err != nil {
		t.Fatalf("NewStoredGenerationRequestBuilder() error = %v", err)
	}
	prepared, err := builder.Build(context.Background(), domain.TemplateGenerationJob{
		ConfirmationMethod: domain.ConfirmationMethodSignature,
		InputKind:          domain.GenerationInputUpload,
		InputJSON:          json.RawMessage(payload),
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if prepared.Request.RedactedDocumentText == "" || prepared.Request.ConfirmationMethod != aiapi.AgreementSignature {
		t.Fatalf("prepared request = %+v", prepared.Request)
	}
}

func TestStoredGenerationRequestBuilderAllowsFieldsWithoutUploadStorage(t *testing.T) {
	payload, err := domain.EncodeGenerationInput(domain.GenerationInputFields, domain.FieldsGenerationInput{
		BusinessCategory: "Beauty",
		ServiceName:      "Lash extensions",
	})
	if err != nil {
		t.Fatalf("EncodeGenerationInput() error = %v", err)
	}
	builder, err := NewStoredGenerationRequestBuilder(nil)
	if err != nil {
		t.Fatalf("NewStoredGenerationRequestBuilder() error = %v", err)
	}
	if _, err := builder.Build(context.Background(), domain.TemplateGenerationJob{
		ConfirmationMethod: domain.ConfirmationMethodConfirmation,
		InputKind:          domain.GenerationInputFields,
		InputJSON:          payload,
	}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
}

func TestUploadOutputGuardRejectsPIIAndCopiedSource(t *testing.T) {
	source := "one two three four five six seven eight nine ten eleven twelve thirteen fourteen"
	guard := NewUploadOutputGuard(source, []string{"Ada Okafor"})

	response := generatedWorkerResponse()
	response.DocumentSchema.Blocks[0].Content[0].Text = "Ada Okafor agrees to these terms."
	if err := guard.Validate(response); err == nil {
		t.Fatal("guard accepted prohibited literal")
	}

	response.DocumentSchema.Blocks[0].Content[0].Text = source
	if err := guard.Validate(response); err == nil {
		t.Fatal("guard accepted a copied 13-word source span")
	}

	response.DocumentSchema.Blocks[0].Content[0].Text = "Contact customer@example.com for details."
	if err := guard.Validate(response); err == nil {
		t.Fatal("guard accepted an email address")
	}
}
