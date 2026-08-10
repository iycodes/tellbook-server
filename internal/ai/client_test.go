package ai

import (
	"context"
	"testing"

	aiapi "booking/go-server/shared/ai_api"
)

type routingGenerator struct {
	calls int
}

func (g *routingGenerator) GenerateJSON(_ context.Context, _, _ string, destination any) error {
	g.calls++
	switch response := destination.(type) {
	case *aiapi.GenerateServiceDescriptionResponse:
		response.Description = "Default provider"
	case *aiapi.SuggestReplyResponse:
		response.Reply = "Inbox provider"
		response.SafeToSend = true
	case *aiapi.GenerateAgreementDocumentResponse:
		response.IsServiceAgreement = true
		response.DocumentType = "service_agreement"
		response.SuggestedTitle = "Agreement"
		response.DocumentSchema = &aiapi.GeneratedDocumentSchema{
			SchemaVersion: aiapi.AgreementDocumentSchemaVersion,
			Blocks: []aiapi.GeneratedAgreementDocumentBlock{
				{Type: aiapi.AgreementBlockParagraph, Content: []aiapi.AgreementInlineNode{{Type: aiapi.AgreementInlineText, Text: "Terms"}}},
			},
		}
	}
	return nil
}

func TestClientRoutesTasksToConfiguredServices(t *testing.T) {
	defaultGenerator := &routingGenerator{}
	agreementGenerator := &routingGenerator{}
	inboxGenerator := &routingGenerator{}
	client := NewClient(
		NewService(defaultGenerator),
		NewService(agreementGenerator),
		NewService(inboxGenerator),
	)

	if !client.Available() {
		t.Fatal("configured client is unavailable")
	}
	if _, err := client.GenerateServiceDescription(context.Background(), aiapi.GenerateServiceDescriptionRequest{ServiceTitle: "Lashes"}); err != nil {
		t.Fatalf("GenerateServiceDescription() error = %v", err)
	}
	if _, err := client.SuggestReply(context.Background(), aiapi.SuggestReplyRequest{LatestCustomerMessage: "Hello"}); err != nil {
		t.Fatalf("SuggestReply() error = %v", err)
	}
	if _, err := client.GenerateAgreementDocument(context.Background(), agreementDocumentRequest()); err != nil {
		t.Fatalf("GenerateAgreementDocument() error = %v", err)
	}

	if defaultGenerator.calls != 1 || agreementGenerator.calls != 1 || inboxGenerator.calls != 1 {
		t.Fatalf("provider calls = default:%d agreement:%d inbox:%d", defaultGenerator.calls, agreementGenerator.calls, inboxGenerator.calls)
	}
}

func TestClientUnavailableWithMissingTaskService(t *testing.T) {
	service := NewService(&routingGenerator{})
	if NewClient(service, nil, service).Available() {
		t.Fatal("client with a missing task service is available")
	}
}
