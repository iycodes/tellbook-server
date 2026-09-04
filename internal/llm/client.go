package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"booking/go-server/internal/aierror"
	"booking/go-server/internal/config"
	"booking/go-server/internal/observability"
)

type Client struct {
	baseURL           string
	path              string
	model             string
	apiKey            string
	temperature       float64
	topP              float64
	topK              int
	minP              float64
	presencePenalty   float64
	repetitionPenalty float64
	enableThinking    bool
	maxOutputTokens   int
	httpClient        *http.Client
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionsRequest struct {
	Model              string    `json:"model"`
	Messages           []Message `json:"messages"`
	Temperature        float64   `json:"temperature,omitempty"`
	TopP               float64   `json:"top_p,omitempty"`
	TopK               int       `json:"top_k,omitempty"`
	MinP               float64   `json:"min_p,omitempty"`
	PresencePenalty    float64   `json:"presence_penalty,omitempty"`
	RepetitionPenalty  float64   `json:"repeat_penalty,omitempty"`
	EnableThinking     *bool     `json:"enable_thinking,omitempty"`
	ChatTemplateKwargs struct {
		EnableThinking bool `json:"enable_thinking"`
	} `json:"chat_template_kwargs"`
	MaxTokens      int                `json:"max_tokens,omitempty"`
	ResponseFormat chatResponseFormat `json:"response_format"`
	JSONSchema     map[string]any     `json:"json_schema,omitempty"`
	Stream         bool               `json:"stream"`
}

type chatResponseFormat struct {
	Type       string                  `json:"type"`
	JSONSchema *chatJSONSchemaEnvelope `json:"json_schema,omitempty"`
}

type chatJSONSchemaEnvelope struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

type chatCompletionsResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func NewClient(cfg config.Config, operationalMetrics ...*observability.Metrics) *Client {
	httpClient := &http.Client{Timeout: cfg.LLMTimeout}
	if len(operationalMetrics) > 0 && operationalMetrics[0] != nil {
		httpClient = operationalMetrics[0].InstrumentHTTPClient(httpClient, "ai_self_hosted")
	}
	return &Client{
		baseURL:           cfg.LLMBaseURL,
		path:              cfg.LLMChatCompletions,
		model:             cfg.LLMModel,
		apiKey:            cfg.LLMAPIKey,
		temperature:       cfg.LLMTemperature,
		topP:              cfg.LLMTopP,
		topK:              cfg.LLMTopK,
		minP:              cfg.LLMMinP,
		presencePenalty:   cfg.LLMPresencePenalty,
		repetitionPenalty: cfg.LLMRepetitionPenalty,
		enableThinking:    cfg.SelfHostedThinking,
		maxOutputTokens:   cfg.LLMMaxOutputTokens,
		httpClient:        httpClient,
	}
}

func (c *Client) GenerateJSON(ctx context.Context, systemPrompt, userPrompt string, dst any) error {
	payload := c.newChatCompletionsRequest(systemPrompt, userPrompt)
	payload.ResponseFormat.Type = "json_object"

	content, err := c.complete(ctx, payload)
	if err != nil {
		return err
	}
	jsonPayload, err := extractJSONObject(content)
	if err != nil {
		return aierror.InvalidOutput("read self-hosted model output", err)
	}
	if err := json.Unmarshal([]byte(jsonPayload), dst); err != nil {
		return aierror.InvalidOutput("decode self-hosted model output", err)
	}

	return nil
}

// GenerateJSONSchema returns the raw JSON object produced under an
// application-managed schema. Keeping the raw bytes lets the application run
// its own strict semantic validation without retaining any model reasoning.
func (c *Client) GenerateJSONSchema(
	ctx context.Context,
	systemPrompt string,
	userPrompt string,
	schemaName string,
	schemaJSON json.RawMessage,
) (json.RawMessage, error) {
	var schema map[string]any
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return nil, aierror.Terminal("prepare self-hosted model schema", aierror.KindConfiguration, err)
	}
	payload := c.newChatCompletionsRequest(systemPrompt, userPrompt)
	// Strict application protocols never request or retain reasoning, even if
	// another self-hosted generation task has thinking enabled globally.
	payload.EnableThinking = boolPtr(false)
	payload.ChatTemplateKwargs.EnableThinking = false
	payload.ResponseFormat = chatResponseFormat{
		Type: "json_schema",
		JSONSchema: &chatJSONSchemaEnvelope{
			Name:   schemaName,
			Strict: true,
			Schema: schema,
		},
	}
	// llama-server builds support either the OpenAI-compatible nested schema or
	// the native top-level json_schema field. Send both identical forms so the
	// configured local runtime actually applies the grammar.
	payload.JSONSchema = schema
	content, err := c.complete(ctx, payload)
	if err != nil {
		return nil, err
	}
	jsonPayload, err := extractJSONObject(content)
	if err != nil {
		return nil, aierror.InvalidOutput("read self-hosted model output", err)
	}
	return json.RawMessage(jsonPayload), nil
}

