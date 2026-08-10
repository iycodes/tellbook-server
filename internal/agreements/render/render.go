package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
	"unicode/utf8"

	"booking/go-server/internal/agreements/domain"
	aiapi "booking/go-server/shared/ai_api"
)

const RendererVersion = 1

type BookingSummary struct {
	ServiceName string `json:"service_name,omitempty"`
	Date        string `json:"date,omitempty"`
	Time        string `json:"time,omitempty"`
	Location    string `json:"location,omitempty"`
	TotalAmount string `json:"total_amount,omitempty"`
}

type Snapshot struct {
	ResolvedDocument  aiapi.DocumentSchema
	RenderedHTML      string
	ResolvedTermsHash string
	SchemaVersion     int
	RendererVersion   int
}

type MissingVariablesError struct {
	Keys []string
}

func (e *MissingVariablesError) Error() string {
	return "missing agreement variables: " + strings.Join(e.Keys, ", ")
}

type semanticAcceptancePayload struct {
	Title              string                    `json:"title"`
	BookingSummary     BookingSummary            `json:"booking_summary"`
	ResolvedDocument   semanticResolvedDocument  `json:"resolved_document"`
	ConfirmationMethod domain.ConfirmationMethod `json:"confirmation_method"`
}

type semanticResolvedDocument struct {
	Blocks []aiapi.GeneratedAgreementDocumentBlock `json:"blocks"`
}

func BuildSnapshot(
	title string,
	bookingSummary BookingSummary,
	document aiapi.DocumentSchema,
	method domain.ConfirmationMethod,
	values map[string]string,
) (Snapshot, error) {
	title = normalizeResolvedText(title)
	bookingSummary = normalizeBookingSummary(bookingSummary)
	if err := validateSnapshotMetadata(title, bookingSummary); err != nil {
		return Snapshot{}, err
	}

	resolved, err := ResolveDocument(document, method, values)
	if err != nil {
		return Snapshot{}, err
	}
	renderedHTML, err := RenderSnapshotHTML(title, bookingSummary, resolved, method)
	if err != nil {
		return Snapshot{}, err
	}
	hash, err := ResolvedTermsHash(title, bookingSummary, resolved, method)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		ResolvedDocument:  resolved,
		RenderedHTML:      renderedHTML,
		ResolvedTermsHash: hash,
		SchemaVersion:     aiapi.AgreementDocumentSchemaVersion,
		RendererVersion:   RendererVersion,
	}, nil
}

