package ai

import (
	"context"

	aiapi "booking/go-server/shared/ai_api"
)

// Client routes each application AI task to its configured in-process service.
type Client struct {
	defaultService   *Service
	agreementService *Service
	inboxService     *Service
}

func NewClient(defaultService, agreementService, inboxService *Service) *Client {
	return &Client{
		defaultService:   defaultService,
		agreementService: agreementService,
		inboxService:     inboxService,
	}
}

func (c *Client) Available() bool {
	return c != nil && c.defaultService != nil && c.agreementService != nil && c.inboxService != nil
}

func (c *Client) GenerateServiceDescription(ctx context.Context, req aiapi.GenerateServiceDescriptionRequest) (aiapi.GenerateServiceDescriptionResponse, error) {
	return c.defaultService.GenerateServiceDescription(ctx, req)
}

func (c *Client) GenerateConversationAgentStep(ctx context.Context, req aiapi.ConversationAgentStepRequest) (aiapi.ConversationAgentStepResponse, error) {
	return c.inboxService.GenerateConversationAgentStep(ctx, req)
}

func (c *Client) SuggestReply(ctx context.Context, req aiapi.SuggestReplyRequest) (aiapi.SuggestReplyResponse, error) {
	return c.inboxService.SuggestReply(ctx, req)
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
