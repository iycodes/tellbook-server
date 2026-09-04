package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"booking/go-server/internal/aierror"
	"booking/go-server/internal/config"
	"booking/go-server/internal/observability"
)

const openAICompatibleMaxResponseBytes = 1 << 20

type OpenAICompatibleClient struct {
	baseURL         string
	path            string
	model           string
	apiKey          string
	maxOutputTokens int
	tokenLimitField string
	temperature     *float64
	topP            *float64
	httpClient      *http.Client
}

type openAICompatibleRequest struct {
	Model               string                   `json:"model"`
	Messages            []Message                `json:"messages"`
	Temperature         *float64                 `json:"temperature,omitempty"`
	TopP                *float64                 `json:"top_p,omitempty"`
	MaxTokens           *int                     `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                     `json:"max_completion_tokens,omitempty"`
	ResponseFormat      compatibleResponseFormat `json:"response_format"`
	Stream              bool                     `json:"stream"`
}

type compatibleResponseFormat struct {
	Type       string                       `json:"type"`
	JSONSchema compatibleJSONSchemaEnvelope `json:"json_schema"`
}

type compatibleJSONSchemaEnvelope struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type openAICompatibleResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		FinishReason *string `json:"finish_reason"`
		Message      struct {
			Role    string `json:"role"`
			Content string `json:"content"`
			Refusal string `json:"refusal"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage"`
}

func NewOpenAICompatibleClient(cfg config.Config, operationalMetrics ...*observability.Metrics) *OpenAICompatibleClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = max(10, cfg.InboxAIMaxConcurrency)
	httpClient := &http.Client{
		Timeout:   cfg.OpenAICompatTimeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if len(operationalMetrics) > 0 && operationalMetrics[0] != nil {
		httpClient = operationalMetrics[0].InstrumentHTTPClient(httpClient, "ai_openai_compatible")
	}
	return &OpenAICompatibleClient{
		baseURL:         cfg.OpenAICompatBaseURL,
		path:            cfg.OpenAICompatChatCompletions,
		model:           cfg.OpenAICompatModel,
		apiKey:          cfg.OpenAICompatAPIKey,
		maxOutputTokens: cfg.OpenAICompatMaxOutputTokens,
		tokenLimitField: cfg.OpenAICompatTokenLimitField,
		temperature:     cfg.OpenAICompatTemperature,
		topP:            cfg.OpenAICompatTopP,
		httpClient:      httpClient,
	}
}

func (c *OpenAICompatibleClient) GenerateJSON(
	ctx context.Context,
	systemPrompt string,
	userPrompt string,
	dst any,
) error {
	schemaName, schema, err := responseSchema(dst)
	if err != nil {
		return aierror.Terminal("prepare external model request", aierror.KindConfiguration, err)
	}
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return aierror.Terminal("prepare external model request", aierror.KindConfiguration, err)
	}
	payload, err := c.generateJSONSchema(ctx, systemPrompt, userPrompt, schemaName, schemaJSON)
	if err != nil {
		return err
	}
	if err := strictDecodeStructuredOutput(payload, dst); err != nil {
		return aierror.InvalidOutput("decode external model output", err)
	}
	return nil
}

func (c *OpenAICompatibleClient) GenerateJSONSchema(
	ctx context.Context,
	systemPrompt string,
	userPrompt string,
	schemaName string,
	schemaJSON json.RawMessage,
) (json.RawMessage, error) {
	return c.generateJSONSchema(ctx, systemPrompt, userPrompt, schemaName, schemaJSON)
}

func (c *OpenAICompatibleClient) generateJSONSchema(
	ctx context.Context,
	systemPrompt string,
	userPrompt string,
	schemaName string,
	schemaJSON json.RawMessage,
) (json.RawMessage, error) {
	if _, err := compiledStructuredSchema(schemaJSON); err != nil {
		return nil, aierror.Terminal("prepare external model schema", aierror.KindConfiguration, err)
	}
	request := openAICompatibleRequest{
		Model: c.model,
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: c.temperature,
		TopP:        c.topP,
		ResponseFormat: compatibleResponseFormat{
			Type: "json_schema",
			JSONSchema: compatibleJSONSchemaEnvelope{
				Name: schemaName, Strict: true, Schema: schemaJSON,
			},
		},
		Stream: false,
	}
	switch c.tokenLimitField {
	case config.OpenAICompatTokenFieldMaxTokens:
		request.MaxTokens = &c.maxOutputTokens
	case config.OpenAICompatTokenFieldMaxCompletionTokens:
		request.MaxCompletionTokens = &c.maxOutputTokens
	default:
		return nil, aierror.Terminal(
			"prepare external model request", aierror.KindConfiguration,
			fmt.Errorf("unsupported token limit field"),
		)
	}

	startedAt := time.Now()
	response, err := c.complete(ctx, request)
	if err != nil {
		logOpenAICompatibleOutcome(c.model, time.Since(startedAt), "failed", response, err)
		return nil, err
	}
	content := strings.TrimSpace(response.Choices[0].Message.Content)
	if content == "" {
		err := aierror.InvalidOutput("read external model output", errors.New("empty assistant content"))
		logOpenAICompatibleOutcome(c.model, time.Since(startedAt), "invalid_output", response, err)
		return nil, err
	}
	raw := json.RawMessage(content)
	if !json.Valid(raw) {
		err := aierror.InvalidOutput("read external model output", errors.New("malformed JSON"))
		logOpenAICompatibleOutcome(c.model, time.Since(startedAt), "invalid_output", response, err)
		return nil, err
	}
	if err := validateStructuredJSON(schemaJSON, raw); err != nil {
		providerErr := aierror.InvalidOutput("validate external model output", err)
		logOpenAICompatibleOutcome(c.model, time.Since(startedAt), "invalid_output", response, providerErr)
		return nil, providerErr
	}
	logOpenAICompatibleOutcome(c.model, time.Since(startedAt), "succeeded", response, nil)
	return raw, nil
}

