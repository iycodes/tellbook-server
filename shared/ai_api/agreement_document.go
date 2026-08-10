package aiapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	AgreementDocumentSchemaVersion = 1
	AgreementDocumentMaxBlocks     = 200
	AgreementDocumentMaxListItems  = 50
	AgreementDocumentMaxInline     = 500
	AgreementDocumentMaxCharacters = 100_000
)

type AgreementConfirmationMethod string

const (
	AgreementConfirmation AgreementConfirmationMethod = "confirmation"
	AgreementSignature    AgreementConfirmationMethod = "signature"
)

type AgreementBlockType string

const (
	AgreementBlockHeading       AgreementBlockType = "heading"
	AgreementBlockParagraph     AgreementBlockType = "paragraph"
	AgreementBlockOrderedList   AgreementBlockType = "ordered_list"
	AgreementBlockUnorderedList AgreementBlockType = "unordered_list"
	AgreementBlockDivider       AgreementBlockType = "divider"
	AgreementBlockAcceptance    AgreementBlockType = "acceptance"
)

type AgreementInlineType string

const (
	AgreementInlineText     AgreementInlineType = "text"
	AgreementInlineVariable AgreementInlineType = "variable"
)

type AgreementInlineNode struct {
	Type AgreementInlineType `json:"type"`
	Text string              `json:"text,omitempty"`
	Key  string              `json:"key,omitempty"`
	Bold bool                `json:"bold"`
}

type AgreementDocumentBlock struct {
	ID      string                      `json:"id"`
	Type    AgreementBlockType          `json:"type"`
	Level   int                         `json:"level,omitempty"`
	Content []AgreementInlineNode       `json:"content,omitempty"`
	Items   [][]AgreementInlineNode     `json:"items,omitempty"`
	Method  AgreementConfirmationMethod `json:"method,omitempty"`
}

type GeneratedAgreementDocumentBlock struct {
	Type    AgreementBlockType          `json:"type"`
	Level   int                         `json:"level,omitempty"`
	Content []AgreementInlineNode       `json:"content,omitempty"`
	Items   [][]AgreementInlineNode     `json:"items,omitempty"`
	Method  AgreementConfirmationMethod `json:"method,omitempty"`
}

type DocumentSchema struct {
	SchemaVersion int                      `json:"schema_version"`
	Blocks        []AgreementDocumentBlock `json:"blocks"`
}

type GeneratedDocumentSchema struct {
	SchemaVersion int                               `json:"schema_version"`
	Blocks        []GeneratedAgreementDocumentBlock `json:"blocks"`
}

var agreementVariableKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func (n *AgreementInlineNode) UnmarshalJSON(data []byte) error {
	type alias AgreementInlineNode
	var discriminator struct {
		Type AgreementInlineType `json:"type"`
	}
	if err := json.Unmarshal(data, &discriminator); err != nil {
		return err
	}
	allowed := map[AgreementInlineType]map[string]struct{}{
		AgreementInlineText:     stringSet("type", "text", "bold"),
		AgreementInlineVariable: stringSet("type", "key", "bold"),
	}
	keys, ok := allowed[discriminator.Type]
	if !ok {
		return fmt.Errorf("unsupported inline node type %q", discriminator.Type)
	}
	if err := rejectUnexpectedKeys(data, keys); err != nil {
		return err
	}
	if err := requireKeys(data, keys); err != nil {
		return err
	}
	return decodeStrictJSON(data, (*alias)(n))
}

func (b *AgreementDocumentBlock) UnmarshalJSON(data []byte) error {
	type alias AgreementDocumentBlock
	keys, err := agreementBlockKeys(data, true)
	if err != nil {
		return err
	}
	if err := rejectUnexpectedKeys(data, keys); err != nil {
		return err
	}
	if err := requireKeys(data, keys); err != nil {
		return err
	}
	return decodeStrictJSON(data, (*alias)(b))
}

func (b *GeneratedAgreementDocumentBlock) UnmarshalJSON(data []byte) error {
	type alias GeneratedAgreementDocumentBlock
	keys, err := agreementBlockKeys(data, false)
	if err != nil {
		return err
	}
	if err := rejectUnexpectedKeys(data, keys); err != nil {
		return err
	}
	if err := requireKeys(data, keys); err != nil {
		return err
	}
	return decodeStrictJSON(data, (*alias)(b))
}

