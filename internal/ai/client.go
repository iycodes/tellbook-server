package ai

import (
	"context"

	aiapi "booking/go-server/shared/ai_api"
)

// Client routes each application AI task to its configured in-process service.
type Client struct {
	defaultService   *Service
	agreementService *Service
}

func NewClient(defaultService, agreementService *Service) *Client {
	return &Client{
		defaultService:   defaultService,
		agreementService: agreementService,
	}
}

func (c *Client) Available() bool {
	return c != nil && c.defaultService != nil && c.agreementService != nil
}

func (c *Client) DefaultAvailable() bool {
	return c != nil && c.defaultService != nil
}

func (c *Client) GenerateInboxReplyDraft(ctx context.Context, req aiapi.InboxReplyDraftRequest) (aiapi.InboxReplyDraftResponse, error) {
	return c.defaultService.GenerateInboxReplyDraft(ctx, req)
}

func (c *Client) GenerateSemiPilotTurnDecision(ctx context.Context, input SemiPilotTurnInput) (SemiPilotTurnDecision, error) {
	return c.defaultService.GenerateSemiPilotTurnDecision(ctx, input)
}

func (c *Client) GenerateAutopilotTurnDecision(ctx context.Context, input SemiPilotTurnInput) (SemiPilotTurnDecision, error) {
	return c.defaultService.GenerateAutopilotTurnDecision(ctx, input)
}

func (c *Client) GenerateServiceDescription(ctx context.Context, req aiapi.GenerateServiceDescriptionRequest) (aiapi.GenerateServiceDescriptionResponse, error) {
	return c.defaultService.GenerateServiceDescription(ctx, req)
}

func (c *Client) GenerateAgreementDocument(ctx context.Context, req aiapi.GenerateAgreementDocumentRequest) (aiapi.GenerateAgreementDocumentResponse, error) {
	return c.agreementService.GenerateAgreementDocument(ctx, req)
}

func (c *Client) GeneratePrepAftercareInstructions(ctx context.Context, req aiapi.GeneratePrepAftercareInstructionsRequest) (aiapi.GeneratePrepAftercareInstructionsResponse, error) {
	return c.defaultService.GeneratePrepAftercareInstructions(ctx, req)
}

func (c *Client) GenerateSectionDescription(ctx context.Context, req aiapi.GenerateSectionDescriptionRequest) (aiapi.GenerateSectionDescriptionResponse, error) {
	return c.defaultService.GenerateSectionDescription(ctx, req)
}

func (c *Client) GeneratePublicPageContent(ctx context.Context, req aiapi.GeneratePublicPageContentRequest) (aiapi.GeneratePublicPageContentResponse, error) {
	return c.defaultService.GeneratePublicPageContent(ctx, req)
}
