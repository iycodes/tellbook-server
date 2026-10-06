package config

import (
	"strings"
	"testing"
	"time"
)

func setRequiredConfig(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("PROCESS_ROLE", ProcessRoleAll)
	t.Setenv("DATABASE_URL", "postgres://example.invalid/database")
	for _, key := range []string{
		"DATABASE_DIRECT_URL", "DATABASE_MAX_CONNECTIONS", "DATABASE_MIN_CONNECTIONS",
		"DATABASE_DIRECT_MAX_CONNECTIONS", "DATABASE_MAX_CONNECTION_LIFETIME",
		"DATABASE_MAX_CONNECTION_LIFETIME_JITTER", "DATABASE_MAX_CONNECTION_IDLE_TIME",
		"DATABASE_HEALTH_CHECK_PERIOD", "DATABASE_CONNECT_TIMEOUT",
		"DATABASE_STATEMENT_TIMEOUT", "DATABASE_LOCK_TIMEOUT",
		"DATABASE_IDLE_TRANSACTION_TIMEOUT",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("DATABASE_DIRECT_URL", "postgres://example.invalid/database")
	t.Setenv("AUTH_ACCESS_TOKEN_SECRET", "test-secret")
	for _, key := range []string{
		"R2_PRIVATE_BUCKET_NAME", "R2_PUBLIC_BUCKET_NAME", "R2_ACCOUNT_ID", "R2_ENDPOINT",
		"R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "R2_PUBLIC_BUCKET_BASE_URL",
	} {
		t.Setenv(key, "")
	}
	for _, key := range []string{
		"META_APP_ID", "META_APP_SECRET", "META_VERIFY_TOKEN", "WABA_TOKEN",
		"WHATSAPP_BUSINESS_ACCOUNT_ID", "WABA_PHONE_NUMBER_ID",
		"NOTIFICATION_DESTINATION_HMAC_KEY", "WHATSAPP_ENABLED_TEMPLATE_KEYS",
		"SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_FROM_EMAIL", "SUPPORT_EMAIL",
		"AUTH_DELIVERY_ENCRYPTION_KEYS", "AUTH_DELIVERY_ACTIVE_KEY", "AUTH_DESTINATION_HMAC_KEY",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("NOTIFICATION_EMAIL_ENABLED", "false")
	t.Setenv("AUTH_EMAIL_ENABLED", "false")
	t.Setenv("AUTH_WHATSAPP_ENABLED", "false")
	t.Setenv("AUTH_DELIVERY_CONCURRENCY", "4")
	t.Setenv("AUTH_DELIVERY_TIMEOUT", "30s")
	t.Setenv("NOTIFICATION_EMAIL_CONCURRENCY", "4")
	t.Setenv("NOTIFICATION_EMAIL_TIMEOUT", "30s")
	t.Setenv("WELCOME_EMAIL_ENABLED", "false")
	t.Setenv("WELCOME_EMAIL_CONCURRENCY", "2")
	t.Setenv("WELCOME_EMAIL_TIMEOUT", "30s")
	t.Setenv("NOTIFICATION_WHATSAPP_ENABLED", "false")
	t.Setenv("WHATSAPP_WORKER_CONCURRENCY", "4")
	t.Setenv("NOTIFICATION_PLANNER_CONCURRENCY", "4")
	t.Setenv("WHATSAPP_GRAPH_BASE_URL", "https://graph.facebook.com")
	t.Setenv("WHATSAPP_GRAPH_VERSION", "v24.0")
	t.Setenv("WHATSAPP_HTTP_TIMEOUT", "15s")
	t.Setenv("REDIS_URL", "")
	t.Setenv("REDIS_KEY_PREFIX", "tellbook:test:v1")
	t.Setenv("REDIS_KEY_HMAC_SECRET", "")
	for _, key := range []string{
		"REDIS_POOL_SIZE", "REDIS_MIN_IDLE_CONNECTIONS", "REDIS_DIAL_TIMEOUT",
		"REDIS_READ_TIMEOUT", "REDIS_WRITE_TIMEOUT", "REDIS_POOL_TIMEOUT",
		"REDIS_MAX_PAYLOAD_BYTES", "REDIS_FALLBACK_MAX_CONCURRENCY",
		"RATE_LIMIT_IP_CEILING_MULTIPLIER",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("DEFAULT_AI_PROVIDER", AIProviderSelfHosted)
	t.Setenv("AGREEMENT_AI_PROVIDER", AIProviderSelfHosted)
	t.Setenv("HOSTED_AI_PROVIDER", HostedProviderOpenAI)
	t.Setenv("LLM_BASE_URL", "http://127.0.0.1:8080")
	t.Setenv("LLM_TIMEOUT", "30s")
	t.Setenv("LLM_MAX_OUTPUT_TOKENS", "1200")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_COMPAT_BASE_URL", "")
	t.Setenv("OPENAI_COMPAT_MODEL", "")
	t.Setenv("OPENAI_COMPAT_API_KEY", "")
	t.Setenv("OPENAI_COMPAT_CHAT_COMPLETIONS_PATH", "/v1/chat/completions")
	t.Setenv("OPENAI_COMPAT_TIMEOUT", "60s")
	t.Setenv("OPENAI_COMPAT_MAX_OUTPUT_TOKENS", "1600")
	t.Setenv("OPENAI_COMPAT_TOKEN_LIMIT_FIELD", OpenAICompatTokenFieldMaxTokens)
	t.Setenv("OPENAI_COMPAT_TEMPERATURE", "")
	t.Setenv("OPENAI_COMPAT_TOP_P", "")
	t.Setenv("TESSA_AI_ENABLED", "false")
	t.Setenv("TESSA_WHATSAPP_LINKING_ENABLED", "false")
	t.Setenv("TESSA_WHATSAPP_CONVERSATIONS_ENABLED", "false")
	t.Setenv("TESSA_AI_PRIMARY_PROVIDER", AIProviderSelfHosted)
	t.Setenv("TESSA_AI_FALLBACK_PROVIDER", "")
	t.Setenv("TESSA_AI_EXTERNAL_PROCESSING_APPROVED", "false")
	t.Setenv("TESSA_AI_PRIMARY_REQUEST_TIMEOUT", "12s")
	t.Setenv("TESSA_AI_FALLBACK_REQUEST_TIMEOUT", "20s")
	t.Setenv("TESSA_AI_TURN_TIMEOUT", "60s")
	t.Setenv("TESSA_AI_WORKER_CONCURRENCY", "2")
	t.Setenv("TESSA_AI_MAX_INPUT_TOKENS", "12000")
	t.Setenv("TESSA_AI_MAX_OUTPUT_TOKENS", "1600")
	t.Setenv("TESSA_AI_NOTICE_REVISION", "2026-08-30")
	t.Setenv("PAYMENTS_ENVIRONMENT", "")
	t.Setenv("FINANCIAL_DATA_ENCRYPTION_KEYS", "")
	t.Setenv("FINANCIAL_DATA_ACTIVE_KEY_VERSION", "")
	t.Setenv("FINANCIAL_DATA_FINGERPRINT_KEY", "")
	t.Setenv("AGREEMENT_TOKEN_ENCRYPTION_KEYS", "")
	t.Setenv("AGREEMENT_TOKEN_ACTIVE_KEY", "")
	t.Setenv("PAYAZA_PUBLIC_KEY", "")
	t.Setenv("PAYAZA_SECRET_KEY", "")
	t.Setenv("PAYAZA_PUBLIC_KEY_TEST", "")
	t.Setenv("PAYAZA_SECRET_KEY_TEST", "")
	t.Setenv("PAYAZA_TRANSFER_PIN", "")
	t.Setenv("PAYAZA_TRANSFER_PIN_TEST", "")
	t.Setenv("PAYAZA_SOURCE_ACCOUNTS", "")
	t.Setenv("PAYAZA_SOURCE_ACCOUNTS_TEST", "")
	t.Setenv("PAYAZA_PAYOUT_SENDER_NAME", "")
	t.Setenv("PAYAZA_PAYOUT_SENDER_PHONE", "")
	t.Setenv("PAYAZA_PAYOUT_SENDER_ADDRESS", "")
	t.Setenv("PAYAZA_NGN_DVA_BANK_CODE", "")
	t.Setenv("PAYAZA_NGN_DVA_ENQUIRY_BANK_CODE", "")
	t.Setenv("PAYSTACK_SECRET_KEY", "")
	t.Setenv("PAYSTACK_SECRET_KEY_TEST", "")
	for _, key := range []string{
		"PAYAZA_CARD_SANDBOX_VERIFIED", "PAYAZA_CARD_PRODUCTION_ENABLED",
		"PAYAZA_BANK_TRANSFER_SANDBOX_VERIFIED", "PAYAZA_BANK_TRANSFER_PRODUCTION_ENABLED",
		"PAYAZA_DESTINATION_SANDBOX_VERIFIED", "PAYAZA_DESTINATION_PRODUCTION_ENABLED",
		"PAYAZA_PAYOUT_SANDBOX_VERIFIED", "PAYAZA_PAYOUT_PRODUCTION_ENABLED",
		"PAYSTACK_CARD_SANDBOX_VERIFIED", "PAYSTACK_CARD_PRODUCTION_ENABLED",
		"PAYSTACK_BANK_TRANSFER_SANDBOX_VERIFIED", "PAYSTACK_BANK_TRANSFER_PRODUCTION_ENABLED",
		"PAYSTACK_DESTINATION_SANDBOX_VERIFIED", "PAYSTACK_DESTINATION_PRODUCTION_ENABLED",
		"PAYSTACK_PAYOUT_SANDBOX_VERIFIED", "PAYSTACK_PAYOUT_PRODUCTION_ENABLED",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("PAYAZA_ENABLED_CAPABILITIES", "")
	t.Setenv("PAYSTACK_ENABLED_CAPABILITIES", "")
}

func TestLoadValidatesNotificationPlannerConcurrency(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("NOTIFICATION_PLANNER_CONCURRENCY", "0")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "NOTIFICATION_PLANNER_CONCURRENCY") {
		t.Fatalf("Load() invalid notification planner concurrency error = %v", err)
	}
}

func TestLoadValidatesAuthCodeDeliveryConfiguration(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("PROCESS_ROLE", ProcessRoleAPI)
	t.Setenv("AUTH_EMAIL_ENABLED", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AUTH_DESTINATION_HMAC_KEY") {
		t.Fatalf("Load() missing auth HMAC key error = %v", err)
	}
	t.Setenv("AUTH_DESTINATION_HMAC_KEY", strings.Repeat("h", 32))
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AUTH_DELIVERY_ENCRYPTION_KEYS") {
		t.Fatalf("Load() missing auth encryption key error = %v", err)
	}
	t.Setenv("AUTH_DELIVERY_ENCRYPTION_KEYS", `{"v1":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`)
	t.Setenv("AUTH_DELIVERY_ACTIVE_KEY", "v1")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() rejected API auth email configuration: %v", err)
	}

	t.Setenv("AUTH_WHATSAPP_ENABLED", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "API webhook") {
		t.Fatalf("Load() enabled API WhatsApp auth without webhook readiness: %v", err)
	}
	t.Setenv("META_APP_SECRET", "meta-app-secret-at-least-sixteen")
	t.Setenv("META_VERIFY_TOKEN", "verify-token-at-least-sixteen")
	t.Setenv("WHATSAPP_BUSINESS_ACCOUNT_ID", "222222222")
	t.Setenv("WABA_PHONE_NUMBER_ID", "333333333")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() rejected webhook-ready API WhatsApp auth: %v", err)
	}
}

