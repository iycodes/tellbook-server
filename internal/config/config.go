package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"booking/go-server/internal/secure"
	"booking/go-server/internal/tessaconfig"
	"github.com/google/uuid"
)

type Config struct {
	AppEnv                                string
	ProcessRole                           string
	HTTPAddr                              string
	SSEMaxConnections                     int
	SSEMaxConnectionsPerIP                int
	PaymentSSEMaxConnectionsPerToken      int
	ClientPublicBaseURL                   string
	MarketplacePublicBaseURL              string
	DefaultAIProvider                     string
	AgreementAIProvider                   string
	InboxAIDraftsEnabled                  bool
	InboxAIProviderAllowlist              []string
	InboxAIAutomationEnabled              bool
	InboxAIAutomationProviderAllowlist    []string
	InboxAIMaxConcurrency                 int
	InboxAISemiPilotReplyDelay            time.Duration
	InboxAIAutopilotPaymentWindow         time.Duration
	TessaAIEnabled                        bool
	TessaWhatsAppLinkingEnabled           bool
	TessaWhatsAppConversationsEnabled     bool
	TessaAIProviderAllowlist              []string
	TessaAIPrimaryProvider                string
	TessaAIFallbackProvider               string
	TessaAIPrimaryRequestTimeout          time.Duration
	TessaAIFallbackRequestTimeout         time.Duration
	TessaAITurnTimeout                    time.Duration
	TessaAIWorkerConcurrency              int
	TessaAIMaxInputTokens                 int
	TessaAIMaxOutputTokens                int
	TessaAINoticeRevision                 string
	TessaAIExternalProcessingApproved     bool
	HostedAIProvider                      string
	LLMBaseURL                            string
	LLMChatCompletions                    string
	LLMModel                              string
	LLMAPIKey                             string
	LLMTimeout                            time.Duration
	LLMMaxOutputTokens                    int
	LLMTemperature                        float64
	LLMTopP                               float64
	LLMTopK                               int
	LLMMinP                               float64
	LLMPresencePenalty                    float64
	LLMRepetitionPenalty                  float64
	SelfHostedThinking                    bool
	OpenAIBaseURL                         string
	OpenAIModel                           string
	OpenAIAPIKey                          string
	OpenAIReasoningEffort                 string
	OpenAITimeout                         time.Duration
	OpenAIMaxOutputTokens                 int64
	OpenAIResponseLogFile                 string
	OpenAICompatBaseURL                   string
	OpenAICompatChatCompletions           string
	OpenAICompatModel                     string
	OpenAICompatAPIKey                    string
	OpenAICompatTimeout                   time.Duration
	OpenAICompatMaxOutputTokens           int
	OpenAICompatTokenLimitField           string
	OpenAICompatTemperature               *float64
	OpenAICompatTopP                      *float64
	HTTPRateLimitPerMinute                int
	HTTPRateLimitBurst                    int
	AIRateLimitPerMinute                  int
	AIRateLimitBurst                      int
	LocationRateLimitPerMinute            int
	LocationRateLimitBurst                int
	MarketplaceAuthRateLimitPerMinute     int
	MarketplaceAuthRateLimitBurst         int
	RedisURL                              string
	RedisKeyPrefix                        string
	RedisKeyHMACSecret                    string
	RedisPoolSize                         int
	RedisMinIdleConnections               int
	RedisDialTimeout                      time.Duration
	RedisReadTimeout                      time.Duration
	RedisWriteTimeout                     time.Duration
	RedisPoolTimeout                      time.Duration
	RedisMaxPayloadBytes                  int
	RedisFallbackMaxConcurrency           int
	RateLimitIPCeilingMultiplier          int
	MetricsAuthToken                      string
	HTTPSuccessLogSampleRate              float64
	HTTPSlowRequestThreshold              time.Duration
	GoogleMapsServerAPIKey                string
	DatabaseURL                           string
	DatabaseDirectURL                     string
	DatabaseMaxConnections                int32
	DatabaseMinConnections                int32
	DatabaseDirectMaxConnections          int32
	DatabaseMaxConnectionLifetime         time.Duration
	DatabaseMaxConnectionLifetimeJitter   time.Duration
	DatabaseMaxConnectionIdleTime         time.Duration
	DatabaseHealthCheckPeriod             time.Duration
	DatabaseConnectTimeout                time.Duration
	DatabaseStatementTimeout              time.Duration
	DatabaseLockTimeout                   time.Duration
	DatabaseIdleTransactionTimeout        time.Duration
	CORSOrigins                           []string
	TrustedProxyCIDRs                     []string
	AuthIssuer                            string
	AuthAccessTokenSecret                 string
	AuthAccessTokenTTL                    time.Duration
	AuthAccessCookieName                  string
	AuthRefreshCookieName                 string
	AuthCookieDomain                      string
	AuthCookieSecure                      bool
	AuthRefreshTokenTTL                   time.Duration
	AuthBcryptCost                        int
	AuthEmailEnabled                      bool
	AuthWhatsAppEnabled                   bool
	AuthDeliveryEncryptionKeys            string
	AuthDeliveryActiveKey                 string
	AuthDestinationHMACKey                string
	AuthDeliveryConcurrency               int
	AuthDeliveryTimeout                   time.Duration
	R2PrivateBucketName                   string
	R2PublicBucketName                    string
	R2AccountID                           string
	R2Endpoint                            string
	R2AccessKeyID                         string
	R2SecretAccessKey                     string
	R2PublicBucketBaseURL                 string
	NotificationEmailEnabled              bool
	NotificationEmailConcurrency          int
	NotificationEmailTimeout              time.Duration
	WelcomeEmailEnabled                   bool
	WelcomeEmailConcurrency               int
	WelcomeEmailTimeout                   time.Duration
	NotificationWhatsAppEnabled           bool
	WhatsAppWorkerConcurrency             int
	NotificationPlannerConcurrency        int
	NotificationDestinationHMACKey        string
	MetaAppID                             string
	MetaAppSecret                         string
	MetaVerifyToken                       string
	WABAToken                             string
	WhatsAppBusinessAccountID             string
	WABAPhoneNumberID                     string
	WABABusinessPhoneE164                 string
	WhatsAppGraphBaseURL                  string
	WhatsAppGraphVersion                  string
	WhatsAppHTTPTimeout                   time.Duration
	WhatsAppEnabledTemplateKeys           []string
	SMTPHost                              string
	SMTPPort                              int
	SMTPUsername                          string
	SMTPPassword                          string
	SMTPFromEmail                         string
	SMTPFromName                          string
	SMTPSecurity                          string
	SMTPInsecureSkipVerify                bool
	SMTPConnectTimeout                    time.Duration
	PaystackSecretKey                     string
	PaystackSecretKeyTest                 string
	PaystackBaseURL                       string
	PayazaPublicKey                       string
	PayazaSecretKey                       string
	PayazaPublicKeyTest                   string
	PayazaSecretKeyTest                   string
	PayazaBaseURL                         string
	PayazaTransferPIN                     string
	PayazaTransferPINTest                 string
	PayazaSourceAccounts                  string
	PayazaSourceAccountsTest              string
	PayazaPayoutSenderName                string
	PayazaPayoutSenderPhone               string
	PayazaPayoutSenderAddress             string
	PayazaNGNDVABankCode                  string
	PayazaNGNDVAEnquiryBankCode           string
	PayazaCardSandboxVerified             bool
	PayazaCardProductionEnabled           bool
	PayazaBankTransferSandboxVerified     bool
	PayazaBankTransferProductionEnabled   bool
	PayazaDestinationSandboxVerified      bool
	PayazaDestinationProductionEnabled    bool
	PayazaPayoutSandboxVerified           bool
	PayazaPayoutProductionEnabled         bool
	PaystackCardSandboxVerified           bool
	PaystackCardProductionEnabled         bool
	PaystackBankTransferSandboxVerified   bool
	PaystackBankTransferProductionEnabled bool
	PaystackDestinationSandboxVerified    bool
	PaystackDestinationProductionEnabled  bool
	PaystackPayoutSandboxVerified         bool
	PaystackPayoutProductionEnabled       bool
	PaystackPayoutOTPDisabled             bool
	PaymentsEnvironment                   string
	FinancialEncryptionKeys               string
	FinancialActiveKey                    string
	FinancialFingerprintKey               string
	AgreementTokenEncryptionKeys          string
	AgreementTokenActiveKey               string
	ShutdownTimeout                       time.Duration
	ReadTimeout                           time.Duration
	ReadHeaderTimeout                     time.Duration
	WriteTimeout                          time.Duration
	IdleTimeout                           time.Duration
}

const (
	ProcessRoleAPI         = "api"
	ProcessRoleWorker      = "worker"
	ProcessRoleAIWorker    = "ai-worker"
	ProcessRoleMaintenance = "maintenance"
	ProcessRoleAll         = "all"

	AIProviderSelfHosted       = "self_hosted"
	AIProviderHosted           = "hosted"
	AIProviderOpenAICompatible = "openai_compatible"
	HostedProviderOpenAI       = "openai"

	OpenAICompatTokenFieldMaxTokens           = "max_tokens"
	OpenAICompatTokenFieldMaxCompletionTokens = "max_completion_tokens"
)

// requiredDirectDatabaseConnections is the exact number of session-bound
// connections the process can hold concurrently. These connections are kept
// out of the ordinary query pool so it can safely use transaction pooling.
func requiredDirectDatabaseConnections(processRole string, tessaEnabled bool) int {
	switch processRole {
	case ProcessRoleAPI:
		connections := 3 // payment, inbox, and booking event listeners
		if tessaEnabled {
			connections++
		}
		return connections
	case ProcessRoleWorker, ProcessRoleAIWorker, ProcessRoleMaintenance:
		return 1 // one wake listener or the maintenance leader connection
	case ProcessRoleAll:
		connections := 6 // API listeners + core/AI wakes + maintenance leader
		if tessaEnabled {
			connections++
		}
		return connections
	default:
		return 1
	}
}

