package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	aiapi "booking/shared/ai_api"
)

var errUnavailable = fmt.Errorf("ai service is unavailable")

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}

	return &Client{
		baseURL:    baseURL,
		httpClient: httpClient,
	}
}

func (c *Client) Available() bool {
	return c != nil && c.baseURL != ""
}

func (c *Client) GenerateServiceDescription(ctx context.Context, req aiapi.GenerateServiceDescriptionRequest) (aiapi.GenerateServiceDescriptionResponse, error) {
	return postJSON[aiapi.GenerateServiceDescriptionRequest, aiapi.GenerateServiceDescriptionResponse](ctx, c, "/v1/services/generate-description", req)
}

func (c *Client) GenerateConversationAgentStep(ctx context.Context, req aiapi.ConversationAgentStepRequest) (aiapi.ConversationAgentStepResponse, error) {
	return postJSON[aiapi.ConversationAgentStepRequest, aiapi.ConversationAgentStepResponse](ctx, c, "/v1/agents/conversation-step", req)
}

func (c *Client) SuggestReply(ctx context.Context, req aiapi.SuggestReplyRequest) (aiapi.SuggestReplyResponse, error) {
	return postJSON[aiapi.SuggestReplyRequest, aiapi.SuggestReplyResponse](ctx, c, "/v1/replies/suggest", req)
}

func (c *Client) GenerateAgreementDocument(ctx context.Context, req aiapi.GenerateAgreementDocumentRequest) (aiapi.GenerateAgreementDocumentResponse, error) {
	return postJSON[aiapi.GenerateAgreementDocumentRequest, aiapi.GenerateAgreementDocumentResponse](ctx, c, "/v1/templates/generate-document", req)
}

func (c *Client) GeneratePrepAftercareInstructions(ctx context.Context, req aiapi.GeneratePrepAftercareInstructionsRequest) (aiapi.GeneratePrepAftercareInstructionsResponse, error) {
	return postJSON[aiapi.GeneratePrepAftercareInstructionsRequest, aiapi.GeneratePrepAftercareInstructionsResponse](ctx, c, "/v1/services/generate-instructions", req)
}

func (c *Client) GenerateSectionDescription(ctx context.Context, req aiapi.GenerateSectionDescriptionRequest) (aiapi.GenerateSectionDescriptionResponse, error) {
	return postJSON[aiapi.GenerateSectionDescriptionRequest, aiapi.GenerateSectionDescriptionResponse](ctx, c, "/v1/sections/generate-description", req)
}

func (c *Client) GeneratePublicPageContent(ctx context.Context, req aiapi.GeneratePublicPageContentRequest) (aiapi.GeneratePublicPageContentResponse, error) {
	return postJSON[aiapi.GeneratePublicPageContentRequest, aiapi.GeneratePublicPageContentResponse](ctx, c, "/v1/profiles/generate-public-page-content", req)
}

func postJSON[Req any, Resp any](ctx context.Context, client *Client, path string, payload Req) (Resp, error) {
	var zero Resp
	if client == nil || !client.Available() {
		return zero, errUnavailable
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return zero, fmt.Errorf("marshal ai request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return zero, fmt.Errorf("build ai request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := client.httpClient.Do(request)
	if err != nil {
		return zero, fmt.Errorf("call ai service: %w", err)
	}
	defer response.Body.Close()

	var parsed Resp
	if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		return zero, fmt.Errorf("decode ai response: %w", err)
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return zero, fmt.Errorf("ai service request failed with status %d", response.StatusCode)
	}

	return parsed, nil
}