func TestLoadRequiresOutboundReadinessForAuthWhatsAppWorker(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("PROCESS_ROLE", ProcessRoleWorker)
	t.Setenv("AUTH_WHATSAPP_ENABLED", "true")
	t.Setenv("AUTH_DESTINATION_HMAC_KEY", strings.Repeat("h", 32))
	t.Setenv("AUTH_DELIVERY_ENCRYPTION_KEYS", `{"v1":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`)
	t.Setenv("AUTH_DELIVERY_ACTIVE_KEY", "v1")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WhatsApp workers") {
		t.Fatalf("Load() enabled worker WhatsApp auth without sender readiness: %v", err)
	}
	t.Setenv("WABA_TOKEN", "access-token")
	t.Setenv("WHATSAPP_BUSINESS_ACCOUNT_ID", "222222222")
	t.Setenv("WABA_PHONE_NUMBER_ID", "333333333")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() rejected sender-ready worker WhatsApp auth: %v", err)
	}
}

func TestLoadRequiresSMTPForAuthEmailWorker(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("PROCESS_ROLE", ProcessRoleWorker)
	t.Setenv("AUTH_EMAIL_ENABLED", "true")
	t.Setenv("AUTH_DESTINATION_HMAC_KEY", strings.Repeat("h", 32))
	t.Setenv("AUTH_DELIVERY_ENCRYPTION_KEYS", `{"v1":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`)
	t.Setenv("AUTH_DELIVERY_ACTIVE_KEY", "v1")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SMTP_USERNAME") {
		t.Fatalf("Load() auth email worker without SMTP error = %v", err)
	}
}

