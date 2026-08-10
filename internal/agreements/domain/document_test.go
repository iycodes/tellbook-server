package domain

import (
	"testing"

	aiapi "booking/go-server/shared/ai_api"

	"github.com/google/uuid"
)

func TestFinalizeGeneratedDocumentAssignsStableShapeAndUUIDs(t *testing.T) {
	generated := aiapi.GeneratedDocumentSchema{
		SchemaVersion: aiapi.AgreementDocumentSchemaVersion,
		Blocks: []aiapi.GeneratedAgreementDocumentBlock{
			{
				Type: aiapi.AgreementBlockParagraph,
				Content: []aiapi.AgreementInlineNode{
					{Type: aiapi.AgreementInlineText, Text: "This agreement is with "},
					{Type: aiapi.AgreementInlineVariable, Key: "CUSTOMER_NAME", Bold: true},
				},
			},
			{Type: aiapi.AgreementBlockAcceptance, Method: aiapi.AgreementSignature},
		},
	}

	document, err := FinalizeGeneratedDocument(
		generated,
		ConfirmationMethodSignature,
		map[string]struct{}{"CUSTOMER_NAME": {}},
	)
	if err != nil {
		t.Fatalf("FinalizeGeneratedDocument returned error: %v", err)
	}
	if len(document.Blocks) != len(generated.Blocks) {
		t.Fatalf("block count = %d", len(document.Blocks))
	}
	seen := make(map[uuid.UUID]struct{}, len(document.Blocks))
	for _, block := range document.Blocks {
		id, err := uuid.Parse(block.ID)
		if err != nil {
			t.Fatalf("invalid block ID %q: %v", block.ID, err)
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate block ID %q", block.ID)
		}
		seen[id] = struct{}{}
	}
	if keys := document.VariableKeys(); len(keys) != 1 || keys[0] != "CUSTOMER_NAME" {
		t.Fatalf("VariableKeys = %#v", keys)
	}
}

func TestValidateDocumentRequiresUUIDBlockIDs(t *testing.T) {
	document := aiapi.DocumentSchema{
		SchemaVersion: aiapi.AgreementDocumentSchemaVersion,
		Blocks: []aiapi.AgreementDocumentBlock{
			{
				ID:   "not-a-uuid",
				Type: aiapi.AgreementBlockParagraph,
				Content: []aiapi.AgreementInlineNode{{
					Type: aiapi.AgreementInlineText,
					Text: "Terms apply.",
				}},
			},
			{ID: uuid.NewString(), Type: aiapi.AgreementBlockAcceptance, Method: aiapi.AgreementConfirmation},
		},
	}
	err := ValidateDocument(document, ConfirmationMethodConfirmation, map[string]struct{}{})
	if err == nil {
		t.Fatal("expected invalid UUID error")
	}
}

func TestFinalizeGeneratedDocumentRejectsBracketPlaceholderText(t *testing.T) {
	generated := aiapi.GeneratedDocumentSchema{
		SchemaVersion: aiapi.AgreementDocumentSchemaVersion,
		Blocks: []aiapi.GeneratedAgreementDocumentBlock{
			{
				Type: aiapi.AgreementBlockParagraph,
				Content: []aiapi.AgreementInlineNode{{
					Type: aiapi.AgreementInlineText,
					Text: "Hello [CUSTOMER_NAME]",
				}},
			},
			{Type: aiapi.AgreementBlockAcceptance, Method: aiapi.AgreementSignature},
		},
	}
	if _, err := FinalizeGeneratedDocument(generated, ConfirmationMethodSignature, AgreementVariableKeySet()); err == nil {
		t.Fatal("FinalizeGeneratedDocument() accepted a bracket placeholder")
	}
}
