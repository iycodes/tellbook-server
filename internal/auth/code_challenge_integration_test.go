package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	"booking/go-server/internal/authchallenge"
	"booking/go-server/internal/config"
	"booking/go-server/internal/mailer"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type authCodeCaptureMailer struct{ messages chan mailer.Message }

func (m *authCodeCaptureMailer) Enabled() bool { return true }
func (m *authCodeCaptureMailer) Send(_ context.Context, message mailer.Message) error {
	m.messages <- message
	return nil
}

func TestProviderEmailCodeLifecycle(t *testing.T) {
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
	email := "provider-code-" + uuid.NewString() + "@example.test"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_auth_challenges WHERE identifier=$1`, email)
		_, _ = pool.Exec(context.Background(), `DELETE FROM clients WHERE email=$1`, email)
	})

	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	challenges, err := authchallenge.NewService(pool, authchallenge.Config{
		EmailEnabled: true, EncryptionKeys: `{"v1":"` + key + `"}`, ActiveKey: "v1",
		DestinationKey: "provider-auth-test-hmac-key-32-bytes",
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(pool), config.Config{
		AuthAccessTokenSecret: "provider-code-integration-secret",
		AuthAccessTokenTTL:    time.Hour, AuthRefreshTokenTTL: 30 * 24 * time.Hour,
		AuthIssuer: "tellbook-provider-code-test",
	}, nil, challenges)

	started, err := service.StartCodeChallenge(ctx, email, authchallenge.ChannelEmail)
	if err != nil {
		t.Fatal(err)
	}
	if started.DeliveryState != "queued" || started.VerificationExpiresInSeconds != nil || started.NextStatusCheckInSeconds == nil {
		t.Fatalf("initial challenge response = %+v", started)
	}
	var ciphertext, nonce []byte
	var keyVersion string
	var verifyExpiresAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT job.payload_ciphertext,job.payload_nonce,job.payload_key_version,challenge.verify_expires_at
		FROM auth_code_delivery_jobs job
		JOIN provider_auth_challenges challenge ON challenge.id=job.provider_challenge_id
		WHERE challenge.id=$1
	`, started.ChallengeID).Scan(&ciphertext, &nonce, &keyVersion, &verifyExpiresAt); err != nil {
		t.Fatal(err)
	}
	if len(ciphertext) == 0 || len(nonce) != 12 || keyVersion != "v1" || verifyExpiresAt != nil || bytes.Contains(ciphertext, []byte(email)) {
		t.Fatalf("queued payload was not encrypted correctly: ciphertext=%d nonce=%d key=%q expiry=%v", len(ciphertext), len(nonce), keyVersion, verifyExpiresAt)
	}

	sender := &authCodeCaptureMailer{messages: make(chan mailer.Message, 8)}
	wake := make(chan struct{}, 1)
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	t.Cleanup(cancelWorker)
	go authchallenge.NewWorker(challenges, sender, nil, wake, nil, 2, 2*time.Second).Start(workerCtx)

	code := deliverProviderCode(t, service, sender, wake, started.ChallengeID)
	if _, err := service.VerifyCodeChallenge(ctx, started.ChallengeID, "00000x", sessionMetadata{}); !errors.Is(err, authchallenge.ErrInvalidChallenge) {
		t.Fatalf("invalid code error = %v", err)
	}
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT failed_attempts FROM provider_auth_challenges WHERE id=$1`, started.ChallengeID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("failed attempts = %d, want 1", attempts)
	}
	verified, err := service.VerifyCodeChallenge(ctx, started.ChallengeID, code, sessionMetadata{UserAgent: "integration", IPAddress: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !verified.IsNewAccount || !verified.OnboardingRequired || verified.RefreshToken == "" || verified.Pair.AccessToken == "" || verified.User.Email != email {
		t.Fatalf("new provider verification = %+v", verified)
	}
	if _, err := service.AuthenticateAccessToken(ctx, verified.Pair.AccessToken); err != nil {
		t.Fatalf("issued access token failed: %v", err)
	}
	if _, err := service.VerifyCodeChallenge(ctx, started.ChallengeID, code, sessionMetadata{}); !errors.Is(err, authchallenge.ErrInvalidChallenge) {
		t.Fatalf("consumed challenge verification error = %v", err)
	}
	if _, err := service.UpdatePassword(ctx, verified.User.ID, "", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	passwordUser, _, passwordRefresh, err := service.Login(ctx, loginInput{
		Identifier: email, Password: "correct horse battery staple",
	}, sessionMetadata{UserAgent: "integration", IPAddress: "127.0.0.1"})
	if err != nil || passwordUser.ID != verified.User.ID || passwordRefresh == "" {
		t.Fatalf("provider password login user=%+v refresh_empty=%t err=%v", passwordUser, passwordRefresh == "", err)
	}
	resetChallenge, err := service.StartPasswordReset(ctx, email, authchallenge.ChannelEmail)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case wake <- struct{}{}:
	default:
	}
	var resetMessage mailer.Message
	select {
	case resetMessage = <-sender.messages:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for provider reset email")
	}
	resetCode := regexp.MustCompile(`\b\d{6}\b`).FindString(resetMessage.Text)
	resetDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(resetDeadline) {
		status, statusErr := challenges.Status(ctx, authchallenge.RealmProvider, resetChallenge.ChallengeID,
			authchallenge.PurposePasswordReset, &verified.User.ID)
		if statusErr == nil && status.VerificationExpiresInSeconds != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	resetVerification, err := service.VerifyPasswordReset(ctx, resetChallenge.ChallengeID, resetCode)
	if err != nil || resetVerification.ResetGrant == "" {
		t.Fatalf("provider reset verification=%+v err=%v", resetVerification, err)
	}
	resetResult, err := service.CompletePasswordReset(ctx, resetVerification.ResetGrant,
		"new correct horse battery staple", sessionMetadata{UserAgent: "integration", IPAddress: "127.0.0.1"})
	if err != nil || resetResult.User.ID != verified.User.ID || resetResult.RefreshToken == "" || resetResult.Pair.AccessToken == "" {
		t.Fatalf("provider password reset result=%+v err=%v", resetResult, err)
	}
	if _, err := service.CompletePasswordReset(ctx, resetVerification.ResetGrant,
		"another secure password", sessionMetadata{}); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("replayed provider reset grant error = %v", err)
	}
	if _, _, _, err := service.Refresh(ctx, passwordRefresh, sessionMetadata{}); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("provider reset did not revoke old refresh session: %v", err)
	}
	unknownEmail := "unknown-provider-" + uuid.NewString() + "@example.test"
	synthetic, err := service.StartPasswordReset(ctx, unknownEmail, authchallenge.ChannelEmail)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_auth_challenges WHERE id=$1`, synthetic.ChallengeID)
	})
	var syntheticDeliveries int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM auth_code_delivery_jobs WHERE provider_challenge_id=$1`, synthetic.ChallengeID).Scan(&syntheticDeliveries); err != nil {
		t.Fatal(err)
	}
	if syntheticDeliveries != 0 {
		t.Fatalf("unknown provider reset queued %d outbound deliveries", syntheticDeliveries)
	}

	backdateChallenge(t, pool, started.ChallengeID)
	repeat, err := service.StartCodeChallenge(ctx, email, authchallenge.ChannelEmail)
	if err != nil {
		t.Fatal(err)
	}
	if repeat.IdentifierType != started.IdentifierType || repeat.DeliveryChannel != started.DeliveryChannel ||
		repeat.DestinationHint != started.DestinationHint || repeat.DeliveryState != started.DeliveryState ||
		(repeat.VerificationExpiresInSeconds == nil) != (started.VerificationExpiresInSeconds == nil) {
		t.Fatalf("existing and unknown identity start responses diverged: first=%+v repeat=%+v", started, repeat)
	}
	if _, err := service.ResendCodeChallenge(ctx, repeat.ChallengeID); !errors.Is(err, authchallenge.ErrTooSoon) {
		t.Fatalf("early resend error = %v", err)
	}
	repeatCode := deliverProviderCode(t, service, sender, wake, repeat.ChallengeID)
	backdateChallenge(t, pool, repeat.ChallengeID)
	resent, err := service.ResendCodeChallenge(ctx, repeat.ChallengeID)
	if err != nil {
		t.Fatal(err)
	}
	resentCode := deliverProviderCode(t, service, sender, wake, resent.ChallengeID)
	if _, err := service.VerifyCodeChallenge(ctx, repeat.ChallengeID, repeatCode, sessionMetadata{}); !errors.Is(err, authchallenge.ErrInvalidChallenge) {
		t.Fatalf("superseded challenge error = %v", err)
	}

	var wait sync.WaitGroup
	wait.Add(2)
	type verificationAttempt struct {
		result CodeVerificationResult
		err    error
	}
	results := make(chan verificationAttempt, 2)
	for range 2 {
		go func() {
			defer wait.Done()
			result, verifyErr := service.VerifyCodeChallenge(ctx, resent.ChallengeID, resentCode, sessionMetadata{})
			results <- verificationAttempt{result: result, err: verifyErr}
		}()
	}
	wait.Wait()
	close(results)
	successes, rejected := 0, 0
	for attempt := range results {
		if attempt.err == nil {
			successes++
			if attempt.result.IsNewAccount || attempt.result.User.ID != verified.User.ID {
				t.Fatalf("existing provider verification = %+v", attempt.result)
			}
		} else if errors.Is(attempt.err, authchallenge.ErrInvalidChallenge) {
			rejected++
		} else {
			t.Fatalf("concurrent verification error = %v", attempt.err)
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("concurrent verification successes=%d rejected=%d", successes, rejected)
	}

	backdateChallenge(t, pool, resent.ChallengeID)
	expiring, err := service.StartCodeChallenge(ctx, email, authchallenge.ChannelEmail)
	if err != nil {
		t.Fatal(err)
	}
	expiringCode := deliverProviderCode(t, service, sender, wake, expiring.ChallengeID)
	if _, err := pool.Exec(ctx, `UPDATE provider_auth_challenges SET delivery_accepted_at=NOW()-INTERVAL '11 minutes',verify_expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, expiring.ChallengeID); err != nil {
		t.Fatal(err)
	}
	expiredStatus, err := service.CodeChallengeStatus(ctx, expiring.ChallengeID)
	if err != nil || expiredStatus.DeliveryState != "expired" {
		t.Fatalf("expired challenge status = %+v, %v", expiredStatus, err)
	}
	if _, err := service.VerifyCodeChallenge(ctx, expiring.ChallengeID, expiringCode, sessionMetadata{}); !errors.Is(err, authchallenge.ErrInvalidChallenge) {
		t.Fatalf("expired challenge error = %v", err)
	}
}

func deliverProviderCode(t *testing.T, service *Service, sender *authCodeCaptureMailer, wake chan<- struct{}, challengeID uuid.UUID) string {
	t.Helper()
	select {
	case wake <- struct{}{}:
	default:
	}
	var message mailer.Message
	select {
	case message = <-sender.messages:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for provider auth email")
	}
	code := regexp.MustCompile(`\b\d{6}\b`).FindString(message.Text)
	if code == "" {
		t.Fatalf("verification code missing from %q", message.Text)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, err := service.CodeChallengeStatus(context.Background(), challengeID)
		if err == nil && status.DeliveryState == "accepted" && status.VerificationExpiresInSeconds != nil {
			if *status.VerificationExpiresInSeconds < 590 || *status.VerificationExpiresInSeconds > 600 {
				t.Fatalf("verification TTL = %d seconds", *status.VerificationExpiresInSeconds)
			}
			return code
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("provider auth challenge delivery was not accepted")
	return ""
}

func backdateChallenge(t *testing.T, pool *pgxpool.Pool, challengeID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE provider_auth_challenges SET created_at=created_at-INTERVAL '46 seconds' WHERE id=$1`, challengeID); err != nil {
		t.Fatal(err)
	}
}