func TestLoadValidatesMetaWhatsAppConfiguration(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("PROCESS_ROLE", ProcessRoleAPI)
	t.Setenv("META_APP_SECRET", "meta-app-secret-at-least-sixteen")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "API webhook") {
		t.Fatalf("Load() partial webhook configuration error = %v", err)
	}

	t.Setenv("META_VERIFY_TOKEN", "verify-token-at-least-sixteen")
	t.Setenv("WHATSAPP_BUSINESS_ACCOUNT_ID", "222222222")
	t.Setenv("WABA_PHONE_NUMBER_ID", "333333333")
	t.Setenv("NOTIFICATION_DESTINATION_HMAC_KEY", strings.Repeat("k", 32))
	t.Setenv("WABA_BUSINESS_PHONE_E164", "+2348012345678")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() rejected webhook-only API configuration: %v", err)
	}
	if !cfg.MetaWebhookConfigured() || cfg.WhatsAppSendConfigured() {
		t.Fatalf("unexpected API Meta readiness: webhook=%t send=%t", cfg.MetaWebhookConfigured(), cfg.WhatsAppSendConfigured())
	}

	t.Setenv("WHATSAPP_GRAPH_VERSION", "24")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WHATSAPP_GRAPH_VERSION") {
		t.Fatalf("Load() invalid graph version error = %v", err)
	}
	t.Setenv("WHATSAPP_GRAPH_VERSION", "v24.0")

	t.Setenv("META_APP_ID", "123456789")
	t.Setenv("WABA_TOKEN", "access-token")
	cfg, err = Load()
	if err != nil || !cfg.MetaWhatsAppConfigured() {
		t.Fatalf("Load() complete Meta configuration = configured:%t err:%v", cfg.MetaWhatsAppConfigured(), err)
	}

	// A channel flag shared with the API deployment must not require outbound
	// worker credentials on that process. Webhook control-message HMAC material
	// remains required independently.
	t.Setenv("WABA_TOKEN", "")
	t.Setenv("NOTIFICATION_WHATSAPP_ENABLED", "true")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() rejected API role with a worker channel flag: %v", err)
	}
}

func TestLoadValidatesWhatsAppWorkerConfigurationIndependently(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("PROCESS_ROLE", ProcessRoleWorker)
	t.Setenv("WABA_TOKEN", "access-token")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WhatsApp workers") {
		t.Fatalf("Load() partial worker configuration error = %v", err)
	}

	t.Setenv("WHATSAPP_BUSINESS_ACCOUNT_ID", "222222222")
	t.Setenv("WABA_PHONE_NUMBER_ID", "333333333")
	t.Setenv("NOTIFICATION_WHATSAPP_ENABLED", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "NOTIFICATION_DESTINATION_HMAC_KEY") {
		t.Fatalf("Load() missing destination HMAC key error = %v", err)
	}
	t.Setenv("NOTIFICATION_DESTINATION_HMAC_KEY", strings.Repeat("k", 32))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() rejected enabled WhatsApp worker configuration: %v", err)
	}
	if cfg.MetaWebhookConfigured() || !cfg.WhatsAppSendConfigured() {
		t.Fatalf("unexpected worker Meta readiness: webhook=%t send=%t", cfg.MetaWebhookConfigured(), cfg.WhatsAppSendConfigured())
	}
}

func TestLoadValidatesWhatsAppWorkerConcurrency(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("WHATSAPP_WORKER_CONCURRENCY", "0")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WHATSAPP_WORKER_CONCURRENCY") {
		t.Fatalf("Load() invalid WhatsApp worker concurrency error = %v", err)
	}
}

func TestLoadRequiresDestinationHMACForWhatsAppStatusWorker(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("PROCESS_ROLE", ProcessRoleWorker)
	t.Setenv("WHATSAPP_BUSINESS_ACCOUNT_ID", "222222222")
	t.Setenv("WABA_PHONE_NUMBER_ID", "333333333")
	t.Setenv("NOTIFICATION_WHATSAPP_ENABLED", "true")
	t.Setenv("WABA_TOKEN", "access-token")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WhatsApp status processing") {
		t.Fatalf("Load() missing status-worker HMAC error = %v", err)
	}
}

func TestLoadRequiresDestinationHMACForEmailWorker(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("PROCESS_ROLE", ProcessRoleWorker)
	t.Setenv("NOTIFICATION_EMAIL_ENABLED", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "NOTIFICATION_DESTINATION_HMAC_KEY") {
		t.Fatalf("Load() missing email destination HMAC key error = %v", err)
	}
	t.Setenv("NOTIFICATION_DESTINATION_HMAC_KEY", strings.Repeat("k", 32))
	t.Setenv("SMTP_USERNAME", "notifications@example.com")
	t.Setenv("SMTP_PASSWORD", "smtp-password")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() rejected enabled email worker configuration: %v", err)
	}
}

func TestLoadValidatesEmailWorkerLimitsAndSMTP(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("PROCESS_ROLE", ProcessRoleWorker)
	t.Setenv("NOTIFICATION_EMAIL_ENABLED", "true")
	t.Setenv("NOTIFICATION_DESTINATION_HMAC_KEY", strings.Repeat("k", 32))
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SMTP_USERNAME") {
		t.Fatalf("Load() missing SMTP configuration error = %v", err)
	}
	t.Setenv("SMTP_USERNAME", "notifications@example.com")
	t.Setenv("SMTP_PASSWORD", "smtp-password")
	t.Setenv("NOTIFICATION_EMAIL_CONCURRENCY", "0")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "NOTIFICATION_EMAIL_CONCURRENCY") {
		t.Fatalf("Load() invalid email concurrency error = %v", err)
	}
	t.Setenv("NOTIFICATION_EMAIL_CONCURRENCY", "4")
	t.Setenv("NOTIFICATION_EMAIL_TIMEOUT", "500ms")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "NOTIFICATION_EMAIL_TIMEOUT") {
		t.Fatalf("Load() invalid email timeout error = %v", err)
	}
}

