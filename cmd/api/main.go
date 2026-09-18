package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"booking/go-server/internal/admin"
	agreementrepo "booking/go-server/internal/agreements/repository"
	agreementservice "booking/go-server/internal/agreements/service"
	agreementworker "booking/go-server/internal/agreements/worker"
	aisvc "booking/go-server/internal/ai"
	"booking/go-server/internal/appdata"
	"booking/go-server/internal/auth"
	"booking/go-server/internal/authchallenge"
	"booking/go-server/internal/config"
	"booking/go-server/internal/database"
	"booking/go-server/internal/llm"
	"booking/go-server/internal/mailer"
	"booking/go-server/internal/marketplaceauth"
	notificationworker "booking/go-server/internal/notifications"
	"booking/go-server/internal/observability"
	"booking/go-server/internal/payments"
	"booking/go-server/internal/payments/capabilities"
	payaza "booking/go-server/internal/payments/payaza"
	paystack "booking/go-server/internal/payments/paystack"
	"booking/go-server/internal/redisstore"
	"booking/go-server/internal/secure"
	"booking/go-server/internal/server"
	"booking/go-server/internal/storage"
	"booking/go-server/internal/tessa"
	"booking/go-server/internal/welcomeemail"
	"booking/go-server/internal/whatsapp"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := config.LoadDotEnv(); err != nil && !errors.Is(err, config.ErrNoEnvFileFound) && !errors.Is(err, os.ErrNotExist) {
		slog.Error("load .env", "error", err)
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	logger := config.NewLogger(cfg.AppEnv)
	slog.SetDefault(logger)
	if err := whatsapp.ValidateEnabledTemplateKeys(cfg.WhatsAppEnabledTemplateKeys); err != nil {
		logger.Error("validate enabled WhatsApp templates", "error", err)
		os.Exit(1)
	}
	runsAPI := cfg.ProcessRole == config.ProcessRoleAPI || cfg.ProcessRole == config.ProcessRoleAll
	runsCoreWorkers := cfg.ProcessRole == config.ProcessRoleWorker || cfg.ProcessRole == config.ProcessRoleAll
	runsAIWorkers := cfg.ProcessRole == config.ProcessRoleAIWorker || cfg.ProcessRole == config.ProcessRoleAll
	runsMaintenance := cfg.ProcessRole == config.ProcessRoleMaintenance || cfg.ProcessRole == config.ProcessRoleAll

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	metrics := observability.New()
	var redisClient *redisstore.Client
	if runsAPI {
		redisClient, err = redisstore.Open(ctx, redisstore.Options{
			URL: cfg.RedisURL, KeyPrefix: cfg.RedisKeyPrefix,
			KeyHMACSecret: cfg.RedisKeyHMACSecret,
			ClientName:    "tellbook-" + cfg.AppEnv + "-" + cfg.ProcessRole,
			PoolSize:      cfg.RedisPoolSize, MinIdleConnections: cfg.RedisMinIdleConnections,
			DialTimeout: cfg.RedisDialTimeout, ReadTimeout: cfg.RedisReadTimeout,
			WriteTimeout: cfg.RedisWriteTimeout, PoolTimeout: cfg.RedisPoolTimeout,
			MaxPayloadBytes: cfg.RedisMaxPayloadBytes, Metrics: metrics,
		})
		if err != nil {
			logger.Error("open Redis", "error", err)
			os.Exit(1)
		}
		if redisClient != nil {
			defer redisClient.Close()
			metrics.RegisterRedisPool(func() observability.RedisPoolSnapshot {
				snapshot := redisClient.PoolSnapshot()
				return observability.RedisPoolSnapshot{
					Hits: snapshot.Hits, Misses: snapshot.Misses, Timeouts: snapshot.Timeouts,
					WaitCount: snapshot.WaitCount, Unusable: snapshot.Unusable,
					WaitDuration:     snapshot.WaitDuration,
					TotalConnections: snapshot.TotalConnections, IdleConnections: snapshot.IdleConnections,
					StaleConnections: snapshot.StaleConnections, PendingRequests: snapshot.PendingRequests,
				}
			})
		}
	}
	dbPool, err := database.OpenPool(ctx, cfg, metrics)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()
	metrics.RegisterDatabasePool(dbPool)
	directDBPool, err := database.OpenDirectPool(ctx, cfg, metrics)
	if err != nil {
		logger.Error("open direct database", "error", err)
		os.Exit(1)
	}
	defer directDBPool.Close()
	metrics.RegisterDirectDatabasePool(directDBPool)
	maintenanceOwnership := "not_applicable"
	var maintenanceConnection *pgxpool.Conn
	if runsMaintenance {
		maintenanceConnection, err = acquireMaintenanceLeadership(ctx, directDBPool)
		if err != nil {
			logger.Error("acquire maintenance leadership", "error", err)
			os.Exit(1)
		}
		maintenanceOwnership = "leader"
		defer releaseMaintenanceLeadership(maintenanceConnection)
		go monitorMaintenanceLeadership(ctx, maintenanceConnection, logger, stop)
	}
	var readiness server.ReadinessChecker = &databaseReadiness{
		query: dbPool, leadership: maintenanceConnection,
	}
	var coreWorkerWake *payments.CoreWorkerWakeBroker
	if runsCoreWorkers {
		coreWorkerWake = payments.NewCoreWorkerWakeBroker(directDBPool, logger)
		go coreWorkerWake.Start(ctx)
	}
	var aiWorkerWake *payments.CoreWorkerWakeBroker
	if runsAIWorkers {
		aiWorkerWake = payments.NewAIWorkerWakeBroker(directDBPool, logger)
		go aiWorkerWake.Start(ctx)
	}

	authRepo := auth.NewRepository(dbPool).WithAdditionalEmails(cfg.AdditionalEmailsEnabled && cfg.AuthEmailEnabled).WithWelcomeURL(cfg.ClientPublicBaseURL + "/")
	r2Service, err := storage.NewR2Service(cfg)
	if err != nil {
		logger.Error("configure R2 storage", "error", err)
		os.Exit(1)
	}

	transactionalSMTPConfig := mailer.Config{
		Host:               cfg.SMTPHost,
		Port:               cfg.SMTPPort,
		Username:           cfg.SMTPUsername,
		Password:           cfg.SMTPPassword,
		FromEmail:          cfg.SMTPFromEmail,
		FromName:           cfg.SMTPFromName,
		Security:           cfg.SMTPSecurity,
		InsecureSkipVerify: cfg.SMTPInsecureSkipVerify,
		ConnectTimeout:     cfg.SMTPConnectTimeout,
	}
	smtpMailer, err := mailer.NewSMTPMailer(transactionalSMTPConfig)
	if err != nil {
		logger.Error("configure smtp mailer", "error", err)
		os.Exit(1)
	}
	notificationSMTPConfig := transactionalSMTPConfig
	notificationSMTPConfig.SendTimeout = cfg.NotificationEmailTimeout
	notificationSMTPConfig.MaxConnections = cfg.NotificationEmailConcurrency
	notificationSMTPMailer, err := mailer.NewSMTPMailer(notificationSMTPConfig)
	if err != nil {
		logger.Error("configure notification smtp mailer", "error", err)
		os.Exit(1)
	}
	welcomeSMTPConfig := transactionalSMTPConfig
	welcomeSMTPConfig.SendTimeout = cfg.WelcomeEmailTimeout
	welcomeSMTPConfig.MaxConnections = cfg.WelcomeEmailConcurrency
	welcomeSMTPMailer, err := mailer.NewSMTPMailer(welcomeSMTPConfig)
	if err != nil {
		logger.Error("configure welcome smtp mailer", "error", err)
		os.Exit(1)
	}
	authSMTPConfig := transactionalSMTPConfig
	authSMTPConfig.SendTimeout = cfg.AuthDeliveryTimeout
	authSMTPConfig.MaxConnections = cfg.AuthDeliveryConcurrency
	authSMTPMailer, err := mailer.NewSMTPMailer(authSMTPConfig)
	if err != nil {
		logger.Error("configure auth smtp mailer", "error", err)
		os.Exit(1)
	}
	authChallenges, err := authchallenge.NewService(dbPool, authchallenge.Config{
		AdditionalEmailsEnabled: cfg.AdditionalEmailsEnabled,
		EmailEnabled:            cfg.AuthEmailEnabled,
		WhatsAppEnabled:         cfg.AuthWhatsAppEnabled,
		EncryptionKeys:          cfg.AuthDeliveryEncryptionKeys, ActiveKey: cfg.AuthDeliveryActiveKey,
		DestinationKey: cfg.AuthDestinationHMACKey,
	})
	if err != nil {
		logger.Error("configure auth challenge delivery", "error", err)
		os.Exit(1)
	}

	authService := auth.NewService(authRepo, cfg, r2Service, authChallenges)
	authHandler := auth.NewHandler(authService, cfg)
	marketplaceAuthRepo := marketplaceauth.NewRepository(dbPool).WithAdditionalEmails(cfg.AdditionalEmailsEnabled && cfg.AuthEmailEnabled).WithWelcomeURL(cfg.MarketplacePublicBaseURL + "/search")
	marketplaceAuthService := marketplaceauth.NewService(marketplaceAuthRepo, cfg, authChallenges)
	if redisClient != nil {
		marketplaceAuthService.ConfigureSessionCache(redisClient, cfg.RedisFallbackMaxConcurrency, metrics)
	}
	marketplaceAuthHandler := marketplaceauth.NewHandler(marketplaceAuthService, marketplaceAuthRepo, cfg)
	appdataRepo := appdata.NewRepository(dbPool).WithAdditionalEmails(cfg.AdditionalEmailsEnabled && cfg.NotificationEmailEnabled)
	appdataRepo.ConfigureGoogleMaps(cfg.GoogleMapsServerAPIKey)
	appdataRepo.ConfigureOperationalMetrics(metrics)
	appdataRepo.ConfigureInboxAIAutopilotPaymentWindow(cfg.InboxAIAutopilotPaymentWindow)
	if runsCoreWorkers {
		marketplaceDiscoveryWorker := appdata.NewMarketplaceDiscoveryWorker(
			appdataRepo,
			logger,
			appdata.MarketplaceDiscoveryWorkerConfig{},
		)
		marketplaceDiscoveryWake, unsubscribeMarketplaceDiscoveryWake := coreWorkerWake.Subscribe()
		defer unsubscribeMarketplaceDiscoveryWake()
		go marketplaceDiscoveryWorker.Start(ctx, marketplaceDiscoveryWake)
	}
	var agreementTokens *agreementservice.PublicTokenManager
	if cfg.AgreementTokenEncryptionKeys != "" {
		agreementKeyring, keyringErr := secure.ParseKeyring(cfg.AgreementTokenEncryptionKeys, cfg.AgreementTokenActiveKey)
		if keyringErr != nil {
			logger.Error("configure agreement token encryption", "error", keyringErr)
			os.Exit(1)
		}
		configuredAgreementTokens, tokenErr := agreementservice.NewPublicTokenManager(agreementKeyring)
		if tokenErr != nil {
			logger.Error("configure agreement public tokens", "error", tokenErr)
			os.Exit(1)
		}
		agreementTokens = configuredAgreementTokens
		appdataRepo.ConfigureAgreementTokens(agreementTokens)
	}
	aiServices := make(map[string]*aisvc.Service, 3)
	if cfg.NeedsSelfHosted() {
		aiServices[config.AIProviderSelfHosted] = aisvc.NewService(llm.NewClient(cfg, metrics))
	}
	if cfg.NeedsHosted() {
		aiServices[config.AIProviderHosted] = aisvc.NewService(llm.NewOpenAIClient(cfg, metrics))
	}
	if cfg.NeedsOpenAICompatible() {
		aiServices[config.AIProviderOpenAICompatible] = aisvc.NewService(llm.NewOpenAICompatibleClient(cfg, metrics))
	}
	aiClient := aisvc.NewClient(
		aiServices[cfg.DefaultAIProvider],
		aiServices[cfg.AgreementAIProvider],
	)
	agreementRepository := agreementrepo.New(dbPool)
	if runsMaintenance {
		if err := agreementRepository.SyncSystemTemplates(ctx); err != nil {
			logger.Error("sync system agreement templates", "error", err)
			os.Exit(1)
		}
	}
	if runsCoreWorkers {
		var agreementUploadPreparer agreementworker.UploadPreparer
		if r2Service != nil && r2Service.PrivateBucketName() != "" {
			agreementUploadPreparer, err = agreementworker.NewPDFUploadPreparer(r2Service)
			if err != nil {
				logger.Error("configure agreement upload preparation", "error", err)
				os.Exit(1)
			}
		}
		agreementRequestBuilder, requestBuilderErr := agreementworker.NewStoredGenerationRequestBuilder(agreementUploadPreparer)
		if requestBuilderErr != nil {
			logger.Error("configure agreement generation request builder", "error", requestBuilderErr)
			os.Exit(1)
		}
		agreementGenerationWorker, workerErr := agreementworker.NewGenerationWorker(
			agreementRepository,
			aiClient,
			agreementRequestBuilder,
			logger,
			agreementworker.GenerationWorkerConfig{PollInterval: 25 * time.Second},
		)
		if workerErr != nil {
			logger.Error("configure agreement generation worker", "error", workerErr)
			os.Exit(1)
		}
		agreementGenerationWake, unsubscribeGenerationWake := coreWorkerWake.Subscribe()
		defer unsubscribeGenerationWake()
		go agreementGenerationWorker.Start(ctx, agreementGenerationWake)
		if agreementTokens != nil {
			var agreementStorage agreementworker.CompletedAgreementStore
			if r2Service != nil {
				agreementStorage = r2Service
			}
			agreementLifecycleWorker, lifecycleErr := agreementworker.NewLifecycleWorker(
				dbPool, agreementTokens, smtpMailer, agreementStorage, cfg.ClientPublicBaseURL, logger,
			)
			if lifecycleErr != nil {
				logger.Error("configure agreement lifecycle worker", "error", lifecycleErr)
				os.Exit(1)
			}
			agreementLifecycleWake, unsubscribeLifecycleWake := coreWorkerWake.Subscribe()
			defer unsubscribeLifecycleWake()
			go agreementLifecycleWorker.Start(ctx, agreementLifecycleWake)
		} else {
			logger.Info("agreement lifecycle worker disabled", "reason", "missing agreement token encryption keys")
		}
	}

	ledgerRepository := payments.NewLedgerRepository(dbPool).WithAdditionalEmails(cfg.AdditionalEmailsEnabled && cfg.AuthEmailEnabled, cfg.AdditionalEmailsEnabled && cfg.NotificationEmailEnabled)
	var financialKeyring *secure.Keyring
	var financialFingerprinter *secure.Fingerprinter
	if cfg.FinancialEncryptionKeys != "" {
		financialKeyring, err = secure.ParseKeyring(cfg.FinancialEncryptionKeys, cfg.FinancialActiveKey)
		if err != nil {
			logger.Error("configure financial encryption", "error", err)
			os.Exit(1)
		}
		financialFingerprinter, err = secure.NewFingerprinter(cfg.FinancialFingerprintKey)
		if err != nil {
			logger.Error("configure financial fingerprinting", "error", err)
			os.Exit(1)
		}
	}
	ledgerService, err := payments.NewLedgerService(ledgerRepository, financialKeyring, financialFingerprinter)
	if err != nil {
		logger.Error("configure financial ledger", "error", err)
		os.Exit(1)
	}
	collectionProviders := make(map[string]payments.CollectionProvider, 2)
	refundProviders := make(map[string]payments.RefundProvider, 2)
	settlementProviders := make(map[string]payments.SettlementProvider, 1)
	destinationProviders := make(map[string]payments.DestinationProvider, 2)
	payoutProviders := make(map[string]payments.PayoutProvider, 2)
	webhookVerifiers := make(map[string]payments.WebhookVerifier, 2)
	payazaSourceAccounts := map[string]string{}
	payazaDestinationConfigured := false
	if cfg.PayazaEnabled() {
		payazaPublicKey, payazaSecretKey := cfg.PayazaCredentials()
		sourceAccounts, sourceErr := cfg.PayazaSourceAccountMap()
		if sourceErr != nil {
			logger.Error("configure payaza source accounts", "error", sourceErr)
			os.Exit(1)
		}
		payazaSourceAccounts = sourceAccounts
		dvaBankName := ""
		switch cfg.PayazaNGNDVABankCode {
		case "1067":
			dvaBankName = "78 FINANCE COMPANY LIMITED"
		case "140":
			dvaBankName = "GLOBUS BANK"
		}
		payazaClient, clientErr := payaza.NewClient(payaza.Config{
			PublicKey: payazaPublicKey, SecretKey: payazaSecretKey,
			BaseURL: cfg.PayazaBaseURL, TenantID: cfg.PaymentsEnvironment,
			TransactionPIN: cfg.PayazaActiveTransferPIN(),
			DVABankCode:    cfg.PayazaNGNDVABankCode, DVAEnquiryBankCode: cfg.PayazaNGNDVAEnquiryBankCode,
			DVABankName:    dvaBankName,
			SourceAccounts: sourceAccounts,
			HTTPClient:     metrics.InstrumentHTTPClient(&http.Client{Timeout: 15 * time.Second}, "payaza"),
			PayoutSender: payaza.PayoutSender{
				Name: cfg.PayazaPayoutSenderName, Phone: cfg.PayazaPayoutSenderPhone,
				Address: cfg.PayazaPayoutSenderAddress,
			},
		})
		if clientErr != nil {
			logger.Error("configure payaza client", "error", clientErr)
			os.Exit(1)
		}
		collectionProviders["payaza"] = payazaClient
		refundProviders["payaza"] = payazaClient
		payoutProviders["payaza"] = payazaClient
		webhookVerifiers["payaza"] = payazaClient

		payazaDirectoryClient := payazaClient
		if cfg.PaymentsEnvironment == string(capabilities.EnvironmentTest) {
			payazaDirectoryClient = nil
			if cfg.PayazaLiveEnabled() {
				payazaDirectoryClient, clientErr = payaza.NewClient(payaza.Config{
					PublicKey: cfg.PayazaPublicKey, SecretKey: cfg.PayazaSecretKey,
					BaseURL: cfg.PayazaBaseURL, TenantID: string(capabilities.EnvironmentLive),
					HTTPClient: metrics.InstrumentHTTPClient(&http.Client{Timeout: 15 * time.Second}, "payaza"),
				})
				if clientErr != nil {
					logger.Error("configure payaza live directory client", "error", clientErr)
					os.Exit(1)
				}
			}
		}
		if payazaDirectoryClient != nil {
			destinationClient, destinationErr := payaza.NewRoutedDestinationClient(payazaDirectoryClient, payazaClient)
			if destinationErr != nil {
				logger.Error("configure payaza destination client", "error", destinationErr)
				os.Exit(1)
			}
			destinationProviders["payaza"] = destinationClient
			payazaDestinationConfigured = true
		}
	}

	var paystackClient *paystack.Client
	if cfg.PaystackEnabled() {
		paystackClient, err = paystack.NewClient(paystack.Config{
			SecretKey:  cfg.PaystackCredentials(),
			BaseURL:    cfg.PaystackBaseURL,
			HTTPClient: metrics.InstrumentHTTPClient(&http.Client{Timeout: 15 * time.Second}, "paystack"),
		})
		if err != nil {
			logger.Error("configure paystack client", "error", err)
			os.Exit(1)
		}
		collectionProviders["paystack"] = paystackClient
		refundProviders["paystack"] = paystackClient
		destinationProviders["paystack"] = paystackClient
		payoutProviders["paystack"] = paystackClient
		webhookVerifiers["paystack"] = paystackClient
	} else {
		logger.Info("paystack payments disabled", "reason", "missing PAYSTACK_SECRET_KEY")
	}

	ready := func(configured, sandboxVerified, productionEnabled bool) capabilities.CapabilityReadiness {
		return capabilities.CapabilityReadiness{
			Configured: configured, SandboxVerified: sandboxVerified, ProductionEnabled: productionEnabled,
		}
	}
	payazaConfigured := cfg.PayazaEnabled()
	paystackConfigured := cfg.PaystackEnabled()
	_, payazaHasNGNSource := payazaSourceAccounts["NGN"]
	capabilityRegistry, err := capabilities.New(capabilities.InitialEntries(capabilities.ProviderReadiness{
		PayazaConfigured: payazaConfigured,
		PayazaCard: ready(
			payazaConfigured,
			cfg.PayazaCardSandboxVerified,
			cfg.PayazaCardProductionEnabled,
		),
		PayazaBankTransfer: ready(
			payazaConfigured && cfg.PayazaNGNDVABankCode != "" && cfg.PayazaNGNDVAEnquiryBankCode != "",
			cfg.PayazaBankTransferSandboxVerified,
			cfg.PayazaBankTransferProductionEnabled,
		),
		PayazaDestination: ready(
			payazaDestinationConfigured, cfg.PayazaDestinationSandboxVerified, cfg.PayazaDestinationProductionEnabled,
		),
		PayazaPayout: ready(
			payazaConfigured && cfg.PayazaActiveTransferPIN() != "" && payazaHasNGNSource && cfg.PayazaPayoutSenderConfigured(),
			cfg.PayazaPayoutSandboxVerified,
			cfg.PayazaPayoutProductionEnabled,
		),
		PaystackConfigured: paystackConfigured,
		PaystackCard: ready(
			paystackConfigured,
			cfg.PaystackCardSandboxVerified,
			cfg.PaystackCardProductionEnabled,
		),
		PaystackBankTransfer: ready(
			paystackConfigured,
			cfg.PaystackBankTransferSandboxVerified,
			cfg.PaystackBankTransferProductionEnabled,
		),
		PaystackDestination: ready(
			paystackConfigured,
			cfg.PaystackDestinationSandboxVerified,
			cfg.PaystackDestinationProductionEnabled,
		),
		PaystackPayout: ready(
			paystackConfigured && cfg.PaystackPayoutOTPDisabled,
			cfg.PaystackPayoutSandboxVerified,
			cfg.PaystackPayoutProductionEnabled,
		),
	}))
	if err != nil {
		logger.Error("configure payment capabilities", "error", err)
		os.Exit(1)
	}
	paystackSettlementEnabled := cfg.PaymentsEnvironment == string(capabilities.EnvironmentTest) &&
		(cfg.PaystackCardSandboxVerified || cfg.PaystackBankTransferSandboxVerified)
	if cfg.PaymentsEnvironment == string(capabilities.EnvironmentLive) {
		paystackSettlementEnabled = cfg.PaystackCardProductionEnabled || cfg.PaystackBankTransferProductionEnabled
	}
	if paystackClient != nil && paystackSettlementEnabled {
		settlementProviders["paystack"] = paystackClient
	}
	checkoutService, err := payments.NewCheckoutService(payments.CheckoutServiceConfig{
		Ledger: ledgerService, Repository: ledgerRepository, Capabilities: capabilityRegistry,
		Environment: capabilities.Environment(cfg.PaymentsEnvironment), Providers: collectionProviders,
	})
	if err != nil {
		logger.Error("configure checkout service", "error", err)
		os.Exit(1)
	}
	destinationService, err := payments.NewDestinationService(
		ledgerService,
		ledgerRepository,
		capabilityRegistry,
		capabilities.Environment(cfg.PaymentsEnvironment),
		destinationProviders,
	)
	if err != nil {
		logger.Error("configure payout destination service", "error", err)
		os.Exit(1)
	}
	payoutService, err := payments.NewPayoutService(payments.PayoutServiceConfig{
		Ledger: ledgerService, Repository: ledgerRepository, Capabilities: capabilityRegistry,
		Environment: capabilities.Environment(cfg.PaymentsEnvironment), Providers: payoutProviders,
	})
	if err != nil {
		logger.Error("configure payout service", "error", err)
		os.Exit(1)
	}
	paymentEvents := payments.NewPaymentEventBroker(directDBPool, logger)
	if runsAPI {
		go paymentEvents.Start(ctx)
	}
	paymentReconciliations := payments.NewPaymentReconciliationScheduler(ledgerRepository)
	if runsCoreWorkers {
		paymentWake, unsubscribePaymentWake := coreWorkerWake.Subscribe()
		defer unsubscribePaymentWake()
		paymentReconciliationWorker := payments.NewPaymentReconciliationWorker(
			ledgerRepository, checkoutService, logger, paymentWake,
		)
		go paymentReconciliationWorker.Start(ctx)
	}
	var providerWebhookHandler *payments.ProviderWebhookHandler
	if financialKeyring != nil && len(webhookVerifiers) > 0 {
		providerWebhookHandler = payments.NewProviderWebhookHandler(ledgerService, webhookVerifiers)
		if runsCoreWorkers {
			webhookWorker := payments.NewCollectionWebhookWorker(ledgerRepository, ledgerService, checkoutService, logger)
			webhookWake, unsubscribeWebhookWake := coreWorkerWake.Subscribe()
			defer unsubscribeWebhookWake()
			go webhookWorker.Start(ctx, webhookWake)
			payoutWebhookWorker := payments.NewPayoutWebhookWorker(ledgerRepository, ledgerService, payoutService, logger)
			payoutWebhookWake, unsubscribePayoutWebhookWake := coreWorkerWake.Subscribe()
			defer unsubscribePayoutWebhookWake()
			go payoutWebhookWorker.Start(ctx, payoutWebhookWake)
		}
	}
	if runsMaintenance {
		payoutReconciler := payments.NewPayoutReconciler(ledgerRepository, payoutService, logger)
		go payoutReconciler.Start(ctx)
		settlementWorker := payments.NewSettlementWorker(ledgerRepository, settlementProviders, logger)
		go settlementWorker.Start(ctx)
	}
	if runsCoreWorkers {
		if cfg.AuthEmailEnabled {
			authWake, unsubscribeAuthWake := coreWorkerWake.Subscribe()
			defer unsubscribeAuthWake()
			go authchallenge.NewWorker(
				authChallenges, authSMTPMailer, logger, authWake, metrics,
				cfg.AuthDeliveryConcurrency, cfg.AuthDeliveryTimeout,
			).Start(ctx)
		}
		if cfg.AuthWhatsAppEnabled {
			authWhatsAppSender, authWhatsAppErr := whatsapp.NewClient(whatsapp.ClientConfig{
				BaseURL: cfg.WhatsAppGraphBaseURL, GraphVersion: cfg.WhatsAppGraphVersion,
				PhoneNumberID: cfg.WABAPhoneNumberID, BusinessAccountID: cfg.WhatsAppBusinessAccountID,
				AccessToken: cfg.WABAToken, Timeout: cfg.WhatsAppHTTPTimeout,
				EnabledTemplateKeys: []string{string(whatsapp.TemplateAuthCode)},
			})
			if authWhatsAppErr != nil {
				logger.Error("configure WhatsApp auth sender", "error", authWhatsAppErr)
				os.Exit(1)
			}
			authWhatsAppWake, unsubscribeAuthWhatsAppWake := coreWorkerWake.Subscribe()
			defer unsubscribeAuthWhatsAppWake()
			go authchallenge.NewWhatsAppWorker(
				authChallenges, authWhatsAppSender, logger, authWhatsAppWake, metrics,
				cfg.AuthDeliveryConcurrency, cfg.AuthDeliveryTimeout,
			).Start(ctx)
		}
		if cfg.WelcomeEmailEnabled {
			welcomeWake, unsubscribeWelcomeWake := coreWorkerWake.Subscribe()
			defer unsubscribeWelcomeWake()
			go welcomeemail.NewWorker(
				welcomeemail.NewRepository(dbPool), welcomeSMTPMailer, logger, welcomeWake, metrics,
				cfg.WelcomeEmailConcurrency, cfg.WelcomeEmailTimeout,
			).Start(ctx)
		}
		if len(cfg.NotificationDestinationHMACKey) >= 32 {
			notificationRepository, notificationErr := notificationworker.NewRepository(
				dbPool, cfg.NotificationDestinationHMACKey, cfg.WhatsAppEnabledTemplateKeys,
				cfg.NotificationEmailEnabled, cfg.NotificationWhatsAppEnabled,
			)
			if notificationErr != nil {
				logger.Error("configure notification planner", "error", notificationErr)
				os.Exit(1)
			}
			notificationRepository.WithAdditionalEmails(cfg.AdditionalEmailsEnabled)
			notificationWake, unsubscribeNotificationWake := coreWorkerWake.Subscribe()
			defer unsubscribeNotificationWake()
			go notificationworker.NewPlannerWorker(
				notificationRepository, logger, notificationWake, cfg.NotificationPlannerConcurrency,
			).Start(ctx)
			if cfg.NotificationEmailEnabled {
				emailWake, unsubscribeEmailWake := coreWorkerWake.Subscribe()
				defer unsubscribeEmailWake()
				go notificationworker.NewEmailWorker(
					notificationRepository, notificationSMTPMailer, logger, emailWake, metrics,
					cfg.NotificationEmailConcurrency, cfg.NotificationEmailTimeout,
					cfg.ClientPublicBaseURL, cfg.MarketplacePublicBaseURL,
				).Start(ctx)
			}
			if cfg.WhatsAppBusinessAccountID != "" && cfg.WABAPhoneNumberID != "" {
				var whatsAppSender *whatsapp.Client
				if cfg.NotificationWhatsAppEnabled {
					whatsAppSender, notificationErr = whatsapp.NewClient(whatsapp.ClientConfig{
						BaseURL: cfg.WhatsAppGraphBaseURL, GraphVersion: cfg.WhatsAppGraphVersion,
						PhoneNumberID: cfg.WABAPhoneNumberID, BusinessAccountID: cfg.WhatsAppBusinessAccountID,
						AccessToken: cfg.WABAToken, Timeout: cfg.WhatsAppHTTPTimeout,
						EnabledTemplateKeys: cfg.WhatsAppEnabledTemplateKeys,
					})
					if notificationErr != nil {
						logger.Error("configure WhatsApp notification sender", "error", notificationErr)
						os.Exit(1)
					}
				}
				whatsAppWake, unsubscribeWhatsAppWake := coreWorkerWake.Subscribe()
				defer unsubscribeWhatsAppWake()
				go notificationworker.NewWhatsAppWorker(
					notificationRepository, whatsAppSender, logger, whatsAppWake, metrics,
					cfg.WhatsAppWorkerConcurrency, cfg.WhatsAppHTTPTimeout,
					cfg.NotificationWhatsAppEnabled,
				).Start(ctx)
			}
		} else {
			logger.Info("notification planner disabled", "reason", "missing destination HMAC key")
		}
		allocationWorker := payments.NewAllocationWorker(
			ledgerRepository,
			capabilityRegistry,
			capabilities.Environment(cfg.PaymentsEnvironment),
			logger,
		)
		allocationWake, unsubscribeAllocationWake := coreWorkerWake.Subscribe()
		defer unsubscribeAllocationWake()
		go allocationWorker.Start(ctx, allocationWake)
		bookingRefundWorker := payments.NewBookingRefundWorker(ledgerRepository, refundProviders, logger)
		bookingRefundWake, unsubscribeBookingRefundWake := coreWorkerWake.Subscribe()
		defer unsubscribeBookingRefundWake()
		go bookingRefundWorker.Start(ctx, bookingRefundWake)
		if cfg.AdditionalEmailsEnabled && cfg.NotificationEmailEnabled {
			financialEmailWake, unsubscribeFinancialEmail := coreWorkerWake.Subscribe()
			defer unsubscribeFinancialEmail()
			go payments.NewFinancialEmailWorker(ledgerRepository, notificationSMTPMailer, cfg.NotificationDestinationHMACKey, cfg.ClientPublicBaseURL, cfg.MarketplacePublicBaseURL, logger).Start(ctx, financialEmailWake)
		}
	}

	appdataHandler := appdata.NewHandler(appdataRepo, authHandler, destinationService, r2Service, smtpMailer, aiClient, checkoutService, payoutService, paymentEvents, paymentReconciliations, cfg.ClientPublicBaseURL, cfg.MarketplacePublicBaseURL)
	var notificationContacts *whatsapp.ContactFoundationRepository
	tessaLinks := whatsapp.NewTessaLinkRepository(dbPool, cfg.WABAPhoneNumberID, cfg.WABABusinessPhoneE164, cfg.TessaWhatsAppLinkingEnabled)
	// A separate chat rollout switch preserves linking without starting AI replies.
	var tessaWhatsAppClient *whatsapp.Client
	if cfg.TessaWhatsAppLinkingEnabled && (runsCoreWorkers || (cfg.TessaWhatsAppConversationsEnabled && runsAIWorkers)) {
		var clientErr error
		tessaWhatsAppClient, clientErr = whatsapp.NewClient(whatsapp.ClientConfig{BaseURL: cfg.WhatsAppGraphBaseURL, GraphVersion: cfg.WhatsAppGraphVersion,
			PhoneNumberID: cfg.WABAPhoneNumberID, BusinessAccountID: cfg.WhatsAppBusinessAccountID, AccessToken: cfg.WABAToken, Timeout: cfg.WhatsAppHTTPTimeout})
		if clientErr != nil {
			logger.Error("configure Tessa WhatsApp transport", "error", clientErr)
			os.Exit(1)
		}
	}
	if cfg.TessaWhatsAppConversationsEnabled {
		if err := tessaLinks.WithAssistantReplies(cfg.ClientPublicBaseURL, cfg.TessaAINoticeRevision); err != nil {
			logger.Error("configure Tessa WhatsApp replies", "error", err)
			os.Exit(1)
		}
		tessaLinks.WithConversationIngress(appdata.NewTessaWhatsAppIngress(cfg.TessaAINoticeRevision))
	}
	if cfg.AuthEmailEnabled {
		tessaLinks.WithEmailLinking(authChallenges, cfg.ClientPublicBaseURL)
	}
	appdataHandler.ConfigureTessaWhatsApp(tessaLinks)
	if runsCoreWorkers {
		var tessaSender whatsapp.TextSender
		if tessaWhatsAppClient != nil {
			tessaSender = tessaWhatsAppClient
		}
		if cfg.WABAPhoneNumberID != "" {
			tessaWake, unsubscribeTessaWake := coreWorkerWake.Subscribe()
			defer unsubscribeTessaWake()
			go whatsapp.NewTessaControlWorker(tessaLinks, tessaSender, logger).Start(ctx, tessaWake)
		}
	}
	if runsAPI && cfg.NotificationContactFoundationConfigured() {
		notificationContacts, err = whatsapp.NewContactFoundationRepository(
			dbPool, cfg.NotificationDestinationHMACKey, cfg.WABABusinessPhoneE164,
			cfg.MetaWebhookConfigured(), cfg.NotificationEmailEnabled, cfg.NotificationWhatsAppEnabled,
		)
		if err != nil {
			logger.Error("configure notification contact foundation", "error", err)
			os.Exit(1)
		}
		appdataHandler.ConfigureNotificationContacts(notificationContacts)
	}
	appdataHandler.ConfigureStreamBudgets(
		cfg.SSEMaxConnections, cfg.SSEMaxConnectionsPerIP, cfg.PaymentSSEMaxConnectionsPerToken,
	)
	appdataHandler.ConfigureOperationalMetrics(metrics)
	if redisClient != nil {
		appdataHandler.ConfigureInboxCommandLimiter(redisClient)
	}
	metrics.RegisterInbox(func() observability.InboxSnapshot {
		snapshot := appdataHandler.InboxMetrics().Snapshot()
		return observability.InboxSnapshot{
			StreamsCurrent: snapshot.StreamsCurrent, StreamsOpened: snapshot.StreamsOpened,
			StreamsRejected: snapshot.StreamsRejected, StreamFailures: snapshot.StreamFailures,
			StreamResets: snapshot.StreamResets, EventsDelivered: snapshot.EventsDelivered,
			EventLagTotal: snapshot.EventLag, EventLagMaximum: snapshot.EventLagMax,
		}
	})
	inboxAIModelName := cfg.DefaultAIModelName()
	inboxAIGenerationLimiter := appdata.NewInboxAIGenerationLimiter(cfg.InboxAIMaxConcurrency)
	appdataHandler.ConfigureInboxAIDrafts(
		cfg.InboxAIDraftsEnabled,
		cfg.DefaultAIProvider,
		inboxAIModelName,
		cfg.InboxAIModelConfigHash(),
		cfg.InboxAIProviderAllowlist,
		inboxAIGenerationLimiter,
	)
	appdataHandler.ConfigureInboxAIAutomation(
		cfg.InboxAIAutomationEnabled,
		cfg.InboxAIAutomationProviderAllowlist,
		cfg.InboxAISemiPilotReplyDelay,
	)
	if cfg.InboxAIDraftsEnabled && runsAIWorkers {
		inboxAIDraftWorker, workerErr := appdata.NewInboxAIDraftWorker(
			appdataRepo,
			aiClient,
			inboxAIGenerationLimiter,
			logger,
			appdata.InboxAIDraftWorkerConfig{
				MaxConcurrency: cfg.InboxAIMaxConcurrency,
				JobTimeout:     cfg.SynchronousAIRouteTimeout(),
			},
		)
		if workerErr != nil {
			logger.Error("configure inbox AI draft worker", "error", workerErr)
			os.Exit(1)
		}
		inboxDraftWakes, unsubscribeInboxDraftWakes := subscribeWorkerWakes(aiWorkerWake, cfg.InboxAIMaxConcurrency)
		defer unsubscribeInboxDraftWakes()
		inboxAIDraftWorker.Start(ctx, inboxDraftWakes...)
	}
	if cfg.TessaAIEnabled && runsAPI {
		tessaEvents := appdata.NewTessaEventBroker(directDBPool, logger)
		go tessaEvents.Start(ctx)
		appdataHandler.ConfigureTessa(
			true, cfg.TessaAINoticeRevision,
			cfg.TessaAIPrimaryProvider, cfg.AIModelName(cfg.TessaAIPrimaryProvider),
			cfg.TessaAIConfigHash(), tessaEvents,
		)
	}
	if cfg.TessaAIEnabled && runsAIWorkers {
		primaryGenerator := newTessaGenerator(cfg, cfg.TessaAIPrimaryProvider, cfg.TessaAIPrimaryRequestTimeout)
		primary := tessa.Provider{
			Name: cfg.TessaAIPrimaryProvider, Model: cfg.AIModelName(cfg.TessaAIPrimaryProvider),
			Generator: primaryGenerator, Timeout: cfg.TessaAIPrimaryRequestTimeout,
		}
		var fallback *tessa.Provider
		if cfg.TessaAIFallbackProvider != "" {
			fallback = &tessa.Provider{
				Name: cfg.TessaAIFallbackProvider, Model: cfg.AIModelName(cfg.TessaAIFallbackProvider),
				Generator: newTessaGenerator(cfg, cfg.TessaAIFallbackProvider, cfg.TessaAIFallbackRequestTimeout),
				Timeout:   cfg.TessaAIFallbackRequestTimeout,
			}
		}
		tessaService, serviceErr := tessa.NewService(primary, fallback, cfg.TessaAIMaxInputTokens)
		if serviceErr != nil {
			logger.Error("configure Tessa model service", "error", serviceErr)
			os.Exit(1)
		}
		helpIndex, helpErr := tessa.LoadHelpIndex()
		if helpErr != nil {
			logger.Error("load Tessa help corpus", "error", helpErr)
			os.Exit(1)
		}
		tessaWorkerConfig := appdata.TessaWorkerConfig{
			MaxConcurrency: cfg.TessaAIWorkerConcurrency, TurnTimeout: cfg.TessaAITurnTimeout,
			ConfigHash: cfg.TessaAIConfigHash(), NoticeRevision: cfg.TessaAINoticeRevision,
		}
		if cfg.TessaWhatsAppConversationsEnabled {
			tessaWorkerConfig.WhatsAppPhoneNumberID = cfg.WABAPhoneNumberID
		}
		tessaWorker, workerErr := appdata.NewTessaWorker(
			appdataRepo, tessaService, helpIndex, inboxAIGenerationLimiter, logger,
			tessaWorkerConfig,
		)
		if workerErr != nil {
			logger.Error("configure Tessa worker", "error", workerErr)
			os.Exit(1)
		}
		if cfg.TessaWhatsAppConversationsEnabled {
			tessaWorker.WithWhatsAppTyping(tessaWhatsAppClient)
		}
		tessaWakes, unsubscribeTessaWakes := subscribeWorkerWakes(aiWorkerWake, cfg.TessaAIWorkerConcurrency)
		defer unsubscribeTessaWakes()
		tessaWorker.Start(ctx, tessaWakes...)
	}
	if runsMaintenance {
		reservationExpiryWorker := appdata.NewInboxAIReservationExpiryWorker(
			dbPool, appdataRepo, checkoutService, appdataHandler.InboxMetrics(), logger,
		)
		go reservationExpiryWorker.Start(ctx)
	}
	if cfg.InboxAIAutomationEnabled && runsAIWorkers {
		semiPilotWorker, workerErr := appdata.NewInboxAISemiPilotWorker(
			appdataRepo,
			aiClient,
			inboxAIGenerationLimiter,
			logger,
			appdata.InboxAISemiPilotWorkerConfig{
				ModelProvider:   cfg.DefaultAIProvider,
				ModelName:       inboxAIModelName,
				ModelConfigHash: cfg.InboxAIModelConfigHash(),
				MaxConcurrency:  cfg.InboxAIMaxConcurrency,
				PollInterval:    25 * time.Second,
			},
		)
		if workerErr != nil {
			logger.Error("configure inbox semi-pilot worker", "error", workerErr)
			os.Exit(1)
		}
		semiPilotWakes, unsubscribeSemiPilotWakes := subscribeWorkerWakes(aiWorkerWake, cfg.InboxAIMaxConcurrency)
		defer unsubscribeSemiPilotWakes()
		semiPilotWorker.Start(ctx, semiPilotWakes...)
	}
	appdataHandler.ConfigureMarketplaceAuthentication(marketplaceAuthHandler)
	if runsAPI {
		inboxEvents := appdata.NewInboxEventBroker(directDBPool, logger)
		appdataHandler.ConfigureInboxEvents(inboxEvents)
		go inboxEvents.Start(ctx)
		bookingEvents := appdata.NewBookingEventBroker(directDBPool, logger)
		appdataHandler.ConfigureBookingEvents(bookingEvents)
		go bookingEvents.Start(ctx)
	}
	if runsMaintenance {
		providerDailyMetrics := appdata.NewProviderDailyMetricsWorker(appdataRepo, logger)
		go providerDailyMetrics.Start(ctx)
		dataMaintenance := appdata.NewDataMaintenanceWorker(dbPool, logger).WithAdminRetention(cfg.AdminEnabled)
		go dataMaintenance.Start(ctx)
		inboxEventRetention := appdata.NewInboxEventRetentionWorker(dbPool, logger)
		go inboxEventRetention.Start(ctx)
		tessaRetention := appdata.NewTessaRetentionWorker(dbPool, logger)
		go tessaRetention.Start(ctx)
		inboxAIRunRetention := appdata.NewInboxAIRunRetentionWorker(dbPool, logger)
		go inboxAIRunRetention.Start(ctx)
		inboxTelemetry := appdata.NewInboxTelemetryWorker(dbPool, appdataHandler.InboxMetrics(), logger)
		go inboxTelemetry.Start(ctx)
	}

	var servedAuthHandler *auth.Handler
	var servedWebhookHandler *payments.ProviderWebhookHandler
	var servedAppdataHandler *appdata.Handler
	var metaWhatsAppWebhook http.Handler
	if runsAPI {
		servedAuthHandler = authHandler
		servedWebhookHandler = providerWebhookHandler
		servedAppdataHandler = appdataHandler
		if cfg.MetaWebhookConfigured() {
			metaHandler, metaHandlerErr := whatsapp.NewWebhookHandler(
				whatsapp.WebhookConfig{
					AppSecret: cfg.MetaAppSecret, VerifyToken: cfg.MetaVerifyToken,
					BusinessID: cfg.WhatsAppBusinessAccountID, PhoneNumberID: cfg.WABAPhoneNumberID,
				},
				whatsapp.NewWebhookRepository(dbPool, notificationContacts).WithTessaLinks(tessaLinks),
				logger,
			)
			if metaHandlerErr != nil {
				logger.Error("configure Meta WhatsApp webhook", "error", metaHandlerErr)
				os.Exit(1)
			}
			metaWhatsAppWebhook = metaHandler
		}
	}
	operational := server.OperationalDependencies{
		Readiness: readiness, Metrics: metrics, Role: cfg.ProcessRole,
		ConfigurationReady: true, WorkersReady: true,
		MaintenanceOwnership: maintenanceOwnership,
		MetaWhatsAppWebhook:  metaWhatsAppWebhook,
	}
	if runsAPI && cfg.AdminEnabled {
		adminService, adminErr := admin.New(dbPool, admin.Config{PublicURL: cfg.AdminPublicURL, EncryptionKeys: cfg.AdminMFAEncryptionKeys, ActiveKey: cfg.AdminMFAActiveKey, BcryptCost: cfg.AuthBcryptCost}, authSMTPMailer)
		if adminErr != nil {
			logger.Error("configure admin", "error", adminErr)
			os.Exit(1)
		}
		operational.AdminHandler = adminService.WithBookingRepository(appdataRepo).Handler(logger)
	}
	if redisClient != nil {
		operational.RedisReadiness = redisClient
		operational.SharedRateLimiter = redisClient
	}
	httpServer := server.New(
		cfg,
		logger,
		servedAuthHandler,
		servedWebhookHandler,
		servedAppdataHandler,
		operational,
	)

	serverErrCh := make(chan error, 1)
	go func() {
		logger.Info(
			"starting server",
			"addr", cfg.HTTPAddr,
			"process_role", cfg.ProcessRole,
			"default_ai_provider", cfg.DefaultAIProvider,
			"agreement_ai_provider", cfg.AgreementAIProvider,
			"self_hosted_model", cfg.LLMModel,
			"openai_model", cfg.OpenAIModel,
			"openai_compatible_model", cfg.OpenAICompatModel,
		)
		serverErrCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-serverErrCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeoutCause(
		context.Background(),
		cfg.ShutdownTimeout,
		errors.New("http shutdown exceeded configured timeout"),
	)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown server", "error", err)
		os.Exit(1)
	}

	logger.Info("server stopped cleanly")
}

