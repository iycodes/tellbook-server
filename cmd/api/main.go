package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	agreementrepo "booking/go-server/internal/agreements/repository"
	agreementservice "booking/go-server/internal/agreements/service"
	agreementworker "booking/go-server/internal/agreements/worker"
	aisvc "booking/go-server/internal/ai"
	"booking/go-server/internal/appdata"
	"booking/go-server/internal/auth"
	"booking/go-server/internal/config"
	"booking/go-server/internal/database"
	"booking/go-server/internal/mailer"
	"booking/go-server/internal/payments"
	"booking/go-server/internal/payments/capabilities"
	payaza "booking/go-server/internal/payments/payaza"
	paystack "booking/go-server/internal/payments/paystack"
	"booking/go-server/internal/secure"
	"booking/go-server/internal/server"
	"booking/go-server/internal/storage"
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dbPool, err := database.OpenPool(ctx, cfg)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	authRepo := auth.NewRepository(dbPool)
	r2Service, err := storage.NewR2Service(cfg)
	if err != nil {
		logger.Error("configure R2 storage", "error", err)
		os.Exit(1)
	}

	smtpMailer, err := mailer.NewSMTPMailer(mailer.Config{
		Host:               cfg.SMTPHost,
		Port:               cfg.SMTPPort,
		Username:           cfg.SMTPUsername,
		Password:           cfg.SMTPPassword,
		FromEmail:          cfg.SMTPFromEmail,
		FromName:           cfg.SMTPFromName,
		Security:           cfg.SMTPSecurity,
		InsecureSkipVerify: cfg.SMTPInsecureSkipVerify,
		ConnectTimeout:     cfg.SMTPConnectTimeout,
	})
	if err != nil {
		logger.Error("configure smtp mailer", "error", err)
		os.Exit(1)
	}

	authService := auth.NewService(authRepo, cfg, r2Service, smtpMailer)
	authHandler := auth.NewHandler(authService, cfg)
	appdataRepo := appdata.NewRepository(dbPool)
	appdataRepo.ConfigureGoogleMaps(cfg.GoogleMapsServerAPIKey)
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
	aiClient := aisvc.NewClient(cfg.AIServiceBaseURL, &http.Client{Timeout: cfg.AIServiceTimeout})
	agreementRepository := agreementrepo.New(dbPool)
	if err := agreementRepository.SyncSystemTemplates(ctx); err != nil {
		logger.Error("sync system agreement templates", "error", err)
		os.Exit(1)
	}
	var agreementUploadPreparer agreementworker.UploadPreparer
	if r2Service != nil && r2Service.PrivateBucketName() != "" {
		agreementUploadPreparer, err = agreementworker.NewPDFUploadPreparer(r2Service)
		if err != nil {
			logger.Error("configure agreement upload preparation", "error", err)
			os.Exit(1)
		}
	}
	agreementRequestBuilder, err := agreementworker.NewStoredGenerationRequestBuilder(agreementUploadPreparer)
	if err != nil {
		logger.Error("configure agreement generation request builder", "error", err)
		os.Exit(1)
	}
	agreementGenerationWorker, err := agreementworker.NewGenerationWorker(
		agreementRepository,
		aiClient,
		agreementRequestBuilder,
		logger,
		agreementworker.GenerationWorkerConfig{},
	)
	if err != nil {
		logger.Error("configure agreement generation worker", "error", err)
		os.Exit(1)
	}
	if aiClient.Available() {
		go agreementGenerationWorker.Start(ctx)
	} else {
		logger.Info("agreement generation worker disabled", "reason", "missing AI_SERVICE_BASE_URL")
	}
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
		go agreementLifecycleWorker.Start(ctx)
	} else {
		logger.Info("agreement lifecycle worker disabled", "reason", "missing agreement token encryption keys")
	}

	ledgerRepository := payments.NewLedgerRepository(dbPool)
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
		payoutProviders["payaza"] = payazaClient
		webhookVerifiers["payaza"] = payazaClient

		payazaDirectoryClient := payazaClient
		if cfg.PaymentsEnvironment == string(capabilities.EnvironmentTest) {
			payazaDirectoryClient = nil
			if cfg.PayazaLiveEnabled() {
				payazaDirectoryClient, clientErr = payaza.NewClient(payaza.Config{
					PublicKey: cfg.PayazaPublicKey, SecretKey: cfg.PayazaSecretKey,
					BaseURL: cfg.PayazaBaseURL, TenantID: string(capabilities.EnvironmentLive),
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
			SecretKey: cfg.PaystackCredentials(),
			BaseURL:   cfg.PaystackBaseURL,
		})
		if err != nil {
			logger.Error("configure paystack client", "error", err)
			os.Exit(1)
		}
		collectionProviders["paystack"] = paystackClient
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
	paymentEvents := payments.NewPaymentEventBroker(dbPool, logger)
	go paymentEvents.Start(ctx)
	activePayments := payments.NewActivePaymentReconciler(ctx, checkoutService, logger)
	var providerWebhookHandler *payments.ProviderWebhookHandler
	if financialKeyring != nil && len(webhookVerifiers) > 0 {
		providerWebhookHandler = payments.NewProviderWebhookHandler(ledgerService, webhookVerifiers)
		webhookWorker := payments.NewCollectionWebhookWorker(ledgerRepository, ledgerService, checkoutService, logger)
		go webhookWorker.Start(ctx)
		payoutWebhookWorker := payments.NewPayoutWebhookWorker(ledgerRepository, ledgerService, payoutService, logger)
		go payoutWebhookWorker.Start(ctx)
	}
	payoutReconciler := payments.NewPayoutReconciler(ledgerRepository, payoutService, logger)
	go payoutReconciler.Start(ctx)
	allocationWorker := payments.NewAllocationWorker(
		ledgerRepository,
		capabilityRegistry,
		capabilities.Environment(cfg.PaymentsEnvironment),
		logger,
	)
	go allocationWorker.Start(ctx)
	settlementWorker := payments.NewSettlementWorker(ledgerRepository, settlementProviders, logger)
	go settlementWorker.Start(ctx)

	appdataHandler := appdata.NewHandler(appdataRepo, authHandler, destinationService, r2Service, smtpMailer, aiClient, checkoutService, payoutService, paymentEvents, activePayments, cfg.ClientPublicBaseURL)
	appdataReconciler := appdata.NewReconciler(ledgerRepository, checkoutService, logger)
	go appdataReconciler.Start(ctx)

	httpServer := server.New(cfg, logger, authHandler, providerWebhookHandler, appdataHandler)

	serverErrCh := make(chan error, 1)
	go func() {
		logger.Info("starting server", "addr", cfg.HTTPAddr)
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