func TestLoadValidatesWelcomeEmailWorkerLimitsAndSMTP(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("PROCESS_ROLE", ProcessRoleWorker)
	t.Setenv("WELCOME_EMAIL_ENABLED", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SMTP_USERNAME") {
		t.Fatalf("Load() missing welcome SMTP configuration error = %v", err)
	}
	t.Setenv("SMTP_USERNAME", "welcome@example.com")
	t.Setenv("SMTP_PASSWORD", "smtp-password")
	t.Setenv("WELCOME_EMAIL_CONCURRENCY", "0")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WELCOME_EMAIL_CONCURRENCY") {
		t.Fatalf("Load() invalid welcome concurrency error = %v", err)
	}
	t.Setenv("WELCOME_EMAIL_CONCURRENCY", "2")
	t.Setenv("WELCOME_EMAIL_TIMEOUT", "500ms")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WELCOME_EMAIL_TIMEOUT") {
		t.Fatalf("Load() invalid welcome timeout error = %v", err)
	}
}

func TestLoadValidatesRedisConfiguration(t *testing.T) {
	setRequiredConfig(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RedisKeyPrefix != "tellbook:test:v1" || cfg.RedisPoolSize != 32 ||
		cfg.RedisMaxPayloadBytes != 64*1024 || cfg.RedisFallbackMaxConcurrency != 32 {
		t.Fatalf("unexpected Redis defaults: %+v", cfg)
	}

	t.Setenv("REDIS_URL", "https://cache.example.com")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a non-Redis URL")
	}
	t.Setenv("REDIS_URL", "redis://127.0.0.1:6379/0")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted Redis without a key HMAC secret")
	}
	t.Setenv("REDIS_KEY_HMAC_SECRET", "test-redis-key-hmac-secret-at-least-32-bytes")
	t.Setenv("REDIS_KEY_PREFIX", "tellbook:test:contains_email@example.com")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a key prefix that could contain sensitive data")
	}
}

func TestLoadValidatesPublicMediaConfiguration(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("R2_PUBLIC_BUCKET_NAME", "tellbook-public")
	t.Setenv("R2_PUBLIC_BUCKET_BASE_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a public bucket without its CDN base URL")
	}

	t.Setenv("R2_PUBLIC_BUCKET_BASE_URL", "https://media.tellbook.test")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() rejected a complete public media configuration: %v", err)
	}

	t.Setenv("APP_ENV", "production")
	t.Setenv("PROCESS_ROLE", ProcessRoleWorker)
	t.Setenv("AUTH_COOKIE_DOMAIN", ".tellbook.test")
	t.Setenv("AUTH_COOKIE_SECURE", "true")
	t.Setenv("R2_PUBLIC_BUCKET_BASE_URL", "http://media.tellbook.test")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "R2_PUBLIC_BUCKET_BASE_URL must use HTTPS") {
		t.Fatalf("Load() insecure public media error = %v", err)
	}
}

func TestLoadRequiresRedisForProductionAPI(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PROCESS_ROLE", ProcessRoleAPI)
	t.Setenv("AUTH_COOKIE_DOMAIN", "example.com")
	t.Setenv("AUTH_COOKIE_SECURE", "true")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a production API without Redis")
	}
	t.Setenv("REDIS_URL", "rediss://cache.example.com:6379/0")
	t.Setenv("REDIS_KEY_HMAC_SECRET", "test-redis-key-hmac-secret-at-least-32-bytes")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() rejected valid production Redis configuration: %v", err)
	}
}

func TestLoadValidatesProcessRoleAndProductionIsolation(t *testing.T) {
	setRequiredConfig(t)
	for _, role := range []string{
		ProcessRoleAPI, ProcessRoleWorker, ProcessRoleAIWorker, ProcessRoleMaintenance, ProcessRoleAll,
	} {
		t.Setenv("PROCESS_ROLE", role)
		cfg, err := Load()
		if err != nil || cfg.ProcessRole != role {
			t.Fatalf("Load() role %q = %q, %v", role, cfg.ProcessRole, err)
		}
	}
	t.Setenv("PROCESS_ROLE", "monolith")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an unknown process role")
	}
	t.Setenv("PROCESS_ROLE", ProcessRoleAll)
	t.Setenv("APP_ENV", "production")
	t.Setenv("AUTH_COOKIE_DOMAIN", "example.com")
	t.Setenv("AUTH_COOKIE_SECURE", "true")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted PROCESS_ROLE=all in production")
	}
}

func TestLoadRequiresDirectDatabaseURLInProduction(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PROCESS_ROLE", ProcessRoleWorker)
	t.Setenv("AUTH_COOKIE_DOMAIN", "example.com")
	t.Setenv("AUTH_COOKIE_SECURE", "true")
	t.Setenv("DATABASE_DIRECT_URL", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_DIRECT_URL") {
		t.Fatalf("Load() direct database error = %v", err)
	}
}

func TestLoadValidatesSSEBudgets(t *testing.T) {
	setRequiredConfig(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SSEMaxConnections != 10000 || cfg.SSEMaxConnectionsPerIP != 40 ||
		cfg.PaymentSSEMaxConnectionsPerToken != 6 {
		t.Fatalf("unexpected SSE budgets: %+v", cfg)
	}
	t.Setenv("SSE_MAX_CONNECTIONS", "99")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an undersized SSE budget")
	}
}

func TestLoadSelectsAIProvidersByTask(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("AGREEMENT_AI_PROVIDER", AIProviderHosted)
	t.Setenv("OPENAI_API_KEY", "test-openai-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DefaultAIProvider != AIProviderSelfHosted || cfg.AgreementAIProvider != AIProviderHosted {
		t.Fatalf("unexpected AI provider routing: %+v", cfg)
	}
}

