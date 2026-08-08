package domain

import (
	"fmt"
	"regexp"

	aiapi "booking/shared/ai_api"

	"github.com/google/uuid"
)

var bracketPlaceholderPattern = regexp.MustCompile(`\[[A-Z][A-Z0-9_]*\]`)

type ConfirmationMethod string

const (
	ConfirmationMethodConfirmation ConfirmationMethod = "confirmation"
	ConfirmationMethodSignature    ConfirmationMethod = "signature"
)

func ParseConfirmationMethod(value string) (ConfirmationMethod, error) {
	method := ConfirmationMethod(value)
	switch method {
	case ConfirmationMethodConfirmation, ConfirmationMethodSignature:
		return method, nil
	default:
		return "", fmt.Errorf("unsupported confirmation method %q", value)
	}
}

func (m ConfirmationMethod) AIAPIValue() aiapi.AgreementConfirmationMethod {
	return aiapi.AgreementConfirmationMethod(m)
}

func FinalizeGeneratedDocument(
	generated aiapi.GeneratedDocumentSchema,
	method ConfirmationMethod,
	knownVariables map[string]struct{},
) (aiapi.DocumentSchema, error) {
	if _, err := ParseConfirmationMethod(string(method)); err != nil {
		return aiapi.DocumentSchema{}, err
	}
	if err := ValidateGeneratedDocument(generated, method, knownVariables); err != nil {
		return aiapi.DocumentSchema{}, fmt.Errorf("validate generated agreement document: %w", err)
	}

	document := aiapi.DocumentSchema{
		SchemaVersion: generated.SchemaVersion,
		Blocks:        make([]aiapi.AgreementDocumentBlock, len(generated.Blocks)),
	}
	for index, block := range generated.Blocks {
		document.Blocks[index] = aiapi.AgreementDocumentBlock{
			ID:      uuid.NewString(),
			Type:    block.Type,
			Level:   block.Level,
			Content: append([]aiapi.AgreementInlineNode(nil), block.Content...),
			Items:   cloneAgreementItems(block.Items),
			Method:  block.Method,
		}
	}
	if err := ValidateDocument(document, method, knownVariables); err != nil {
		return aiapi.DocumentSchema{}, fmt.Errorf("validate finalized agreement document: %w", err)
	}
	return document, nil
}

func ValidateGeneratedDocument(
	document aiapi.GeneratedDocumentSchema,
	method ConfirmationMethod,
	knownVariables map[string]struct{},
) error {
	if _, err := ParseConfirmationMethod(string(method)); err != nil {
		return err
	}
	if err := document.Validate(method.AIAPIValue(), knownVariables); err != nil {
		return err
	}
	for blockIndex, block := range document.Blocks {
		for nodeIndex, node := range block.Content {
			if node.Type == aiapi.AgreementInlineText && bracketPlaceholderPattern.MatchString(node.Text) {
				return fmt.Errorf("blocks[%d].content[%d] contains a bracket placeholder", blockIndex, nodeIndex)
			}
		}
		for itemIndex, item := range block.Items {
			for nodeIndex, node := range item {
				if node.Type == aiapi.AgreementInlineText && bracketPlaceholderPattern.MatchString(node.Text) {
					return fmt.Errorf("blocks[%d].items[%d][%d] contains a bracket placeholder", blockIndex, itemIndex, nodeIndex)
				}
			}
		}
	}
	return nil
}

func ValidateDocument(document aiapi.DocumentSchema, method ConfirmationMethod, knownVariables map[string]struct{}) error {
	if _, err := ParseConfirmationMethod(string(method)); err != nil {
		return err
	}
	if err := document.Validate(method.AIAPIValue(), knownVariables); err != nil {
		return err
	}
	for index, block := range document.Blocks {
		if _, err := uuid.Parse(block.ID); err != nil {
			return fmt.Errorf("blocks[%d].id must be a UUID", index)
		}
	}
	return nil
}

func cloneAgreementItems(items [][]aiapi.AgreementInlineNode) [][]aiapi.AgreementInlineNode {
	if len(items) == 0 {
		return nil
	}
	result := make([][]aiapi.AgreementInlineNode, len(items))
	for index, item := range items {
		result[index] = append([]aiapi.AgreementInlineNode(nil), item...)
	}
	return result
}