func newTessaGenerator(cfg config.Config, provider string, timeout time.Duration) aisvc.JSONGenerator {
	tessaConfig := cfg
	switch provider {
	case config.AIProviderHosted:
		tessaConfig.OpenAITimeout = timeout
		tessaConfig.OpenAIMaxOutputTokens = int64(cfg.TessaAIMaxOutputTokens)
		tessaConfig.OpenAIResponseLogFile = ""
		return llm.NewOpenAIClient(tessaConfig)
	case config.AIProviderOpenAICompatible:
		tessaConfig.OpenAICompatTimeout = timeout
		tessaConfig.OpenAICompatMaxOutputTokens = cfg.TessaAIMaxOutputTokens
		return llm.NewOpenAICompatibleClient(tessaConfig)
	default:
		tessaConfig.LLMTimeout = timeout
		tessaConfig.LLMMaxOutputTokens = cfg.TessaAIMaxOutputTokens
		return llm.NewClient(tessaConfig)
	}
}

const maintenanceLeadershipKey = "tellbook-maintenance-v1"

type databaseReadiness struct {
	query      *pgxpool.Pool
	leadership *pgxpool.Conn
}

func (readiness *databaseReadiness) Ping(ctx context.Context) error {
	if readiness == nil || readiness.query == nil {
		return errors.New("query database pool is not configured")
	}
	if err := readiness.query.Ping(ctx); err != nil {
		return fmt.Errorf("query database: %w", err)
	}
	if readiness.leadership == nil {
		return nil
	}
	var owned bool
	if err := readiness.leadership.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_locks
			WHERE locktype='advisory' AND pid=pg_backend_pid() AND granted
		)
	`).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return errors.New("maintenance leadership is not owned")
	}
	return nil
}

func acquireMaintenanceLeadership(ctx context.Context, pool *pgxpool.Pool) (*pgxpool.Conn, error) {
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var owned bool
	if err := connection.QueryRow(
		ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, maintenanceLeadershipKey,
	).Scan(&owned); err != nil {
		connection.Release()
		return nil, err
	}
	if !owned {
		connection.Release()
		return nil, errors.New("another maintenance process owns the leader lock")
	}
	return connection, nil
}

func releaseMaintenanceLeadership(connection *pgxpool.Conn) {
	if connection == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = connection.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, maintenanceLeadershipKey)
	connection.Release()
}

func monitorMaintenanceLeadership(
	ctx context.Context,
	connection *pgxpool.Conn,
	logger *slog.Logger,
	stop context.CancelFunc,
) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var owned bool
			err := connection.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM pg_locks
					WHERE locktype='advisory' AND pid=pg_backend_pid() AND granted
				)
			`).Scan(&owned)
			if err != nil || !owned {
				logger.Error("maintenance leadership lost", "error", err)
				stop()
				return
			}
		}
	}
}

func subscribeWorkerWakes(
	broker *payments.CoreWorkerWakeBroker,
	count int,
) ([]<-chan struct{}, func()) {
	if broker == nil || count < 1 {
		return nil, func() {}
	}
	wakes := make([]<-chan struct{}, 0, count)
	unsubscribes := make([]func(), 0, count)
	for range count {
		wake, unsubscribe := broker.Subscribe()
		wakes = append(wakes, wake)
		unsubscribes = append(unsubscribes, unsubscribe)
	}
	return wakes, func() {
		for _, unsubscribe := range unsubscribes {
			unsubscribe()
		}
	}
}
