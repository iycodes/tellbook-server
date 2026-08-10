package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"booking/go-server/internal/config"
	aiapi "booking/go-server/shared/ai_api"

	"github.com/invopop/jsonschema"
	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
)

var schemaNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
var openAIResponseLogMu sync.Mutex

type OpenAIClient struct {
	client          openai.Client
	model           string
	reasoningEffort string
	maxOutputTokens int64
	responseLogFile string
}

func NewOpenAIClient(cfg config.Config) *OpenAIClient {
	httpClient := &http.Client{Timeout: cfg.OpenAITimeout}
	client := openai.NewClient(
		option.WithBaseURL(cfg.OpenAIBaseURL),
		option.WithAPIKey(cfg.OpenAIAPIKey),
		option.WithHTTPClient(httpClient),
		option.WithMaxRetries(0),
	)

	return &OpenAIClient{
		client:          client,
		model:           cfg.OpenAIModel,
		reasoningEffort: cfg.OpenAIReasoningEffort,
		maxOutputTokens: cfg.OpenAIMaxOutputTokens,
		responseLogFile: cfg.OpenAIResponseLogFile,
	}
}

func (c *OpenAIClient) GenerateJSON(ctx context.Context, systemPrompt, userPrompt string, dst any) error {
	startedAt := time.Now()
	schemaName, schema, err := responseSchema(dst)
	if err != nil {
		return err
	}

	format := responses.ResponseFormatTextConfigParamOfJSONSchema(schemaName, schema)
	format.OfJSONSchema.Strict = openai.Bool(true)
	result, err := c.client.Responses.New(ctx, responses.ResponseNewParams{
		Model:        shared.ResponsesModel(c.model),
		Instructions: openai.String(systemPrompt),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String(userPrompt),
		},
		Reasoning: shared.ReasoningParam{
			Effort: shared.ReasoningEffort(c.reasoningEffort),
		},
		Text:            responses.ResponseTextConfigParam{Format: format},
		Store:           openai.Bool(false),
		MaxOutputTokens: openai.Int(c.maxOutputTokens),
	})
	if err != nil {
		logOpenAIError(err, c.model, time.Since(startedAt))
		return safeOpenAIError(err)
	}
	if err := appendOpenAIResponseLog(c.responseLogFile, c.model, result); err != nil {
		slog.Warn("write openai response output log", "error", err)
	}
	if result.Status != responses.ResponseStatusCompleted {
		return openAIResponseStatusError(result, c.model, time.Since(startedAt))
	}

	content := strings.TrimSpace(result.OutputText())
	if content == "" {
		reason := "empty_output"
		if openAIResponseRefused(result) {
			reason = "refused"
		}
		slog.Error(
			"openai response invalid",
			"provider", config.HostedProviderOpenAI,
			"model", c.model,
			"duration", time.Since(startedAt),
			"reason", reason,
			"input_tokens", result.Usage.InputTokens,
			"output_tokens", result.Usage.OutputTokens,
			"reasoning_tokens", result.Usage.OutputTokensDetails.ReasoningTokens,
		)
		if reason == "refused" {
			return fmt.Errorf("call openai: response was refused")
		}
		return fmt.Errorf("call openai: response contained no output")
	}
	if err := json.Unmarshal([]byte(content), dst); err != nil {
		slog.Error(
			"openai structured output invalid",
			"provider", config.HostedProviderOpenAI,
			"model", c.model,
			"duration", time.Since(startedAt),
			"reason", "json_decode_failed",
			"decode_error", err.Error(),
			"content_bytes", len(content),
			"input_tokens", result.Usage.InputTokens,
			"output_tokens", result.Usage.OutputTokens,
			"reasoning_tokens", result.Usage.OutputTokensDetails.ReasoningTokens,
		)
		return fmt.Errorf("decode openai structured output: provider returned incomplete JSON")
	}

	slog.Info(
		"openai request succeeded",
		"provider", config.HostedProviderOpenAI,
		"model", c.model,
		"duration", time.Since(startedAt),
		"input_tokens", result.Usage.InputTokens,
		"output_tokens", result.Usage.OutputTokens,
		"total_tokens", result.Usage.TotalTokens,
	)
	return nil
}

type openAIResponseLogEntry struct {
	Timestamp        time.Time                `json:"timestamp"`
	Model            string                   `json:"model"`
	ResponseID       string                   `json:"response_id"`
	Status           responses.ResponseStatus `json:"status"`
	IncompleteReason string                   `json:"incomplete_reason,omitempty"`
	InputTokens      int64                    `json:"input_tokens"`
	OutputTokens     int64                    `json:"output_tokens"`
	ReasoningTokens  int64                    `json:"reasoning_tokens"`
	Output           string                   `json:"output"`
}

