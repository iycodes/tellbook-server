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
	AIServiceBaseURL                      string
	AIServiceTimeout                      time.Duration
	AIRouteTimeout                        time.Duration
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

func Load() (Config, error) {
	cfg := Config{
		AppEnv:                                getEnv("APP_ENV", "development"),
		HTTPAddr:                              getEnv("HTTP_ADDR", ":8000"),
		ClientPublicBaseURL:                   strings.TrimRight(getEnv("CLIENT_PUBLIC_BASE_URL", "http://localhost:5275"), "/"),
		AIServiceBaseURL:                      strings.TrimRight(getEnv("AI_SERVICE_BASE_URL", "http://127.0.0.1:8090"), "/"),
		AIServiceTimeout:                      getEnvDuration("AI_SERVICE_TIMEOUT", 20*time.Second),
		AIRouteTimeout:                        getEnvDuration("AI_ROUTE_TIMEOUT", 120*time.Second),
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