func TestLoadReadsInboxAIDraftFeatureFlag(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("INBOX_AI_DRAFTS_ENABLED", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.InboxAIDraftsEnabled {
		t.Fatal("Load() did not enable inbox AI drafts")
	}
}

func TestLoadReadsAndValidatesInboxAIControls(t *testing.T) {
	setRequiredConfig(t)
	providerID := "10000000-0000-4000-8000-000000000001"
	t.Setenv("INBOX_AI_PROVIDER_ALLOWLIST", providerID)
	t.Setenv("INBOX_AI_AUTOMATION_ENABLED", "true")
	t.Setenv("INBOX_AI_AUTOMATION_PROVIDER_ALLOWLIST", providerID)
	t.Setenv("INBOX_AI_MAX_CONCURRENCY", "3")
	t.Setenv("INBOX_AI_SEMI_PILOT_REPLY_DELAY", "650ms")
	t.Setenv("INBOX_AI_AUTOPILOT_PAYMENT_WINDOW", "45m")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.InboxAIMaxConcurrency != 3 || cfg.InboxAISemiPilotReplyDelay != 650*time.Millisecond ||
		cfg.InboxAIAutopilotPaymentWindow != 45*time.Minute ||
		len(cfg.InboxAIProviderAllowlist) != 1 ||
		cfg.InboxAIProviderAllowlist[0] != providerID || !cfg.InboxAIAutomationEnabled ||
		len(cfg.InboxAIAutomationProviderAllowlist) != 1 ||
		cfg.InboxAIAutomationProviderAllowlist[0] != providerID {
		t.Fatalf("unexpected inbox AI controls: %+v", cfg)
	}
	t.Setenv("INBOX_AI_PROVIDER_ALLOWLIST", "not-a-uuid")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an invalid inbox AI provider allowlist")
	}
	t.Setenv("INBOX_AI_PROVIDER_ALLOWLIST", providerID)
	t.Setenv("INBOX_AI_AUTOMATION_PROVIDER_ALLOWLIST", "not-a-uuid")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an invalid inbox AI automation provider allowlist")
	}
	t.Setenv("INBOX_AI_AUTOMATION_PROVIDER_ALLOWLIST", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() enabled inbox AI automation without an explicit provider allowlist")
	}
	t.Setenv("INBOX_AI_AUTOMATION_ENABLED", "false")
	t.Setenv("INBOX_AI_AUTOPILOT_PAYMENT_WINDOW", "2m")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an unsafe autopilot payment window")
	}
}

func TestLoadReadsAndValidatesTessaAIControls(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("TESSA_AI_ENABLED", "true")
	t.Setenv("TESSA_AI_FALLBACK_PROVIDER", AIProviderHosted)
	t.Setenv("TESSA_AI_EXTERNAL_PROCESSING_APPROVED", "true")
	t.Setenv("OPENAI_API_KEY", "test-openai-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.TessaAIEnabled || cfg.TessaAIPrimaryProvider != AIProviderSelfHosted ||
		cfg.TessaAIFallbackProvider != AIProviderHosted || !cfg.TessaAIExternalProcessingApproved {
		t.Fatalf("unexpected Tessa AI controls: %+v", cfg)
	}

	t.Setenv("TESSA_AI_NOTICE_REVISION", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() enabled Tessa without an explicit notice revision")
	}
	t.Setenv("TESSA_AI_NOTICE_REVISION", "revision-2")
	t.Setenv("TESSA_AI_PRIMARY_PROVIDER", AIProviderHosted)
	t.Setenv("TESSA_AI_FALLBACK_PROVIDER", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an external Tessa primary provider")
	}
	t.Setenv("TESSA_AI_PRIMARY_PROVIDER", AIProviderSelfHosted)
	t.Setenv("TESSA_AI_FALLBACK_PROVIDER", AIProviderHosted)
	t.Setenv("TESSA_AI_EXTERNAL_PROCESSING_APPROVED", "false")
	if _, err := Load(); err == nil {
		t.Fatal("Load() enabled an external Tessa fallback without approval")
	}
}

func TestLoadTessaIgnoresRemovedProviderAllowlist(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("TESSA_AI_ENABLED", "true")
	for _, oldValue := range []string{"", "10000000-0000-4000-8000-000000000001", "obsolete-value"} {
		t.Setenv("TESSA_AI_PROVIDER_ALLOWLIST", oldValue)
		cfg, err := Load()
		if err != nil || !cfg.TessaAIEnabled {
			t.Fatalf("removed account restriction must not affect enabled Tessa: %v", err)
		}
	}
	t.Setenv("TESSA_AI_ENABLED", "false")
	if cfg, err := Load(); err != nil || cfg.TessaAIEnabled {
		t.Fatalf("the global off switch must remain effective: %v", err)
	}
}

func TestTessaWhatsAppConversationRollout(t *testing.T) {
	setRequiredConfig(t)
	cfg, err := Load()
	if err != nil || cfg.TessaWhatsAppConversationsEnabled {
		t.Fatal("chat must default off", err)
	}
	t.Setenv("TESSA_WHATSAPP_CONVERSATIONS_ENABLED", "true")
	if _, err = Load(); err == nil || !strings.Contains(err.Error(), "TESSA_WHATSAPP_LINKING_ENABLED") {
		t.Fatal("chat bypassed linking gate", err)
	}
	t.Setenv("TESSA_AI_ENABLED", "true")
	t.Setenv("TESSA_WHATSAPP_LINKING_ENABLED", "true")
	t.Setenv("META_APP_SECRET", strings.Repeat("a", 32))
	t.Setenv("META_VERIFY_TOKEN", strings.Repeat("b", 32))
	t.Setenv("WHATSAPP_BUSINESS_ACCOUNT_ID", "100")
	t.Setenv("WABA_PHONE_NUMBER_ID", "19990001")
	t.Setenv("WABA_BUSINESS_PHONE_E164", "+2348000000000")
	t.Setenv("WABA_TOKEN", "test-token")
	t.Setenv("NOTIFICATION_DESTINATION_HMAC_KEY", strings.Repeat("k", 32))
	for _, origin := range []string{"http://localhost:5275", "https://user:pass@provider.example.invalid", "https://provider.example.invalid/path"} {
		t.Setenv("CLIENT_PUBLIC_BASE_URL", origin)
		if _, err = Load(); err == nil {
			t.Fatal("unsafe reply origin accepted")
		}
	}
	t.Setenv("CLIENT_PUBLIC_BASE_URL", "https://provider.example.invalid")
	cfg, err = Load()
	if err != nil || !cfg.TessaWhatsAppConversationsEnabled {
		t.Fatal("valid controlled rollout rejected", err)
	}
}

func TestTessaAIConfigHashExcludesCredentialsAndTracksBehavior(t *testing.T) {
	base := Config{
		TessaAIPrimaryProvider: AIProviderSelfHosted, LLMModel: "gemma",
		TessaAIMaxInputTokens: 12000, TessaAIMaxOutputTokens: 1600,
		TessaAIPrimaryRequestTimeout: 12 * time.Second, TessaAITurnTimeout: time.Minute,
		TessaAINoticeRevision: "revision-1", LLMAPIKey: "secret-one",
	}
	credentialChange := base
	credentialChange.LLMAPIKey = "secret-two"
	if base.TessaAIConfigHash() != credentialChange.TessaAIConfigHash() {
		t.Fatal("Tessa config hash changed with credentials")
	}
	unusedProviderChange := base
	unusedProviderChange.OpenAIReasoningEffort = "high"
	unusedProviderChange.OpenAICompatChatCompletions = "/different/path"
	if base.TessaAIConfigHash() != unusedProviderChange.TessaAIConfigHash() {
		t.Fatal("Tessa config hash changed with settings for an unselected provider")
	}
	changes := []Config{base, base, base, base}
	changes[0].TessaAIMaxOutputTokens++
	changes[1].LLMTemperature = 0.35
	changes[2].LLMChatCompletions = "/gateway/chat/completions"
	changes[3].SelfHostedThinking = true
	for index, behaviorChange := range changes {
		if base.TessaAIConfigHash() == behaviorChange.TessaAIConfigHash() {
			t.Fatalf("Tessa config hash ignored behavior-affecting settings case %d", index)
		}
	}
}

func TestInboxAIModelConfigHashExcludesCredentialsAndTracksBehavior(t *testing.T) {
	base := Config{DefaultAIProvider: AIProviderSelfHosted, LLMModel: "gemma", LLMMaxOutputTokens: 800, LLMAPIKey: "secret-one"}
	credentialChange := base
	credentialChange.LLMAPIKey = "secret-two"
	if base.InboxAIModelConfigHash() != credentialChange.InboxAIModelConfigHash() {
		t.Fatal("model config hash changed with credentials")
	}
	behaviorChange := base
	behaviorChange.LLMMaxOutputTokens++
	if base.InboxAIModelConfigHash() == behaviorChange.InboxAIModelConfigHash() {
		t.Fatal("model config hash ignored behavior-affecting settings")
	}
}

func TestLoadBudgetsDatabasePoolForDedicatedListeners(t *testing.T) {
	setRequiredConfig(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DatabaseMaxConnections != 14 || cfg.DatabaseMinConnections != 2 || cfg.DatabaseDirectMaxConnections != 6 {
		t.Fatalf(
			"unexpected database pool budget: max=%d min=%d direct=%d",
			cfg.DatabaseMaxConnections,
			cfg.DatabaseMinConnections,
			cfg.DatabaseDirectMaxConnections,
		)
	}

	t.Setenv("DATABASE_DIRECT_MAX_CONNECTIONS", "5")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a direct pool too small for the role's session connections")
	}
	t.Setenv("DATABASE_DIRECT_MAX_CONNECTIONS", "6")
	t.Setenv("INBOX_AI_MAX_CONCURRENCY", "11")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() coupled AI concurrency to database connections: %v", err)
	}

	t.Setenv("PROCESS_ROLE", ProcessRoleAPI)
	t.Setenv("DATABASE_DIRECT_MAX_CONNECTIONS", "3")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() rejected the API role's exact direct pool budget: %v", err)
	}
	t.Setenv("TESSA_AI_ENABLED", "true")
	t.Setenv("DATABASE_DIRECT_MAX_CONNECTIONS", "3")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an API direct pool without room for the Tessa listener")
	}
}