func appendOpenAIResponseLog(path, model string, result *responses.Response) error {
	if strings.TrimSpace(path) == "" || result == nil {
		return nil
	}
	entry := openAIResponseLogEntry{
		Timestamp:        time.Now().UTC(),
		Model:            model,
		ResponseID:       result.ID,
		Status:           result.Status,
		IncompleteReason: result.IncompleteDetails.Reason,
		InputTokens:      result.Usage.InputTokens,
		OutputTokens:     result.Usage.OutputTokens,
		ReasoningTokens:  result.Usage.OutputTokensDetails.ReasoningTokens,
		Output:           result.OutputText(),
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode response output log: %w", err)
	}
	payload = append(payload, '\n')

	openAIResponseLogMu.Lock()
	defer openAIResponseLogMu.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open response output log: %w", err)
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("secure response output log: %w", err)
	}
	if _, err := file.Write(payload); err != nil {
		return fmt.Errorf("append response output log: %w", err)
	}
	return nil
}

func openAIResponseStatusError(result *responses.Response, model string, duration time.Duration) error {
	reason := strings.TrimSpace(result.IncompleteDetails.Reason)
	slog.Error(
		"openai response not completed",
		"provider", config.HostedProviderOpenAI,
		"model", model,
		"duration", duration,
		"status", result.Status,
		"reason", reason,
		"error_code", result.Error.Code,
		"input_tokens", result.Usage.InputTokens,
		"output_tokens", result.Usage.OutputTokens,
		"reasoning_tokens", result.Usage.OutputTokensDetails.ReasoningTokens,
	)

	if result.Status == responses.ResponseStatusIncomplete {
		switch reason {
		case "max_output_tokens":
			return fmt.Errorf("call openai: response exceeded output token limit")
		case "content_filter":
			return fmt.Errorf("call openai: response was stopped by content filtering")
		}
	}
	return fmt.Errorf("call openai: response status was %s", result.Status)
}

func openAIResponseRefused(result *responses.Response) bool {
	for _, item := range result.Output {
		for _, content := range item.Content {
			if content.Type == "refusal" {
				return true
			}
		}
	}
	return false
}

func responseSchema(dst any) (string, map[string]any, error) {
	if dst == nil {
		return "", nil, fmt.Errorf("generate openai response schema: destination is nil")
	}
	if _, ok := dst.(*aiapi.GenerateAgreementDocumentResponse); ok {
		return "GenerateAgreementDocumentResponse", aiapi.GenerateAgreementDocumentResponseJSONSchema(), nil
	}

	typeOf := reflect.TypeOf(dst)
	for typeOf.Kind() == reflect.Pointer {
		typeOf = typeOf.Elem()
	}
	if typeOf.Kind() != reflect.Struct {
		return "", nil, fmt.Errorf("generate openai response schema: destination must point to a struct")
	}

	reflector := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		DoNotReference:            true,
	}
	reflected := reflector.ReflectFromType(typeOf)
	rawSchema, err := json.Marshal(reflected)
	if err != nil {
		return "", nil, fmt.Errorf("marshal openai response schema: %w", err)
	}

	var schema map[string]any
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		return "", nil, fmt.Errorf("decode openai response schema: %w", err)
	}
	strictifySchema(schema)

	name := schemaNameSanitizer.ReplaceAllString(typeOf.Name(), "_")
	if name == "" {
		name = "tellbook_response"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name, schema, nil
}

func strictifySchema(value any) {
	switch node := value.(type) {
	case map[string]any:
		if properties, ok := node["properties"].(map[string]any); ok {
			required := make([]string, 0, len(properties))
			for key, property := range properties {
				required = append(required, key)
				strictifySchema(property)
			}
			sort.Strings(required)
			node["required"] = required
			node["additionalProperties"] = false
		}
		for key, child := range node {
			if key != "properties" {
				strictifySchema(child)
			}
		}
	case []any:
		for _, child := range node {
			strictifySchema(child)
		}
	}
}

func logOpenAIError(err error, model string, duration time.Duration) {
	attributes := []any{
		"provider", config.HostedProviderOpenAI,
		"model", model,
		"duration", duration,
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		attributes = append(attributes,
			"status_code", apiErr.StatusCode,
			"error_type", apiErr.Type,
			"error_code", apiErr.Code,
			"error_param", apiErr.Param,
		)
	} else {
		attributes = append(attributes, "error_type", "transport")
	}
	slog.Error("openai request failed", attributes...)
}

func safeOpenAIError(err error) error {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		return fmt.Errorf("call openai: provider returned status %d", apiErr.StatusCode)
	}
	return fmt.Errorf("call openai: provider request failed")
}
