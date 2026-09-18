package authchallenge

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/mailer"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type securityCaptureSender struct {
	messages []mailer.Message
	err      error
}

func (s *securityCaptureSender) Enabled() bool { return true }
func (s *securityCaptureSender) Send(_ context.Context, m mailer.Message) error {
	s.messages = append(s.messages, m)
	return s.err
}

func tessaSecurityEmailFixture(t *testing.T) (context.Context, *Service, uuid.UUID, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	s, err := NewService(db, Config{EmailEnabled: true, EncryptionKeys: `{"v1":"` + key + `"}`, ActiveKey: "v1", DestinationKey: "tessa-security-test-hmac-key-at-least-32"})
	if err != nil {
		t.Fatal(err)
	}
	clientID, eventID := uuid.New(), uuid.New()
	if _, err = db.Exec(ctx, `INSERT INTO clients(id,full_name,email,email_verified_at) VALUES($1,'Tessa security test',$2,NOW())`, clientID, clientID.String()+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(ctx, `DELETE FROM clients WHERE id=$1`, clientID) })
	if _, err = db.Exec(ctx, `INSERT INTO tessa_whatsapp_security_events(id,client_id,connection_revision,kind,destination,recipient_email) VALUES($1,$2,1,'linked','+2348142751683',$3)`, eventID, clientID, clientID.String()+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	return ctx, s, clientID, eventID
}

