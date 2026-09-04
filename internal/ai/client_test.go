package ai

import (
	"context"
	"encoding/json"
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
	client := NewClient(
		NewService(defaultGenerator),
		NewService(agreementGenerator),
	)

	if !client.Available() {
		t.Fatal("configured client is unavailable")
	}
	if _, err := client.GenerateServiceDescription(context.Background(), aiapi.GenerateServiceDescriptionRequest{ServiceTitle: "Lashes"}); err != nil {
		t.Fatalf("GenerateServiceDescription() error = %v", err)
	}
	if _, err := client.GenerateAgreementDocument(context.Background(), agreementDocumentRequest()); err != nil {
		t.Fatalf("GenerateAgreementDocument() error = %v", err)
	}

	if defaultGenerator.calls != 1 || agreementGenerator.calls != 1 {
		t.Fatalf("provider calls = default:%d agreement:%d", defaultGenerator.calls, agreementGenerator.calls)
	}
}

func TestClientUnavailableWithMissingTaskService(t *testing.T) {
	service := NewService(&routingGenerator{})
	if NewClient(service, nil).Available() {
		t.Fatal("client with a missing task service is available")
	}
}

func TestClientForwardsAutopilotToDefaultService(t *testing.T) {
	generator := &scriptedSemiPilotGenerator{payloads: []json.RawMessage{
		json.RawMessage(`{"protocol_version":1,"reply":"Use Book here when you are ready.","next_state":"qualifying","tool_call":null,"missing_facts":[],"handoff_reason":""}`),
	}}
	service := NewService(generator)
	client := NewClient(service, service)
	decision, err := client.GenerateAutopilotTurnDecision(context.Background(), validSemiPilotTurnInput())
	if err != nil {
		t.Fatalf("GenerateAutopilotTurnDecision() error = %v", err)
	}
	if generator.calls != 1 || decision.Reply == "" {
		t.Fatalf("autopilot forwarding calls=%d decision=%+v", generator.calls, decision)
	}
}
