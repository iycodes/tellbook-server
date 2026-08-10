package ai

import (
	"context"
	"strings"
	"testing"

	aiapi "booking/go-server/shared/ai_api"
)

type contentGenerator struct {
	generate func(any)
}

func (g contentGenerator) GenerateJSON(_ context.Context, _, _ string, destination any) error {
	g.generate(destination)
	return nil
}

func TestGenerateServiceDescriptionRejectsOversizedOutput(t *testing.T) {
	service := NewService(contentGenerator{generate: func(destination any) {
		destination.(*aiapi.GenerateServiceDescriptionResponse).Description = strings.Repeat("a", 1201)
	}})

	_, err := service.GenerateServiceDescription(context.Background(), aiapi.GenerateServiceDescriptionRequest{ServiceTitle: "Lashes"})
	if err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Fatalf("error = %v", err)
	}
}

func TestGeneratePublicPageContentRequiresRequestedFields(t *testing.T) {
	service := NewService(contentGenerator{generate: func(destination any) {
		destination.(*aiapi.GeneratePublicPageContentResponse).Content.Headline = "Lash artist"
	}})

	_, err := service.GeneratePublicPageContent(context.Background(), aiapi.GeneratePublicPageContentRequest{
		BusinessName:    "TellBook Beauty",
		FieldsToImprove: []string{"bio"},
	})
	if err == nil || !strings.Contains(err.Error(), "empty bio") {
		t.Fatalf("error = %v", err)
	}
}
