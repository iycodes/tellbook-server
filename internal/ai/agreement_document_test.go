package ai

import (
	"context"
	"strings"
	"testing"

	aiapi "booking/go-server/shared/ai_api"
)

type agreementDocumentGenerator struct {
	response aiapi.GenerateAgreementDocumentResponse
}

func (g agreementDocumentGenerator) GenerateJSON(_ context.Context, _, _ string, destination any) error {
	response := destination.(*aiapi.GenerateAgreementDocumentResponse)
	*response = g.response
	return nil
}

func TestGenerateAgreementDocumentValidatesStructuredOutput(t *testing.T) {
	request := agreementDocumentRequest()
	service := NewService(agreementDocumentGenerator{response: validGeneratedAgreementResponse()})
	response, err := service.GenerateAgreementDocument(context.Background(), request)
	if err != nil {
		t.Fatalf("GenerateAgreementDocument() error = %v", err)
	}
	if response.DocumentSchema == nil || len(response.DocumentSchema.VariableKeys()) != 1 {
		t.Fatalf("response = %+v", response)
	}
}

func TestGenerateAgreementDocumentAppendsRequestedAcceptanceMethod(t *testing.T) {
	request := agreementDocumentRequest()
	request.ConfirmationMethod = aiapi.AgreementSignature
	service := NewService(agreementDocumentGenerator{response: validGeneratedAgreementResponse()})
	response, err := service.GenerateAgreementDocument(context.Background(), request)
	if err != nil {
		t.Fatalf("GenerateAgreementDocument() error = %v", err)
	}
	lastBlock := response.DocumentSchema.Blocks[len(response.DocumentSchema.Blocks)-1]
	if lastBlock.Type != aiapi.AgreementBlockAcceptance || lastBlock.Method != aiapi.AgreementSignature {
		t.Fatalf("acceptance block = %#v", lastBlock)
	}
}

func TestGenerateAgreementDocumentRejectsBracketPlaceholderText(t *testing.T) {
	response := validGeneratedAgreementResponse()
	response.DocumentSchema.Blocks[0].Content[0] = aiapi.AgreementInlineNode{Type: aiapi.AgreementInlineText, Text: "Hello [CUSTOMER_NAME]"}
	service := NewService(agreementDocumentGenerator{response: response})
	_, err := service.GenerateAgreementDocument(context.Background(), agreementDocumentRequest())
	if err == nil || !strings.Contains(err.Error(), "bracket placeholder") {
		t.Fatalf("error = %v", err)
	}
}

func agreementDocumentRequest() aiapi.GenerateAgreementDocumentRequest {
	return aiapi.GenerateAgreementDocumentRequest{
		Source:             aiapi.AgreementGenerationFromFields,
		SchemaVersion:      aiapi.AgreementDocumentSchemaVersion,
		ConfirmationMethod: aiapi.AgreementConfirmation,
		SupportedVariables: []aiapi.SupportedAgreementVariable{{Key: "BUSINESS_NAME", Label: "Business name"}},
		BusinessCategory:   "Beauty",
		ServiceName:        "Lashes",
	}
}

func validGeneratedAgreementResponse() aiapi.GenerateAgreementDocumentResponse {
	return aiapi.GenerateAgreementDocumentResponse{
		IsServiceAgreement: true,
		DocumentType:       "service_agreement",
		SuggestedTitle:     "Lash Service Agreement",
		DocumentSchema: &aiapi.GeneratedDocumentSchema{
			SchemaVersion: aiapi.AgreementDocumentSchemaVersion,
			Blocks: []aiapi.GeneratedAgreementDocumentBlock{
				{
					Type: aiapi.AgreementBlockParagraph,
					Content: []aiapi.AgreementInlineNode{
						{Type: aiapi.AgreementInlineText, Text: "Provider: "},
						{Type: aiapi.AgreementInlineVariable, Key: "BUSINESS_NAME"},
					},
				},
			},
		},
		Warnings: []aiapi.Warning{{Code: " review ", Message: " Check terms "}, {Code: "review", Message: "Check terms"}},
	}
}
