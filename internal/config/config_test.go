package config

import (
	"testing"
	"time"
)

func setRequiredConfig(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_URL", "postgres://example.invalid/database")
	t.Setenv("AUTH_ACCESS_TOKEN_SECRET", "test-secret")
	t.Setenv("DEFAULT_AI_PROVIDER", AIProviderSelfHosted)
	t.Setenv("AGREEMENT_AI_PROVIDER", AIProviderSelfHosted)
	t.Setenv("INBOX_AI_PROVIDER", AIProviderSelfHosted)
	t.Setenv("HOSTED_AI_PROVIDER", HostedProviderOpenAI)
	t.Setenv("LLM_BASE_URL", "http://127.0.0.1:8080")
	t.Setenv("LLM_TIMEOUT", "30s")
	t.Setenv("LLM_MAX_OUTPUT_TOKENS", "1200")
	t.Setenv("OPENAI_API_KEY", "")
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
		t.Setenv(key, "false")
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
	if cfg.DefaultAIProvider != AIProviderSelfHosted || cfg.AgreementAIProvider != AIProviderHosted || cfg.InboxAIProvider != AIProviderSelfHosted {
		t.Fatalf("unexpected AI provider routing: %+v", cfg)
	}
}

func TestLoadRequiresSelectedAIProviderCredentials(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("AGREEMENT_AI_PROVIDER", AIProviderHosted)

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted hosted AI without an OpenAI API key")
	}
}

func TestSynchronousAIRouteTimeoutUsesSelectedProviderTimeout(t *testing.T) {
	cfg := Config{
		DefaultAIProvider: AIProviderSelfHosted,
		InboxAIProvider:   AIProviderHosted,
		LLMTimeout:        30 * time.Second,
		OpenAITimeout:     45 * time.Second,
	}
	if got := cfg.SynchronousAIRouteTimeout(); got != 50*time.Second {
		t.Fatalf("SynchronousAIRouteTimeout() = %v", got)
	}
}

func TestLoadRejectsInvalidLocalSamplingConfig(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("MIN_P", "1.5")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted MIN_P above one")
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

func TestLoadRequiresFinancialSecurityForVerifiedProviders(t *testing.T) {
	setRequiredConfig(t)
	t.Setenv("PAYSTACK_CARD_SANDBOX_VERIFIED", "true")

	if _, err := Load(); err == nil {
		t.Fatal("Load() enabled a verified provider without financial security configuration")
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