// InboxAIModelConfigHash fingerprints behavior-affecting model settings without
// including credentials or endpoint secrets.
func (cfg Config) InboxAIModelConfigHash() string {
	settings := struct {
		Provider            string        `json:"provider"`
		Model               string        `json:"model"`
		MaxOutputTokens     int64         `json:"max_output_tokens"`
		Temperature         float64       `json:"temperature,omitempty"`
		TopP                float64       `json:"top_p,omitempty"`
		TopK                int           `json:"top_k,omitempty"`
		MinP                float64       `json:"min_p,omitempty"`
		PresencePenalty     float64       `json:"presence_penalty,omitempty"`
		RepetitionPenalty   float64       `json:"repetition_penalty,omitempty"`
		Thinking            bool          `json:"thinking,omitempty"`
		ReasoningEffort     string        `json:"reasoning_effort,omitempty"`
		Timeout             time.Duration `json:"timeout,omitempty"`
		BaseURL             string        `json:"base_url,omitempty"`
		Path                string        `json:"path,omitempty"`
		TokenLimitField     string        `json:"token_limit_field,omitempty"`
		OptionalTemperature *float64      `json:"optional_temperature,omitempty"`
		OptionalTopP        *float64      `json:"optional_top_p,omitempty"`
	}{Provider: cfg.DefaultAIProvider}
	switch cfg.DefaultAIProvider {
	case AIProviderHosted:
		settings.Model = cfg.OpenAIModel
		settings.MaxOutputTokens = cfg.OpenAIMaxOutputTokens
		settings.ReasoningEffort = cfg.OpenAIReasoningEffort
		settings.Timeout = cfg.OpenAITimeout
	case AIProviderOpenAICompatible:
		settings.Model = cfg.OpenAICompatModel
		settings.MaxOutputTokens = int64(cfg.OpenAICompatMaxOutputTokens)
		settings.Timeout = cfg.OpenAICompatTimeout
		settings.BaseURL = cfg.OpenAICompatBaseURL
		settings.Path = cfg.OpenAICompatChatCompletions
		settings.TokenLimitField = cfg.OpenAICompatTokenLimitField
		settings.OptionalTemperature = cfg.OpenAICompatTemperature
		settings.OptionalTopP = cfg.OpenAICompatTopP
	default:
		settings.Model = cfg.LLMModel
		settings.MaxOutputTokens = int64(cfg.LLMMaxOutputTokens)
		settings.Timeout = cfg.LLMTimeout
		settings.Temperature = cfg.LLMTemperature
		settings.TopP = cfg.LLMTopP
		settings.TopK = cfg.LLMTopK
		settings.MinP = cfg.LLMMinP
		settings.PresencePenalty = cfg.LLMPresencePenalty
		settings.RepetitionPenalty = cfg.LLMRepetitionPenalty
		settings.Thinking = cfg.SelfHostedThinking
	}
	payload, _ := json.Marshal(settings)
	hash := sha256.Sum256(payload)
	return fmt.Sprintf("%x", hash[:])
}