func (c *OpenAICompatibleClient) complete(
	ctx context.Context,
	payload openAICompatibleRequest,
) (*openAICompatibleResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, aierror.Terminal("encode external model request", aierror.KindConfiguration, err)
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.baseURL+c.path, bytes.NewReader(body),
	)
	if err != nil {
		return nil, aierror.Terminal("build external model request", aierror.KindConfiguration, err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		var networkErr net.Error
		if errors.As(err, &networkErr) && networkErr.Timeout() {
			return nil, aierror.Transient("call external model", aierror.KindTimeout, err)
		}
		return nil, aierror.Transient("call external model", aierror.KindTransport, err)
	}
	defer response.Body.Close()

	limited := io.LimitReader(response.Body, openAICompatibleMaxResponseBytes+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, aierror.Transient("read external model response", aierror.KindTransport, err)
	}
	if len(responseBody) > openAICompatibleMaxResponseBytes {
		return nil, aierror.Terminal(
			"read external model response", aierror.KindMalformedResponse,
			fmt.Errorf("response exceeded %d bytes", openAICompatibleMaxResponseBytes),
		)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, classifyOpenAICompatibleStatus(response.StatusCode)
	}

	var decoded openAICompatibleResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return nil, aierror.Terminal("decode external model response", aierror.KindMalformedResponse, err)
	}
	if len(decoded.Choices) == 0 {
		return &decoded, aierror.Terminal(
			"decode external model response", aierror.KindMalformedResponse,
			errors.New("response contained no choices"),
		)
	}
	choice := decoded.Choices[0]
	if choice.Message.Role != "assistant" {
		return &decoded, aierror.Terminal(
			"decode external model response", aierror.KindMalformedResponse,
			errors.New("first choice was not an assistant message"),
		)
	}
	if strings.TrimSpace(choice.Message.Refusal) != "" {
		return &decoded, aierror.Terminal("call external model", aierror.KindRefusal, nil)
	}
	if choice.FinishReason == nil {
		return &decoded, aierror.Terminal(
			"decode external model response", aierror.KindMalformedResponse,
			errors.New("finish_reason was missing"),
		)
	}
	switch strings.TrimSpace(*choice.FinishReason) {
	case "stop":
		return &decoded, nil
	case "length":
		return &decoded, aierror.Terminal("call external model", aierror.KindOutputTruncated, nil)
	case "content_filter":
		return &decoded, aierror.Terminal("call external model", aierror.KindContentFiltered, nil)
	default:
		return &decoded, aierror.Terminal(
			"call external model", aierror.KindMalformedResponse,
			fmt.Errorf("unsupported finish reason"),
		)
	}
}

func classifyOpenAICompatibleStatus(status int) error {
	return classifyProviderHTTPStatus("call external model", status)
}

func classifyProviderHTTPStatus(operation string, status int) error {
	switch {
	case status == http.StatusUnauthorized:
		return aierror.Terminal(operation, aierror.KindAuthentication, nil)
	case status == http.StatusForbidden:
		return aierror.Terminal(operation, aierror.KindPermission, nil)
	case status == http.StatusTooManyRequests:
		return aierror.Transient(operation, aierror.KindRateLimited, nil)
	case status == http.StatusRequestTimeout:
		return aierror.Transient(operation, aierror.KindTimeout, nil)
	case status == http.StatusInternalServerError,
		status == http.StatusBadGateway,
		status == http.StatusServiceUnavailable,
		status == http.StatusGatewayTimeout:
		return aierror.Transient(operation, aierror.KindUnavailable, nil)
	default:
		return aierror.Terminal(operation, aierror.KindConfiguration, nil)
	}
}

func strictDecodeStructuredOutput(payload json.RawMessage, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func logOpenAICompatibleOutcome(
	model string,
	duration time.Duration,
	outcome string,
	response *openAICompatibleResponse,
	err error,
) {
	attributes := []any{
		"adapter", config.AIProviderOpenAICompatible,
		"model", model,
		"duration", duration,
		"outcome", outcome,
	}
	if response != nil {
		attributes = append(attributes,
			"input_tokens", response.Usage.PromptTokens,
			"output_tokens", response.Usage.CompletionTokens,
			"total_tokens", response.Usage.TotalTokens,
		)
	}
	if aierror.IsRetryable(err) {
		attributes = append(attributes, "retryable", true)
	}
	if err != nil {
		slog.Warn("external OpenAI-compatible request completed", attributes...)
		return
	}
	slog.Info("external OpenAI-compatible request completed", attributes...)
}
