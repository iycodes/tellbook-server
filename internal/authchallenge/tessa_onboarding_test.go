package authchallenge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/whatsapp"
	"github.com/google/uuid"
)

type onboardingHarness struct {
	t        *testing.T
	ctx      context.Context
	s        *Service
	clientID uuid.UUID
	handler  *whatsapp.WebhookHandler
	repo     *whatsapp.TessaLinkRepository
	sender   string
}

func newOnboardingHarness(t *testing.T) *onboardingHarness {
	ctx, s, id, eventID := tessaSecurityEmailFixture(t)
	if _, err := s.db.Exec(ctx, `DELETE FROM tessa_whatsapp_security_events WHERE id=$1`, eventID); err != nil {
		t.Fatal(err)
	}
	repo := whatsapp.NewTessaLinkRepository(s.db, "19990001", "+2348000000000", true, []string{id.String()}).WithEmailLinking(s, "https://provider.example.invalid")
	h, err := whatsapp.NewWebhookHandler(whatsapp.WebhookConfig{AppSecret: "test-only-secret", VerifyToken: "test-only-verify", BusinessID: "100", PhoneNumberID: "19990001"}, whatsapp.NewWebhookRepository(s.db, nil).WithTessaLinks(repo), nil)
	if err != nil {
		t.Fatal(err)
	}
	sender := "2348142751683"
	t.Cleanup(func() {
		s.db.Exec(ctx, `DELETE FROM tessa_whatsapp_onboarding WHERE phone_number_id='19990001' AND destination=$1`, "+"+sender)
	})
	return &onboardingHarness{t: t, ctx: ctx, s: s, clientID: id, handler: h, repo: repo, sender: sender}
}
func (h *onboardingHarness) body(text, messageID string) string {
	encoded, _ := json.Marshal(text)
	return fmt.Sprintf(`{"object":"whatsapp_business_account","entry":[{"id":"100","changes":[{"field":"messages","value":{"messaging_product":"whatsapp","metadata":{"phone_number_id":"19990001"},"messages":[{"id":%q,"from":%q,"timestamp":%q,"type":"text","text":{"body":%s}}]}}]}]}`, messageID, h.sender, fmt.Sprint(time.Now().Unix()), encoded)
}
func (h *onboardingHarness) sendBody(body string) int {
	mac := hmac.New(sha256.New, []byte("test-only-secret"))
	mac.Write([]byte(body))
	req := httptest.NewRequest(http.MethodPost, "/webhooks/whatsapp", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	response := httptest.NewRecorder()
	h.handler.ServeHTTP(response, req)
	return response.Code
}
func (h *onboardingHarness) send(text string) (string, string) {
	h.t.Helper()
	id := uuid.NewString()
	body := h.body(text, id)
	h.t.Cleanup(func() {
		h.s.db.Exec(h.ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE phone_number_id='19990001' AND wamid=$1`, id)
	})
	if code := h.sendBody(body); code != http.StatusOK {
		h.t.Fatalf("signed ingress HTTP %d", code)
	}
	var kind string
	err := h.s.db.QueryRow(h.ctx, `SELECT COALESCE((SELECT d.kind FROM tessa_whatsapp_outbox d WHERE d.source_receipt_id=q.id),'') FROM meta_whatsapp_webhook_receipts q WHERE q.phone_number_id='19990001' AND q.wamid=$1`, id).Scan(&kind)
	if err != nil {
		h.t.Fatal(err)
	}
	return kind, body
}
func (h *onboardingHarness) step(text, want string) {
	h.t.Helper()
	if kind, _ := h.send(text); kind != want {
		h.t.Fatalf("onboarding kind=%s want=%s", kind, want)
	}
}
func (h *onboardingHarness) begin(email string) uuid.UUID {
	h.t.Helper()
	h.step("hello", "onboarding_menu")
	h.step("CONNECT", "onboarding_consent")
	h.step("AGREE", "onboarding_email")
	h.step(email, "onboarding_code")
	var id *uuid.UUID
	if err := h.s.db.QueryRow(h.ctx, `SELECT challenge_id FROM tessa_whatsapp_onboarding WHERE phone_number_id='19990001' AND destination=$1`, "+"+h.sender).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	if id == nil {
		return uuid.Nil
	}
	return *id
}

func TestTessaOnboardingSignedEndToEndIntegration(t *testing.T) {
	h := newOnboardingHarness(t)
	id := h.begin(h.clientID.String() + "@example.invalid")
	if id == uuid.Nil {
		t.Fatal("eligible provider did not receive challenge")
	}
	job, code := claimTessaEmail(t, h.ctx, h.s, id)
	NewWorker(h.s, &securityCaptureSender{}, nil, nil, nil, 1, time.Second).processOne(h.ctx, job)
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	kind, wrongBody := h.send(wrong)
	if kind != "onboarding_invalid" {
		t.Fatal("invalid code did not return generic response")
	}
	if h.sendBody(wrongBody) != http.StatusOK {
		t.Fatal("replay rejected")
	}
	var attempts int
	if err := h.s.db.QueryRow(h.ctx, `SELECT failed_attempts FROM tessa_whatsapp_email_challenges WHERE id=$1`, id).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal("replay consumed attempt budget", err)
	}
	messageID := uuid.NewString()
	body := h.body(code, messageID)
	t.Cleanup(func() { h.s.db.Exec(h.ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE wamid=$1`, messageID) })
	results := make(chan int, 2)
	for range 2 {
		go func() { results <- h.sendBody(body) }()
	}
	for range 2 {
		if <-results != http.StatusOK {
			t.Fatal("concurrent completion failed")
		}
	}
	var replies int
	if err := h.s.db.QueryRow(h.ctx, `SELECT COUNT(*) FROM tessa_whatsapp_outbox WHERE client_id=$1 AND kind='linked'`, h.clientID).Scan(&replies); err != nil || replies != 1 {
		t.Fatal("completion reply missing or duplicated", err)
	}
	if h.sendBody(body) != http.StatusOK {
		t.Fatal("completion replay rejected")
	}
	var complete bool
	if err := h.s.db.QueryRow(h.ctx, `SELECT EXISTS(SELECT 1 FROM tessa_whatsapp_connections g JOIN tessa_whatsapp_email_challenges q ON q.client_id=g.client_id
      JOIN tessa_whatsapp_onboarding o ON o.client_id=g.client_id WHERE g.client_id=$1 AND g.destination=$2 AND g.status='active' AND q.id=$3 AND q.consumed_at IS NOT NULL AND o.stage='closed')`, h.clientID, "+"+h.sender, id).Scan(&complete); err != nil || !complete {
		t.Fatal("grant/proof/onboarding did not commit together", err)
	}
	var notices int
	if err := h.s.db.QueryRow(h.ctx, `SELECT COUNT(*) FROM tessa_whatsapp_security_events WHERE client_id=$1`, h.clientID).Scan(&notices); err != nil || notices != 1 {
		t.Fatal("security notice not deduplicated", err)
	}
	h.step("What bookings do I have?", "")
	state, err := h.repo.State(h.ctx, h.clientID)
	if err != nil || state.Status != "connected" || state.ConversationAvailable {
		t.Fatal("untruthful connection capability", err)
	}
	h.step("TESSA DISCONNECT", "disconnected")
	state, err = h.repo.State(h.ctx, h.clientID)
	if err != nil || state.Status != "disconnected" {
		t.Fatal("disconnect failed", err)
	}
}

func TestTessaOnboardingEmailReplacementIntegration(t *testing.T) {
	h := newOnboardingHarness(t)
	_, err := h.s.db.Exec(h.ctx, `INSERT INTO tessa_whatsapp_connections(client_id,phone_number_id,destination,status,security_revision,notice_revision,expires_at)
	 SELECT id,'19990001','+2348000000002','active',security_revision,$2,NOW()+INTERVAL '30 days' FROM clients WHERE id=$1`, h.clientID, whatsapp.TessaWhatsAppNotice)
	if err != nil {
		t.Fatal(err)
	}
	id := h.begin(h.clientID.String() + "@example.invalid")
	job, code := claimTessaEmail(t, h.ctx, h.s, id)
	NewWorker(h.s, &securityCaptureSender{}, nil, nil, nil, 1, time.Second).processOne(h.ctx, job)
	h.step(code, "linked")
	var replaced bool
	if err = h.s.db.QueryRow(h.ctx, `SELECT EXISTS(SELECT 1 FROM tessa_whatsapp_security_events WHERE client_id=$1 AND kind='replaced')`, h.clientID).Scan(&replaced); err != nil || !replaced {
		t.Fatal("replacement notice missing", err)
	}
	var destination string
	if err = h.s.db.QueryRow(h.ctx, `SELECT destination FROM tessa_whatsapp_connections WHERE client_id=$1 AND status='active'`, h.clientID).Scan(&destination); err != nil || destination != "+"+h.sender {
		t.Fatal("replacement did not use shared completion", err)
	}
}

func TestTessaOnboardingGenericEligibilityAndConsentIntegration(t *testing.T) {
	for _, scenario := range []string{"unknown", "unverified", "not_allowed", "eligible"} {
		t.Run(scenario, func(t *testing.T) {
			h := newOnboardingHarness(t)
			email := h.clientID.String() + "@example.invalid"
			switch scenario {
			case "unknown":
				email = "not-registered@example.invalid"
			case "unverified":
				if _, err := h.s.db.Exec(h.ctx, `UPDATE clients SET email_verified_at=NULL WHERE id=$1`, h.clientID); err != nil {
					t.Fatal(err)
				}
			case "not_allowed":
				h.repo = whatsapp.NewTessaLinkRepository(h.s.db, "19990001", "+2348000000000", true, nil).WithEmailLinking(h.s, "https://provider.example.invalid")
				var err error
				h.handler, err = whatsapp.NewWebhookHandler(whatsapp.WebhookConfig{AppSecret: "test-only-secret", VerifyToken: "test-only-verify", BusinessID: "100", PhoneNumberID: "19990001"}, whatsapp.NewWebhookRepository(h.s.db, nil).WithTessaLinks(h.repo), nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			h.step(email, "onboarding_menu") // an email alone is not consent
			h.step("AGREE", "onboarding_menu")
			h.step("CONNECT", "onboarding_consent")
			h.step("AGREE", "onboarding_email")
			h.step(email, "onboarding_code")
			var count int
			if err := h.s.db.QueryRow(h.ctx, `SELECT COUNT(*) FROM tessa_whatsapp_email_challenges WHERE client_id=$1`, h.clientID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 0
			if scenario == "eligible" {
				want = 1
			}
			if count != want {
				t.Fatal("eligibility was not enforced")
			}
			h.step("not-a-code", "onboarding_invalid")
			h.step("RESEND", "onboarding_code")
			h.step("STOP", "")
			var closed bool
			if err := h.s.db.QueryRow(h.ctx, `SELECT stage='closed' FROM tessa_whatsapp_onboarding WHERE phone_number_id='19990001' AND destination=$1`, "+"+h.sender).Scan(&closed); err != nil || !closed {
				t.Fatal("STOP did not close onboarding", err)
			}
		})
	}
}

func TestTessaOnboardingRollbackEmailAndCompletionIntegration(t *testing.T) {
	for _, phase := range []string{"email", "completion"} {
		t.Run(phase, func(t *testing.T) {
			h := newOnboardingHarness(t)
			h.step("CONNECT", "onboarding_consent")
			h.step("AGREE", "onboarding_email")
			text := h.clientID.String() + "@example.invalid"
			var challengeID uuid.UUID
			if phase == "completion" {
				h.step(text, "onboarding_code")
				if err := h.s.db.QueryRow(h.ctx, `SELECT id FROM tessa_whatsapp_email_challenges WHERE client_id=$1`, h.clientID).Scan(&challengeID); err != nil {
					t.Fatal(err)
				}
				job, code := claimTessaEmail(t, h.ctx, h.s, challengeID)
				text = code
				NewWorker(h.s, &securityCaptureSender{}, nil, nil, nil, 1, time.Second).processOne(h.ctx, job)
			}
			messageID := uuid.NewString()
			body := h.body(text, messageID)
			t.Cleanup(func() { h.s.db.Exec(h.ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE wamid=$1`, messageID) })
			// Temporary, isolated-database trigger proves the final reply failure rolls
			// back receipt, email/proof, grant and security intent together.
			_, err := h.s.db.Exec(h.ctx, `CREATE FUNCTION test_tessa_onboarding_rollback() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
          IF NEW.kind IN ('onboarding_code','linked') THEN RAISE EXCEPTION 'forced onboarding rollback'; END IF; RETURN NEW; END $$;
          CREATE TRIGGER test_tessa_onboarding_rollback BEFORE INSERT ON tessa_whatsapp_outbox FOR EACH ROW EXECUTE FUNCTION test_tessa_onboarding_rollback()`)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				h.s.db.Exec(h.ctx, `DROP TRIGGER IF EXISTS test_tessa_onboarding_rollback ON tessa_whatsapp_outbox; DROP FUNCTION IF EXISTS test_tessa_onboarding_rollback()`)
			})
			if h.sendBody(body) != http.StatusServiceUnavailable {
				t.Fatal("failed reply did not fail ingress")
			}
			var count int
			if err = h.s.db.QueryRow(h.ctx, `SELECT COUNT(*) FROM meta_whatsapp_webhook_receipts WHERE wamid=$1`, messageID).Scan(&count); err != nil || count != 0 {
				t.Fatal("receipt survived rollback", err)
			}
			if err = h.s.db.QueryRow(h.ctx, `SELECT COUNT(*) FROM tessa_whatsapp_connections WHERE client_id=$1`, h.clientID).Scan(&count); err != nil || count != 0 {
				t.Fatal("grant survived rollback", err)
			}
			if phase == "email" {
				if err = h.s.db.QueryRow(h.ctx, `SELECT COUNT(*) FROM tessa_whatsapp_email_challenges WHERE client_id=$1`, h.clientID).Scan(&count); err != nil || count != 0 {
					t.Fatal("email intent survived rollback", err)
				}
			} else {
				var consumed bool
				if err = h.s.db.QueryRow(h.ctx, `SELECT consumed_at IS NOT NULL FROM tessa_whatsapp_email_challenges WHERE id=$1`, challengeID).Scan(&consumed); err != nil || consumed {
					t.Fatal("proof consumption survived rollback", err)
				}
			}
			if _, err = h.s.db.Exec(h.ctx, `DROP TRIGGER test_tessa_onboarding_rollback ON tessa_whatsapp_outbox; DROP FUNCTION test_tessa_onboarding_rollback()`); err != nil {
				t.Fatal(err)
			}
			if h.sendBody(body) != http.StatusOK {
				t.Fatal("restart/retry did not complete")
			}
		})
	}
}
