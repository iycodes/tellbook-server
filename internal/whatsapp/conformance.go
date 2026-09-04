package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var templatePlaceholderPattern = regexp.MustCompile(`\{\{([1-9][0-9]*)\}\}`)

type TemplateConformanceReport struct {
	Checked []string `json:"checked"`
	Holds   []string `json:"holds,omitempty"`
	Errors  []string `json:"errors,omitempty"`
}

func (report TemplateConformanceReport) Valid() bool { return len(report.Errors) == 0 }

type graphTemplate struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Category   string `json:"category"`
	Language   string `json:"language"`
	Components []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Buttons []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			URL  string `json:"url"`
		} `json:"buttons"`
	} `json:"components"`
}

func (client *Client) ConformTemplates(ctx context.Context) (TemplateConformanceReport, error) {
	templates, err := client.fetchTemplates(ctx)
	if err != nil {
		return TemplateConformanceReport{}, err
	}
	return ConformTemplateInventory(RegisteredTemplates(), templates), nil
}

func (client *Client) fetchTemplates(ctx context.Context) ([]graphTemplate, error) {
	const maxPages = 3
	templates := make([]graphTemplate, 0, 32)
	after := ""
	for page := 0; page < maxPages; page++ {
		query := url.Values{
			"fields": {"name,status,category,language,components"},
			"limit":  {"100"},
		}
		if after != "" {
			query.Set("after", after)
		}
		endpoint := client.baseURL + "/" + client.graphVersion + "/" + client.businessAccountID + "/message_templates?" + query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("create WhatsApp template inventory request: %w", err)
		}
		request.Header.Set("Authorization", "Bearer "+client.accessToken)
		response, err := client.httpClient.Do(request)
		if err != nil {
			return nil, fmt.Errorf("fetch WhatsApp template inventory: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxGraphResponseBytes+1))
		_ = response.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read WhatsApp template inventory: %w", readErr)
		}
		if len(body) > maxGraphResponseBytes {
			return nil, errors.New("WhatsApp template inventory response exceeds limit")
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, decodeGraphError(response.StatusCode, response.Header, body)
		}
		var envelope struct {
			Data   []graphTemplate `json:"data"`
			Paging struct {
				Cursors struct {
					After string `json:"after"`
				} `json:"cursors"`
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, fmt.Errorf("decode WhatsApp template inventory: %w", err)
		}
		templates = append(templates, envelope.Data...)
		if envelope.Paging.Next == "" {
			return templates, nil
		}
		after = strings.TrimSpace(envelope.Paging.Cursors.After)
		if after == "" {
			return nil, errors.New("WhatsApp template inventory pagination has no cursor")
		}
	}
	return nil, errors.New("WhatsApp template inventory exceeded the three-page safety limit")
}

func ConformTemplateInventory(definitions []TemplateDefinition, templates []graphTemplate) TemplateConformanceReport {
	report := TemplateConformanceReport{}
	for _, definition := range definitions {
		identity := definition.Name + ":" + definition.Language
		report.Checked = append(report.Checked, identity)
		if definition.RequiresContractHold {
			report.Holds = append(report.Holds, string(definition.Key))
		}
		matches := make([]graphTemplate, 0, 1)
		for _, template := range templates {
			if template.Name == definition.Name && template.Language == definition.Language {
				matches = append(matches, template)
			}
		}
		if len(matches) != 1 {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: expected one template, found %d", identity, len(matches)))
			continue
		}
		template := matches[0]
		if template.Status != "APPROVED" {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: status is %s, expected APPROVED", identity, template.Status))
		}
		if template.Category != definition.Category {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: category is %s, expected %s", identity, template.Category, definition.Category))
		}
		validateTemplateComponents(&report, identity, definition, template)
	}
	sort.Strings(report.Checked)
	sort.Strings(report.Holds)
	sort.Strings(report.Errors)
	return report
}

func validateTemplateComponents(report *TemplateConformanceReport, identity string, definition TemplateDefinition, template graphTemplate) {
	var bodyCount, footerCount, buttonsCount int
	for _, component := range template.Components {
		switch component.Type {
		case "BODY":
			bodyCount++
			if component.Text != definition.BodyText ||
				countSequentialPlaceholders(component.Text) != len(definition.BodyParameters) {
				report.Errors = append(report.Errors, fmt.Sprintf("%s: body parameter contract changed", identity))
			}
		case "FOOTER":
			footerCount++
			if component.Text != definition.FooterText {
				report.Errors = append(report.Errors, fmt.Sprintf("%s: footer contract changed", identity))
			}
		case "BUTTONS":
			buttonsCount++
			if len(component.Buttons) != 1 || component.Buttons[0].Type != "URL" ||
				component.Buttons[0].Text != definition.ButtonText || component.Buttons[0].URL != definition.ButtonURL ||
				countSequentialPlaceholders(component.Buttons[0].URL) != len(definition.ButtonParameters) {
				report.Errors = append(report.Errors, fmt.Sprintf("%s: URL button contract changed", identity))
			}
		default:
			report.Errors = append(report.Errors, fmt.Sprintf("%s: unexpected component %s", identity, component.Type))
		}
	}
	expectedFooterCount := 0
	if definition.FooterText != "" {
		expectedFooterCount = 1
	}
	if bodyCount != 1 || footerCount != expectedFooterCount || buttonsCount != 1 ||
		len(template.Components) != 2+expectedFooterCount {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: component contract changed", identity))
	}
}

func countSequentialPlaceholders(value string) int {
	matches := templatePlaceholderPattern.FindAllStringSubmatch(value, -1)
	seen := make(map[int]struct{}, len(matches))
	for _, match := range matches {
		index, err := strconv.Atoi(match[1])
		if err != nil {
			return -1
		}
		seen[index] = struct{}{}
	}
	if len(seen) != len(matches) {
		return -1
	}
	for index := 1; index <= len(seen); index++ {
		if _, ok := seen[index]; !ok {
			return -1
		}
	}
	return len(seen)
}

func ValidateEnabledTemplateKeys(rawKeys []string) error {
	seen := make(map[TemplateKey]struct{}, len(rawKeys))
	for _, rawKey := range rawKeys {
		key := TemplateKey(strings.TrimSpace(rawKey))
		definition, ok := LookupTemplate(key)
		if !ok {
			return fmt.Errorf("unknown enabled WhatsApp template key %q", rawKey)
		}
		if definition.RequiresContractHold {
			return fmt.Errorf("WhatsApp template %q is blocked by an unresolved contract hold", key)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate enabled WhatsApp template key %q", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}