func Load() (Config, error) {
	defaultAIProvider := normalizeAIProvider(getEnv("DEFAULT_AI_PROVIDER", AIProviderSelfHosted))
	agreementAIProvider := normalizeAIProvider(getEnv("AGREEMENT_AI_PROVIDER", AIProviderHosted))
	usesOpenAICompatible := defaultAIProvider == AIProviderOpenAICompatible ||
		agreementAIProvider == AIProviderOpenAICompatible
	openAICompatTimeout, compatTimeoutErr := getEnvDurationStrict("OPENAI_COMPAT_TIMEOUT", 60*time.Second)
	if compatTimeoutErr != nil && usesOpenAICompatible {
		return Config{}, compatTimeoutErr
	}
	if compatTimeoutErr != nil {
		openAICompatTimeout = 60 * time.Second
	}
	openAICompatMaxOutputTokens, compatTokensErr := getEnvIntStrict("OPENAI_COMPAT_MAX_OUTPUT_TOKENS", 1600)
	if compatTokensErr != nil && usesOpenAICompatible {
		return Config{}, compatTokensErr
	}
	if compatTokensErr != nil {
		openAICompatMaxOutputTokens = 1600
	}
	openAICompatTemperature, compatTemperatureErr := getOptionalEnvFloat("OPENAI_COMPAT_TEMPERATURE")
	if compatTemperatureErr != nil && usesOpenAICompatible {
		return Config{}, compatTemperatureErr
	}
	if compatTemperatureErr != nil {
		openAICompatTemperature = nil
	}
	openAICompatTopP, compatTopPErr := getOptionalEnvFloat("OPENAI_COMPAT_TOP_P")
	if compatTopPErr != nil && usesOpenAICompatible {
		return Config{}, compatTopPErr
	}
	if compatTopPErr != nil {
		openAICompatTopP = nil
	}
	appEnv := strings.ToLower(strings.TrimSpace(getEnv("APP_ENV", "development")))
	processRole := strings.ToLower(strings.TrimSpace(getEnv("PROCESS_ROLE", ProcessRoleAll)))
	tessaAIEnabled := getEnvBool("TESSA_AI_ENABLED", false)
	cfg := Config{
		AppEnv:                                appEnv,
		ProcessRole:                           processRole,
		HTTPAddr:                              getEnv("HTTP_ADDR", ":8200"),
		SSEMaxConnections:                     getEnvInt("SSE_MAX_CONNECTIONS", 10000),
		SSEMaxConnectionsPerIP:                getEnvInt("SSE_MAX_CONNECTIONS_PER_IP", 40),
		PaymentSSEMaxConnectionsPerToken:      getEnvInt("PAYMENT_SSE_MAX_CONNECTIONS_PER_TOKEN", 6),
		ClientPublicBaseURL:                   strings.TrimRight(getEnv("CLIENT_PUBLIC_BASE_URL", "http://localhost:5275"), "/"),
		MarketplacePublicBaseURL:              strings.TrimRight(getEnv("MARKETPLACE_PUBLIC_BASE_URL", "http://localhost:5375"), "/"),
		DefaultAIProvider:                     defaultAIProvider,
		AgreementAIProvider:                   agreementAIProvider,
		InboxAIDraftsEnabled:                  getEnvBool("INBOX_AI_DRAFTS_ENABLED", false),
		InboxAIProviderAllowlist:              splitCSV(os.Getenv("INBOX_AI_PROVIDER_ALLOWLIST")),
		InboxAIAutomationEnabled:              getEnvBool("INBOX_AI_AUTOMATION_ENABLED", false),
		InboxAIAutomationProviderAllowlist:    splitCSV(os.Getenv("INBOX_AI_AUTOMATION_PROVIDER_ALLOWLIST")),
		InboxAIMaxConcurrency:                 getEnvInt("INBOX_AI_MAX_CONCURRENCY", 2),
		InboxAISemiPilotReplyDelay:            getEnvDuration("INBOX_AI_SEMI_PILOT_REPLY_DELAY", 800*time.Millisecond),
		InboxAIAutopilotPaymentWindow:         getEnvDuration("INBOX_AI_AUTOPILOT_PAYMENT_WINDOW", 30*time.Minute),
		TessaAIEnabled:                        tessaAIEnabled,
		TessaWhatsAppLinkingEnabled:           getEnvBool("TESSA_WHATSAPP_LINKING_ENABLED", false),
		TessaWhatsAppConversationsEnabled:     getEnvBool("TESSA_WHATSAPP_CONVERSATIONS_ENABLED", false),
		TessaAIProviderAllowlist:              splitCSV(os.Getenv("TESSA_AI_PROVIDER_ALLOWLIST")),
		TessaAIPrimaryProvider:                normalizeAIProvider(getEnv("TESSA_AI_PRIMARY_PROVIDER", AIProviderSelfHosted)),
		TessaAIFallbackProvider:               normalizeAIProvider(os.Getenv("TESSA_AI_FALLBACK_PROVIDER")),
		TessaAIPrimaryRequestTimeout:          getEnvDuration("TESSA_AI_PRIMARY_REQUEST_TIMEOUT", 30*time.Second),
		TessaAIFallbackRequestTimeout:         getEnvDuration("TESSA_AI_FALLBACK_REQUEST_TIMEOUT", 20*time.Second),
		TessaAITurnTimeout:                    getEnvDuration("TESSA_AI_TURN_TIMEOUT", 75*time.Second),
		TessaAIWorkerConcurrency:              getEnvInt("TESSA_AI_WORKER_CONCURRENCY", 2),
		TessaAIMaxInputTokens:                 getEnvInt("TESSA_AI_MAX_INPUT_TOKENS", 12000),
		TessaAIMaxOutputTokens:                getEnvInt("TESSA_AI_MAX_OUTPUT_TOKENS", 1600),
		TessaAINoticeRevision:                 strings.TrimSpace(os.Getenv("TESSA_AI_NOTICE_REVISION")),
		TessaAIExternalProcessingApproved:     getEnvBool("TESSA_AI_EXTERNAL_PROCESSING_APPROVED", false),
		HostedAIProvider:                      strings.ToLower(strings.TrimSpace(getEnv("HOSTED_AI_PROVIDER", HostedProviderOpenAI))),
		LLMBaseURL:                            strings.TrimRight(strings.TrimSpace(os.Getenv("LLM_BASE_URL")), "/"),
		LLMChatCompletions:                    getEnv("LLM_CHAT_COMPLETIONS_PATH", "/v1/chat/completions"),
		LLMModel:                              getEnv("LLM_MODEL", "local-model"),
		LLMAPIKey:                             strings.TrimSpace(os.Getenv("LLM_API_KEY")),
		LLMTimeout:                            getEnvDuration("LLM_TIMEOUT", 30*time.Second),
		LLMMaxOutputTokens:                    getEnvInt("LLM_MAX_OUTPUT_TOKENS", 1200),
		LLMTemperature:                        getEnvFloat("LLM_TEMPERATURE", 0.2),
		LLMTopP:                               getEnvFloat("TOP_P", 0.9),
		LLMTopK:                               getEnvInt("TOP_K", 40),
		LLMMinP:                               getEnvFloat("MIN_P", 0.1),
		LLMPresencePenalty:                    getEnvFloat("PRESENCE_PENALTY", 0),
		LLMRepetitionPenalty:                  getEnvFloat("REPETITION_PENALTY", 1),
		SelfHostedThinking:                    getEnvBool("SELF_HOSTED_THINKING", false),
		OpenAIBaseURL:                         strings.TrimRight(strings.TrimSpace(getEnv("OPENAI_BASE_URL", "https://api.openai.com/v1")), "/"),
		OpenAIModel:                           strings.TrimSpace(getEnv("OPENAI_MODEL", "gpt-5.6-luna")),
		OpenAIAPIKey:                          strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		OpenAIReasoningEffort:                 strings.ToLower(strings.TrimSpace(getEnv("OPENAI_REASONING_EFFORT", "none"))),
		OpenAITimeout:                         getEnvDuration("OPENAI_TIMEOUT", 120*time.Second),
		OpenAIMaxOutputTokens:                 getEnvInt64("OPENAI_MAX_OUTPUT_TOKENS", 16000),
		OpenAIResponseLogFile:                 strings.TrimSpace(os.Getenv("OPENAI_RESPONSE_LOG_FILE")),
		OpenAICompatBaseURL:                   strings.TrimRight(strings.TrimSpace(os.Getenv("OPENAI_COMPAT_BASE_URL")), "/"),
		OpenAICompatChatCompletions:           strings.TrimSpace(getEnv("OPENAI_COMPAT_CHAT_COMPLETIONS_PATH", "/v1/chat/completions")),
		OpenAICompatModel:                     strings.TrimSpace(os.Getenv("OPENAI_COMPAT_MODEL")),
		OpenAICompatAPIKey:                    strings.TrimSpace(os.Getenv("OPENAI_COMPAT_API_KEY")),
		OpenAICompatTimeout:                   openAICompatTimeout,
		OpenAICompatMaxOutputTokens:           openAICompatMaxOutputTokens,
		OpenAICompatTokenLimitField:           strings.ToLower(strings.TrimSpace(getEnv("OPENAI_COMPAT_TOKEN_LIMIT_FIELD", OpenAICompatTokenFieldMaxTokens))),
		OpenAICompatTemperature:               openAICompatTemperature,
		OpenAICompatTopP:                      openAICompatTopP,
		HTTPRateLimitPerMinute:                getEnvInt("HTTP_RATE_LIMIT_PER_MINUTE", 300),
		HTTPRateLimitBurst:                    getEnvInt("HTTP_RATE_LIMIT_BURST", 100),
		AIRateLimitPerMinute:                  getEnvInt("AI_RATE_LIMIT_PER_MINUTE", 12),
		AIRateLimitBurst:                      getEnvInt("AI_RATE_LIMIT_BURST", 4),
		LocationRateLimitPerMinute:            getEnvInt("LOCATION_RATE_LIMIT_PER_MINUTE", 20),
		LocationRateLimitBurst:                getEnvInt("LOCATION_RATE_LIMIT_BURST", 5),
		MarketplaceAuthRateLimitPerMinute:     getEnvInt("MARKETPLACE_AUTH_RATE_LIMIT_PER_MINUTE", 20),
		MarketplaceAuthRateLimitBurst:         getEnvInt("MARKETPLACE_AUTH_RATE_LIMIT_BURST", 6),
		RedisURL:                              strings.TrimSpace(os.Getenv("REDIS_URL")),
		RedisKeyPrefix:                        strings.TrimSpace(getEnv("REDIS_KEY_PREFIX", "tellbook:"+appEnv+":v1")),
		RedisKeyHMACSecret:                    os.Getenv("REDIS_KEY_HMAC_SECRET"),
		RedisPoolSize:                         getEnvInt("REDIS_POOL_SIZE", 32),
		RedisMinIdleConnections:               getEnvInt("REDIS_MIN_IDLE_CONNECTIONS", 4),
		RedisDialTimeout:                      getEnvDuration("REDIS_DIAL_TIMEOUT", 750*time.Millisecond),
		RedisReadTimeout:                      getEnvDuration("REDIS_READ_TIMEOUT", 250*time.Millisecond),
		RedisWriteTimeout:                     getEnvDuration("REDIS_WRITE_TIMEOUT", 250*time.Millisecond),
		RedisPoolTimeout:                      getEnvDuration("REDIS_POOL_TIMEOUT", 500*time.Millisecond),
		RedisMaxPayloadBytes:                  getEnvInt("REDIS_MAX_PAYLOAD_BYTES", 64*1024),
		RedisFallbackMaxConcurrency:           getEnvInt("REDIS_FALLBACK_MAX_CONCURRENCY", 32),
		RateLimitIPCeilingMultiplier:          getEnvInt("RATE_LIMIT_IP_CEILING_MULTIPLIER", 8),
		MetricsAuthToken:                      strings.TrimSpace(os.Getenv("METRICS_AUTH_TOKEN")),
		HTTPSuccessLogSampleRate:              getEnvFloat("HTTP_SUCCESS_LOG_SAMPLE_RATE", 0.1),
		HTTPSlowRequestThreshold:              getEnvDuration("HTTP_SLOW_REQUEST_THRESHOLD", 750*time.Millisecond),
		GoogleMapsServerAPIKey:                strings.TrimSpace(os.Getenv("GOOGLE_MAPS_SERVER_API_KEY")),
		DatabaseURL:                           strings.TrimSpace(os.Getenv("DATABASE_URL")),
		DatabaseDirectURL:                     strings.TrimSpace(os.Getenv("DATABASE_DIRECT_URL")),
		DatabaseMaxConnections:                int32(getEnvInt("DATABASE_MAX_CONNECTIONS", 14)),
		DatabaseMinConnections:                int32(getEnvInt("DATABASE_MIN_CONNECTIONS", 2)),
		DatabaseDirectMaxConnections:          int32(getEnvInt("DATABASE_DIRECT_MAX_CONNECTIONS", requiredDirectDatabaseConnections(processRole, tessaAIEnabled))),
		DatabaseMaxConnectionLifetime:         getEnvDuration("DATABASE_MAX_CONNECTION_LIFETIME", 30*time.Minute),
		DatabaseMaxConnectionLifetimeJitter:   getEnvDuration("DATABASE_MAX_CONNECTION_LIFETIME_JITTER", 5*time.Minute),
		DatabaseMaxConnectionIdleTime:         getEnvDuration("DATABASE_MAX_CONNECTION_IDLE_TIME", 5*time.Minute),
		DatabaseHealthCheckPeriod:             getEnvDuration("DATABASE_HEALTH_CHECK_PERIOD", 30*time.Second),
		DatabaseConnectTimeout:                getEnvDuration("DATABASE_CONNECT_TIMEOUT", 5*time.Second),
		DatabaseStatementTimeout:              getEnvDuration("DATABASE_STATEMENT_TIMEOUT", 30*time.Second),
		DatabaseLockTimeout:                   getEnvDuration("DATABASE_LOCK_TIMEOUT", 5*time.Second),
		DatabaseIdleTransactionTimeout:        getEnvDuration("DATABASE_IDLE_TRANSACTION_TIMEOUT", 30*time.Second),
		CORSOrigins:                           splitCSV(getEnv("CORS_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173")),
		TrustedProxyCIDRs:                     splitCSV(getEnv("TRUSTED_PROXY_CIDRS", "127.0.0.1/32,::1/128")),
		AuthIssuer:                            getEnv("AUTH_ISSUER", "booking-api"),
		AuthAccessTokenSecret:                 strings.TrimSpace(os.Getenv("AUTH_ACCESS_TOKEN_SECRET")),
		AuthAccessTokenTTL:                    getEnvDuration("AUTH_ACCESS_TOKEN_TTL", 15*time.Minute),
		AuthAccessCookieName:                  getEnv("AUTH_ACCESS_COOKIE_NAME", "booking_access"),
		AuthRefreshCookieName:                 getEnv("AUTH_REFRESH_COOKIE_NAME", "booking_refresh"),
		AuthCookieDomain:                      strings.TrimSpace(os.Getenv("AUTH_COOKIE_DOMAIN")),
		AuthCookieSecure:                      getEnvBool("AUTH_COOKIE_SECURE", false),
		AuthRefreshTokenTTL:                   getEnvDuration("AUTH_REFRESH_TOKEN_TTL", 24*30*time.Hour),
		AuthBcryptCost:                        getEnvInt("AUTH_BCRYPT_COST", 12),
		AuthEmailEnabled:                      getEnvBool("AUTH_EMAIL_ENABLED", false),
		AuthWhatsAppEnabled:                   getEnvBool("AUTH_WHATSAPP_ENABLED", false),
		AuthDeliveryEncryptionKeys:            strings.TrimSpace(os.Getenv("AUTH_DELIVERY_ENCRYPTION_KEYS")),
		AuthDeliveryActiveKey:                 strings.TrimSpace(os.Getenv("AUTH_DELIVERY_ACTIVE_KEY")),
		AuthDestinationHMACKey:                strings.TrimSpace(os.Getenv("AUTH_DESTINATION_HMAC_KEY")),
		AuthDeliveryConcurrency:               getEnvInt("AUTH_DELIVERY_CONCURRENCY", 4),
		AuthDeliveryTimeout:                   getEnvDuration("AUTH_DELIVERY_TIMEOUT", 30*time.Second),
		R2PrivateBucketName:                   strings.TrimSpace(os.Getenv("R2_PRIVATE_BUCKET_NAME")),
		R2PublicBucketName:                    strings.TrimSpace(os.Getenv("R2_PUBLIC_BUCKET_NAME")),
		R2AccountID:                           strings.TrimSpace(os.Getenv("R2_ACCOUNT_ID")),
		R2Endpoint:                            strings.TrimSpace(os.Getenv("R2_ENDPOINT")),
		R2AccessKeyID:                         strings.TrimSpace(os.Getenv("R2_ACCESS_KEY_ID")),
		R2SecretAccessKey:                     strings.TrimSpace(os.Getenv("R2_SECRET_ACCESS_KEY")),
		R2PublicBucketBaseURL:                 strings.TrimSpace(os.Getenv("R2_PUBLIC_BUCKET_BASE_URL")),
		NotificationEmailEnabled:              getEnvBool("NOTIFICATION_EMAIL_ENABLED", false),
		NotificationEmailConcurrency:          getEnvInt("NOTIFICATION_EMAIL_CONCURRENCY", 4),
		NotificationEmailTimeout:              getEnvDuration("NOTIFICATION_EMAIL_TIMEOUT", 30*time.Second),
		WelcomeEmailEnabled:                   getEnvBool("WELCOME_EMAIL_ENABLED", false),
		WelcomeEmailConcurrency:               getEnvInt("WELCOME_EMAIL_CONCURRENCY", 2),
		WelcomeEmailTimeout:                   getEnvDuration("WELCOME_EMAIL_TIMEOUT", 30*time.Second),
		NotificationWhatsAppEnabled:           getEnvBool("NOTIFICATION_WHATSAPP_ENABLED", false),
		WhatsAppWorkerConcurrency:             getEnvInt("WHATSAPP_WORKER_CONCURRENCY", 4),
		NotificationPlannerConcurrency:        getEnvInt("NOTIFICATION_PLANNER_CONCURRENCY", 4),
		NotificationDestinationHMACKey:        strings.TrimSpace(os.Getenv("NOTIFICATION_DESTINATION_HMAC_KEY")),
		MetaAppID:                             strings.TrimSpace(os.Getenv("META_APP_ID")),
		MetaAppSecret:                         strings.TrimSpace(os.Getenv("META_APP_SECRET")),
		MetaVerifyToken:                       strings.TrimSpace(os.Getenv("META_VERIFY_TOKEN")),
		WABAToken:                             strings.TrimSpace(os.Getenv("WABA_TOKEN")),
		WhatsAppBusinessAccountID:             strings.TrimSpace(os.Getenv("WHATSAPP_BUSINESS_ACCOUNT_ID")),
		WABAPhoneNumberID:                     strings.TrimSpace(os.Getenv("WABA_PHONE_NUMBER_ID")),
		WABABusinessPhoneE164:                 strings.TrimSpace(os.Getenv("WABA_BUSINESS_PHONE_E164")),
		WhatsAppGraphBaseURL:                  strings.TrimRight(getEnv("WHATSAPP_GRAPH_BASE_URL", "https://graph.facebook.com"), "/"),
		WhatsAppGraphVersion:                  strings.TrimSpace(getEnv("WHATSAPP_GRAPH_VERSION", "v24.0")),
		WhatsAppHTTPTimeout:                   getEnvDuration("WHATSAPP_HTTP_TIMEOUT", 15*time.Second),
		WhatsAppEnabledTemplateKeys:           splitCSV(os.Getenv("WHATSAPP_ENABLED_TEMPLATE_KEYS")),
		SMTPHost:                              getEnv("SMTP_HOST", "smtp.zoho.com"),
		SMTPPort:                              getEnvInt("SMTP_PORT", 465),
		SMTPUsername:                          strings.TrimSpace(os.Getenv("SMTP_USERNAME")),
		SMTPPassword:                          strings.TrimSpace(os.Getenv("SMTP_PASSWORD")),
		SMTPFromEmail:                         strings.TrimSpace(os.Getenv("SMTP_FROM_EMAIL")),
		SMTPFromName:                          getEnv("SMTP_FROM_NAME", "Booking"),
		SMTPSecurity:                          getEnv("SMTP_SECURITY", "tls"),
		SMTPInsecureSkipVerify:                getEnvBool("SMTP_INSECURE_SKIP_VERIFY", false),
		SMTPConnectTimeout:                    getEnvDuration("SMTP_CONNECT_TIMEOUT", 10*time.Second),
		PaystackSecretKey:                     strings.TrimSpace(os.Getenv("PAYSTACK_SECRET_KEY")),
		PaystackSecretKeyTest:                 strings.TrimSpace(os.Getenv("PAYSTACK_SECRET_KEY_TEST")),
		PaystackBaseURL:                       strings.TrimSpace(os.Getenv("PAYSTACK_BASE_URL")),
		PayazaPublicKey:                       strings.TrimSpace(os.Getenv("PAYAZA_PUBLIC_KEY")),
		PayazaSecretKey:                       strings.TrimSpace(os.Getenv("PAYAZA_SECRET_KEY")),
		PayazaPublicKeyTest:                   strings.TrimSpace(os.Getenv("PAYAZA_PUBLIC_KEY_TEST")),
		PayazaSecretKeyTest:                   strings.TrimSpace(os.Getenv("PAYAZA_SECRET_KEY_TEST")),
		PayazaBaseURL:                         strings.TrimSpace(os.Getenv("PAYAZA_BASE_URL")),
		PayazaTransferPIN:                     strings.TrimSpace(os.Getenv("PAYAZA_TRANSFER_PIN")),
		PayazaTransferPINTest:                 strings.TrimSpace(os.Getenv("PAYAZA_TRANSFER_PIN_TEST")),
		PayazaSourceAccounts:                  strings.TrimSpace(os.Getenv("PAYAZA_SOURCE_ACCOUNTS")),
		PayazaSourceAccountsTest:              strings.TrimSpace(os.Getenv("PAYAZA_SOURCE_ACCOUNTS_TEST")),
		PayazaPayoutSenderName:                strings.TrimSpace(os.Getenv("PAYAZA_PAYOUT_SENDER_NAME")),
		PayazaPayoutSenderPhone:               strings.TrimSpace(os.Getenv("PAYAZA_PAYOUT_SENDER_PHONE")),
		PayazaPayoutSenderAddress:             strings.TrimSpace(os.Getenv("PAYAZA_PAYOUT_SENDER_ADDRESS")),
		PayazaNGNDVABankCode:                  strings.TrimSpace(os.Getenv("PAYAZA_NGN_DVA_BANK_CODE")),
		PayazaNGNDVAEnquiryBankCode:           strings.TrimSpace(os.Getenv("PAYAZA_NGN_DVA_ENQUIRY_BANK_CODE")),
		PayazaCardSandboxVerified:             getEnvBool("PAYAZA_CARD_SANDBOX_VERIFIED", false),
		PayazaCardProductionEnabled:           getEnvBool("PAYAZA_CARD_PRODUCTION_ENABLED", false),
		PayazaBankTransferSandboxVerified:     getEnvBool("PAYAZA_BANK_TRANSFER_SANDBOX_VERIFIED", false),
		PayazaBankTransferProductionEnabled:   getEnvBool("PAYAZA_BANK_TRANSFER_PRODUCTION_ENABLED", false),
		PayazaDestinationSandboxVerified:      getEnvBool("PAYAZA_DESTINATION_SANDBOX_VERIFIED", false),
		PayazaDestinationProductionEnabled:    getEnvBool("PAYAZA_DESTINATION_PRODUCTION_ENABLED", false),
		PayazaPayoutSandboxVerified:           getEnvBool("PAYAZA_PAYOUT_SANDBOX_VERIFIED", false),
		PayazaPayoutProductionEnabled:         getEnvBool("PAYAZA_PAYOUT_PRODUCTION_ENABLED", false),
		PaystackCardSandboxVerified:           getEnvBool("PAYSTACK_CARD_SANDBOX_VERIFIED", false),
		PaystackCardProductionEnabled:         getEnvBool("PAYSTACK_CARD_PRODUCTION_ENABLED", false),
		PaystackBankTransferSandboxVerified:   getEnvBool("PAYSTACK_BANK_TRANSFER_SANDBOX_VERIFIED", false),
		PaystackBankTransferProductionEnabled: getEnvBool("PAYSTACK_BANK_TRANSFER_PRODUCTION_ENABLED", false),
		PaystackDestinationSandboxVerified:    getEnvBool("PAYSTACK_DESTINATION_SANDBOX_VERIFIED", false),
		PaystackDestinationProductionEnabled:  getEnvBool("PAYSTACK_DESTINATION_PRODUCTION_ENABLED", false),
		PaystackPayoutSandboxVerified:         getEnvBool("PAYSTACK_PAYOUT_SANDBOX_VERIFIED", false),
		PaystackPayoutProductionEnabled:       getEnvBool("PAYSTACK_PAYOUT_PRODUCTION_ENABLED", false),
		PaystackPayoutOTPDisabled:             getEnvBool("PAYSTACK_PAYOUT_OTP_DISABLED", false),
		PaymentsEnvironment:                   strings.ToLower(getEnv("PAYMENTS_ENVIRONMENT", "test")),
		FinancialEncryptionKeys:               strings.TrimSpace(os.Getenv("FINANCIAL_DATA_ENCRYPTION_KEYS")),
		FinancialActiveKey:                    strings.TrimSpace(os.Getenv("FINANCIAL_DATA_ACTIVE_KEY_VERSION")),
		FinancialFingerprintKey:               strings.TrimSpace(os.Getenv("FINANCIAL_DATA_FINGERPRINT_KEY")),
		AgreementTokenEncryptionKeys:          strings.TrimSpace(os.Getenv("AGREEMENT_TOKEN_ENCRYPTION_KEYS")),
		AgreementTokenActiveKey:               strings.TrimSpace(os.Getenv("AGREEMENT_TOKEN_ACTIVE_KEY")),
		ShutdownTimeout:                       getEnvDuration("HTTP_SHUTDOWN_TIMEOUT", 10*time.Second),
		ReadTimeout:                           getEnvDuration("HTTP_READ_TIMEOUT", 15*time.Second),
		ReadHeaderTimeout:                     getEnvDuration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		WriteTimeout:                          getEnvDuration("HTTP_WRITE_TIMEOUT", 15*time.Second),
		IdleTimeout:                           getEnvDuration("HTTP_IDLE_TIMEOUT", 60*time.Second),
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if strings.EqualFold(cfg.AppEnv, "production") && cfg.DatabaseDirectURL == "" {
		return Config{}, fmt.Errorf("DATABASE_DIRECT_URL is required in production for session-bound connections")
	}
	switch cfg.ProcessRole {
	case ProcessRoleAPI, ProcessRoleWorker, ProcessRoleAIWorker, ProcessRoleMaintenance, ProcessRoleAll:
	default:
		return Config{}, fmt.Errorf("PROCESS_ROLE must be api, worker, ai-worker, maintenance, or all")
	}
	if strings.EqualFold(cfg.AppEnv, "production") && cfg.ProcessRole == ProcessRoleAll {
		return Config{}, fmt.Errorf("PROCESS_ROLE=all is restricted to local development")
	}
	if cfg.RedisURL == "" && strings.EqualFold(cfg.AppEnv, "production") && cfg.ProcessRole == ProcessRoleAPI {
		return Config{}, fmt.Errorf("REDIS_URL is required for the production API role")
	}
	if cfg.RedisURL != "" {
		redisURL, err := url.Parse(cfg.RedisURL)
		if err != nil || (redisURL.Scheme != "redis" && redisURL.Scheme != "rediss") || redisURL.Host == "" {
			return Config{}, fmt.Errorf("REDIS_URL must be a valid redis:// or rediss:// URL")
		}
		if len(cfg.RedisKeyHMACSecret) < 32 {
			return Config{}, fmt.Errorf("REDIS_KEY_HMAC_SECRET must be at least 32 bytes when Redis is enabled")
		}
	}
	if !validRedisKeyPrefix(cfg.RedisKeyPrefix) {
		return Config{}, fmt.Errorf("REDIS_KEY_PREFIX must contain only lowercase letters, digits, hyphens, and colon separators")
	}
	if cfg.RedisPoolSize < 4 || cfg.RedisPoolSize > 256 {
		return Config{}, fmt.Errorf("REDIS_POOL_SIZE must be between 4 and 256")
	}
	if cfg.RedisMinIdleConnections < 0 || cfg.RedisMinIdleConnections > cfg.RedisPoolSize {
		return Config{}, fmt.Errorf("REDIS_MIN_IDLE_CONNECTIONS must be between 0 and REDIS_POOL_SIZE")
	}
	for name, value := range map[string]time.Duration{
		"REDIS_DIAL_TIMEOUT":  cfg.RedisDialTimeout,
		"REDIS_READ_TIMEOUT":  cfg.RedisReadTimeout,
		"REDIS_WRITE_TIMEOUT": cfg.RedisWriteTimeout,
		"REDIS_POOL_TIMEOUT":  cfg.RedisPoolTimeout,
	} {
		if value < 50*time.Millisecond || value > 5*time.Second {
			return Config{}, fmt.Errorf("%s must be between 50ms and 5s", name)
		}
	}
	if cfg.RedisMaxPayloadBytes < 1024 || cfg.RedisMaxPayloadBytes > 1024*1024 {
		return Config{}, fmt.Errorf("REDIS_MAX_PAYLOAD_BYTES must be between 1024 and 1048576")
	}
	if cfg.RedisFallbackMaxConcurrency < 1 || cfg.RedisFallbackMaxConcurrency > 256 {
		return Config{}, fmt.Errorf("REDIS_FALLBACK_MAX_CONCURRENCY must be between 1 and 256")
	}
	if cfg.RateLimitIPCeilingMultiplier < 2 || cfg.RateLimitIPCeilingMultiplier > 100 {
		return Config{}, fmt.Errorf("RATE_LIMIT_IP_CEILING_MULTIPLIER must be between 2 and 100")
	}
	if cfg.SSEMaxConnections < 100 || cfg.SSEMaxConnections > 100000 {
		return Config{}, fmt.Errorf("SSE_MAX_CONNECTIONS must be between 100 and 100000")
	}
	if cfg.SSEMaxConnectionsPerIP < 1 || cfg.SSEMaxConnectionsPerIP > cfg.SSEMaxConnections {
		return Config{}, fmt.Errorf("SSE_MAX_CONNECTIONS_PER_IP must be positive and no greater than SSE_MAX_CONNECTIONS")
	}
	if cfg.PaymentSSEMaxConnectionsPerToken < 1 || cfg.PaymentSSEMaxConnectionsPerToken > 100 {
		return Config{}, fmt.Errorf("PAYMENT_SSE_MAX_CONNECTIONS_PER_TOKEN must be between 1 and 100")
	}
	if !finiteFloat(cfg.HTTPSuccessLogSampleRate) || cfg.HTTPSuccessLogSampleRate < 0 || cfg.HTTPSuccessLogSampleRate > 1 {
		return Config{}, fmt.Errorf("HTTP_SUCCESS_LOG_SAMPLE_RATE must be between 0 and 1")
	}
	if cfg.HTTPSlowRequestThreshold <= 0 || cfg.HTTPSlowRequestThreshold > time.Minute {
		return Config{}, fmt.Errorf("HTTP_SLOW_REQUEST_THRESHOLD must be between 1ns and 1m")
	}
	if cfg.InboxAIMaxConcurrency < 1 || cfg.InboxAIMaxConcurrency > 32 {
		return Config{}, fmt.Errorf("INBOX_AI_MAX_CONCURRENCY must be between 1 and 32")
	}
	if cfg.InboxAISemiPilotReplyDelay < 0 || cfg.InboxAISemiPilotReplyDelay > 10*time.Second {
		return Config{}, fmt.Errorf("INBOX_AI_SEMI_PILOT_REPLY_DELAY must be between 0s and 10s")
	}
	if cfg.InboxAIAutopilotPaymentWindow < 5*time.Minute || cfg.InboxAIAutopilotPaymentWindow > 24*time.Hour {
		return Config{}, fmt.Errorf("INBOX_AI_AUTOPILOT_PAYMENT_WINDOW must be between 5m and 24h")
	}
	if cfg.TessaAIWorkerConcurrency < 1 || cfg.TessaAIWorkerConcurrency > 32 {
		return Config{}, fmt.Errorf("TESSA_AI_WORKER_CONCURRENCY must be between 1 and 32")
	}
	if cfg.TessaAIPrimaryRequestTimeout <= 0 || cfg.TessaAIPrimaryRequestTimeout > cfg.TessaAITurnTimeout {
		return Config{}, fmt.Errorf("TESSA_AI_PRIMARY_REQUEST_TIMEOUT must be positive and no greater than TESSA_AI_TURN_TIMEOUT")
	}
	if cfg.TessaAIFallbackRequestTimeout <= 0 || cfg.TessaAIFallbackRequestTimeout > cfg.TessaAITurnTimeout {
		return Config{}, fmt.Errorf("TESSA_AI_FALLBACK_REQUEST_TIMEOUT must be positive and no greater than TESSA_AI_TURN_TIMEOUT")
	}
	if cfg.TessaAITurnTimeout < 5*time.Second || cfg.TessaAITurnTimeout > 5*time.Minute {
		return Config{}, fmt.Errorf("TESSA_AI_TURN_TIMEOUT must be between 5s and 5m")
	}
	if cfg.TessaAIMaxInputTokens < tessaconfig.MinimumInputTokens || cfg.TessaAIMaxInputTokens > 100000 {
		return Config{}, fmt.Errorf(
			"TESSA_AI_MAX_INPUT_TOKENS must be between %d and 100000",
			tessaconfig.MinimumInputTokens,
		)
	}
	if cfg.TessaAIMaxOutputTokens < 100 || cfg.TessaAIMaxOutputTokens > 16000 {
		return Config{}, fmt.Errorf("TESSA_AI_MAX_OUTPUT_TOKENS must be between 100 and 16000")
	}
	if len(cfg.TessaAINoticeRevision) > 80 || (cfg.TessaAIEnabled && cfg.TessaAINoticeRevision == "") {
		return Config{}, fmt.Errorf("TESSA_AI_NOTICE_REVISION must be explicitly configured with 1 to 80 characters when Tessa is enabled")
	}
	for _, rawID := range cfg.InboxAIProviderAllowlist {
		if _, err := uuid.Parse(rawID); err != nil {
			return Config{}, fmt.Errorf("INBOX_AI_PROVIDER_ALLOWLIST contains invalid UUID %q", rawID)
		}
	}
	for _, rawID := range cfg.InboxAIAutomationProviderAllowlist {
		if _, err := uuid.Parse(rawID); err != nil {
			return Config{}, fmt.Errorf("INBOX_AI_AUTOMATION_PROVIDER_ALLOWLIST contains invalid UUID %q", rawID)
		}
	}
	for _, rawID := range cfg.TessaAIProviderAllowlist {
		if _, err := uuid.Parse(rawID); err != nil {
			return Config{}, fmt.Errorf("TESSA_AI_PROVIDER_ALLOWLIST contains invalid UUID %q", rawID)
		}
	}
	if cfg.InboxAIAutomationEnabled && len(cfg.InboxAIAutomationProviderAllowlist) == 0 {
		return Config{}, fmt.Errorf("INBOX_AI_AUTOMATION_PROVIDER_ALLOWLIST is required when automation is enabled")
	}
	if cfg.TessaAIEnabled && len(cfg.TessaAIProviderAllowlist) == 0 {
		return Config{}, fmt.Errorf("TESSA_AI_PROVIDER_ALLOWLIST is required when Tessa is enabled")
	}
	if err := validateAIProvider("TESSA_AI_PRIMARY_PROVIDER", cfg.TessaAIPrimaryProvider); err != nil {
		return Config{}, err
	}
	if cfg.TessaAIEnabled && cfg.TessaAIPrimaryProvider != AIProviderSelfHosted {
		return Config{}, fmt.Errorf("TESSA_AI_PRIMARY_PROVIDER must be self_hosted; external providers are supported only as Tessa fallback")
	}
	if cfg.TessaAIFallbackProvider != "" {
		if err := validateAIProvider("TESSA_AI_FALLBACK_PROVIDER", cfg.TessaAIFallbackProvider); err != nil {
			return Config{}, err
		}
		if cfg.TessaAIFallbackProvider == cfg.TessaAIPrimaryProvider {
			return Config{}, fmt.Errorf("TESSA_AI_FALLBACK_PROVIDER must differ from TESSA_AI_PRIMARY_PROVIDER")
		}
		if cfg.TessaAIEnabled && isExternalAIProvider(cfg.TessaAIFallbackProvider) && !cfg.TessaAIExternalProcessingApproved {
			return Config{}, fmt.Errorf("TESSA_AI_EXTERNAL_PROCESSING_APPROVED must be true before enabling an external fallback")
		}
	}
	if cfg.DatabaseMaxConnections < 4 {
		return Config{}, fmt.Errorf("DATABASE_MAX_CONNECTIONS must be at least 4")
	}
	if cfg.DatabaseMinConnections < 0 || cfg.DatabaseMinConnections >= cfg.DatabaseMaxConnections {
		return Config{}, fmt.Errorf("DATABASE_MIN_CONNECTIONS must be non-negative and lower than DATABASE_MAX_CONNECTIONS")
	}
	requiredDirectConnections := int32(requiredDirectDatabaseConnections(cfg.ProcessRole, cfg.TessaAIEnabled))
	if cfg.DatabaseDirectMaxConnections < requiredDirectConnections {
		return Config{}, fmt.Errorf(
			"DATABASE_DIRECT_MAX_CONNECTIONS must be at least %d for PROCESS_ROLE=%s",
			requiredDirectConnections,
			cfg.ProcessRole,
		)
	}
	if cfg.DatabaseDirectMaxConnections > 32 {
		return Config{}, fmt.Errorf("DATABASE_DIRECT_MAX_CONNECTIONS must be at most 32")
	}
	for name, value := range map[string]time.Duration{
		"DATABASE_MAX_CONNECTION_LIFETIME":        cfg.DatabaseMaxConnectionLifetime,
		"DATABASE_MAX_CONNECTION_LIFETIME_JITTER": cfg.DatabaseMaxConnectionLifetimeJitter,
		"DATABASE_MAX_CONNECTION_IDLE_TIME":       cfg.DatabaseMaxConnectionIdleTime,
		"DATABASE_HEALTH_CHECK_PERIOD":            cfg.DatabaseHealthCheckPeriod,
		"DATABASE_CONNECT_TIMEOUT":                cfg.DatabaseConnectTimeout,
		"DATABASE_STATEMENT_TIMEOUT":              cfg.DatabaseStatementTimeout,
		"DATABASE_LOCK_TIMEOUT":                   cfg.DatabaseLockTimeout,
		"DATABASE_IDLE_TRANSACTION_TIMEOUT":       cfg.DatabaseIdleTransactionTimeout,
	} {
		if value < time.Millisecond {
			return Config{}, fmt.Errorf("%s must be at least 1ms", name)
		}
	}
	if cfg.DatabaseMaxConnectionLifetimeJitter >= cfg.DatabaseMaxConnectionLifetime {
		return Config{}, fmt.Errorf("DATABASE_MAX_CONNECTION_LIFETIME_JITTER must be lower than DATABASE_MAX_CONNECTION_LIFETIME")
	}
	if cfg.DatabaseLockTimeout > cfg.DatabaseStatementTimeout {
		return Config{}, fmt.Errorf("DATABASE_LOCK_TIMEOUT must be no greater than DATABASE_STATEMENT_TIMEOUT")
	}
	if cfg.AuthAccessTokenSecret == "" {
		return Config{}, fmt.Errorf("AUTH_ACCESS_TOKEN_SECRET is required")
	}
	for _, cidr := range cfg.TrustedProxyCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return Config{}, fmt.Errorf("TRUSTED_PROXY_CIDRS contains invalid CIDR %q", cidr)
		}
	}
	for _, setting := range []struct {
		name  string
		value string
	}{
		{name: "DEFAULT_AI_PROVIDER", value: cfg.DefaultAIProvider},
		{name: "AGREEMENT_AI_PROVIDER", value: cfg.AgreementAIProvider},
	} {
		if err := validateAIProvider(setting.name, setting.value); err != nil {
			return Config{}, err
		}
	}
	if cfg.HostedAIProvider != HostedProviderOpenAI {
		return Config{}, fmt.Errorf("HOSTED_AI_PROVIDER must be openai")
	}
	if cfg.NeedsSelfHosted() && cfg.LLMBaseURL == "" {
		return Config{}, fmt.Errorf("LLM_BASE_URL is required when a task uses self-hosted AI")
	}
	if cfg.NeedsSelfHosted() && cfg.LLMMaxOutputTokens <= 0 {
		return Config{}, fmt.Errorf("LLM_MAX_OUTPUT_TOKENS must be greater than zero")
	}
	if cfg.NeedsHosted() {
		if cfg.OpenAIAPIKey == "" {
			return Config{}, fmt.Errorf("OPENAI_API_KEY is required when a task uses hosted AI")
		}
		if cfg.OpenAIModel == "" {
			return Config{}, fmt.Errorf("OPENAI_MODEL is required when a task uses hosted AI")
		}
		if !validReasoningEffort(cfg.OpenAIReasoningEffort) {
			return Config{}, fmt.Errorf("OPENAI_REASONING_EFFORT must be one of none, low, medium, high, xhigh, or max")
		}
		if cfg.OpenAIMaxOutputTokens <= 0 {
			return Config{}, fmt.Errorf("OPENAI_MAX_OUTPUT_TOKENS must be greater than zero")
		}
	}
	if cfg.NeedsOpenAICompatible() {
		if cfg.OpenAICompatBaseURL == "" {
			return Config{}, fmt.Errorf("OPENAI_COMPAT_BASE_URL is required when a task uses external OpenAI-compatible AI")
		}
		if err := validateOpenAICompatibleBaseURL(cfg.OpenAICompatBaseURL); err != nil {
			return Config{}, fmt.Errorf("OPENAI_COMPAT_BASE_URL: %w", err)
		}
		if err := validateOpenAICompatiblePath(cfg.OpenAICompatChatCompletions); err != nil {
			return Config{}, fmt.Errorf("OPENAI_COMPAT_CHAT_COMPLETIONS_PATH: %w", err)
		}
		if cfg.OpenAICompatModel == "" {
			return Config{}, fmt.Errorf("OPENAI_COMPAT_MODEL is required when a task uses external OpenAI-compatible AI")
		}
		if cfg.OpenAICompatAPIKey == "" {
			return Config{}, fmt.Errorf("OPENAI_COMPAT_API_KEY is required when a task uses external OpenAI-compatible AI")
		}
		if cfg.OpenAICompatTimeout <= 0 {
			return Config{}, fmt.Errorf("OPENAI_COMPAT_TIMEOUT must be greater than zero")
		}
		if cfg.OpenAICompatMaxOutputTokens <= 0 {
			return Config{}, fmt.Errorf("OPENAI_COMPAT_MAX_OUTPUT_TOKENS must be greater than zero")
		}
		switch cfg.OpenAICompatTokenLimitField {
		case OpenAICompatTokenFieldMaxTokens, OpenAICompatTokenFieldMaxCompletionTokens:
		default:
			return Config{}, fmt.Errorf("OPENAI_COMPAT_TOKEN_LIMIT_FIELD must be max_tokens or max_completion_tokens")
		}
		if cfg.OpenAICompatTemperature != nil &&
			(!finiteFloat(*cfg.OpenAICompatTemperature) || *cfg.OpenAICompatTemperature < 0 || *cfg.OpenAICompatTemperature > 2) {
			return Config{}, fmt.Errorf("OPENAI_COMPAT_TEMPERATURE must be between 0 and 2")
		}
		if cfg.OpenAICompatTopP != nil &&
			(!finiteFloat(*cfg.OpenAICompatTopP) || *cfg.OpenAICompatTopP <= 0 || *cfg.OpenAICompatTopP > 1) {
			return Config{}, fmt.Errorf("OPENAI_COMPAT_TOP_P must be greater than 0 and at most 1")
		}
		if cfg.OpenAICompatTemperature != nil && cfg.OpenAICompatTopP != nil {
			return Config{}, fmt.Errorf("configure only one of OPENAI_COMPAT_TEMPERATURE or OPENAI_COMPAT_TOP_P")
		}
	}
	if !strings.HasPrefix(cfg.OpenAICompatChatCompletions, "/") {
		cfg.OpenAICompatChatCompletions = "/" + cfg.OpenAICompatChatCompletions
	}
	if !strings.HasPrefix(cfg.LLMChatCompletions, "/") {
		cfg.LLMChatCompletions = "/" + cfg.LLMChatCompletions
	}
	if cfg.LLMTimeout <= 0 {
		return Config{}, fmt.Errorf("LLM_TIMEOUT must be greater than zero")
	}
	if cfg.LLMTemperature < 0 || cfg.LLMTemperature > 2 {
		return Config{}, fmt.Errorf("LLM_TEMPERATURE must be between 0 and 2")
	}
	if cfg.LLMTopP < 0 || cfg.LLMTopP > 1 {
		return Config{}, fmt.Errorf("TOP_P must be between 0 and 1")
	}
	if cfg.LLMTopK < 0 {
		return Config{}, fmt.Errorf("TOP_K must be zero or greater")
	}
	if cfg.LLMMinP < 0 || cfg.LLMMinP > 1 {
		return Config{}, fmt.Errorf("MIN_P must be between 0 and 1")
	}
	if cfg.LLMPresencePenalty < -2 || cfg.LLMPresencePenalty > 2 {
		return Config{}, fmt.Errorf("PRESENCE_PENALTY must be between -2 and 2")
	}
	if cfg.LLMRepetitionPenalty <= 0 {
		return Config{}, fmt.Errorf("REPETITION_PENALTY must be greater than zero")
	}
	if strings.EqualFold(cfg.AppEnv, "production") {
		if cfg.AuthCookieDomain == "" {
			return Config{}, fmt.Errorf("AUTH_COOKIE_DOMAIN is required in production")
		}
		if !cfg.AuthCookieSecure {
			return Config{}, fmt.Errorf("AUTH_COOKIE_SECURE must be true in production")
		}
	}
	if cfg.AuthDeliveryConcurrency < 1 || cfg.AuthDeliveryConcurrency > 32 {
		return Config{}, fmt.Errorf("AUTH_DELIVERY_CONCURRENCY must be between 1 and 32")
	}
	if cfg.AuthDeliveryTimeout < time.Second || cfg.AuthDeliveryTimeout > 2*time.Minute {
		return Config{}, fmt.Errorf("AUTH_DELIVERY_TIMEOUT must be between 1s and 2m")
	}
	if cfg.AuthEmailEnabled || cfg.AuthWhatsAppEnabled {
		if len(cfg.AuthDestinationHMACKey) < 32 {
			return Config{}, fmt.Errorf("AUTH_DESTINATION_HMAC_KEY must be at least 32 bytes when auth delivery is enabled")
		}
		if _, err := secure.ParseKeyring(cfg.AuthDeliveryEncryptionKeys, cfg.AuthDeliveryActiveKey); err != nil {
			return Config{}, fmt.Errorf("AUTH_DELIVERY_ENCRYPTION_KEYS: %w", err)
		}
	}
	if err := validatePublicBaseURL(cfg.ClientPublicBaseURL); err != nil {
		return Config{}, fmt.Errorf("CLIENT_PUBLIC_BASE_URL: %w", err)
	}
	if err := validatePublicBaseURL(cfg.MarketplacePublicBaseURL); err != nil {
		return Config{}, fmt.Errorf("MARKETPLACE_PUBLIC_BASE_URL: %w", err)
	}
	if (cfg.R2PublicBucketName == "") != (cfg.R2PublicBucketBaseURL == "") {
		return Config{}, fmt.Errorf("R2_PUBLIC_BUCKET_NAME and R2_PUBLIC_BUCKET_BASE_URL must be configured together")
	}
	if cfg.R2PublicBucketBaseURL != "" {
		if err := validatePublicBaseURL(cfg.R2PublicBucketBaseURL); err != nil {
			return Config{}, fmt.Errorf("R2_PUBLIC_BUCKET_BASE_URL: %w", err)
		}
		if strings.EqualFold(cfg.AppEnv, "production") && !strings.HasPrefix(cfg.R2PublicBucketBaseURL, "https://") {
			return Config{}, fmt.Errorf("R2_PUBLIC_BUCKET_BASE_URL must use HTTPS in production")
		}
	}
	for name, value := range map[string]string{
		"META_APP_ID":                  cfg.MetaAppID,
		"WHATSAPP_BUSINESS_ACCOUNT_ID": cfg.WhatsAppBusinessAccountID,
		"WABA_PHONE_NUMBER_ID":         cfg.WABAPhoneNumberID,
	} {
		if value != "" && !isDecimalIdentifier(value) {
			return Config{}, fmt.Errorf("%s must contain only decimal digits", name)
		}
	}
	if cfg.MetaAppSecret != "" && len(cfg.MetaAppSecret) < 16 {
		return Config{}, fmt.Errorf("META_APP_SECRET must be at least 16 characters")
	}
	if cfg.MetaVerifyToken != "" && len(cfg.MetaVerifyToken) < 16 {
		return Config{}, fmt.Errorf("META_VERIFY_TOKEN must be at least 16 characters")
	}
	runsAPI := cfg.ProcessRole == ProcessRoleAPI || cfg.ProcessRole == ProcessRoleAll
	runsNotificationWorkers := cfg.ProcessRole == ProcessRoleWorker || cfg.ProcessRole == ProcessRoleAll
	if cfg.TessaWhatsAppLinkingEnabled && (!cfg.TessaAIEnabled || !cfg.MetaWebhookConfigured() || !cfg.WhatsAppSendConfigured() || !cfg.NotificationContactFoundationConfigured()) {
		return Config{}, fmt.Errorf("TESSA_WHATSAPP_LINKING_ENABLED requires Tessa, Meta webhook, WhatsApp sender and contact configuration")
	}
	if cfg.TessaWhatsAppConversationsEnabled {
		if !cfg.TessaWhatsAppLinkingEnabled {
			return Config{}, fmt.Errorf("TESSA_WHATSAPP_CONVERSATIONS_ENABLED requires TESSA_WHATSAPP_LINKING_ENABLED")
		}
		origin, err := url.Parse(cfg.ClientPublicBaseURL)
		if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
			return Config{}, fmt.Errorf("TESSA_WHATSAPP_CONVERSATIONS_ENABLED requires an HTTPS CLIENT_PUBLIC_BASE_URL origin")
		}
	}
	if runsNotificationWorkers && cfg.AuthEmailEnabled && !cfg.SMTPConfigured() {
		return Config{}, fmt.Errorf("SMTP_USERNAME and SMTP_PASSWORD are required when email authentication is enabled")
	}
	webhookRequested := cfg.MetaAppSecret != "" || cfg.MetaVerifyToken != "" || cfg.AuthWhatsAppEnabled
	if runsAPI && webhookRequested && !cfg.MetaWebhookConfigured() {
		return Config{}, fmt.Errorf("META_APP_SECRET, META_VERIFY_TOKEN, WHATSAPP_BUSINESS_ACCOUNT_ID, and WABA_PHONE_NUMBER_ID must be configured together for the API webhook")
	}
	contactFoundationRequested := cfg.NotificationDestinationHMACKey != "" || cfg.WABABusinessPhoneE164 != ""
	if runsAPI && contactFoundationRequested && !cfg.NotificationContactFoundationConfigured() {
		return Config{}, fmt.Errorf("NOTIFICATION_DESTINATION_HMAC_KEY and WABA_BUSINESS_PHONE_E164 are required for WhatsApp verification and control messages")
	}
	sendingRequested := cfg.WABAToken != "" || len(cfg.WhatsAppEnabledTemplateKeys) > 0 || cfg.NotificationWhatsAppEnabled || cfg.AuthWhatsAppEnabled
	if runsNotificationWorkers && sendingRequested && !cfg.WhatsAppSendConfigured() {
		return Config{}, fmt.Errorf("WABA_TOKEN, WHATSAPP_BUSINESS_ACCOUNT_ID, and WABA_PHONE_NUMBER_ID must be configured together for WhatsApp workers")
	}
	if err := validateOpenAICompatibleBaseURL(cfg.WhatsAppGraphBaseURL); err != nil {
		return Config{}, fmt.Errorf("WHATSAPP_GRAPH_BASE_URL: %w", err)
	}
	if !validGraphVersion(cfg.WhatsAppGraphVersion) {
		return Config{}, fmt.Errorf("WHATSAPP_GRAPH_VERSION must use the form v<major>.<minor>")
	}
	if cfg.WhatsAppHTTPTimeout < time.Second || cfg.WhatsAppHTTPTimeout > time.Minute {
		return Config{}, fmt.Errorf("WHATSAPP_HTTP_TIMEOUT must be between 1s and 1m")
	}
	if cfg.NotificationDestinationHMACKey != "" && len(cfg.NotificationDestinationHMACKey) < 32 {
		return Config{}, fmt.Errorf("NOTIFICATION_DESTINATION_HMAC_KEY must be at least 32 bytes")
	}
	if runsNotificationWorkers && (cfg.NotificationWhatsAppEnabled || cfg.NotificationDestinationHMACKey != "") &&
		len(cfg.NotificationDestinationHMACKey) < 32 {
		return Config{}, fmt.Errorf("NOTIFICATION_DESTINATION_HMAC_KEY must be at least 32 bytes for WhatsApp status processing")
	}
	if cfg.NotificationPlannerConcurrency < 1 || cfg.NotificationPlannerConcurrency > 32 {
		return Config{}, fmt.Errorf("NOTIFICATION_PLANNER_CONCURRENCY must be between 1 and 32")
	}
	if cfg.NotificationEmailConcurrency < 1 || cfg.NotificationEmailConcurrency > 32 {
		return Config{}, fmt.Errorf("NOTIFICATION_EMAIL_CONCURRENCY must be between 1 and 32")
	}
	if cfg.WelcomeEmailConcurrency < 1 || cfg.WelcomeEmailConcurrency > 32 {
		return Config{}, fmt.Errorf("WELCOME_EMAIL_CONCURRENCY must be between 1 and 32")
	}
	if cfg.WhatsAppWorkerConcurrency < 1 || cfg.WhatsAppWorkerConcurrency > 32 {
		return Config{}, fmt.Errorf("WHATSAPP_WORKER_CONCURRENCY must be between 1 and 32")
	}
	if cfg.NotificationEmailTimeout < time.Second || cfg.NotificationEmailTimeout > 2*time.Minute {
		return Config{}, fmt.Errorf("NOTIFICATION_EMAIL_TIMEOUT must be between 1s and 2m")
	}
	if cfg.WelcomeEmailTimeout < time.Second || cfg.WelcomeEmailTimeout > 2*time.Minute {
		return Config{}, fmt.Errorf("WELCOME_EMAIL_TIMEOUT must be between 1s and 2m")
	}
	if (cfg.NotificationEmailEnabled || cfg.NotificationWhatsAppEnabled) && runsNotificationWorkers {
		if len(cfg.NotificationDestinationHMACKey) < 32 {
			return Config{}, fmt.Errorf("NOTIFICATION_DESTINATION_HMAC_KEY must be at least 32 bytes when outbound notifications are enabled")
		}
	}
	if cfg.NotificationEmailEnabled && runsNotificationWorkers && !cfg.SMTPConfigured() {
		return Config{}, fmt.Errorf("SMTP_USERNAME and SMTP_PASSWORD are required when email notifications are enabled")
	}
	if cfg.WelcomeEmailEnabled && runsNotificationWorkers && !cfg.SMTPConfigured() {
		return Config{}, fmt.Errorf("SMTP_USERNAME and SMTP_PASSWORD are required when welcome emails are enabled")
	}
	if cfg.PaymentsEnvironment != "" && cfg.PaymentsEnvironment != "test" && cfg.PaymentsEnvironment != "live" {
		return Config{}, fmt.Errorf("PAYMENTS_ENVIRONMENT must be test or live")
	}
	if (cfg.PayazaPublicKey == "") != (cfg.PayazaSecretKey == "") {
		return Config{}, fmt.Errorf("PAYAZA_PUBLIC_KEY and PAYAZA_SECRET_KEY must be configured together")
	}
	if (cfg.PayazaPublicKeyTest == "") != (cfg.PayazaSecretKeyTest == "") {
		return Config{}, fmt.Errorf("PAYAZA_PUBLIC_KEY_TEST and PAYAZA_SECRET_KEY_TEST must be configured together")
	}
	if (cfg.PayazaNGNDVABankCode == "") != (cfg.PayazaNGNDVAEnquiryBankCode == "") {
		return Config{}, fmt.Errorf("PAYAZA_NGN_DVA_BANK_CODE and PAYAZA_NGN_DVA_ENQUIRY_BANK_CODE must be configured together")
	}
	if cfg.PayazaNGNDVABankCode != "" && cfg.PayazaNGNDVABankCode != "1067" && cfg.PayazaNGNDVABankCode != "140" {
		return Config{}, fmt.Errorf("PAYAZA_NGN_DVA_BANK_CODE must be 1067 or 140")
	}
	if cfg.PayazaTransferPIN != "" && !isSixDigitPIN(cfg.PayazaTransferPIN) {
		return Config{}, fmt.Errorf("PAYAZA_TRANSFER_PIN must be a six-digit positive integer without a leading zero")
	}
	if cfg.PayazaTransferPINTest != "" && !isSixDigitPIN(cfg.PayazaTransferPINTest) {
		return Config{}, fmt.Errorf("PAYAZA_TRANSFER_PIN_TEST must be a six-digit positive integer without a leading zero")
	}
	senderFields := 0
	for _, value := range []string{cfg.PayazaPayoutSenderName, cfg.PayazaPayoutSenderPhone, cfg.PayazaPayoutSenderAddress} {
		if value != "" {
			senderFields++
		}
	}
	if senderFields != 0 && senderFields != 3 {
		return Config{}, fmt.Errorf("PAYAZA_PAYOUT_SENDER_NAME, PAYAZA_PAYOUT_SENDER_PHONE, and PAYAZA_PAYOUT_SENDER_ADDRESS must be configured together")
	}
	if cfg.PaystackSecretKey != "" && !strings.HasPrefix(cfg.PaystackSecretKey, "sk_live_") {
		return Config{}, fmt.Errorf("PAYSTACK_SECRET_KEY must use the sk_live_ prefix")
	}
	if cfg.PaystackSecretKeyTest != "" && !strings.HasPrefix(cfg.PaystackSecretKeyTest, "sk_test_") {
		return Config{}, fmt.Errorf("PAYSTACK_SECRET_KEY_TEST must use the sk_test_ prefix")
	}
	for key, value := range map[string]string{
		"PAYAZA_SOURCE_ACCOUNTS":      cfg.PayazaSourceAccounts,
		"PAYAZA_SOURCE_ACCOUNTS_TEST": cfg.PayazaSourceAccountsTest,
	} {
		if value != "" {
			if _, err := parsePayazaSourceAccountMap(key, value); err != nil {
				return Config{}, err
			}
		}
	}
	financialSecurityValues := 0
	for _, value := range []string{cfg.FinancialEncryptionKeys, cfg.FinancialActiveKey, cfg.FinancialFingerprintKey} {
		if value != "" {
			financialSecurityValues++
		}
	}
	if financialSecurityValues != 0 && financialSecurityValues != 3 {
		return Config{}, fmt.Errorf("financial data encryption keyring, active version, and fingerprint key must be configured together")
	}
	if financialSecurityValues == 3 {
		if _, err := secure.ParseKeyring(cfg.FinancialEncryptionKeys, cfg.FinancialActiveKey); err != nil {
			return Config{}, fmt.Errorf("validate financial data encryption keys: %w", err)
		}
		if _, err := secure.NewFingerprinter(cfg.FinancialFingerprintKey); err != nil {
			return Config{}, fmt.Errorf("validate financial data fingerprint key: %w", err)
		}
	}
	providerFinancialFeaturesEnabled := cfg.AnyPaymentCapabilityEnabled()
	if providerFinancialFeaturesEnabled && financialSecurityValues != 3 {
		return Config{}, fmt.Errorf("financial data encryption must be configured before enabling verified payment providers")
	}
	if (cfg.AgreementTokenEncryptionKeys == "") != (cfg.AgreementTokenActiveKey == "") {
		return Config{}, fmt.Errorf("agreement token encryption keyring and active key must be configured together")
	}
	if cfg.AgreementTokenEncryptionKeys != "" {
		if _, err := secure.ParseKeyring(cfg.AgreementTokenEncryptionKeys, cfg.AgreementTokenActiveKey); err != nil {
			return Config{}, fmt.Errorf("validate agreement token encryption keys: %w", err)
		}
	}

	if cfg.AuthBcryptCost < 10 {
		cfg.AuthBcryptCost = 10
	}
	if cfg.HTTPRateLimitPerMinute <= 0 || cfg.HTTPRateLimitBurst <= 0 ||
		cfg.AIRateLimitPerMinute <= 0 || cfg.AIRateLimitBurst <= 0 ||
		cfg.LocationRateLimitPerMinute <= 0 || cfg.LocationRateLimitBurst <= 0 ||
		cfg.MarketplaceAuthRateLimitPerMinute <= 0 || cfg.MarketplaceAuthRateLimitBurst <= 0 {
		return Config{}, fmt.Errorf("rate-limit values must be positive")
	}

	return cfg, nil
}