func (d *DocumentSchema) UnmarshalJSON(data []byte) error {
	type alias DocumentSchema
	if err := rejectUnexpectedKeys(data, stringSet("schema_version", "blocks")); err != nil {
		return err
	}
	if err := requireKeys(data, stringSet("schema_version", "blocks")); err != nil {
		return err
	}
	return decodeStrictJSON(data, (*alias)(d))
}

func (d *GeneratedDocumentSchema) UnmarshalJSON(data []byte) error {
	type alias GeneratedDocumentSchema
	if err := rejectUnexpectedKeys(data, stringSet("schema_version", "blocks")); err != nil {
		return err
	}
	if err := requireKeys(data, stringSet("schema_version", "blocks")); err != nil {
		return err
	}
	return decodeStrictJSON(data, (*alias)(d))
}

func (d DocumentSchema) Validate(method AgreementConfirmationMethod, knownVariables map[string]struct{}) error {
	blocks := make([]agreementBlockForValidation, len(d.Blocks))
	seenIDs := make(map[string]struct{}, len(d.Blocks))
	for index, block := range d.Blocks {
		id := strings.TrimSpace(block.ID)
		if id == "" {
			return fmt.Errorf("blocks[%d].id is required", index)
		}
		if !utf8.ValidString(id) {
			return fmt.Errorf("blocks[%d].id is not valid UTF-8", index)
		}
		if _, exists := seenIDs[id]; exists {
			return fmt.Errorf("blocks[%d].id is duplicated", index)
		}
		seenIDs[id] = struct{}{}
		blocks[index] = agreementBlockForValidation{
			typeName: block.Type,
			level:    block.Level,
			content:  block.Content,
			items:    block.Items,
			method:   block.Method,
		}
	}
	return validateAgreementDocument(d.SchemaVersion, blocks, method, knownVariables)
}

func (d GeneratedDocumentSchema) Validate(method AgreementConfirmationMethod, knownVariables map[string]struct{}) error {
	blocks := make([]agreementBlockForValidation, len(d.Blocks))
	for index, block := range d.Blocks {
		blocks[index] = agreementBlockForValidation{
			typeName: block.Type,
			level:    block.Level,
			content:  block.Content,
			items:    block.Items,
			method:   block.Method,
		}
	}
	return validateAgreementDocument(d.SchemaVersion, blocks, method, knownVariables)
}

func (d DocumentSchema) VariableKeys() []string {
	keys := make(map[string]struct{})
	for _, block := range d.Blocks {
		collectAgreementVariableKeys(keys, block.Content, block.Items)
	}
	return sortedAgreementVariableKeys(keys)
}

func (d GeneratedDocumentSchema) VariableKeys() []string {
	keys := make(map[string]struct{})
	for _, block := range d.Blocks {
		collectAgreementVariableKeys(keys, block.Content, block.Items)
	}
	return sortedAgreementVariableKeys(keys)
}

type agreementBlockForValidation struct {
	typeName AgreementBlockType
	level    int
	content  []AgreementInlineNode
	items    [][]AgreementInlineNode
	method   AgreementConfirmationMethod
}

