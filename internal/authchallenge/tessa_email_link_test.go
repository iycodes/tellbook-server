package authchallenge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/whatsapp"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func tessaEmailRequest(t *testing.T, ctx context.Context, s *Service, clientID uuid.UUID) TessaEmailLinkRequest {
	t.Helper()
	id := uuid.New()
	hash := sha256.Sum256(id[:])
	_, err := s.db.Exec(ctx, `INSERT INTO meta_whatsapp_webhook_receipts(id,dedupe_key,waba_id,phone_number_id,event_kind,wamid,processing_status,processed_at,provider_timestamp)
	 VALUES($1,$2,'100','19990001','inbound_message',$3,'completed',NOW(),NOW())`, id, hex.EncodeToString(hash[:]), id.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE id=$1`, id) })
	return TessaEmailLinkRequest{SourceReceiptID: id, ClientID: clientID, Email: clientID.String() + "@example.invalid", PhoneNumberID: "19990001", Sender: "+2348142751683", NoticeRevision: whatsapp.TessaWhatsAppNotice}
}

func issueTessaEmail(t *testing.T, ctx context.Context, s *Service, request TessaEmailLinkRequest) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { var err error; id, err = s.IssueTessaEmailLinkTx(ctx, tx, request); return err })
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func claimTessaEmail(t *testing.T, ctx context.Context, s *Service, id uuid.UUID) (DeliveryJob, string) {
	t.Helper()
	jobs, err := s.claimEmailJobs(ctx, "tessa-link-test", 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.ChallengeID == id {
			payload, err := s.decryptPayload(job)
			if err != nil {
				t.Fatal(err)
			}
			return job, payload.Code
		}
	}
	t.Fatal("linking email not claimed")
	return DeliveryJob{}, ""
}

func verifyTessaEmail(ctx context.Context, s *Service, id uuid.UUID, r TessaEmailLinkRequest, code string) (TessaEmailLinkResult, error) {
	var result TessaEmailLinkResult
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		result, err = s.VerifyTessaEmailLinkTx(ctx, tx, id, r.PhoneNumberID, r.Sender, code, r.NoticeRevision)
		return err
	})
	return result, err
}

func TestTessaEmailLinkLifecycleIntegration(t *testing.T) {
	ctx, s, clientID, _ := tessaSecurityEmailFixture(t)
	r := tessaEmailRequest(t, ctx, s, clientID)
	id := issueTessaEmail(t, ctx, s, r)
	if id == uuid.Nil || issueTessaEmail(t, ctx, s, r) != id {
		t.Fatal("issuance is not idempotent")
	}
	job, code := claimTessaEmail(t, ctx, s, id)
	if result, err := verifyTessaEmail(ctx, s, id, r, code); err != nil || result.Verified {
		t.Fatal("unaccepted delivery verified", err)
	}
	if err := validateScope(RealmProvider, PurposeTessaWhatsAppLink, nil); err == nil {
		t.Fatal("linking purpose accepted for auth")
	}
	for _, realm := range []string{RealmProvider, RealmMarketplaceCustomer} {
		if _, err := s.Start(ctx, StartRequest{Realm: realm, Channel: ChannelEmail, RawIdentifier: r.Email, Purpose: PurposeTessaWhatsAppLink}); err == nil {
			t.Fatal("auth issued Tessa-purpose challenge")
		}
	}
	sender := &securityCaptureSender{}
	NewWorker(s, sender, nil, nil, nil, 1, time.Second).processOne(ctx, job)
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0].Text, "ending 1683") || strings.Contains(sender.messages[0].Text, r.Sender) || !strings.Contains(sender.messages[0].Text, code) || !strings.Contains(sender.messages[0].Text, "not a sign-in code") {
		t.Fatal("incorrect linking mail")
	}
	var accepted, cleared bool
	if err := s.db.QueryRow(ctx, `SELECT status='accepted',payload_ciphertext IS NULL FROM auth_code_delivery_jobs WHERE id=$1`, job.ID).Scan(&accepted, &cleared); err != nil || !accepted || !cleared {
		t.Fatal("delivery not finalized safely", err)
	}
	for _, realm := range []string{RealmProvider, RealmMarketplaceCustomer} {
		for _, purpose := range []string{PurposeSignIn, PurposePasswordReset, PurposeLinkIdentity, PurposeTessaWhatsAppLink} {
			if _, err := s.Verify(ctx, realm, id, code, purpose, nil); !errors.Is(err, ErrInvalidChallenge) {
				t.Fatal("linking proof accepted by authentication", err)
			}
		}
		if _, err := s.VerifyPasswordReset(ctx, realm, id, code); !errors.Is(err, ErrInvalidChallenge) {
			t.Fatal("linking proof accepted for password reset", err)
		}
	}
	for _, mutate := range []func(*TessaEmailLinkRequest){func(r *TessaEmailLinkRequest) { r.Sender = "+2348000000002" }, func(r *TessaEmailLinkRequest) { r.PhoneNumberID = "999" }, func(r *TessaEmailLinkRequest) { r.NoticeRevision = "old" }} {
		other := r
		mutate(&other)
		if result, err := verifyTessaEmail(ctx, s, id, other, code); err != nil || result.Verified {
			t.Fatal("cross-context verification", err)
		}
	}
	results := make(chan TessaEmailLinkResult, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() { result, err := verifyTessaEmail(ctx, s, id, r, code); results <- result; errs <- err }()
	}
	successes := 0
	for range 2 {
		result := <-results
		if result.Verified {
			successes++
			if result.ClientID != clientID {
				t.Fatal("wrong provider")
			}
		}
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent proof consumptions=%d", successes)
	}
}

func TestTessaEmailLinkEligibilityAndRollbackIntegration(t *testing.T) {
	ctx, s, clientID, _ := tessaSecurityEmailFixture(t)
	r := tessaEmailRequest(t, ctx, s, clientID)
	for _, mutate := range []func(*TessaEmailLinkRequest){func(r *TessaEmailLinkRequest) { r.ClientID = uuid.New() }, func(r *TessaEmailLinkRequest) { r.Email = "unknown@example.invalid" }} {
		other := r
		mutate(&other)
		if issueTessaEmail(t, ctx, s, other) != uuid.Nil {
			t.Fatal("ineligible provider received mail")
		}
	}
	if _, err := s.db.Exec(ctx, `UPDATE clients SET email_verified_at=NULL WHERE id=$1`, clientID); err != nil {
		t.Fatal(err)
	}
	if issueTessaEmail(t, ctx, s, r) != uuid.Nil {
		t.Fatal("unverified provider received mail")
	}
	if _, err := s.db.Exec(ctx, `UPDATE clients SET email_verified_at=NOW() WHERE id=$1`, clientID); err != nil {
		t.Fatal(err)
	}
	abort := errors.New("rollback test")
	var id uuid.UUID
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		id, err = s.IssueTessaEmailLinkTx(ctx, tx, r)
		if err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) || id == uuid.Nil {
		t.Fatal("rollback setup failed", err)
	}
	var count int
	if err = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_whatsapp_email_challenges WHERE client_id=$1`, clientID).Scan(&count); err != nil || count != 0 {
		t.Fatal("rollback left challenge", err)
	}
	if err = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM auth_code_delivery_jobs WHERE tessa_link_challenge_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal("rollback left delivery", err)
	}
	if issueTessaEmail(t, ctx, s, r) == uuid.Nil {
		t.Fatal("rollback consumed issue budget")
	}
}