func (c Config) AnyPaymentCapabilityEnabled() bool {
	return c.PayazaCardSandboxVerified || c.PayazaCardProductionEnabled ||
		c.PayazaBankTransferSandboxVerified || c.PayazaBankTransferProductionEnabled ||
		c.PayazaDestinationSandboxVerified || c.PayazaDestinationProductionEnabled ||
		c.PayazaPayoutSandboxVerified || c.PayazaPayoutProductionEnabled ||
		c.PaystackCardSandboxVerified || c.PaystackCardProductionEnabled ||
		c.PaystackBankTransferSandboxVerified || c.PaystackBankTransferProductionEnabled ||
		c.PaystackDestinationSandboxVerified || c.PaystackDestinationProductionEnabled ||
		c.PaystackPayoutSandboxVerified || c.PaystackPayoutProductionEnabled
}

func (c Config) MetaWebhookConfigured() bool {
	return c.MetaAppSecret != "" && c.MetaVerifyToken != "" &&
		c.WhatsAppBusinessAccountID != "" && c.WABAPhoneNumberID != ""
}

func (c Config) WhatsAppSendConfigured() bool {
	return c.WABAToken != "" && c.WhatsAppBusinessAccountID != "" && c.WABAPhoneNumberID != ""
}

func (c Config) SMTPConfigured() bool {
	return strings.TrimSpace(c.SMTPUsername) != "" && strings.TrimSpace(c.SMTPPassword) != ""
}