func validateAgreementDocument(schemaVersion int, blocks []agreementBlockForValidation, method AgreementConfirmationMethod, knownVariables map[string]struct{}) error {
	if schemaVersion != AgreementDocumentSchemaVersion {
		return fmt.Errorf("schema_version must be %d", AgreementDocumentSchemaVersion)
	}
	if method != AgreementConfirmation && method != AgreementSignature {
		return fmt.Errorf("unsupported confirmation method %q", method)
	}
	if len(blocks) == 0 {
		return fmt.Errorf("blocks must not be empty")
	}
	if len(blocks) > AgreementDocumentMaxBlocks {
		return fmt.Errorf("blocks exceeds the limit of %d", AgreementDocumentMaxBlocks)
	}

	acceptanceCount := 0
	visibleCharacters := 0
	for index, block := range blocks {
		path := fmt.Sprintf("blocks[%d]", index)
		switch block.typeName {
		case AgreementBlockHeading:
			if block.level < 1 || block.level > 3 {
				return fmt.Errorf("%s.level must be between 1 and 3", path)
			}
			if len(block.items) != 0 || block.method != "" {
				return fmt.Errorf("%s contains fields that do not belong to a heading", path)
			}
			count, err := validateAgreementInlineNodes(path+".content", block.content, knownVariables)
			if err != nil {
				return err
			}
			visibleCharacters += count
		case AgreementBlockParagraph:
			if block.level != 0 || len(block.items) != 0 || block.method != "" {
				return fmt.Errorf("%s contains fields that do not belong to a paragraph", path)
			}
			count, err := validateAgreementInlineNodes(path+".content", block.content, knownVariables)
			if err != nil {
				return err
			}
			visibleCharacters += count
		case AgreementBlockOrderedList, AgreementBlockUnorderedList:
			if block.level != 0 || len(block.content) != 0 || block.method != "" {
				return fmt.Errorf("%s contains fields that do not belong to a list", path)
			}
			if len(block.items) == 0 || len(block.items) > AgreementDocumentMaxListItems {
				return fmt.Errorf("%s.items must contain between 1 and %d items", path, AgreementDocumentMaxListItems)
			}
			inlineCount := 0
			for itemIndex, item := range block.items {
				count, err := validateAgreementInlineNodes(fmt.Sprintf("%s.items[%d]", path, itemIndex), item, knownVariables)
				if err != nil {
					return err
				}
				inlineCount += len(item)
				visibleCharacters += count
			}
			if inlineCount > AgreementDocumentMaxInline {
				return fmt.Errorf("%s exceeds the inline-node limit of %d", path, AgreementDocumentMaxInline)
			}
		case AgreementBlockDivider:
			if block.level != 0 || len(block.content) != 0 || len(block.items) != 0 || block.method != "" {
				return fmt.Errorf("%s contains fields that do not belong to a divider", path)
			}
		case AgreementBlockAcceptance:
			if block.level != 0 || len(block.content) != 0 || len(block.items) != 0 {
				return fmt.Errorf("%s contains fields that do not belong to acceptance", path)
			}
			if block.method != method {
				return fmt.Errorf("%s.method must match %q", path, method)
			}
			acceptanceCount++
		default:
			return fmt.Errorf("%s.type %q is unsupported", path, block.typeName)
		}
		if visibleCharacters > AgreementDocumentMaxCharacters {
			return fmt.Errorf("document exceeds the visible-character limit of %d", AgreementDocumentMaxCharacters)
		}
	}
	if acceptanceCount != 1 {
		return fmt.Errorf("document must contain exactly one acceptance block")
	}
	return nil
}

func validateAgreementInlineNodes(path string, nodes []AgreementInlineNode, knownVariables map[string]struct{}) (int, error) {
	if len(nodes) == 0 {
		return 0, fmt.Errorf("%s must not be empty", path)
	}
	if len(nodes) > AgreementDocumentMaxInline {
		return 0, fmt.Errorf("%s exceeds the inline-node limit of %d", path, AgreementDocumentMaxInline)
	}
	characters := 0
	for index, node := range nodes {
		nodePath := fmt.Sprintf("%s[%d]", path, index)
		switch node.Type {
		case AgreementInlineText:
			if node.Key != "" {
				return 0, fmt.Errorf("%s.key does not belong to a text node", nodePath)
			}
			if !utf8.ValidString(node.Text) || strings.TrimSpace(node.Text) == "" {
				return 0, fmt.Errorf("%s.text must contain valid, visible UTF-8 text", nodePath)
			}
			characters += utf8.RuneCountInString(node.Text)
		case AgreementInlineVariable:
			if node.Text != "" {
				return 0, fmt.Errorf("%s.text does not belong to a variable node", nodePath)
			}
			if !agreementVariableKeyPattern.MatchString(node.Key) {
				return 0, fmt.Errorf("%s.key is malformed", nodePath)
			}
			if _, ok := knownVariables[node.Key]; !ok {
				return 0, fmt.Errorf("%s.key %q is not registered", nodePath, node.Key)
			}
			characters += utf8.RuneCountInString(node.Key)
		default:
			return 0, fmt.Errorf("%s.type %q is unsupported", nodePath, node.Type)
		}
	}
	return characters, nil
}