func TestLoadValidatesDatabaseConnectionLifecycle(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("DATABASE_MAX_CONNECTION_LIFETIME", "30m")
	t.Setenv("DATABASE_MAX_CONNECTION_LIFETIME_JITTER", "5m")
	t.Setenv("DATABASE_MAX_CONNECTION_IDLE_TIME", "5m")
	t.Setenv("DATABASE_HEALTH_CHECK_PERIOD", "30s")
	t.Setenv("DATABASE_CONNECT_TIMEOUT", "5s")
	t.Setenv("DATABASE_STATEMENT_TIMEOUT", "30s")
	t.Setenv("DATABASE_LOCK_TIMEOUT", "5s")
	t.Setenv("DATABASE_IDLE_TRANSACTION_TIMEOUT", "30s")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() rejected valid database lifecycle settings: %v", err)
	}
	t.Setenv("DATABASE_MAX_CONNECTION_LIFETIME_JITTER", "30m")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted lifetime jitter equal to the connection lifetime")
	}
	t.Setenv("DATABASE_MAX_CONNECTION_LIFETIME_JITTER", "5m")
	t.Setenv("DATABASE_LOCK_TIMEOUT", "31s")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a lock timeout longer than the statement timeout")
	}
	t.Setenv("DATABASE_LOCK_TIMEOUT", "500us")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a PostgreSQL timeout that rounds down to disabled")
	}
}

func TestLoadRejectsInvalidTrustedProxyCIDR(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("TRUSTED_PROXY_CIDRS", "127.0.0.1/32,not-a-network")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an invalid trusted proxy CIDR")
	}
}

func TestLoadRequiresSelectedAIProviderCredentials(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("AGREEMENT_AI_PROVIDER", AIProviderHosted)

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted hosted AI without an OpenAI API key")
	}
}

