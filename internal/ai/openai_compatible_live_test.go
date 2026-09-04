package ai

import (
	"context"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/config"
	"booking/go-server/internal/llm"
	aiapi "booking/go-server/shared/ai_api"
)

// TestExternalOpenAICompatibleConformance is intentionally opt-in because it
// performs real inference against the configured external endpoint.
func TestExternalOpenAICompatibleConformance(t *testing.T) {
	if os.Getenv("RUN_OPENAI_COMPAT_EVALS") != "true" {
		t.Skip("RUN_OPENAI_COMPAT_EVALS is not enabled")
	}
	cfg := liveOpenAICompatibleConfig(t)
	service := NewService(llm.NewOpenAICompatibleClient(cfg))
	run := func(t *testing.T, call func(context.Context) error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), cfg.OpenAICompatTimeout+5*time.Second)
		defer cancel()
		if err := call(ctx); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("inbox draft", func(t *testing.T) {
		run(t, func(ctx context.Context) error {
			_, err := service.GenerateInboxReplyDraft(ctx, aiapi.InboxReplyDraftRequest{
				BusinessName: "Tellbook Beauty", CustomerName: "Ada", GeneratedAt: time.Now().UTC(),
				Messages: []aiapi.MessageTurn{{Role: "customer", Content: "Do you offer knotless braids?"}},
			})
			return err
		})
	})
	t.Run("service description", func(t *testing.T) {
		run(t, func(ctx context.Context) error {
			_, err := service.GenerateServiceDescription(ctx, aiapi.GenerateServiceDescriptionRequest{
				BusinessName: "Tellbook Beauty", ServiceTitle: "Knotless braids", Category: "Hair",
			})
			return err
		})
	})
	t.Run("preparation and aftercare", func(t *testing.T) {
		run(t, func(ctx context.Context) error {
			_, err := service.GeneratePrepAftercareInstructions(ctx, aiapi.GeneratePrepAftercareInstructionsRequest{
				BusinessName: "Tellbook Beauty", ServiceTitle: "Knotless braids", Category: "Hair",
				IncludePrep: true, IncludeAftercare: true,
			})
			return err
		})
	})
	t.Run("section description", func(t *testing.T) {
		run(t, func(ctx context.Context) error {
			_, err := service.GenerateSectionDescription(ctx, aiapi.GenerateSectionDescriptionRequest{
				BusinessName: "Tellbook Beauty", SectionTitle: "Protective styles",
				ServiceTitles: []string{"Knotless braids", "Cornrows"},
			})
			return err
		})
	})
	t.Run("public page content", func(t *testing.T) {
		run(t, func(ctx context.Context) error {
			_, err := service.GeneratePublicPageContent(ctx, aiapi.GeneratePublicPageContentRequest{
				BusinessName: "Tellbook Beauty", BusinessType: "Hair salon", Location: "Lagos",
				ServiceTitles: []string{"Knotless braids"}, FieldsToImprove: []string{"headline", "bio"},
			})
			return err
		})
	})
	t.Run("semi-pilot", func(t *testing.T) {
		run(t, func(ctx context.Context) error {
			_, err := service.GenerateSemiPilotTurnDecision(ctx, validSemiPilotTurnInput())
			return err
		})
	})
	t.Run("autopilot", func(t *testing.T) {
		run(t, func(ctx context.Context) error {
			_, err := service.GenerateAutopilotTurnDecision(ctx, validSemiPilotTurnInput())
			return err
		})
	})
	t.Run("agreement", func(t *testing.T) {
		run(t, func(ctx context.Context) error {
			_, err := service.GenerateAgreementDocument(ctx, agreementDocumentRequest())
			return err
		})
	})
}

func liveOpenAICompatibleConfig(t *testing.T) config.Config {
	t.Helper()
	required := func(key string) string {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			t.Fatalf("%s is required for live conformance", key)
		}
		return value
	}
	parseInt := func(key string, fallback int) int {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			return fallback
		}
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			t.Fatalf("%s must be a positive integer", key)
		}
		return parsed
	}
	parseFloat := func(key string) *float64 {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			return nil
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			t.Fatalf("%s must be a finite number", key)
		}
		return &parsed
	}
	timeout := 60 * time.Second
	if value := strings.TrimSpace(os.Getenv("OPENAI_COMPAT_TIMEOUT")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			t.Fatal("OPENAI_COMPAT_TIMEOUT must be a positive duration")
		}
		timeout = parsed
	}
	path := strings.TrimSpace(os.Getenv("OPENAI_COMPAT_CHAT_COMPLETIONS_PATH"))
	if path == "" {
		path = "/v1/chat/completions"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	tokenField := strings.TrimSpace(os.Getenv("OPENAI_COMPAT_TOKEN_LIMIT_FIELD"))
	if tokenField == "" {
		tokenField = config.OpenAICompatTokenFieldMaxTokens
	}
	temperature := parseFloat("OPENAI_COMPAT_TEMPERATURE")
	topP := parseFloat("OPENAI_COMPAT_TOP_P")
	if temperature != nil && (*temperature < 0 || *temperature > 2) {
		t.Fatal("OPENAI_COMPAT_TEMPERATURE must be between 0 and 2")
	}
	if topP != nil && (*topP <= 0 || *topP > 1) {
		t.Fatal("OPENAI_COMPAT_TOP_P must be greater than 0 and at most 1")
	}
	if temperature != nil && topP != nil {
		t.Fatal("configure at most one external sampling control")
	}
	return config.Config{
		OpenAICompatBaseURL:         strings.TrimRight(required("OPENAI_COMPAT_BASE_URL"), "/"),
		OpenAICompatChatCompletions: path,
		OpenAICompatModel:           required("OPENAI_COMPAT_MODEL"),
		OpenAICompatAPIKey:          required("OPENAI_COMPAT_API_KEY"),
		OpenAICompatTimeout:         timeout,
		OpenAICompatMaxOutputTokens: parseInt("OPENAI_COMPAT_MAX_OUTPUT_TOKENS", 1600),
		OpenAICompatTokenLimitField: tokenField,
		OpenAICompatTemperature:     temperature,
		OpenAICompatTopP:            topP,
	}
}
