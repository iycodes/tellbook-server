package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	aiapi "booking/go-server/shared/ai_api"
)

type JSONGenerator interface {
	GenerateJSON(ctx context.Context, systemPrompt, userPrompt string, dst any) error
}

type Service struct {
	generator JSONGenerator
}

var bracketPlaceholderPattern = regexp.MustCompile(`\[[A-Z][A-Z0-9_]*\]`)

func NewService(generator JSONGenerator) *Service {
	return &Service{generator: generator}
}

func (s *Service) GenerateAgreementDocument(ctx context.Context, req aiapi.GenerateAgreementDocumentRequest) (aiapi.GenerateAgreementDocumentResponse, error) {
	if err := req.Validate(); err != nil {
		return aiapi.GenerateAgreementDocumentResponse{}, err
	}
	var response aiapi.GenerateAgreementDocumentResponse
	if err := s.generator.GenerateJSON(
		ctx,
		generateAgreementDocumentSystemPrompt(),
		buildPrompt("Create a reusable structured service agreement document.", req),
		&response,
	); err != nil {
		return aiapi.GenerateAgreementDocumentResponse{}, err
	}
	return finalizeGeneratedAgreementDocument(req, response)
}

func finalizeGeneratedAgreementDocument(
	req aiapi.GenerateAgreementDocumentRequest,
	response aiapi.GenerateAgreementDocumentResponse,
) (aiapi.GenerateAgreementDocumentResponse, error) {
	response.DocumentType = strings.TrimSpace(response.DocumentType)
	response.Reason = strings.TrimSpace(response.Reason)
	response.SuggestedTitle = strings.TrimSpace(response.SuggestedTitle)
	response.SuggestedDescription = strings.TrimSpace(response.SuggestedDescription)
	response.Warnings = normalizeWarnings(response.Warnings)

	if !response.IsServiceAgreement {
		if response.Reason == "" {
			return aiapi.GenerateAgreementDocumentResponse{}, fmt.Errorf("llm did not explain why the source is not a service agreement")
		}
		response.DocumentSchema = nil
		return response, nil
	}
	if response.DocumentType != "service_agreement" {
		return aiapi.GenerateAgreementDocumentResponse{}, fmt.Errorf("llm returned unsupported agreement document type %q", response.DocumentType)
	}
	if response.SuggestedTitle == "" {
		return aiapi.GenerateAgreementDocumentResponse{}, fmt.Errorf("llm returned an empty agreement title")
	}
	if response.DocumentSchema == nil {
		return aiapi.GenerateAgreementDocumentResponse{}, fmt.Errorf("llm returned no agreement document schema")
	}
	response.DocumentSchema.Blocks = append(response.DocumentSchema.Blocks, aiapi.GeneratedAgreementDocumentBlock{
		Type:   aiapi.AgreementBlockAcceptance,
		Method: req.ConfirmationMethod,
	})
	if err := response.DocumentSchema.Validate(req.ConfirmationMethod, req.SupportedVariableKeySet()); err != nil {
		return aiapi.GenerateAgreementDocumentResponse{}, fmt.Errorf("validate generated agreement document: %w", err)
	}
	if generatedDocumentContainsBracketPlaceholder(*response.DocumentSchema) {
		return aiapi.GenerateAgreementDocumentResponse{}, fmt.Errorf("generated agreement contains bracket placeholder text")
	}
	return response, nil
}

func generatedDocumentContainsBracketPlaceholder(document aiapi.GeneratedDocumentSchema) bool {
	for _, block := range document.Blocks {
		for _, node := range block.Content {
			if node.Type == aiapi.AgreementInlineText && bracketPlaceholderPattern.MatchString(node.Text) {
				return true
			}
		}
		for _, item := range block.Items {
			for _, node := range item {
				if node.Type == aiapi.AgreementInlineText && bracketPlaceholderPattern.MatchString(node.Text) {
					return true
				}
			}
		}
	}
	return false
}