func (c Config) NotificationContactFoundationConfigured() bool {
	return len(c.NotificationDestinationHMACKey) >= 32 && validE164(c.WABABusinessPhoneE164)
}

func validE164(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 9 || len(value) > 16 || value[0] != '+' || value[1] == '0' {
		return false
	}
	for _, character := range value[1:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

// MetaWhatsAppConfigured reports whether this process has both independent
// webhook-ingress and outbound-send credentials plus the app identity used by
// operational subscription checks.
func (c Config) MetaWhatsAppConfigured() bool {
	return c.MetaAppID != "" && c.MetaWebhookConfigured() && c.WhatsAppSendConfigured()
}

func (c Config) NeedsSelfHosted() bool {
	return c.DefaultAIProvider == AIProviderSelfHosted ||
		c.AgreementAIProvider == AIProviderSelfHosted ||
		(c.TessaAIEnabled && (c.TessaAIPrimaryProvider == AIProviderSelfHosted ||
			c.TessaAIFallbackProvider == AIProviderSelfHosted))
}

func (c Config) NeedsHosted() bool {
	return c.DefaultAIProvider == AIProviderHosted ||
		c.AgreementAIProvider == AIProviderHosted ||
		(c.TessaAIEnabled && (c.TessaAIPrimaryProvider == AIProviderHosted ||
			c.TessaAIFallbackProvider == AIProviderHosted))
}

func (c Config) NeedsOpenAICompatible() bool {
	return c.DefaultAIProvider == AIProviderOpenAICompatible ||
		c.AgreementAIProvider == AIProviderOpenAICompatible ||
		(c.TessaAIEnabled && (c.TessaAIPrimaryProvider == AIProviderOpenAICompatible ||
			c.TessaAIFallbackProvider == AIProviderOpenAICompatible))
}

func (c Config) AIModelName(provider string) string {
	switch provider {
	case "":
		return ""
	case AIProviderHosted:
		return c.OpenAIModel
	case AIProviderOpenAICompatible:
		return c.OpenAICompatModel
	default:
		return c.LLMModel
	}
}

func (c Config) TessaAIConfigHash() string {
	type providerSettings struct {
		Provider        string   `json:"provider"`
		Model           string   `json:"model"`
		Path            string   `json:"path,omitempty"`
		Temperature     float64  `json:"temperature,omitempty"`
		TopP            float64  `json:"top_p,omitempty"`
		TopK            int      `json:"top_k,omitempty"`
		MinP            float64  `json:"min_p,omitempty"`
		PresencePenalty float64  `json:"presence_penalty,omitempty"`
		RepeatPenalty   float64  `json:"repeat_penalty,omitempty"`
		Thinking        bool     `json:"thinking,omitempty"`
		Reasoning       string   `json:"reasoning,omitempty"`
		TokenLimitField string   `json:"token_limit_field,omitempty"`
		OptionalTemp    *float64 `json:"optional_temperature,omitempty"`
		OptionalTopP    *float64 `json:"optional_top_p,omitempty"`
	}
	providerConfig := func(provider string) providerSettings {
		settings := providerSettings{Provider: provider, Model: c.AIModelName(provider)}
		switch provider {
		case AIProviderSelfHosted:
			settings.Path = c.LLMChatCompletions
			settings.Temperature = c.LLMTemperature
			settings.TopP = c.LLMTopP
			settings.TopK = c.LLMTopK
			settings.MinP = c.LLMMinP
			settings.PresencePenalty = c.LLMPresencePenalty
			settings.RepeatPenalty = c.LLMRepetitionPenalty
			settings.Thinking = c.SelfHostedThinking
		case AIProviderHosted:
			settings.Reasoning = c.OpenAIReasoningEffort
		case AIProviderOpenAICompatible:
			settings.Path = c.OpenAICompatChatCompletions
			settings.TokenLimitField = c.OpenAICompatTokenLimitField
			settings.OptionalTemp = c.OpenAICompatTemperature
			settings.OptionalTopP = c.OpenAICompatTopP
		}
		return settings
	}
	settings := struct {
		Primary         providerSettings  `json:"primary"`
		Fallback        *providerSettings `json:"fallback,omitempty"`
		PrimaryTimeout  time.Duration     `json:"primary_timeout"`
		FallbackTimeout time.Duration     `json:"fallback_timeout,omitempty"`
		TurnTimeout     time.Duration     `json:"turn_timeout"`
		MaxInputTokens  int               `json:"max_input_tokens"`
		MaxOutputTokens int               `json:"max_output_tokens"`
		NoticeRevision  string            `json:"notice_revision"`
		SchemaRevision  string            `json:"schema_revision"`
	}{
		Primary:         providerConfig(c.TessaAIPrimaryProvider),
		PrimaryTimeout:  c.TessaAIPrimaryRequestTimeout,
		TurnTimeout:     c.TessaAITurnTimeout,
		MaxInputTokens:  c.TessaAIMaxInputTokens,
		MaxOutputTokens: c.TessaAIMaxOutputTokens,
		NoticeRevision:  c.TessaAINoticeRevision,
		SchemaRevision:  tessaconfig.SchemaRevision,
	}
	if c.TessaAIFallbackProvider != "" {
		fallback := providerConfig(c.TessaAIFallbackProvider)
		settings.Fallback = &fallback
		settings.FallbackTimeout = c.TessaAIFallbackRequestTimeout
	}
	payload, _ := json.Marshal(settings)
	hash := sha256.Sum256(payload)
	return fmt.Sprintf("%x", hash[:])
}

func (c Config) DefaultAIModelName() string {
	return c.AIModelName(c.DefaultAIProvider)
}

func (c Config) SynchronousAIRouteTimeout() time.Duration {
	timeout := c.LLMTimeout
	if c.DefaultAIProvider == AIProviderHosted {
		timeout = c.OpenAITimeout
	} else if c.DefaultAIProvider == AIProviderOpenAICompatible {
		timeout = c.OpenAICompatTimeout
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return timeout + 5*time.Second
}

func normalizeAIProvider(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validateAIProvider(name, value string) error {
	switch value {
	case AIProviderSelfHosted, AIProviderHosted, AIProviderOpenAICompatible:
		return nil
	default:
		return fmt.Errorf("%s must be one of self_hosted, hosted, or openai_compatible", name)
	}
}

func isExternalAIProvider(value string) bool {
	return value == AIProviderHosted || value == AIProviderOpenAICompatible
}

func validReasoningEffort(value string) bool {
	switch value {
	case "none", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func validatePublicBaseURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	return nil
}

func validateOpenAICompatibleBaseURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" {
		return fmt.Errorf("must use HTTPS outside loopback development")
	}
	host := strings.TrimSpace(parsed.Hostname())
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if address := net.ParseIP(host); address != nil && address.IsLoopback() {
		return nil
	}
	return fmt.Errorf("must use HTTPS outside loopback development")
}

func validateOpenAICompatiblePath(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.Path == "" {
		return fmt.Errorf("must be a path without scheme, host, query, or fragment")
	}
	return nil
}

func isDecimalIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func validGraphVersion(value string) bool {
	parts := strings.Split(strings.TrimPrefix(value, "v"), ".")
	if !strings.HasPrefix(value, "v") || len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if !isDecimalIdentifier(part) {
			return false
		}
	}
	return true
}

func (c Config) PaystackEnabled() bool {
	return c.PaystackCredentials() != ""
}

func (c Config) PaystackCredentials() string {
	if c.PaymentsEnvironment == "live" {
		return c.PaystackSecretKey
	}
	return c.PaystackSecretKeyTest
}

func (c Config) PayazaEnabled() bool {
	publicKey, secretKey := c.PayazaCredentials()
	return publicKey != "" && secretKey != ""
}

func (c Config) PayazaLiveEnabled() bool {
	return c.PayazaPublicKey != "" && c.PayazaSecretKey != ""
}

func (c Config) PayazaCredentials() (string, string) {
	if c.PaymentsEnvironment == "live" {
		return c.PayazaPublicKey, c.PayazaSecretKey
	}
	return c.PayazaPublicKeyTest, c.PayazaSecretKeyTest
}

func (c Config) PayazaActiveTransferPIN() string {
	if c.PaymentsEnvironment == "live" {
		return c.PayazaTransferPIN
	}
	return c.PayazaTransferPINTest
}

func (c Config) PayazaPayoutSenderConfigured() bool {
	return c.PayazaPayoutSenderName != "" && c.PayazaPayoutSenderPhone != "" && c.PayazaPayoutSenderAddress != ""
}

func (c Config) PayazaSourceAccountMap() (map[string]string, error) {
	key := "PAYAZA_SOURCE_ACCOUNTS_TEST"
	value := c.PayazaSourceAccountsTest
	if c.PaymentsEnvironment == "live" {
		key = "PAYAZA_SOURCE_ACCOUNTS"
		value = c.PayazaSourceAccounts
	}
	return parsePayazaSourceAccountMap(key, value)
}

func parsePayazaSourceAccountMap(key, value string) (map[string]string, error) {
	if strings.TrimSpace(value) == "" {
		return map[string]string{}, nil
	}
	var accounts map[string]string
	if err := json.Unmarshal([]byte(value), &accounts); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object of currency to account reference", key)
	}
	cleaned := make(map[string]string, len(accounts))
	for currency, reference := range accounts {
		currency = strings.ToUpper(strings.TrimSpace(currency))
		reference = strings.TrimSpace(reference)
		if len(currency) != 3 || reference == "" {
			return nil, fmt.Errorf("%s contains an invalid currency or account reference", key)
		}
		cleaned[currency] = reference
	}
	return cleaned, nil
}

func NewLogger(appEnv string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}

	if strings.EqualFold(appEnv, "development") {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}

	return parsed
}

func getEnvFloat(key string, fallback float64) float64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}

	return parsed
}

func getOptionalEnvFloat(key string) (*float64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return nil, fmt.Errorf("%s must be a number", key)
	}
	return &parsed, nil
}

func finiteFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func getEnvDurationStrict(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration", key)
	}
	return parsed, nil
}

func getEnvIntStrict(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return parsed, nil
}

func getEnvInt64(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}

	return parsed
}

func getEnvBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}

	return parsed
}

func getEnvInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}

	return parsed
}

func isSixDigitPIN(value string) bool {
	if len(value) != 6 || value[0] == '0' {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func validRedisKeyPrefix(value string) bool {
	if len(value) < 3 || len(value) > 96 || strings.HasPrefix(value, ":") || strings.HasSuffix(value, ":") {
		return false
	}
	for _, part := range strings.Split(value, ":") {
		if part == "" {
			return false
		}
		for _, character := range part {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}

	parts := strings.Split(value, ",")
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			filtered = append(filtered, part)
		}
	}

	return filtered
}
