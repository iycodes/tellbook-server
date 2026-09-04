package marketplaceauth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"booking/go-server/internal/config"
	"booking/go-server/internal/mailer"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type captureMailer struct{ message mailer.Message }

func (m *captureMailer) Enabled() bool { return true }
func (m *captureMailer) Send(_ context.Context, message mailer.Message) error {
	m.message = message
	return nil
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
	repo := NewRepository(pool)
	sender := &captureMailer{}
	service := NewService(repo, config.Config{AuthRefreshTokenTTL: 30 * 24 * time.Hour, AuthBcryptCost: 10}, sender)
	challenge, err := service.StartChallenge(ctx, email, "email")
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`\b\d{6}\b`).FindString(sender.message.Text)
	if code == "" {
		t.Fatalf("verification code missing from %q", sender.message.Text)
	}
	customer, sessionToken, err := service.VerifyChallenge(ctx, challenge.ChallengeID, code, "integration-test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if customer.Email != email || customer.EmailVerifiedAt == nil {
		t.Fatalf("customer = %+v", customer)
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
	passwordCustomer, err := service.SetPassword(ctx, customer.ID, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !passwordCustomer.HasPassword {
		t.Fatal("saved password was not reflected on the customer")
	}
	principalAfterPassword, err := service.AuthenticatePrincipal(ctx, sessionToken, true)
	if err != nil {
		t.Fatal(err)
	}
	if principalAfterPassword.SecurityRevision <= principalBeforeSecurityChange.SecurityRevision {
		t.Fatalf("password change did not advance security revision: before=%d after=%d",
			principalBeforeSecurityChange.SecurityRevision, principalAfterPassword.SecurityRevision)
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
	phone := fmt.Sprintf("+2348%09d", time.Now().UnixNano()%1_000_000_000)
	linkCode, linkHash, err := newSixDigitCode()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	linkChallenge := Challenge{
		ID: uuid.New(), IdentifierType: "phone", Identifier: phone, DeliveryChannel: "sms",
		Purpose: "link_identity", TargetCustomerID: &customer.ID, CodeHash: linkHash,
		ExpiresAt: now.Add(challengeTTL), CreatedAt: now,
	}
	if err := repo.CreateChallenge(ctx, linkChallenge); err != nil {
		t.Fatal(err)
	}
	linked, err := service.VerifyIdentityLink(ctx, customer.ID, linkChallenge.ID, linkCode)
	if err != nil {
		t.Fatal(err)
	}
	if linked.ID != customer.ID || linked.Phone != phone || linked.PhoneVerifiedAt == nil {
		t.Fatalf("linked customer = %+v", linked)
	}
	principalAfterIdentity, err := service.AuthenticatePrincipal(ctx, sessionToken, true)
	if err != nil {
		t.Fatal(err)
	}
	if principalAfterIdentity.SecurityRevision <= principalAfterPassword.SecurityRevision {
		t.Fatalf("identity change did not advance security revision: password=%d identity=%d",
			principalAfterPassword.SecurityRevision, principalAfterIdentity.SecurityRevision)
	}
	whatsAppCode, whatsAppHash, err := newSixDigitCode()
	if err != nil {
		t.Fatal(err)
	}
	whatsAppChallenge := Challenge{
		ID: uuid.New(), IdentifierType: "whatsapp", Identifier: phone, DeliveryChannel: "whatsapp",
		Purpose: "link_identity", TargetCustomerID: &customer.ID, CodeHash: whatsAppHash,
		ExpiresAt: now.Add(challengeTTL), CreatedAt: now,
	}
	if err := repo.CreateChallenge(ctx, whatsAppChallenge); err != nil {
		t.Fatal(err)
	}
	linked, err = service.VerifyIdentityLink(ctx, customer.ID, whatsAppChallenge.ID, whatsAppCode)
	if err != nil {
		t.Fatal(err)
	}
	if linked.WhatsApp != phone || linked.WhatsAppVerifiedAt == nil {
		t.Fatalf("WhatsApp verification did not stay on the existing customer: %+v", linked)
	}
	signInCode, signInHash, err := newSixDigitCode()
	if err != nil {
		t.Fatal(err)
	}
	whatsAppSignIn := Challenge{
		ID: uuid.New(), IdentifierType: "whatsapp", Identifier: phone, DeliveryChannel: "whatsapp",
		Purpose: "sign_in", CodeHash: signInHash, ExpiresAt: now.Add(challengeTTL), CreatedAt: now,
	}
	if err := repo.CreateChallenge(ctx, whatsAppSignIn); err != nil {
		t.Fatal(err)
	}
	channelCustomer, _, err := service.VerifyChallenge(ctx, whatsAppSignIn.ID, signInCode, "integration-test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if channelCustomer.ID != customer.ID {
		t.Fatalf("same phone contact created another customer: got %s, want %s", channelCustomer.ID, customer.ID)
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
	readAgain, err := repo.GetPreferences(ctx, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !initialPrefs.UpdatedAt.Equal(readAgain.UpdatedAt) {
		t.Fatal("reading preferences changed updated_at")
	}
	prefs, err := repo.UpdatePreferences(ctx, customer.ID, NotificationPreferencesInput{BookingEmail: true, BookingWhatsApp: true})
	if err != nil {
		t.Fatal(err)
	}
	if !prefs.BookingEmail || !prefs.BookingWhatsApp {
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