func TestLoadSelectsExternalOpenAICompatibleProvider(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("DEFAULT_AI_PROVIDER", AIProviderOpenAICompatible)
	t.Setenv("OPENAI_COMPAT_BASE_URL", "https://models.example.com/gateway")
	t.Setenv("OPENAI_COMPAT_CHAT_COMPLETIONS_PATH", "v1/chat/completions")
	t.Setenv("OPENAI_COMPAT_MODEL", "gemma-external")
	t.Setenv("OPENAI_COMPAT_API_KEY", "external-key")
	t.Setenv("OPENAI_COMPAT_TIMEOUT", "75s")
	t.Setenv("OPENAI_COMPAT_MAX_OUTPUT_TOKENS", "2048")
	t.Setenv("OPENAI_COMPAT_TOKEN_LIMIT_FIELD", OpenAICompatTokenFieldMaxCompletionTokens)
	t.Setenv("OPENAI_COMPAT_TEMPERATURE", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.NeedsOpenAICompatible() || cfg.DefaultAIModelName() != "gemma-external" ||
		cfg.OpenAICompatChatCompletions != "/v1/chat/completions" ||
		cfg.OpenAICompatTimeout != 75*time.Second || cfg.OpenAICompatMaxOutputTokens != 2048 ||
		cfg.OpenAICompatTokenLimitField != OpenAICompatTokenFieldMaxCompletionTokens ||
		cfg.OpenAICompatTemperature == nil || *cfg.OpenAICompatTemperature != 0 || cfg.OpenAICompatTopP != nil {
		t.Fatalf("external provider config = %+v", cfg)
	}
}

func TestLoadRejectsInvalidExternalOpenAICompatibleConfig(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "non-loopback HTTP", key: "OPENAI_COMPAT_BASE_URL", value: "http://models.example.com"},
		{name: "credentials in URL", key: "OPENAI_COMPAT_BASE_URL", value: "https://user:pass@models.example.com"},
		{name: "invalid token field", key: "OPENAI_COMPAT_TOKEN_LIMIT_FIELD", value: "tokens"},
		{name: "query in completion path", key: "OPENAI_COMPAT_CHAT_COMPLETIONS_PATH", value: "/v1/chat/completions?debug=true"},
		{name: "invalid temperature", key: "OPENAI_COMPAT_TEMPERATURE", value: "2.1"},
		{name: "non-finite temperature", key: "OPENAI_COMPAT_TEMPERATURE", value: "NaN"},
		{name: "invalid top p", key: "OPENAI_COMPAT_TOP_P", value: "0"},
		{name: "non-finite top p", key: "OPENAI_COMPAT_TOP_P", value: "NaN"},
		{name: "invalid timeout", key: "OPENAI_COMPAT_TIMEOUT", value: "soon"},
		{name: "invalid output token count", key: "OPENAI_COMPAT_MAX_OUTPUT_TOKENS", value: "many"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setRequiredConfig(t)
			t.Setenv("DEFAULT_AI_PROVIDER", AIProviderOpenAICompatible)
			t.Setenv("OPENAI_COMPAT_BASE_URL", "https://models.example.com")
			t.Setenv("OPENAI_COMPAT_MODEL", "external-model")
			t.Setenv("OPENAI_COMPAT_API_KEY", "external-key")
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted invalid %s", test.key)
			}
		})
	}

	t.Run("two sampling controls", func(t *testing.T) {
		setRequiredConfig(t)
		t.Setenv("DEFAULT_AI_PROVIDER", AIProviderOpenAICompatible)
		t.Setenv("OPENAI_COMPAT_BASE_URL", "https://models.example.com")
		t.Setenv("OPENAI_COMPAT_MODEL", "external-model")
		t.Setenv("OPENAI_COMPAT_API_KEY", "external-key")
		t.Setenv("OPENAI_COMPAT_TEMPERATURE", "0.2")
		t.Setenv("OPENAI_COMPAT_TOP_P", "0.9")
		if _, err := Load(); err == nil {
			t.Fatal("Load() accepted both external sampling controls")
		}
	})

	t.Run("missing key", func(t *testing.T) {
		setRequiredConfig(t)
		t.Setenv("DEFAULT_AI_PROVIDER", AIProviderOpenAICompatible)
		t.Setenv("OPENAI_COMPAT_BASE_URL", "http://127.0.0.1:7080")
		t.Setenv("OPENAI_COMPAT_MODEL", "external-model")
		if _, err := Load(); err == nil {
			t.Fatal("Load() accepted an external provider without an API key")
		}
	})
}

func TestSynchronousAIRouteTimeoutUsesDefaultProviderTimeout(t *testing.T) {
	cfg := Config{
		DefaultAIProvider: AIProviderSelfHosted,
		LLMTimeout:        30 * time.Second,
		OpenAITimeout:     45 * time.Second,
	}
	if got := cfg.SynchronousAIRouteTimeout(); got != 35*time.Second {
		t.Fatalf("SynchronousAIRouteTimeout() = %v", got)
	}
}

func TestSynchronousAIRouteTimeoutUsesExternalProviderTimeout(t *testing.T) {
	cfg := Config{
		DefaultAIProvider:   AIProviderOpenAICompatible,
		LLMTimeout:          30 * time.Second,
		OpenAITimeout:       45 * time.Second,
		OpenAICompatTimeout: 70 * time.Second,
	}
	if got := cfg.SynchronousAIRouteTimeout(); got != 75*time.Second {
		t.Fatalf("SynchronousAIRouteTimeout() = %v", got)
	}
}

func TestExternalModelConfigHashExcludesKeyAndTracksBehavior(t *testing.T) {
	temperature := 0.2
	base := Config{
		DefaultAIProvider:           AIProviderOpenAICompatible,
		OpenAICompatBaseURL:         "https://models.example.com",
		OpenAICompatChatCompletions: "/v1/chat/completions",
		OpenAICompatModel:           "external-model",
		OpenAICompatAPIKey:          "secret-one",
		OpenAICompatTimeout:         60 * time.Second,
		OpenAICompatMaxOutputTokens: 1600,
		OpenAICompatTokenLimitField: OpenAICompatTokenFieldMaxTokens,
		OpenAICompatTemperature:     &temperature,
	}
	credentialChange := base
	credentialChange.OpenAICompatAPIKey = "secret-two"
	if base.InboxAIModelConfigHash() != credentialChange.InboxAIModelConfigHash() {
		t.Fatal("external model hash changed with credentials")
	}
	behaviorChange := base
	behaviorChange.OpenAICompatTokenLimitField = OpenAICompatTokenFieldMaxCompletionTokens
	if base.InboxAIModelConfigHash() == behaviorChange.InboxAIModelConfigHash() {
		t.Fatal("external model hash ignored token field behavior")
	}
	timeoutChange := base
	timeoutChange.OpenAICompatTimeout++
	if base.InboxAIModelConfigHash() == timeoutChange.InboxAIModelConfigHash() {
		t.Fatal("external model hash ignored timeout behavior")
	}
}

func TestLoadRejectsInvalidLocalSamplingConfig(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("MIN_P", "1.5")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted MIN_P above one")
	}
}

func TestLoadRejectsTessaInputLimitBelowItsStaticPrompt(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("TESSA_AI_MAX_INPUT_TOKENS", "1000")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a Tessa input limit that cannot fit the static planning prompt")
	}
}

func TestLoadReadsPayazaTransferPIN(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("PAYAZA_TRANSFER_PIN", "123456")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PayazaTransferPIN != "123456" {
		t.Fatalf("PayazaTransferPIN was not loaded")
	}
}

func TestLoadRejectsMalformedPayazaTransferPIN(t *testing.T) {
	for _, pin := range []string{"12345", "1234567", "12a456", "012345"} {
		t.Run(pin, func(t *testing.T) {
			setRequiredConfig(t)
			t.Setenv("PAYAZA_TRANSFER_PIN", pin)

			if _, err := Load(); err == nil {
				t.Fatal("Load() accepted a malformed Payaza transfer PIN")
			}
		})
	}
}

func TestPayazaPayoutConfigSelectsActiveEnvironment(t *testing.T) {
	cfg := Config{
		PayazaTransferPIN: "111111", PayazaTransferPINTest: "222222",
		PayazaSourceAccounts:     `{"NGN":"live-source"}`,
		PayazaSourceAccountsTest: `{"NGN":"test-source"}`,
		PaymentsEnvironment:      "test",
	}
	accounts, err := cfg.PayazaSourceAccountMap()
	if err != nil || cfg.PayazaActiveTransferPIN() != "222222" || accounts["NGN"] != "test-source" {
		t.Fatal("test Payaza payout configuration was not selected")
	}
	cfg.PaymentsEnvironment = "live"
	accounts, err = cfg.PayazaSourceAccountMap()
	if err != nil || cfg.PayazaActiveTransferPIN() != "111111" || accounts["NGN"] != "live-source" {
		t.Fatal("live Payaza payout configuration was not selected")
	}
}