func agreementBlockKeys(data []byte, withID bool) (map[string]struct{}, error) {
	var discriminator struct {
		Type AgreementBlockType `json:"type"`
	}
	if err := json.Unmarshal(data, &discriminator); err != nil {
		return nil, err
	}
	base := []string{"type"}
	if withID {
		base = append(base, "id")
	}
	switch discriminator.Type {
	case AgreementBlockHeading:
		return stringSet(append(base, "level", "content")...), nil
	case AgreementBlockParagraph:
		return stringSet(append(base, "content")...), nil
	case AgreementBlockOrderedList, AgreementBlockUnorderedList:
		return stringSet(append(base, "items")...), nil
	case AgreementBlockDivider:
		return stringSet(base...), nil
	case AgreementBlockAcceptance:
		return stringSet(append(base, "method")...), nil
	default:
		return nil, fmt.Errorf("unsupported agreement block type %q", discriminator.Type)
	}
}

func decodeStrictJSON(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func rejectUnexpectedKeys(data []byte, allowed map[string]struct{}) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	for key := range object {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown field %q", key)
		}
	}
	return nil
}

func requireKeys(data []byte, required map[string]struct{}) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	for key := range required {
		if _, ok := object[key]; !ok {
			return fmt.Errorf("required field %q is missing", key)
		}
	}
	return nil
}

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func collectAgreementVariableKeys(keys map[string]struct{}, content []AgreementInlineNode, items [][]AgreementInlineNode) {
	for _, node := range content {
		if node.Type == AgreementInlineVariable {
			keys[node.Key] = struct{}{}
		}
	}
	for _, item := range items {
		for _, node := range item {
			if node.Type == AgreementInlineVariable {
				keys[node.Key] = struct{}{}
			}
		}
	}
}

func sortedAgreementVariableKeys(keys map[string]struct{}) []string {
	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func GeneratedDocumentJSONSchema() map[string]any {
	const (
		maxGeneratedBlocks      = 20
		maxGeneratedListItems   = 12
		maxGeneratedInlineNodes = 32
	)

	inlineSchema := func() map[string]any {
		return map[string]any{
			"anyOf": []any{
				strictObjectSchema(map[string]any{
					"type": map[string]any{"type": "string", "enum": []any{"text"}},
					"text": map[string]any{"type": "string"},
					"bold": map[string]any{"type": "boolean"},
				}, "type", "text", "bold"),
				strictObjectSchema(map[string]any{
					"type": map[string]any{"type": "string", "enum": []any{"variable"}},
					"key":  map[string]any{"type": "string"},
					"bold": map[string]any{"type": "boolean"},
				}, "type", "key", "bold"),
			},
		}
	}
	contentSchema := func() map[string]any {
		return map[string]any{
			"type":     "array",
			"items":    inlineSchema(),
			"minItems": 1,
			"maxItems": maxGeneratedInlineNodes,
		}
	}
	listItemsSchema := func() map[string]any {
		return map[string]any{
			"type":     "array",
			"items":    contentSchema(),
			"minItems": 1,
			"maxItems": maxGeneratedListItems,
		}
	}
	blockSchema := map[string]any{
		"anyOf": []any{
			strictObjectSchema(map[string]any{
				"type":    map[string]any{"type": "string", "enum": []any{"heading"}},
				"level":   map[string]any{"type": "integer", "enum": []any{1, 2, 3}},
				"content": contentSchema(),
			}, "type", "level", "content"),
			strictObjectSchema(map[string]any{
				"type":    map[string]any{"type": "string", "enum": []any{"paragraph"}},
				"content": contentSchema(),
			}, "type", "content"),
			strictObjectSchema(map[string]any{
				"type":  map[string]any{"type": "string", "enum": []any{"ordered_list"}},
				"items": listItemsSchema(),
			}, "type", "items"),
			strictObjectSchema(map[string]any{
				"type":  map[string]any{"type": "string", "enum": []any{"unordered_list"}},
				"items": listItemsSchema(),
			}, "type", "items"),
		},
	}
	return strictObjectSchema(map[string]any{
		"schema_version": map[string]any{"type": "integer", "enum": []any{AgreementDocumentSchemaVersion}},
		"blocks": map[string]any{
			"type":     "array",
			"items":    blockSchema,
			"minItems": 2,
			"maxItems": maxGeneratedBlocks,
		},
	}, "schema_version", "blocks")
}

func strictObjectSchema(properties map[string]any, required ...string) map[string]any {
	items := make([]any, len(required))
	for index, value := range required {
		items[index] = value
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             items,
		"additionalProperties": false,
	}
}