func TestTessaEmailLinkResendAttemptBudgetIntegration(t *testing.T) {
	ctx, s, clientID, _ := tessaSecurityEmailFixture(t)
	r := tessaEmailRequest(t, ctx, s, clientID)
	id := issueTessaEmail(t, ctx, s, r)
	job, code := claimTessaEmail(t, ctx, s, id)
	NewWorker(s, &securityCaptureSender{}, nil, nil, nil, 1, time.Second).processOne(ctx, job)
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for range 3 {
		if result, err := verifyTessaEmail(ctx, s, id, r, wrong); err != nil || result.Verified {
			t.Fatal("invalid code verified", err)
		}
	}
	r2 := tessaEmailRequest(t, ctx, s, clientID)
	if issueTessaEmail(t, ctx, s, r2) != uuid.Nil {
		t.Fatal("resend cooldown bypassed")
	}
	if _, err := s.db.Exec(ctx, `UPDATE tessa_whatsapp_email_challenges SET created_at=NOW()-INTERVAL '2 minutes' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	id2 := issueTessaEmail(t, ctx, s, r2)
	if id2 == uuid.Nil {
		t.Fatal("resend not issued")
	}
	job2, code2 := claimTessaEmail(t, ctx, s, id2)
	NewWorker(s, &securityCaptureSender{}, nil, nil, nil, 1, time.Second).processOne(ctx, job2)
	if result, err := verifyTessaEmail(ctx, s, id, r, code); err != nil || result.Verified {
		t.Fatal("replaced code verified", err)
	}
	wrong = "000000"
	if code2 == wrong {
		wrong = "111111"
	}
	for range 2 {
		if result, err := verifyTessaEmail(ctx, s, id2, r2, wrong); err != nil || result.Verified {
			t.Fatal("invalid resend code verified", err)
		}
	}
	if result, err := verifyTessaEmail(ctx, s, id2, r2, code2); err != nil || result.Verified {
		t.Fatal("resend reset attempt budget", err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE tessa_whatsapp_email_challenges SET created_at=NOW()-INTERVAL '2 minutes' WHERE id=$1`, id2); err != nil {
		t.Fatal(err)
	}
	if issueTessaEmail(t, ctx, s, tessaEmailRequest(t, ctx, s, clientID)) != uuid.Nil {
		t.Fatal("exhausted sender received another code")
	}
}

