package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"booking/go-server/internal/secure"
)

type Config struct {
	AppEnv                                string
	HTTPAddr                              string
	ClientPublicBaseURL                   string
	DefaultAIProvider                     string
	AgreementAIProvider                   string
	InboxAIProvider                       string
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
	HTTPRateLimitPerMinute                int
	HTTPRateLimitBurst                    int
	AIRateLimitPerMinute                  int
	AIRateLimitBurst                      int
	LocationRateLimitPerMinute            int
	LocationRateLimitBurst                int
	GoogleMapsServerAPIKey                string
	DatabaseURL                           string
	CORSOrigins                           []string
	AuthIssuer                            string
	AuthAccessTokenSecret                 string
	AuthAccessTokenTTL                    time.Duration
	AuthAccessCookieName                  string
	AuthRefreshCookieName                 string
	AuthCookieDomain                      string
	AuthCookieSecure                      bool
	AuthRefreshTokenTTL                   time.Duration
	AuthBcryptCost                        int
	R2PrivateBucketName                   string
	R2PublicBucketName                    string
	R2AccountID                           string
	R2Endpoint                            string
	R2AccessKeyID                         string
	R2SecretAccessKey                     string
	R2PublicBucketBaseURL                 string
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
	AIProviderSelfHosted = "self_hosted"
	AIProviderHosted     = "hosted"
	HostedProviderOpenAI = "openai"
)

func Load() (Config, error) {
	cfg := Config{
		AppEnv:                                getEnv("APP_ENV", "development"),
		HTTPAddr:                              getEnv("HTTP_ADDR", ":8000"),
		ClientPublicBaseURL:                   strings.TrimRight(getEnv("CLIENT_PUBLIC_BASE_URL", "http://localhost:5275"), "/"),
		DefaultAIProvider:                     normalizeAIProvider(getEnv("DEFAULT_AI_PROVIDER", AIProviderSelfHosted)),
		AgreementAIProvider:                   normalizeAIProvider(getEnv("AGREEMENT_AI_PROVIDER", AIProviderHosted)),
		InboxAIProvider:                       normalizeAIProvider(getEnv("INBOX_AI_PROVIDER", AIProviderSelfHosted)),
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
		HTTPRateLimitPerMinute:                getEnvInt("HTTP_RATE_LIMIT_PER_MINUTE", 300),
		HTTPRateLimitBurst:                    getEnvInt("HTTP_RATE_LIMIT_BURST", 100),
		AIRateLimitPerMinute:                  getEnvInt("AI_RATE_LIMIT_PER_MINUTE", 12),
		AIRateLimitBurst:                      getEnvInt("AI_RATE_LIMIT_BURST", 4),
		LocationRateLimitPerMinute:            getEnvInt("LOCATION_RATE_LIMIT_PER_MINUTE", 20),
		LocationRateLimitBurst:                getEnvInt("LOCATION_RATE_LIMIT_BURST", 5),
		GoogleMapsServerAPIKey:                strings.TrimSpace(os.Getenv("GOOGLE_MAPS_SERVER_API_KEY")),
		DatabaseURL:                           strings.TrimSpace(os.Getenv("DATABASE_URL")),
		CORSOrigins:                           splitCSV(getEnv("CORS_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173")),
		AuthIssuer:                            getEnv("AUTH_ISSUER", "booking-api"),
		AuthAccessTokenSecret:                 strings.TrimSpace(os.Getenv("AUTH_ACCESS_TOKEN_SECRET")),
		AuthAccessTokenTTL:                    getEnvDuration("AUTH_ACCESS_TOKEN_TTL", 15*time.Minute),
		AuthAccessCookieName:                  getEnv("AUTH_ACCESS_COOKIE_NAME", "booking_access"),
		AuthRefreshCookieName:                 getEnv("AUTH_REFRESH_COOKIE_NAME", "booking_refresh"),
		AuthCookieDomain:                      strings.TrimSpace(os.Getenv("AUTH_COOKIE_DOMAIN")),
		AuthCookieSecure:                      getEnvBool("AUTH_COOKIE_SECURE", false),
		AuthRefreshTokenTTL:                   getEnvDuration("AUTH_REFRESH_TOKEN_TTL", 24*30*time.Hour),
		AuthBcryptCost:                        getEnvInt("AUTH_BCRYPT_COST", 12),
		R2PrivateBucketName:                   strings.TrimSpace(os.Getenv("R2_PRIVATE_BUCKET_NAME")),
		R2PublicBucketName:                    strings.TrimSpace(os.Getenv("R2_PUBLIC_BUCKET_NAME")),
		R2AccountID:                           strings.TrimSpace(os.Getenv("R2_ACCOUNT_ID")),
		R2Endpoint:                            strings.TrimSpace(os.Getenv("R2_ENDPOINT")),
		R2AccessKeyID:                         strings.TrimSpace(os.Getenv("R2_ACCESS_KEY_ID")),
		R2SecretAccessKey:                     strings.TrimSpace(os.Getenv("R2_SECRET_ACCESS_KEY")),
		R2PublicBucketBaseURL:                 strings.TrimSpace(os.Getenv("R2_PUBLIC_BUCKET_BASE_URL")),
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
	if cfg.AuthAccessTokenSecret == "" {
		return Config{}, fmt.Errorf("AUTH_ACCESS_TOKEN_SECRET is required")
	}
	for _, setting := range []struct {
		name  string
		value string
	}{
		{name: "DEFAULT_AI_PROVIDER", value: cfg.DefaultAIProvider},
		{name: "AGREEMENT_AI_PROVIDER", value: cfg.AgreementAIProvider},
		{name: "INBOX_AI_PROVIDER", value: cfg.InboxAIProvider},
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
	if err := validatePublicBaseURL(cfg.ClientPublicBaseURL); err != nil {
		return Config{}, fmt.Errorf("CLIENT_PUBLIC_BASE_URL: %w", err)
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
		cfg.LocationRateLimitPerMinute <= 0 || cfg.LocationRateLimitBurst <= 0 {
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

func (c Config) NeedsSelfHosted() bool {
	return c.DefaultAIProvider == AIProviderSelfHosted ||
		c.AgreementAIProvider == AIProviderSelfHosted ||
		c.InboxAIProvider == AIProviderSelfHosted
}

func (c Config) NeedsHosted() bool {
	return c.DefaultAIProvider == AIProviderHosted ||
		c.AgreementAIProvider == AIProviderHosted ||
		c.InboxAIProvider == AIProviderHosted
}

func (c Config) SynchronousAIRouteTimeout() time.Duration {
	timeout := time.Duration(0)
	for _, provider := range []string{c.DefaultAIProvider, c.InboxAIProvider} {
		providerTimeout := c.LLMTimeout
		if provider == AIProviderHosted {
			providerTimeout = c.OpenAITimeout
		}
		if providerTimeout > timeout {
			timeout = providerTimeout
		}
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
	case AIProviderSelfHosted, AIProviderHosted:
		return nil
	default:
		return fmt.Errorf("%s must be one of self_hosted or hosted", name)
	}
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