func TestTessaSecurityEmailLifecycleIntegration(t *testing.T) {
	ctx, s, clientID, eventID := tessaSecurityEmailFixture(t)
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- s.prepareTessaSecurityEmails(ctx, 100) }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := s.claimEmailJobs(ctx, "security-test", 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var job DeliveryJob
	for _, j := range jobs {
		if j.ChallengeID == eventID {
			job = j
		}
	}
	if job.ID == uuid.Nil || job.TemplateKey != tessaSecurityEmailTemplate {
		t.Fatal("security job not claimed")
	}
	payload, err := s.decryptPayload(job)
	if err != nil || payload.Code != "" || payload.Security == nil {
		t.Fatalf("security payload invalid: %v", err)
	}
	// Identity updates do not retarget a committed security event.
	if _, err = s.db.Exec(ctx, `UPDATE clients SET email=$2,security_revision=security_revision+1 WHERE id=$1`, clientID, "new-"+clientID.String()+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	sender := &securityCaptureSender{}
	w := NewWorker(s, sender, nil, nil, nil, 1, time.Second)
	w.processOne(ctx, job)
	if len(sender.messages) != 1 || sender.messages[0].ToEmail != clientID.String()+"@example.invalid" || !strings.Contains(sender.messages[0].Text, "ending 1683") || strings.Contains(sender.messages[0].Text, "+2348142751683") {
		t.Fatal("incorrect security email recipient or content")
	}
	if !strings.Contains(sender.messages[0].HTML, "WHATSAPP CONNECTED") {
		t.Fatal("worker did not render the Tessa HTML email")
	}
	var accepted bool
	if err = s.db.QueryRow(ctx, `SELECT email_accepted_at IS NOT NULL FROM tessa_whatsapp_security_events WHERE id=$1`, eventID).Scan(&accepted); err != nil || !accepted {
		t.Fatalf("acceptance=%v err=%v", accepted, err)
	}
	// Even after terminal transport rows are retained/deleted, the event is not re-enqueued.
	if _, err = s.db.Exec(ctx, `DELETE FROM auth_code_delivery_jobs WHERE tessa_security_event_id=$1`, eventID); err != nil {
		t.Fatal(err)
	}
	if err = s.prepareTessaSecurityEmails(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM auth_code_delivery_jobs WHERE tessa_security_event_id=$1`, eventID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("re-enqueued jobs=%d err=%v", count, err)
	}
}

func TestTessaSecurityEmailAmbiguousAndDisabledIntegration(t *testing.T) {
	ctx, s, _, eventID := tessaSecurityEmailFixture(t)
	s.emailEnabled = false
	if err := s.prepareTessaSecurityEmails(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var queued bool
	if err := s.db.QueryRow(ctx, `SELECT email_queued_at IS NOT NULL FROM tessa_whatsapp_security_events WHERE id=$1`, eventID).Scan(&queued); err != nil || queued {
		t.Fatal("disabled email queued a send")
	}
	s.emailEnabled = true
	if err := s.prepareTessaSecurityEmails(ctx, 100); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.claimEmailJobs(ctx, "security-unknown", 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sender := &securityCaptureSender{err: &mailer.TransportError{Disposition: mailer.DispositionAmbiguous, Cause: errors.New("lost SMTP acknowledgement")}}
	w := NewWorker(s, sender, nil, nil, nil, 1, time.Second)
	for _, job := range jobs {
		if job.ChallengeID == eventID {
			w.processOne(ctx, job)
		}
	}
	var status string
	if err = s.db.QueryRow(ctx, `SELECT status FROM auth_code_delivery_jobs WHERE tessa_security_event_id=$1`, eventID).Scan(&status); err != nil || status != "unknown" {
		t.Fatalf("status=%s err=%v", status, err)
	}
	jobs, err = s.claimEmailJobs(ctx, "security-restart", 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.ChallengeID == eventID {
			t.Fatal("ambiguous SMTP delivery was retried")
		}
	}
	if len(sender.messages) != 1 {
		t.Fatal("security email was not attempted once")
	}
}

func TestTessaSecurityEmailDoesNotAuthorizeLogin(t *testing.T) {
	if err := validateScope(RealmProvider, tessaSecurityEmailTemplate, nil); err == nil {
		t.Fatal("security notice accepted as auth purpose")
	}
	if validSecurityEmailPayload(&securityEmailPayload{Kind: "linked", PhoneSuffix: "123\n", OccurredAt: time.Now()}) {
		t.Fatal("unsafe phone suffix accepted")
	}
	if validSecurityEmailPayload(&securityEmailPayload{Kind: "sign_in", PhoneSuffix: "1683", OccurredAt: time.Now()}) {
		t.Fatal("auth purpose accepted as security event")
	}
}

func TestTessaSecurityEmailDoesNotDelayAuthCodeIntegration(t *testing.T) {
	ctx, s, clientID, eventID := tessaSecurityEmailFixture(t)
	if err := s.prepareTessaSecurityEmails(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE auth_code_delivery_jobs SET next_attempt_at=NOW()-INTERVAL '1 minute' WHERE tessa_security_event_id=$1`, eventID); err != nil {
		t.Fatal(err)
	}
	challenge, err := s.Start(ctx, StartRequest{Realm: RealmProvider, RawIdentifier: clientID.String() + "@example.invalid", Channel: ChannelEmail, Purpose: PurposeSignIn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Exec(ctx, `DELETE FROM provider_auth_challenges WHERE id=$1`, challenge.ChallengeID) })
	jobs, err := s.claimEmailJobs(ctx, "auth-priority", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ChallengeID != challenge.ChallengeID {
		t.Fatal("security notice displaced a pending login code")
	}
}

func TestTessaSecurityEmailExpiresWithoutLateSendingIntegration(t *testing.T) {
	ctx, s, _, eventID := tessaSecurityEmailFixture(t)
	if _, err := s.db.Exec(ctx, `UPDATE tessa_whatsapp_security_events SET email_deadline=NOW()-INTERVAL '1 second' WHERE id=$1`, eventID); err != nil {
		t.Fatal(err)
	}
	if err := s.prepareTessaSecurityEmails(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var reason string
	var jobs int
	if err := s.db.QueryRow(ctx, `SELECT skip_reason,(SELECT COUNT(*) FROM auth_code_delivery_jobs WHERE tessa_security_event_id=e.id) FROM tessa_whatsapp_security_events e WHERE id=$1`, eventID).Scan(&reason, &jobs); err != nil {
		t.Fatal(err)
	}
	if reason != "expired" || jobs != 0 {
		t.Fatalf("reason=%s jobs=%d", reason, jobs)
	}
}