func TestLoadRejectsPartialPayazaCredentials(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("PAYAZA_PUBLIC_KEY", "public-key")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted partial Payaza credentials")
	}
}

func TestLoadRejectsPartialPayazaTestCredentials(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("PAYAZA_PUBLIC_KEY_TEST", "public-key")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted partial Payaza test credentials")
	}
}

func TestLoadRejectsPartialPayazaPayoutSender(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("PAYAZA_PAYOUT_SENDER_NAME", "TellBook")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a partial Payaza payout sender identity")
	}
}

func TestPayazaCredentialsSelectActiveEnvironment(t *testing.T) {
	cfg := Config{
		PayazaPublicKey: "live-public", PayazaSecretKey: "live-secret",
		PayazaPublicKeyTest: "test-public", PayazaSecretKeyTest: "test-secret",
		PaymentsEnvironment: "test",
	}
	publicKey, secretKey := cfg.PayazaCredentials()
	if publicKey != "test-public" || secretKey != "test-secret" || !cfg.PayazaEnabled() {
		t.Fatalf("test credentials were not selected")
	}

	cfg.PaymentsEnvironment = "live"
	publicKey, secretKey = cfg.PayazaCredentials()
	if publicKey != "live-public" || secretKey != "live-secret" || !cfg.PayazaEnabled() {
		t.Fatalf("live credentials were not selected")
	}
}

func TestPayazaSourceAccountMapNormalizesCurrency(t *testing.T) {
	config := Config{PayazaSourceAccounts: `{"ngn":" account-reference "}`, PaymentsEnvironment: "live"}
	accounts, err := config.PayazaSourceAccountMap()
	if err != nil {
		t.Fatalf("PayazaSourceAccountMap() error = %v", err)
	}
	if accounts["NGN"] != "account-reference" {
		t.Fatalf("accounts = %#v", accounts)
	}
}

func TestLoadRejectsInvalidPaymentsEnvironment(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("PAYMENTS_ENVIRONMENT", "production")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted invalid payments environment")
	}
}

func TestLoadRejectsInvalidClientPublicBaseURL(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("CLIENT_PUBLIC_BASE_URL", "javascript:alert(1)")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an invalid client public base URL")
	}
}

func TestLoadRejectsInvalidMarketplacePublicBaseURL(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("MARKETPLACE_PUBLIC_BASE_URL", "javascript:alert(1)")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an invalid marketplace public base URL")
	}
}

func TestLoadReadsAuthCookieDomain(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("AUTH_COOKIE_DOMAIN", "tellbook.app")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AuthCookieDomain != "tellbook.app" {
		t.Fatalf("AuthCookieDomain = %q", cfg.AuthCookieDomain)
	}
}

func TestLoadRequiresSecureSharedCookiesInProduction(t *testing.T) {
	t.Run("domain", func(t *testing.T) {
		setRequiredConfig(t)
		t.Setenv("APP_ENV", "production")
		t.Setenv("AUTH_COOKIE_DOMAIN", "")
		t.Setenv("AUTH_COOKIE_SECURE", "true")

		if _, err := Load(); err == nil {
			t.Fatal("Load() accepted production config without AUTH_COOKIE_DOMAIN")
		}
	})

	t.Run("secure", func(t *testing.T) {
		setRequiredConfig(t)
		t.Setenv("APP_ENV", "production")
		t.Setenv("AUTH_COOKIE_DOMAIN", "tellbook.app")
		t.Setenv("AUTH_COOKIE_SECURE", "false")

		if _, err := Load(); err == nil {
			t.Fatal("Load() accepted insecure production auth cookies")
		}
	})
}

func TestLoadRejectsPartialFinancialSecurityConfig(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("FINANCIAL_DATA_ACTIVE_KEY_VERSION", "v1")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted partial financial security configuration")
	}
}

func TestLoadValidatesAgreementTokenEncryptionConfig(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("AGREEMENT_TOKEN_ACTIVE_KEY", "v1")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a partial agreement token keyring")
	}

	setRequiredConfig(t)
	t.Setenv("AGREEMENT_TOKEN_ACTIVE_KEY", "v1")
	t.Setenv("AGREEMENT_TOKEN_ENCRYPTION_KEYS", `{"v1":"not-base64"}`)
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an invalid agreement token keyring")
	}
}

func TestLoadRequiresFinancialSecurityForEnabledCapabilities(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("PAYSTACK_ENABLED_CAPABILITIES", "card")

	if _, err := Load(); err == nil {
		t.Fatal("Load() enabled payment capabilities without financial security configuration")
	}
}

func TestLoadRejectsPaystackKeysWithWrongPrefixes(t *testing.T) {
	for key, value := range map[string]string{
		"PAYSTACK_SECRET_KEY":      "sk_test_example",
		"PAYSTACK_SECRET_KEY_TEST": "sk_live_example",
	} {
		t.Run(key, func(t *testing.T) {
			setRequiredConfig(t)
			t.Setenv(key, value)
			if _, err := Load(); err == nil {
				t.Fatal("Load() accepted a Paystack key with the wrong environment prefix")
			}
		})
	}
}

func TestPaystackCredentialsSelectActiveEnvironment(t *testing.T) {
	cfg := Config{
		PaystackSecretKey: "sk_live_example", PaystackSecretKeyTest: "sk_test_example",
		PaymentsEnvironment: "test",
	}
	if cfg.PaystackCredentials() != "sk_test_example" || !cfg.PaystackEnabled() {
		t.Fatal("test Paystack credentials were not selected")
	}
	cfg.PaymentsEnvironment = "live"
	if cfg.PaystackCredentials() != "sk_live_example" || !cfg.PaystackEnabled() {
		t.Fatal("live Paystack credentials were not selected")
	}
}

func TestAdditionalEmailsDefaultOff(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("ADDITIONAL_EMAILS_ENABLED", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdditionalEmailsEnabled {
		t.Fatal("additional emails enabled by default")
	}
	t.Setenv("ADDITIONAL_EMAILS_ENABLED", "true")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AdditionalEmailsEnabled {
		t.Fatal("additional email switch ignored")
	}
}