func (c *Client) newChatCompletionsRequest(systemPrompt, userPrompt string) chatCompletionsRequest {
	payload := chatCompletionsRequest{
		Model: c.model,
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature:       c.temperature,
		TopP:              c.topP,
		TopK:              c.topK,
		MinP:              c.minP,
		PresencePenalty:   c.presencePenalty,
		RepetitionPenalty: c.repetitionPenalty,
		EnableThinking:    boolPtr(c.enableThinking),
		MaxTokens:         c.maxOutputTokens,
		Stream:            false,
	}
	payload.ChatTemplateKwargs.EnableThinking = c.enableThinking
	return payload
}

func (c *Client) complete(ctx context.Context, payload chatCompletionsRequest) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", aierror.Terminal("encode self-hosted model request", aierror.KindConfiguration, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+c.path, bytes.NewReader(body))
	if err != nil {
		return "", aierror.Terminal("build self-hosted model request", aierror.KindConfiguration, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		var networkErr net.Error
		if errors.As(err, &networkErr) && networkErr.Timeout() {
			return "", aierror.Transient("call self-hosted model", aierror.KindTimeout, err)
		}
		return "", aierror.Transient("call self-hosted model", aierror.KindTransport, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", aierror.Transient("read self-hosted model response", aierror.KindTransport, err)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return "", classifyProviderHTTPStatus("call self-hosted model", resp.StatusCode)
	}

	var decoded chatCompletionsResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return "", aierror.Terminal("decode self-hosted model response", aierror.KindMalformedResponse, err)
	}
	if decoded.Error != nil && strings.TrimSpace(decoded.Error.Message) != "" {
		return "", aierror.Terminal("call self-hosted model", aierror.KindMalformedResponse, nil)
	}
	if len(decoded.Choices) == 0 {
		return "", aierror.Terminal("decode self-hosted model response", aierror.KindMalformedResponse, nil)
	}

	content := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if content == "" {
		return "", aierror.InvalidOutput("read self-hosted model output", errors.New("empty assistant content"))
	}
	return content, nil
}

func boolPtr(value bool) *bool {
	return &value
}

func extractJSONObject(content string) (string, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("llm response content was empty")
	}

	if strings.HasPrefix(content, "```") {
		if stripped := stripCodeFence(content); stripped != "" {
			content = stripped
		}
	}

	start := strings.IndexAny(content, "[{")
	if start == -1 {
		return "", fmt.Errorf("llm response did not contain json")
	}

	segment := content[start:]
	if candidate, ok := balancedJSONPrefix(segment); ok {
		return candidate, nil
	}

	return "", fmt.Errorf("llm response did not contain a complete json payload")
}

func stripCodeFence(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) < 3 {
		return ""
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
		return ""
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
		return ""
	}

	return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
}

func balancedJSONPrefix(content string) (string, bool) {
	var stack []rune
	inString := false
	escaped := false

	for idx, r := range content {
		if escaped {
			escaped = false
			continue
		}

		if inString {
			switch r {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch r {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, r)
		case '}':
			if len(stack) == 0 || stack[len(stack)-1] != '{' {
				return "", false
			}
			stack = stack[:len(stack)-1]
		case ']':
			if len(stack) == 0 || stack[len(stack)-1] != '[' {
				return "", false
			}
			stack = stack[:len(stack)-1]
		}

		if len(stack) == 0 {
			return strings.TrimSpace(content[:idx+1]), true
		}
	}

	return "", false
}
