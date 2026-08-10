package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	aiapi "booking/go-server/shared/ai_api"
)

type templateHashPayload struct {
	SchemaVersion      int                                     `json:"schema_version"`
	Blocks             []aiapi.GeneratedAgreementDocumentBlock `json:"blocks"`
	ConfirmationMethod ConfirmationMethod                      `json:"confirmation_method"`
}

func TemplateSchemaHash(document aiapi.DocumentSchema, method ConfirmationMethod) (string, error) {
	if err := ValidateDocument(document, method, AgreementVariableKeySet()); err != nil {
		return "", fmt.Errorf("validate template document for hashing: %w", err)
	}
	blocks := make([]aiapi.GeneratedAgreementDocumentBlock, len(document.Blocks))
	for index, block := range document.Blocks {
		blocks[index] = aiapi.GeneratedAgreementDocumentBlock{
			Type:    block.Type,
			Level:   block.Level,
			Content: normalizeTemplateInlineNodes(block.Content),
			Items:   normalizeTemplateInlineItems(block.Items),
			Method:  block.Method,
		}
	}
	payload, err := json.Marshal(templateHashPayload{
		SchemaVersion:      document.SchemaVersion,
		Blocks:             blocks,
		ConfirmationMethod: method,
	})
	if err != nil {
		return "", fmt.Errorf("marshal template hash payload: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func normalizeTemplateInlineNodes(nodes []aiapi.AgreementInlineNode) []aiapi.AgreementInlineNode {
	if len(nodes) == 0 {
		return nil
	}
	result := make([]aiapi.AgreementInlineNode, len(nodes))
	for index, node := range nodes {
		node.Text = normalizeTemplateLineEndings(node.Text)
		result[index] = node
	}
	return result
}

func normalizeTemplateInlineItems(items [][]aiapi.AgreementInlineNode) [][]aiapi.AgreementInlineNode {
	if len(items) == 0 {
		return nil
	}
	result := make([][]aiapi.AgreementInlineNode, len(items))
	for index, item := range items {
		result[index] = normalizeTemplateInlineNodes(item)
	}
	return result
}

func normalizeTemplateLineEndings(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}