func ResolveDocument(
	document aiapi.DocumentSchema,
	method domain.ConfirmationMethod,
	values map[string]string,
) (aiapi.DocumentSchema, error) {
	knownVariables := domain.AgreementVariableKeySet()
	if err := domain.ValidateDocument(document, method, knownVariables); err != nil {
		return aiapi.DocumentSchema{}, fmt.Errorf("validate agreement document: %w", err)
	}

	missing := make(map[string]struct{})
	resolved := aiapi.DocumentSchema{
		SchemaVersion: document.SchemaVersion,
		Blocks:        make([]aiapi.AgreementDocumentBlock, len(document.Blocks)),
	}
	for blockIndex, block := range document.Blocks {
		resolvedBlock := aiapi.AgreementDocumentBlock{
			ID:     block.ID,
			Type:   block.Type,
			Level:  block.Level,
			Method: block.Method,
		}
		resolvedBlock.Content = resolveInlineNodes(block.Content, values, missing)
		if len(block.Items) > 0 {
			resolvedBlock.Items = make([][]aiapi.AgreementInlineNode, len(block.Items))
			for itemIndex, item := range block.Items {
				resolvedBlock.Items[itemIndex] = resolveInlineNodes(item, values, missing)
			}
		}
		resolved.Blocks[blockIndex] = resolvedBlock
	}
	if len(missing) > 0 {
		keys := make([]string, 0, len(missing))
		for key := range missing {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return aiapi.DocumentSchema{}, &MissingVariablesError{Keys: keys}
	}
	if err := domain.ValidateDocument(resolved, method, map[string]struct{}{}); err != nil {
		return aiapi.DocumentSchema{}, fmt.Errorf("validate resolved agreement document: %w", err)
	}
	return resolved, nil
}

func RenderHTML(document aiapi.DocumentSchema, method domain.ConfirmationMethod) (string, error) {
	return RenderHTMLVersion(RendererVersion, document, method)
}

func RenderSnapshotHTML(
	title string,
	bookingSummary BookingSummary,
	document aiapi.DocumentSchema,
	method domain.ConfirmationMethod,
) (string, error) {
	title = normalizeResolvedText(title)
	bookingSummary = normalizeBookingSummary(bookingSummary)
	if err := validateSnapshotMetadata(title, bookingSummary); err != nil {
		return "", err
	}
	body, err := RenderHTML(document, method)
	if err != nil {
		return "", err
	}

	var output strings.Builder
	output.WriteString(`<section class="tellbook-agreement-document">`)
	output.WriteString(`<header class="tellbook-agreement-header"><h1>`)
	output.WriteString(html.EscapeString(title))
	output.WriteString(`</h1>`)
	renderBookingSummaryHTML(&output, bookingSummary)
	output.WriteString(`</header>`)
	output.WriteString(body)
	output.WriteString(`</section>`)
	return output.String(), nil
}

func RenderHTMLVersion(version int, document aiapi.DocumentSchema, method domain.ConfirmationMethod) (string, error) {
	if version != RendererVersion {
		return "", fmt.Errorf("unsupported agreement renderer version %d", version)
	}
	if err := domain.ValidateDocument(document, method, map[string]struct{}{}); err != nil {
		return "", fmt.Errorf("validate resolved agreement document: %w", err)
	}

	var output strings.Builder
	output.WriteString(`<article class="tellbook-agreement">`)
	for _, block := range document.Blocks {
		switch block.Type {
		case aiapi.AgreementBlockHeading:
			fmt.Fprintf(&output, "<h%d>", block.Level)
			renderInlineHTML(&output, block.Content)
			fmt.Fprintf(&output, "</h%d>", block.Level)
		case aiapi.AgreementBlockParagraph:
			output.WriteString("<p>")
			renderInlineHTML(&output, block.Content)
			output.WriteString("</p>")
		case aiapi.AgreementBlockOrderedList, aiapi.AgreementBlockUnorderedList:
			tag := "ul"
			if block.Type == aiapi.AgreementBlockOrderedList {
				tag = "ol"
			}
			output.WriteByte('<')
			output.WriteString(tag)
			output.WriteByte('>')
			for _, item := range block.Items {
				output.WriteString("<li>")
				renderInlineHTML(&output, item)
				output.WriteString("</li>")
			}
			output.WriteString("</")
			output.WriteString(tag)
			output.WriteByte('>')
		case aiapi.AgreementBlockDivider:
			output.WriteString("<hr>")
		case aiapi.AgreementBlockAcceptance:
			output.WriteString(`<section class="tellbook-agreement-acceptance" data-method="`)
			output.WriteString(html.EscapeString(string(block.Method)))
			output.WriteString(`"></section>`)
		}
	}
	output.WriteString("</article>")
	return output.String(), nil
}

func ResolvedTermsHash(
	title string,
	bookingSummary BookingSummary,
	document aiapi.DocumentSchema,
	method domain.ConfirmationMethod,
) (string, error) {
	if err := domain.ValidateDocument(document, method, map[string]struct{}{}); err != nil {
		return "", fmt.Errorf("validate resolved agreement document: %w", err)
	}
	title = normalizeResolvedText(title)
	bookingSummary = normalizeBookingSummary(bookingSummary)
	if err := validateSnapshotMetadata(title, bookingSummary); err != nil {
		return "", err
	}
	blocks := make([]aiapi.GeneratedAgreementDocumentBlock, len(document.Blocks))
	for index, block := range document.Blocks {
		blocks[index] = aiapi.GeneratedAgreementDocumentBlock{
			Type:    block.Type,
			Level:   block.Level,
			Content: normalizeInlineNodes(block.Content),
			Items:   normalizeInlineItems(block.Items),
			Method:  block.Method,
		}
	}
	payload := semanticAcceptancePayload{
		Title:              title,
		BookingSummary:     bookingSummary,
		ResolvedDocument:   semanticResolvedDocument{Blocks: blocks},
		ConfirmationMethod: method,
	}
	canonicalJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal canonical agreement payload: %w", err)
	}
	digest := sha256.Sum256(canonicalJSON)
	return hex.EncodeToString(digest[:]), nil
}

