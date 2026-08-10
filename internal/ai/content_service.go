package ai

import (
	"context"
	"fmt"
	"strings"

	aiapi "booking/go-server/shared/ai_api"
)

func (s *Service) GenerateServiceDescription(ctx context.Context, req aiapi.GenerateServiceDescriptionRequest) (aiapi.GenerateServiceDescriptionResponse, error) {
	var response aiapi.GenerateServiceDescriptionResponse
	if err := s.generator.GenerateJSON(ctx, generateServiceDescriptionSystemPrompt(), buildPrompt("Generate a customer-facing service description.", req), &response); err != nil {
		return aiapi.GenerateServiceDescriptionResponse{}, err
	}

	response.Description = strings.TrimSpace(response.Description)
	response.AlternativeDescription = strings.TrimSpace(response.AlternativeDescription)
	if err := validateGeneratedText("description", response.Description, 1200, true); err != nil {
		return aiapi.GenerateServiceDescriptionResponse{}, err
	}
	if err := validateGeneratedText("alternative description", response.AlternativeDescription, 1200, false); err != nil {
		return aiapi.GenerateServiceDescriptionResponse{}, err
	}
	return response, nil
}

func (s *Service) GeneratePrepAftercareInstructions(ctx context.Context, req aiapi.GeneratePrepAftercareInstructionsRequest) (aiapi.GeneratePrepAftercareInstructionsResponse, error) {
	var response aiapi.GeneratePrepAftercareInstructionsResponse
	if err := s.generator.GenerateJSON(ctx, generatePrepAftercareInstructionsSystemPrompt(), buildPrompt("Generate prep and aftercare instructions for a service.", req), &response); err != nil {
		return aiapi.GeneratePrepAftercareInstructionsResponse{}, err
	}

	response.Instructions = strings.TrimSpace(response.Instructions)
	response.AlternativeInstructions = strings.TrimSpace(response.AlternativeInstructions)
	if err := validateGeneratedText("instructions", response.Instructions, 3000, true); err != nil {
		return aiapi.GeneratePrepAftercareInstructionsResponse{}, err
	}
	if err := validateGeneratedText("alternative instructions", response.AlternativeInstructions, 3000, false); err != nil {
		return aiapi.GeneratePrepAftercareInstructionsResponse{}, err
	}
	return response, nil
}

func (s *Service) GenerateSectionDescription(ctx context.Context, req aiapi.GenerateSectionDescriptionRequest) (aiapi.GenerateSectionDescriptionResponse, error) {
	var response aiapi.GenerateSectionDescriptionResponse
	if err := s.generator.GenerateJSON(ctx, generateSectionDescriptionSystemPrompt(), buildPrompt("Generate a description for a service section.", req), &response); err != nil {
		return aiapi.GenerateSectionDescriptionResponse{}, err
	}

	response.Description = strings.TrimSpace(response.Description)
	response.AlternativeDescription = strings.TrimSpace(response.AlternativeDescription)
	if err := validateGeneratedText("section description", response.Description, 800, true); err != nil {
		return aiapi.GenerateSectionDescriptionResponse{}, err
	}
	if err := validateGeneratedText("alternative section description", response.AlternativeDescription, 800, false); err != nil {
		return aiapi.GenerateSectionDescriptionResponse{}, err
	}
	return response, nil
}

func (s *Service) GeneratePublicPageContent(ctx context.Context, req aiapi.GeneratePublicPageContentRequest) (aiapi.GeneratePublicPageContentResponse, error) {
	var response aiapi.GeneratePublicPageContentResponse
	if err := s.generator.GenerateJSON(ctx, generatePublicPageContentSystemPrompt(), buildPrompt("Generate coordinated public-page content blocks.", req), &response); err != nil {
		return aiapi.GeneratePublicPageContentResponse{}, err
	}

	response.Content.Headline = strings.TrimSpace(response.Content.Headline)
	response.Content.Bio = strings.TrimSpace(response.Content.Bio)
	response.Content.About = strings.TrimSpace(response.Content.About)
	response.Content.BookingIntro = strings.TrimSpace(response.Content.BookingIntro)
	if response.Content.Headline == "" && response.Content.Bio == "" && response.Content.About == "" && response.Content.BookingIntro == "" {
		return aiapi.GeneratePublicPageContentResponse{}, fmt.Errorf("llm returned empty public page content")
	}
	for _, field := range []struct {
		name     string
		value    string
		maxRunes int
	}{
		{name: "headline", value: response.Content.Headline, maxRunes: 160},
		{name: "bio", value: response.Content.Bio, maxRunes: 600},
		{name: "about", value: response.Content.About, maxRunes: 2000},
		{name: "booking intro", value: response.Content.BookingIntro, maxRunes: 800},
	} {
		if err := validateGeneratedText(field.name, field.value, field.maxRunes, false); err != nil {
			return aiapi.GeneratePublicPageContentResponse{}, err
		}
	}
	for _, field := range req.FieldsToImprove {
		var value string
		switch strings.TrimSpace(field) {
		case "headline":
			value = response.Content.Headline
		case "bio":
			value = response.Content.Bio
		case "about":
			value = response.Content.About
		case "booking_intro":
			value = response.Content.BookingIntro
		default:
			continue
		}
		if strings.TrimSpace(value) == "" {
			return aiapi.GeneratePublicPageContentResponse{}, fmt.Errorf("llm returned empty %s", field)
		}
	}
	return response, nil
}

func validateGeneratedText(field, value string, maxRunes int, required bool) error {
	length := len([]rune(strings.TrimSpace(value)))
	if required && length == 0 {
		return fmt.Errorf("llm returned empty %s", field)
	}
	if length > maxRunes {
		return fmt.Errorf("llm returned %s longer than %d characters", field, maxRunes)
	}
	return nil
}
