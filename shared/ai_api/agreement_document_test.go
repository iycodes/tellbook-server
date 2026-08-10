package aiapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDocumentSchemaValidationAndVariableDerivation(t *testing.T) {
	document := validAgreementDocument()
	knownVariables := map[string]struct{}{
		"BUSINESS_NAME": {},
		"CUSTOMER_NAME": {},
	}
	if err := document.Validate(AgreementSignature, knownVariables); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	keys := document.VariableKeys()
	if len(keys) != 2 || keys[0] != "BUSINESS_NAME" || keys[1] != "CUSTOMER_NAME" {
		t.Fatalf("VariableKeys = %#v", keys)
	}
}

func TestDocumentSchemaRejectsUnknownVariableAndAcceptanceMismatch(t *testing.T) {
	document := validAgreementDocument()
	if err := document.Validate(AgreementSignature, map[string]struct{}{}); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected unregistered variable error, got %v", err)
	}

	knownVariables := map[string]struct{}{"BUSINESS_NAME": {}, "CUSTOMER_NAME": {}}
	if err := document.Validate(AgreementConfirmation, knownVariables); err == nil || !strings.Contains(err.Error(), "must match") {
		t.Fatalf("expected acceptance mismatch error, got %v", err)
	}
}

func TestDocumentSchemaRejectsDuplicateBlockIDs(t *testing.T) {
	document := validAgreementDocument()
	document.Blocks[1].ID = document.Blocks[0].ID
	err := document.Validate(AgreementSignature, map[string]struct{}{"BUSINESS_NAME": {}, "CUSTOMER_NAME": {}})
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("expected duplicate ID error, got %v", err)
	}
}

func TestDocumentSchemaDecodingRejectsUnknownAndCrossVariantFields(t *testing.T) {
	tests := []string{
		`{"schema_version":1,"blocks":[],"unexpected":true}`,
		`{"schema_version":1,"blocks":[{"id":"one","type":"divider","content":[]}]}`,
		`{"schema_version":1,"blocks":[{"id":"one","type":"paragraph","content":[{"type":"text","text":"Hello","key":"INVALID","bold":false}]}]}`,
		`{"schema_version":1,"blocks":[{"id":"one","type":"paragraph","content":[{"type":"text","text":"Hello"}]}]}`,
	}
	for _, input := range tests {
		var document DocumentSchema
		if err := json.Unmarshal([]byte(input), &document); err == nil {
			t.Fatalf("expected strict decode failure for %s", input)
		}
	}
}

func TestDocumentSchemaMarshalsOnlyVariantFields(t *testing.T) {
	payload, err := json.Marshal(validAgreementDocument())
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	text := string(payload)
	if strings.Contains(text, `"items"`) || strings.Contains(text, `"level":0`) {
		t.Fatalf("payload contains fields from another block variant: %s", text)
	}
}

func TestGeneratedDocumentSchemaUsesDiscriminatedJSONSchema(t *testing.T) {
	payload, err := json.Marshal(GeneratedDocumentJSONSchema())
	if err != nil {
		t.Fatalf("Marshal schema: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	properties := decoded["properties"].(map[string]any)
	blocks := properties["blocks"].(map[string]any)
	if blocks["minItems"] != float64(2) || blocks["maxItems"] != float64(20) {
		t.Fatalf("block bounds = min %#v, max %#v", blocks["minItems"], blocks["maxItems"])
	}
	items := blocks["items"].(map[string]any)
	anyOf, ok := items["anyOf"].([]any)
	if !ok || len(anyOf) != 4 {
		t.Fatalf("block schema anyOf = %#v", items["anyOf"])
	}
	if strings.Contains(string(payload), `"id"`) {
		t.Fatalf("generated document schema contains block IDs: %s", payload)
	}
	if strings.Contains(string(payload), `"divider"`) || strings.Contains(string(payload), `"acceptance"`) {
		t.Fatalf("generated document schema contains server-owned blocks: %s", payload)
	}
	paragraph := anyOf[1].(map[string]any)
	paragraphProperties := paragraph["properties"].(map[string]any)
	content := paragraphProperties["content"].(map[string]any)
	if content["minItems"] != float64(1) || content["maxItems"] != float64(32) {
		t.Fatalf("inline bounds = min %#v, max %#v", content["minItems"], content["maxItems"])
	}
	list := anyOf[2].(map[string]any)
	listProperties := list["properties"].(map[string]any)
	listItems := listProperties["items"].(map[string]any)
	if listItems["minItems"] != float64(1) || listItems["maxItems"] != float64(12) {
		t.Fatalf("list bounds = min %#v, max %#v", listItems["minItems"], listItems["maxItems"])
	}
}

func TestGeneratedDocumentValidationDoesNotRequireBlockIDs(t *testing.T) {
	document := GeneratedDocumentSchema{
		SchemaVersion: AgreementDocumentSchemaVersion,
		Blocks: []GeneratedAgreementDocumentBlock{
			{
				Type: AgreementBlockParagraph,
				Content: []AgreementInlineNode{{
					Type: AgreementInlineText,
					Text: "Terms apply.",
				}},
			},
			{Type: AgreementBlockAcceptance, Method: AgreementConfirmation},
		},
	}
	if err := document.Validate(AgreementConfirmation, map[string]struct{}{}); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
}

func validAgreementDocument() DocumentSchema {
	return DocumentSchema{
		SchemaVersion: AgreementDocumentSchemaVersion,
		Blocks: []AgreementDocumentBlock{
			{
				ID:    "heading-id",
				Type:  AgreementBlockHeading,
				Level: 1,
				Content: []AgreementInlineNode{{
					Type: AgreementInlineText,
					Text: "SERVICE AGREEMENT",
					Bold: true,
				}},
			},
			{
				ID:   "paragraph-id",
				Type: AgreementBlockParagraph,
				Content: []AgreementInlineNode{
					{Type: AgreementInlineText, Text: "This agreement is between "},
					{Type: AgreementInlineVariable, Key: "BUSINESS_NAME", Bold: true},
					{Type: AgreementInlineText, Text: " and "},
					{Type: AgreementInlineVariable, Key: "CUSTOMER_NAME", Bold: true},
					{Type: AgreementInlineText, Text: "."},
				},
			},
			{ID: "acceptance-id", Type: AgreementBlockAcceptance, Method: AgreementSignature},
		},
	}
}
