package domain

import (
	"testing"

	aiapi "booking/go-server/shared/ai_api"

	"github.com/google/uuid"
)

func TestTemplateSchemaHashIgnoresBlockIDs(t *testing.T) {
	document := hashTestDocument()
	first, err := TemplateSchemaHash(document, ConfirmationMethodConfirmation)
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	for index := range document.Blocks {
		document.Blocks[index].ID = uuid.NewString()
	}
	second, err := TemplateSchemaHash(document, ConfirmationMethodConfirmation)
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	if first != second {
		t.Fatalf("block IDs changed hash: %s != %s", first, second)
	}
}

func TestTemplateSchemaHashChangesWithTemplateTerms(t *testing.T) {
	document := hashTestDocument()
	first, err := TemplateSchemaHash(document, ConfirmationMethodConfirmation)
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	document.Blocks[0].Content[0].Text = "Different terms"
	second, err := TemplateSchemaHash(document, ConfirmationMethodConfirmation)
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	if first == second {
		t.Fatal("different terms produced the same hash")
	}
}

func hashTestDocument() aiapi.DocumentSchema {
	return aiapi.DocumentSchema{
		SchemaVersion: aiapi.AgreementDocumentSchemaVersion,
		Blocks: []aiapi.AgreementDocumentBlock{
			{
				ID:   uuid.NewString(),
				Type: aiapi.AgreementBlockParagraph,
				Content: []aiapi.AgreementInlineNode{{
					Type: aiapi.AgreementInlineText,
					Text: "Terms apply",
				}},
			},
			{ID: uuid.NewString(), Type: aiapi.AgreementBlockAcceptance, Method: aiapi.AgreementConfirmation},
		},
	}
}
