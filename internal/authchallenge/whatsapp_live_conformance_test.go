package authchallenge

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/config"
	"booking/go-server/internal/whatsapp"

	"github.com/jackc/pgx/v5/pgxpool"
)

type liveAuthWhatsAppSender struct {
	client      *whatsapp.Client
	instruction string
}

func (sender *liveAuthWhatsAppSender) SendTemplate(ctx context.Context, message whatsapp.TemplateMessage) (whatsapp.SendResult, error) {
	sender.instruction = message.Values.Body["code_instruction"]
	return sender.client.SendTemplate(ctx, message)
}

func TestWhatsAppAuthLiveConformance(t *testing.T) {
	runWhatsAppAuthLiveConformance(t, RealmProvider)
}

func TestMarketplaceWhatsAppAuthLiveConformance(t *testing.T) {
	runWhatsAppAuthLiveConformance(t, RealmMarketplaceCustomer)
}

func runWhatsAppAuthLiveConformance(t *testing.T, realm string) {
	if os.Getenv("RUN_AUTH_WHATSAPP_LIVE_CONFORMANCE") != "true" {
		t.Skip("set RUN_AUTH_WHATSAPP_LIVE_CONFORMANCE=true to send a real authentication message")
	}
	t.Chdir("../..")
	if err := config.LoadDotEnv(); err != nil && !errors.Is(err, config.ErrNoEnvFileFound) && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	destination := strings.TrimSpace(os.Getenv("AUTH_WHATSAPP_TEST_DESTINATION"))
	if destination == "" {
		t.Fatal("AUTH_WHATSAPP_TEST_DESTINATION is required for the authorized live recipient")
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		t.Fatal("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	service, err := NewService(pool, Config{
		WhatsAppEnabled: true,
		EncryptionKeys:  os.Getenv("AUTH_DELIVERY_ENCRYPTION_KEYS"),
		ActiveKey:       os.Getenv("AUTH_DELIVERY_ACTIVE_KEY"),
		DestinationKey:  os.Getenv("AUTH_DESTINATION_HMAC_KEY"),
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := whatsapp.NewClient(whatsapp.ClientConfig{
		BaseURL:             envOr("WHATSAPP_GRAPH_BASE_URL", "https://graph.facebook.com"),
		GraphVersion:        envOr("WHATSAPP_GRAPH_VERSION", "v24.0"),
		PhoneNumberID:       os.Getenv("WABA_PHONE_NUMBER_ID"),
		BusinessAccountID:   os.Getenv("WHATSAPP_BUSINESS_ACCOUNT_ID"),
		AccessToken:         os.Getenv("WABA_TOKEN"),
		Timeout:             15 * time.Second,
		EnabledTemplateKeys: []string{string(whatsapp.TemplateAuthCode)},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(ctx, StartRequest{
		Realm: realm, RawIdentifier: destination,
		Channel: ChannelWhatsApp, Purpose: PurposeSignIn,
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
	sender := &liveAuthWhatsAppSender{client: client}
	worker := NewWhatsAppWorker(service, sender, nil, nil, nil, 1, 15*time.Second)
	worker.processDeliveryCycle(ctx)
	status, err := service.Status(ctx, realm, started.ChallengeID, PurposeSignIn, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.DeliveryState != "accepted" || status.VerificationExpiresInSeconds == nil {
		t.Fatalf("live delivery state = %q", status.DeliveryState)
	}
	callbackDeadline := time.NewTimer(30 * time.Second)
	defer callbackDeadline.Stop()
	callbackPoll := time.NewTicker(time.Second)
	defer callbackPoll.Stop()
	for status.DeliveryState != "sent" && status.DeliveryState != "delivered" {
		select {
		case <-ctx.Done():
			t.Fatal("live WhatsApp status callback timed out")
		case <-callbackDeadline.C:
			t.Fatal("live WhatsApp status callback was not received within 30 seconds")
		case <-callbackPoll.C:
			worker.processStatusCycle(ctx)
			status, err = service.Status(ctx, realm, started.ChallengeID, PurposeSignIn, nil)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	code := strings.TrimPrefix(sender.instruction, "Use the code ")
	if !validCode(code) || sender.instruction != "Use the code "+code {
		t.Fatal("live sender did not use the exact v_c_x code parameter contract")
	}
	if _, err := service.Verify(ctx, realm, started.ChallengeID, code, PurposeSignIn, nil); err != nil {
		t.Fatalf("verify accepted live code: %v", err)
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
