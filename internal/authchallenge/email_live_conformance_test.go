package authchallenge

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/config"
	"booking/go-server/internal/mailer"

	"github.com/jackc/pgx/v5/pgxpool"
)

type liveAuthEmailSender struct {
	sender      mailer.Sender
	destination string
	code        string
}

func (sender *liveAuthEmailSender) Send(ctx context.Context, message mailer.Message) error {
	sender.destination = message.ToEmail
	for _, field := range strings.Fields(message.Text) {
		if validCode(field) {
			sender.code = field
			break
		}
	}
	return sender.sender.Send(ctx, message)
}

func (sender *liveAuthEmailSender) Enabled() bool {
	return sender != nil && sender.sender != nil && sender.sender.Enabled()
}

func TestEmailAuthLiveConformance(t *testing.T) {
	if os.Getenv("RUN_AUTH_EMAIL_LIVE_CONFORMANCE") != "true" {
		t.Skip("set RUN_AUTH_EMAIL_LIVE_CONFORMANCE=true to send real authentication emails")
	}
	t.Chdir("../..")
	if err := config.LoadDotEnv(); err != nil && !errors.Is(err, config.ErrNoEnvFileFound) && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	destination := strings.TrimSpace(os.Getenv("AUTH_EMAIL_TEST_DESTINATION"))
	if destination == "" {
		t.Fatal("AUTH_EMAIL_TEST_DESTINATION is required for the authorized live recipient")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AuthEmailEnabled {
		t.Fatal("AUTH_EMAIL_ENABLED must be true")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM auth_code_delivery_jobs
		WHERE channel='email' AND status IN ('pending','retry','processing')
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Fatalf("refusing live email conformance with %d unrelated queued email job(s)", queued)
	}

	smtpSender, err := mailer.NewSMTPMailer(mailer.Config{
		Host: cfg.SMTPHost, Port: cfg.SMTPPort,
		Username: cfg.SMTPUsername, Password: cfg.SMTPPassword,
		FromEmail: cfg.SMTPFromEmail, FromName: cfg.SMTPFromName,
		Security: cfg.SMTPSecurity, InsecureSkipVerify: cfg.SMTPInsecureSkipVerify,
		ConnectTimeout: cfg.SMTPConnectTimeout, SendTimeout: cfg.AuthDeliveryTimeout,
		MaxConnections: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if smtpSender == nil {
		t.Fatal("SMTP credentials are required")
	}
	service, err := NewService(pool, Config{
		EmailEnabled:   true,
		EncryptionKeys: cfg.AuthDeliveryEncryptionKeys,
		ActiveKey:      cfg.AuthDeliveryActiveKey,
		DestinationKey: cfg.AuthDestinationHMACKey,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, realm := range []string{RealmProvider, RealmMarketplaceCustomer} {
		t.Run(realm, func(t *testing.T) {
			runEmailAuthLiveConformance(t, ctx, pool, service, smtpSender, realm, destination, cfg.AuthDeliveryTimeout)
		})
	}
}

func runEmailAuthLiveConformance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, service *Service, smtpSender mailer.Sender, realm, destination string, timeout time.Duration) {
	t.Helper()
	started, err := service.Start(ctx, StartRequest{
		Realm: realm, RawIdentifier: destination,
		Channel: ChannelEmail, Purpose: PurposeSignIn,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if realm == RealmProvider {
			_, _ = pool.Exec(context.Background(), `DELETE FROM provider_auth_challenges WHERE id=$1`, started.ChallengeID)
			return
		}
		_, _ = pool.Exec(context.Background(), `DELETE FROM marketplace_auth_challenges WHERE id=$1`, started.ChallengeID)
	})

	sender := &liveAuthEmailSender{sender: smtpSender}
	worker := NewWorker(service, sender, nil, nil, nil, 1, timeout)
	jobs, err := service.claimEmailJobs(ctx, worker.workerID, 1, worker.leaseDuration)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ChallengeID != started.ChallengeID {
		t.Fatalf("claimed email jobs do not match the new %s challenge", realm)
	}
	worker.processOne(ctx, jobs[0])
	status, err := service.Status(ctx, realm, started.ChallengeID, PurposeSignIn, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.DeliveryState != "accepted" || status.VerificationExpiresInSeconds == nil {
		t.Fatalf("live email delivery state = %q", status.DeliveryState)
	}
	if sender.destination != destination || !validCode(sender.code) {
		t.Fatal("live sender did not receive the expected destination and code contract")
	}
	if _, err := service.Verify(ctx, realm, started.ChallengeID, sender.code, PurposeSignIn, nil); err != nil {
		t.Fatalf("verify accepted live email code: %v", err)
	}
}
