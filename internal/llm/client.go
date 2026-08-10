package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"booking/go-server/internal/config"
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
	Model             string    `json:"model"`
	Messages          []Message `json:"messages"`
	Temperature       float64   `json:"temperature,omitempty"`
	TopP              float64   `json:"top_p,omitempty"`
	TopK              int       `json:"top_k,omitempty"`
	MinP              float64   `json:"min_p,omitempty"`
	PresencePenalty   float64   `json:"presence_penalty,omitempty"`
	RepetitionPenalty float64   `json:"repeat_penalty,omitempty"`
	EnableThinking    *bool     `json:"enable_thinking,omitempty"`
	MaxTokens         int       `json:"max_tokens,omitempty"`
	ResponseFormat    struct {
		Type string `json:"type"`
	} `json:"response_format"`
	Stream bool `json:"stream"`
}

type chatCompletionsResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func NewClient(cfg config.Config) *Client {
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
		httpClient: &http.Client{
			Timeout: cfg.LLMTimeout,
		},
	}
}

func (c *Client) GenerateJSON(ctx context.Context, systemPrompt, userPrompt string, dst any) error {
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
	payload.ResponseFormat.Type = "json_object"

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal llm request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+c.path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build llm request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call llm server: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read llm response: %w", err)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("llm server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var decoded chatCompletionsResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return fmt.Errorf("decode llm response: %w", err)
	}
	if decoded.Error != nil && strings.TrimSpace(decoded.Error.Message) != "" {
		return fmt.Errorf("llm server error: %s", decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return fmt.Errorf("llm response contained no choices")
	}

	content := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if content == "" {
		return fmt.Errorf("llm response content was empty")
	}

	jsonPayload, err := extractJSONObject(content)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(jsonPayload), dst); err != nil {
		return fmt.Errorf("decode llm json payload: %w", err)
	}

	return nil
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
