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

func buildPrompt(task string, req any) string {
	payload, _ := json.MarshalIndent(req, "", "  ")
	return task + "\n\nInput JSON:\n" + string(payload)
}