func normalizeWarnings(warnings []aiapi.Warning) []aiapi.Warning {
	result := make([]aiapi.Warning, 0, len(warnings))
	seen := make(map[string]struct{}, len(warnings))
	for _, warning := range warnings {
		warning.Code = strings.TrimSpace(warning.Code)
		warning.Message = strings.TrimSpace(warning.Message)
		if warning.Code == "" || warning.Message == "" {
			continue
		}
		key := warning.Code + "\x00" + warning.Message
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, warning)
	}
	return result
}

func (s *Service) SuggestReply(ctx context.Context, req aiapi.SuggestReplyRequest) (aiapi.SuggestReplyResponse, error) {
	var response aiapi.SuggestReplyResponse
	if err := s.generator.GenerateJSON(ctx, suggestReplySystemPrompt(), buildPrompt("Suggest the next reply in a customer conversation.", req), &response); err != nil {
		return aiapi.SuggestReplyResponse{}, err
	}
	if strings.TrimSpace(response.Reply) == "" {
		return aiapi.SuggestReplyResponse{}, fmt.Errorf("llm returned an empty reply")
	}

	response.Reply = strings.TrimSpace(response.Reply)
	response.Intent = strings.TrimSpace(response.Intent)
	response.EscalationReason = strings.TrimSpace(response.EscalationReason)
	response.Confidence = clamp(response.Confidence, 0, 1)
	if !response.SafeToSend && !response.NeedsHumanReview {
		response.NeedsHumanReview = true
	}
	if response.NeedsHumanReview && response.EscalationReason == "" {
		response.EscalationReason = "review recommended before sending"
	}
	return response, nil
}

func (s *Service) GenerateConversationAgentStep(ctx context.Context, req aiapi.ConversationAgentStepRequest) (aiapi.ConversationAgentStepResponse, error) {
	var response aiapi.ConversationAgentStepResponse
	if err := s.generator.GenerateJSON(ctx, conversationAgentStepSystemPrompt(), buildPrompt("Choose the next constrained action and reply for an inbox autopilot conversation.", req), &response); err != nil {
		return aiapi.ConversationAgentStepResponse{}, err
	}
	response.Action = aiapi.AgentAction(strings.TrimSpace(string(response.Action)))
	response.Reply = strings.TrimSpace(response.Reply)
	response.EscalationReason = strings.TrimSpace(response.EscalationReason)
	response.NextState = strings.TrimSpace(response.NextState)
	response.BookingIntent = strings.TrimSpace(response.BookingIntent)
	response.Confidence = clamp(response.Confidence, 0, 1)
	for index := range response.MissingFields {
		response.MissingFields[index] = strings.TrimSpace(response.MissingFields[index])
	}
	if response.Action == "" {
		return aiapi.ConversationAgentStepResponse{}, fmt.Errorf("llm returned an empty action")
	}
	if !isAllowedAgentAction(response.Action) {
		return aiapi.ConversationAgentStepResponse{}, fmt.Errorf("llm returned unsupported action %q", response.Action)
	}
	if response.Action != aiapi.AgentActionHandoffToHuman && response.Reply == "" {
		return aiapi.ConversationAgentStepResponse{}, fmt.Errorf("llm returned an empty reply")
	}
	if response.Action == aiapi.AgentActionSendBookingLink {
		response.ShouldSendBookingLink = true
	} else {
		response.ShouldSendBookingLink = false
	}
	if !response.SafeToSend && !response.NeedsHumanReview {
		response.NeedsHumanReview = true
	}
	if response.NeedsHumanReview && response.EscalationReason == "" {
		response.EscalationReason = "human review recommended"
	}
	return response, nil
}

func isAllowedAgentAction(action aiapi.AgentAction) bool {
	switch action {
	case aiapi.AgentActionReplyOnly,
		aiapi.AgentActionAskFollowUp,
		aiapi.AgentActionSendBookingLink,
		aiapi.AgentActionBookingReady,
		aiapi.AgentActionHandoffToHuman:
		return true
	default:
		return false
	}
}

func buildPrompt(task string, req any) string {
	payload, _ := json.MarshalIndent(req, "", "  ")
	return task + "\n\nInput JSON:\n" + string(payload)
}

func clamp(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