func TestTessaEmailLinkStaleDispatchIntegration(t *testing.T) {
	for _, scenario := range []string{"security_revision", "expired", "disconnect", "dashboard_replacement", "smtp_unknown"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, s, clientID, _ := tessaSecurityEmailFixture(t)
			r := tessaEmailRequest(t, ctx, s, clientID)
			id := issueTessaEmail(t, ctx, s, r)
			job, code := claimTessaEmail(t, ctx, s, id)
			repo := whatsapp.NewTessaLinkRepository(s.db, r.PhoneNumberID, "+2348000000000", true, []string{clientID.String()})
			var err error
			sender := &securityCaptureSender{}
			switch scenario {
			case "security_revision":
				_, err = s.db.Exec(ctx, `UPDATE clients SET security_revision=security_revision+1 WHERE id=$1`, clientID)
			case "expired":
				_, err = s.db.Exec(ctx, `UPDATE tessa_whatsapp_email_challenges SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, id)
			case "disconnect":
				err = repo.Disconnect(ctx, clientID)
			case "dashboard_replacement":
				_, err = repo.Start(ctx, clientID, r.Sender, r.NoticeRevision)
			case "smtp_unknown":
				sender.err = errors.New("SMTP acceptance unknown")
			}
			if err != nil {
				t.Fatal(err)
			}
			NewWorker(s, sender, nil, nil, nil, 1, time.Second).processOne(ctx, job)
			wantSends := 0
			if scenario == "smtp_unknown" {
				wantSends = 1
			}
			if len(sender.messages) != wantSends {
				t.Fatal("stale linking email dispatched")
			}
			if result, err := verifyTessaEmail(ctx, s, id, r, code); err != nil || result.Verified {
				t.Fatal("inactive code verified", err)
			}
			if scenario == "smtp_unknown" {
				var status string
				if err = s.db.QueryRow(ctx, `SELECT status FROM auth_code_delivery_jobs WHERE id=$1`, job.ID).Scan(&status); err != nil || status != "unknown" {
					t.Fatal("ambiguous delivery not quarantined", err)
				}
				jobs, err := s.claimEmailJobs(ctx, "retry-test", 100, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				for _, j := range jobs {
					if j.ID == job.ID {
						t.Fatal("ambiguous email retried")
					}
				}
			}
		})
	}
}

func TestTessaEmailLinkRevisionAfterDeliveryIntegration(t *testing.T) {
	ctx, s, clientID, _ := tessaSecurityEmailFixture(t)
	r := tessaEmailRequest(t, ctx, s, clientID)
	id := issueTessaEmail(t, ctx, s, r)
	job, code := claimTessaEmail(t, ctx, s, id)
	NewWorker(s, &securityCaptureSender{}, nil, nil, nil, 1, time.Second).processOne(ctx, job)
	if _, err := s.db.Exec(ctx, `UPDATE clients SET security_revision=security_revision+1 WHERE id=$1`, clientID); err != nil {
		t.Fatal(err)
	}
	if result, err := verifyTessaEmail(ctx, s, id, r, code); err != nil || result.Verified {
		t.Fatal("old account-revision code verified", err)
	}
}

func TestTessaEmailLinkPayloadIsolationIntegration(t *testing.T) {
	ctx, s, clientID, _ := tessaSecurityEmailFixture(t)
	r := tessaEmailRequest(t, ctx, s, clientID)
	s.emailEnabled = false
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { _, err := s.IssueTessaEmailLinkTx(ctx, tx, r); return err })
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal("disabled email issued code", err)
	}
	s.emailEnabled = true
	id := issueTessaEmail(t, ctx, s, r)
	job, _ := claimTessaEmail(t, ctx, s, id)
	for _, mutate := range []func(*DeliveryJob){func(j *DeliveryJob) { j.Realm = RealmMarketplaceCustomer }, func(j *DeliveryJob) { j.Channel = ChannelWhatsApp }, func(j *DeliveryJob) { j.TemplateKey = "auth_code_email" }, func(j *DeliveryJob) { j.TemplateKey = tessaSecurityEmailTemplate }} {
		other := job
		mutate(&other)
		if _, err := s.decryptPayload(other); err == nil {
			t.Fatal("cross-purpose payload accepted")
		}
	}
	payload, err := s.decryptPayload(job)
	if err != nil {
		t.Fatal(err)
	}
	payload.Link.PhoneSuffix = "<x/>"
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	job.Ciphertext, err = s.keyring.Encrypt(encoded, deliveryAAD(job.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.decryptPayload(job); err == nil {
		t.Fatal("invalid linking email content accepted")
	}
}