func ArtifactSHA256(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func resolveInlineNodes(nodes []aiapi.AgreementInlineNode, values map[string]string, missing map[string]struct{}) []aiapi.AgreementInlineNode {
	if len(nodes) == 0 {
		return nil
	}
	result := make([]aiapi.AgreementInlineNode, len(nodes))
	for index, node := range nodes {
		if node.Type == aiapi.AgreementInlineVariable {
			value := normalizeResolvedText(values[node.Key])
			if value == "" || !utf8.ValidString(value) {
				missing[node.Key] = struct{}{}
			}
			result[index] = aiapi.AgreementInlineNode{
				Type: aiapi.AgreementInlineText,
				Text: value,
				Bold: node.Bold,
			}
			continue
		}
		node.Text = normalizeLineEndings(node.Text)
		result[index] = node
	}
	return result
}

func renderInlineHTML(output *strings.Builder, nodes []aiapi.AgreementInlineNode) {
	for _, node := range nodes {
		text := html.EscapeString(node.Text)
		if node.Bold {
			output.WriteString("<strong>")
			output.WriteString(text)
			output.WriteString("</strong>")
			continue
		}
		output.WriteString(text)
	}
}

func renderBookingSummaryHTML(output *strings.Builder, summary BookingSummary) {
	items := []struct {
		label string
		value string
	}{
		{label: "Service", value: summary.ServiceName},
		{label: "Date", value: summary.Date},
		{label: "Time", value: summary.Time},
		{label: "Location", value: summary.Location},
		{label: "Total", value: summary.TotalAmount},
	}
	visible := false
	for _, item := range items {
		if item.value != "" {
			visible = true
			break
		}
	}
	if !visible {
		return
	}
	output.WriteString(`<dl class="tellbook-agreement-summary">`)
	for _, item := range items {
		if item.value == "" {
			continue
		}
		output.WriteString("<div><dt>")
		output.WriteString(item.label)
		output.WriteString("</dt><dd>")
		output.WriteString(html.EscapeString(item.value))
		output.WriteString("</dd></div>")
	}
	output.WriteString("</dl>")
}

func normalizeInlineNodes(nodes []aiapi.AgreementInlineNode) []aiapi.AgreementInlineNode {
	if len(nodes) == 0 {
		return nil
	}
	result := make([]aiapi.AgreementInlineNode, len(nodes))
	for index, node := range nodes {
		node.Text = normalizeLineEndings(node.Text)
		result[index] = node
	}
	return result
}

func normalizeInlineItems(items [][]aiapi.AgreementInlineNode) [][]aiapi.AgreementInlineNode {
	if len(items) == 0 {
		return nil
	}
	result := make([][]aiapi.AgreementInlineNode, len(items))
	for index, item := range items {
		result[index] = normalizeInlineNodes(item)
	}
	return result
}

func normalizeBookingSummary(summary BookingSummary) BookingSummary {
	summary.ServiceName = normalizeResolvedText(summary.ServiceName)
	summary.Date = normalizeResolvedText(summary.Date)
	summary.Time = normalizeResolvedText(summary.Time)
	summary.Location = normalizeResolvedText(summary.Location)
	summary.TotalAmount = normalizeResolvedText(summary.TotalAmount)
	return summary
}

func validateSnapshotMetadata(title string, summary BookingSummary) error {
	if title == "" {
		return fmt.Errorf("agreement title is required")
	}
	values := []string{title, summary.ServiceName, summary.Date, summary.Time, summary.Location, summary.TotalAmount}
	for _, value := range values {
		if !utf8.ValidString(value) {
			return fmt.Errorf("agreement snapshot metadata must be valid UTF-8")
		}
	}
	return nil
}

func normalizeResolvedText(value string) string {
	return strings.TrimSpace(normalizeLineEndings(value))
}

func normalizeLineEndings(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}
