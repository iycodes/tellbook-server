package marketplaceauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"regexp"
	"testing"
	"time"

	"booking/go-server/internal/authchallenge"
	"booking/go-server/internal/config"
	"booking/go-server/internal/mailer"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type captureMailer struct {
	messages chan mailer.Message
	wake     chan struct{}
}

func (m *captureMailer) Enabled() bool { return true }
func (m *captureMailer) Send(_ context.Context, message mailer.Message) error {
	m.messages <- message
	return nil
}

func newTestChallengeService(t *testing.T, pool *pgxpool.Pool) *authchallenge.Service {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	service, err := authchallenge.NewService(pool, authchallenge.Config{
		EmailEnabled: true, EncryptionKeys: `{"v1":"` + key + `"}`, ActiveKey: "v1",
		DestinationKey: "auth-challenge-test-hmac-key-32-bytes",
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func startTestAuthWorker(t *testing.T, challenges *authchallenge.Service) *captureMailer {
	t.Helper()
	sender := &captureMailer{messages: make(chan mailer.Message, 8), wake: make(chan struct{}, 1)}
	workerContext, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go authchallenge.NewWorker(challenges, sender, nil, sender.wake, nil, 1, 2*time.Second).Start(workerContext)
	return sender
}

func awaitAuthCode(t *testing.T, sender *captureMailer) string {
	t.Helper()
	select {
	case sender.wake <- struct{}{}:
	default:
	}
	select {
	case message := <-sender.messages:
		code := regexp.MustCompile(`\b\d{6}\b`).FindString(message.Text)
		if code == "" {
			t.Fatalf("verification code missing from %q", message.Text)
		}
		return code
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for auth email")
		return ""
	}
}

func awaitMarketplaceChallengeReady(t *testing.T, service *Service, challengeID uuid.UUID) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, err := service.ChallengeStatus(context.Background(), challengeID)
		if err == nil && status.DeliveryState == "accepted" && status.VerificationExpiresInSeconds != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("auth challenge delivery was not accepted")
}

func TestPasswordlessCustomerAccountLifecycle(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	email := "marketplace-auth-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM marketplace_auth_challenges WHERE identifier=$1`, email)
		_, _ = pool.Exec(ctx, `DELETE FROM marketplace_customers WHERE email=$1`, email)
	})
	repo := NewRepository(pool).WithAdditionalEmails(true)
	challenges := newTestChallengeService(t, pool)
	sender := startTestAuthWorker(t, challenges)
	service := NewService(repo, config.Config{AuthRefreshTokenTTL: 30 * 24 * time.Hour, AuthBcryptCost: 10}, challenges)
	challenge, err := service.StartChallenge(ctx, email, "email")
	if err != nil {
		t.Fatal(err)
	}
	code := awaitAuthCode(t, sender)
	awaitMarketplaceChallengeReady(t, service, challenge.ChallengeID)
	customer, sessionToken, isNewAccount, err := service.VerifyChallenge(ctx, challenge.ChallengeID, code, "integration-test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !isNewAccount {
		t.Fatal("first verified sign-in was not marked as a new account")
	}
	if customer.Email != email || customer.EmailVerifiedAt == nil {
		t.Fatalf("customer = %+v", customer)
	}
	var welcomeCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM welcome_email_jobs
		WHERE marketplace_customer_id=$1 AND audience='marketplace_customer'
	`, customer.ID).Scan(&welcomeCount); err != nil {
		t.Fatal(err)
	}
	if welcomeCount != 1 {
		t.Fatalf("marketplace welcome jobs = %d, want 1", welcomeCount)
	}
	authed, err := service.Authenticate(ctx, sessionToken)
	if err != nil {
		t.Fatal(err)
	}
	if authed.ID != customer.ID {
		t.Fatalf("session customer = %s, want %s", authed.ID, customer.ID)
	}
	principalBeforeSecurityChange, err := service.AuthenticatePrincipal(ctx, sessionToken, true)
	if err != nil {
		t.Fatal(err)
	}
	if principalBeforeSecurityChange.CustomerID != customer.ID ||
		principalBeforeSecurityChange.SessionID == uuid.Nil ||
		principalBeforeSecurityChange.SecurityRevision < 1 ||
		principalBeforeSecurityChange.SessionRevision < 1 {
		t.Fatalf("session principal = %+v", principalBeforeSecurityChange)
	}
	passwordCustomer, err := service.SetPassword(ctx, customer.ID, "", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !passwordCustomer.HasPassword {
		t.Fatal("saved password was not reflected on the customer")
	}
	if _, err := service.AuthenticatePrincipal(ctx, sessionToken, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("password change did not revoke the existing session: %v", err)
	}
	var revisionAfterPassword int64
	if err := pool.QueryRow(ctx, `SELECT security_revision FROM marketplace_customers WHERE id=$1`, customer.ID).Scan(&revisionAfterPassword); err != nil {
		t.Fatal(err)
	}
	if revisionAfterPassword <= principalBeforeSecurityChange.SecurityRevision {
		t.Fatalf("password change did not advance security revision: before=%d after=%d",
			principalBeforeSecurityChange.SecurityRevision, revisionAfterPassword)
	}
	passwordAuthed, passwordToken, err := service.PasswordLogin(ctx, email, "correct horse battery staple", "integration-test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if passwordAuthed.ID != customer.ID || passwordToken == "" {
		t.Fatalf("password login customer = %+v, token empty = %t", passwordAuthed, passwordToken == "")
	}
	if _, _, err := service.PasswordLogin(ctx, email, "wrong password", "integration-test", "127.0.0.1"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("wrong password error = %v", err)
	}
	resetChallenge, err := service.StartPasswordReset(ctx, email, "email")
	if err != nil {
		t.Fatal(err)
	}
	resetCode := awaitAuthCode(t, sender)
	resetDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(resetDeadline) {
		status, statusErr := challenges.Status(ctx, authchallenge.RealmMarketplaceCustomer,
			resetChallenge.ChallengeID, authchallenge.PurposePasswordReset, &customer.ID)
		if statusErr == nil && status.VerificationExpiresInSeconds != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	verification, err := service.VerifyPasswordReset(ctx, resetChallenge.ChallengeID, resetCode)
	if err != nil || verification.ResetGrant == "" {
		t.Fatalf("verify password reset = %+v, %v", verification, err)
	}
	resetCustomer, resetToken, err := service.CompletePasswordReset(
		ctx, verification.ResetGrant, "new correct horse battery staple", "integration-test", "127.0.0.1",
	)
	if err != nil || resetCustomer.ID != customer.ID || resetToken == "" {
		t.Fatalf("complete password reset customer=%+v token_empty=%t err=%v", resetCustomer, resetToken == "", err)
	}

	var resetNoticeCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM account_security_events WHERE marketplace_customer_id=$1 AND kind='password_reset'`, customer.ID).Scan(&resetNoticeCount); err != nil || resetNoticeCount != 1 {
		t.Fatalf("password reset notices=%d err=%v", resetNoticeCount, err)
	}
	if _, _, err := service.CompletePasswordReset(ctx, verification.ResetGrant, "another secure password", "integration-test", "127.0.0.1"); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatalf("replayed reset grant error = %v", err)
	}
	if _, err := service.Authenticate(ctx, passwordToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("password reset did not revoke old session: %v", err)
	}
	if _, err := service.Authenticate(ctx, resetToken); err != nil {
		t.Fatalf("replacement reset session failed: %v", err)
	}
	unknownEmail := "unknown-" + uuid.NewString() + "@example.com"
	synthetic, err := service.StartPasswordReset(ctx, unknownEmail, "email")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM marketplace_auth_challenges WHERE id=$1`, synthetic.ChallengeID)
	})
	var syntheticDeliveries int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM auth_code_delivery_jobs WHERE marketplace_challenge_id=$1
	`, synthetic.ChallengeID).Scan(&syntheticDeliveries); err != nil {
		t.Fatal(err)
	}
	if syntheticDeliveries != 0 {
		t.Fatalf("unknown-account reset queued %d outbound deliveries", syntheticDeliveries)
	}
	if _, err := pool.Exec(ctx, `UPDATE marketplace_auth_challenges SET created_at=created_at-INTERVAL '46 seconds' WHERE id=$1`, challenge.ChallengeID); err != nil {
		t.Fatal(err)
	}
	repeatChallenge, err := service.StartChallenge(ctx, email, "email")
	if err != nil {
		t.Fatal(err)
	}
	repeatCode := awaitAuthCode(t, sender)
	awaitMarketplaceChallengeReady(t, service, repeatChallenge.ChallengeID)
	repeatCustomer, _, repeatIsNew, err := service.VerifyChallenge(ctx, repeatChallenge.ChallengeID, repeatCode, "integration-test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if repeatCustomer.ID != customer.ID || repeatIsNew {
		t.Fatalf("repeat sign-in account = %s new=%t, want %s false", repeatCustomer.ID, repeatIsNew, customer.ID)
	}
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM welcome_email_jobs WHERE marketplace_customer_id=$1
	`, customer.ID).Scan(&welcomeCount); err != nil {
		t.Fatal(err)
	}
	if welcomeCount != 1 {
		t.Fatalf("repeat sign-in changed marketplace welcome jobs to %d", welcomeCount)
	}
	updated, err := repo.UpdateProfile(ctx, customer.ID, ProfileInput{FullName: "Test Customer", Birthday: "1995-03-14"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.FullName != "Test Customer" || updated.Birthday != "1995-03-14" {
		t.Fatalf("updated customer = %+v", updated)
	}
	address, err := repo.SaveAddress(ctx, customer.ID, nil, AddressInput{Label: "Home", AddressLine1: "14 Test Street", Locality: "Lekki", CountryCode: "NG", IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := repo.ListAddresses(ctx, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 1 || addresses[0].ID != address.ID || !addresses[0].IsDefault {
		t.Fatalf("addresses = %+v", addresses)
	}
	defaultLocation, err := repo.DefaultAddressLocation(ctx, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if defaultLocation.LocationToken == "" || defaultLocation.FormattedAddress == "" {
		t.Fatalf("default location = %+v", defaultLocation)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM resolved_locations WHERE public_token=$1`, defaultLocation.LocationToken)
	})
	initialPrefs, err := repo.GetPreferences(ctx, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !initialPrefs.EmailAvailable || initialPrefs.WhatsAppAvailable {
		t.Fatalf("verified notification capabilities = %+v", initialPrefs)
	}
	readAgain, err := repo.GetPreferences(ctx, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !initialPrefs.UpdatedAt.Equal(readAgain.UpdatedAt) {
		t.Fatal("reading preferences changed updated_at")
	}
	prefs, err := repo.UpdatePreferences(ctx, customer.ID, NotificationPreferencesInput{BookingEmail: true})
	if err != nil {
		t.Fatal(err)
	}
	if !prefs.BookingEmail || prefs.BookingWhatsApp || !prefs.EmailAvailable || prefs.WhatsAppAvailable {
		t.Fatalf("preferences = %+v", prefs)
	}
	if err := service.Logout(ctx, sessionToken); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, sessionToken); err == nil {
		t.Fatal("logged-out session remained valid")
	}
	if _, err := service.AuthenticatePrincipal(ctx, sessionToken, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("logged-out principal error = %v, want ErrNotFound", err)
	}
}
