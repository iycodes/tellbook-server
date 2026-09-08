package whatsapp

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func tessaLinkFixture(t *testing.T) (context.Context, *TessaLinkRepository, uuid.UUID, *WebhookRepository) {
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
	id := uuid.New()
	_, err = db.Exec(ctx, `INSERT INTO clients(id,full_name,email,password_hash,email_verified_at) VALUES($1,'Tessa B1 test',$2,'not-a-password',NOW())`, id, id.String()+"@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	r := NewTessaLinkRepository(db, "19990001", "+2348000000000", true, []string{id.String()})
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM meta_whatsapp_webhook_receipts WHERE id IN (SELECT source_receipt_id FROM tessa_whatsapp_outbox WHERE client_id=$1)`, id)
		_, _ = db.Exec(context.Background(), `DELETE FROM clients WHERE id=$1`, id)
	})
	return ctx, r, id, NewWebhookRepository(db, nil).WithTessaLinks(r)
}
func tessaChallengeToken(t *testing.T, c TessaLinkChallenge) string {
	t.Helper()
	u, err := url.Parse(c.URL)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(u.Query().Get("text"), "TESSA LINK ")
}
func tessaInbound(kind, token, sender string) WebhookReceipt {
	now := time.Now()
	message := uuid.NewString()
	return WebhookReceipt{DedupeKey: receiptDedupeKey("19990001", message), BusinessID: "100", PhoneNumberID: "19990001", EventKind: "inbound_message", MessageID: message, ProviderTimestamp: &now, ProcessingStatus: "completed", control: inboundControl{kind: kind, token: token, sender: sender}}
}

func TestTessaStopPendingEmailLinkIntegration(t *testing.T) {
	for _, kind := range []string{"stop", "tessa_disconnect"} {
		t.Run(kind, func(t *testing.T) {
			ctx, r, clientID, store := tessaLinkFixture(t)
			source := tessaInbound("", "", "2348142751683")
			if err := store.StoreWebhookReceipts(ctx, []WebhookReceipt{source}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				r.db.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, source.DedupeKey)
			})
			id := uuid.New()
			hash := sha256.Sum256([]byte("test-only-hash"))
			_, err := r.db.Exec(ctx, `INSERT INTO tessa_whatsapp_email_challenges(id,source_receipt_id,client_id,phone_number_id,destination,security_revision,notice_revision,code_hash)
		 SELECT $1,q.id,c.id,'19990001','+2348142751683',c.security_revision,$4,$5 FROM clients c,meta_whatsapp_webhook_receipts q WHERE c.id=$2 AND q.dedupe_key=$3`, id, clientID, source.DedupeKey, TessaWhatsAppNotice, hash[:])
			if err != nil {
				t.Fatal(err)
			}
			// Revocation remains available even if new linking is disabled.
			r.enabled = false
			for _, sender := range []string{"2348000000002", "2348142751683"} {
				receipt := tessaInbound(kind, "", sender)
				if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{receipt}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					r.db.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, receipt.DedupeKey)
				})
				var consumed bool
				if err = r.db.QueryRow(ctx, `SELECT consumed_at IS NOT NULL FROM tessa_whatsapp_email_challenges WHERE id=$1`, id).Scan(&consumed); err != nil {
					t.Fatal(err)
				}
				if consumed != (sender == "2348142751683") {
					t.Fatal("email challenge cancellation escaped its sender scope")
				}
			}
		})
	}
}
func TestTessaLinkNumberBoundReplayAndDisconnectIntegration(t *testing.T) {
	ctx, r, id, store := tessaLinkFixture(t)
	if _, err := r.Start(ctx, id, "+2348142751683", "old"); !errors.Is(err, ErrTessaLinkNotice) {
		t.Fatalf("notice: %v", err)
	}
	c, err := r.Start(ctx, id, "+2348142751683", TessaWhatsAppNotice)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(ctx, id, "+2348142751683", TessaWhatsAppNotice); !errors.Is(err, ErrTessaLinkRateLimited) {
		t.Fatalf("cooldown: %v", err)
	}
	token := tessaChallengeToken(t, c)
	wrong := tessaInbound("tessa_link", token, "2348000000001")
	for range 2 {
		if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{wrong}); err != nil {
			t.Fatal(err)
		}
	}
	var attempts int
	if err = r.db.QueryRow(ctx, `SELECT attempts FROM tessa_whatsapp_link_challenges WHERE client_id=$1`, id).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	state, err := r.State(ctx, id)
	if err != nil || state.Status != "disconnected" {
		t.Fatalf("wrong sender connected: %+v %v", state, err)
	}
	correct := tessaInbound("tessa_link", token, "2348142751683")
	for range 2 {
		if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{correct}); err != nil {
			t.Fatal(err)
		}
	}
	state, err = r.State(ctx, id)
	if err != nil || state.Status != "connected" || state.ConversationAvailable || state.PendingExpiresAt != nil {
		t.Fatalf("state: %+v %v", state, err)
	}
	var count int
	r.db.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_whatsapp_outbox WHERE client_id=$1 AND kind='linked'`, id).Scan(&count)
	if count != 1 {
		t.Fatalf("linked outbox=%d", count)
	}
	if err = r.Disconnect(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{tessaInbound("tessa_link", token, "2348142751683")}); err != nil {
		t.Fatal(err)
	}
	state, err = r.State(ctx, id)
	if err != nil || state.Status != "disconnected" {
		t.Fatalf("replay after revoke: %+v %v", state, err)
	}
}
func TestTessaLinkSecurityExpiryAndAttemptsIntegration(t *testing.T) {
	for _, scenario := range []string{"security_revision", "expiry", "attempts"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, r, id, store := tessaLinkFixture(t)
			c, err := r.Start(ctx, id, "+2348142751683", TessaWhatsAppNotice)
			if err != nil {
				t.Fatal(err)
			}
			token := tessaChallengeToken(t, c)
			switch scenario {
			case "security_revision":
				_, err = r.db.Exec(ctx, `UPDATE clients SET security_revision=security_revision+1 WHERE id=$1`, id)
			case "expiry":
				_, err = r.db.Exec(ctx, `UPDATE tessa_whatsapp_link_challenges SET expires_at=NOW()-INTERVAL '1 second' WHERE client_id=$1`, id)
			case "attempts":
				for range 6 {
					if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{tessaInbound("tessa_link", token, "2348000000001")}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{tessaInbound("tessa_link", token, "2348142751683")}); err != nil {
				t.Fatal(err)
			}
			state, err := r.State(ctx, id)
			if err != nil || state.Status != "disconnected" {
				t.Fatalf("invalid grant: %+v %v", state, err)
			}
		})
	}
}
func TestTessaLinkConcurrentCompletionIntegration(t *testing.T) {
	ctx, r, id, store := tessaLinkFixture(t)
	c, err := r.Start(ctx, id, "+2348142751683", TessaWhatsAppNotice)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.StoreWebhookReceipts(ctx, []WebhookReceipt{tessaInbound("tessa_link", tessaChallengeToken(t, c), "2348142751683")})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	r.db.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_whatsapp_outbox WHERE client_id=$1 AND kind='linked'`, id).Scan(&count)
	if count != 1 {
		t.Fatalf("completion count=%d", count)
	}
}
func TestTessaControlTransactionRollbackIntegration(t *testing.T) {
	ctx, r, id, store := tessaLinkFixture(t)
	c, err := r.Start(ctx, id, "+2348142751683", TessaWhatsAppNotice)
	if err != nil {
		t.Fatal(err)
	}
	receipt := tessaInbound("tessa_link", tessaChallengeToken(t, c), "2348142751683")
	tx, err := r.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Missing parent receipt forces outbox insertion to fail after the grant transition.
	if err = r.applyControlTx(ctx, tx, receipt, uuid.New()); err == nil {
		t.Fatal("expected outbox FK failure")
	}
	tx.Rollback(ctx)
	if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{receipt}); err != nil {
		t.Fatal(err)
	}
	state, err := r.State(ctx, id)
	if err != nil || state.Status != "connected" {
		t.Fatalf("retry lost transition: %+v %v", state, err)
	}
}

type fakeTessaTextSender struct {
	calls        int
	beforeReturn func(string)
	err          error
}

func (s *fakeTessaTextSender) SendURLButton(ctx context.Context, to, body string, _ URLButton, correlation string) (SendResult, error) {
	return s.SendText(ctx, to, body, correlation)
}

func (s *fakeTessaTextSender) SendText(_ context.Context, _, _, correlation string) (SendResult, error) {
	s.calls++
	if s.beforeReturn != nil {
		s.beforeReturn(correlation)
	}
	return SendResult{MessageID: "wamid.tessa." + correlation}, s.err
}
func TestTessaControlWorkerAndEarlyCallbackIntegration(t *testing.T) {
	ctx, r, id, store := tessaLinkFixture(t)
	c, err := r.Start(ctx, id, "+2348142751683", TessaWhatsAppNotice)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{tessaInbound("tessa_link", tessaChallengeToken(t, c), "2348142751683")}); err != nil {
		t.Fatal(err)
	}
	sender := &fakeTessaTextSender{}
	worker := NewTessaControlWorker(r, sender, nil)
	sender.beforeReturn = func(correlation string) {
		now := time.Now()
		receipt := WebhookReceipt{DedupeKey: receiptDedupeKey("tessa-status", uuid.NewString()), BusinessID: "100", PhoneNumberID: r.phoneID, EventKind: "status", MessageID: "wamid.tessa." + correlation, MessageStatus: "delivered", CorrelationID: correlation, ProviderTimestamp: &now, ProcessingStatus: "pending"}
		if err := store.StoreWebhookReceipts(ctx, []WebhookReceipt{receipt}); err != nil {
			t.Fatal(err)
		}
		if err := worker.processStatuses(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = worker.processOne(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	r.db.QueryRow(ctx, `SELECT status FROM tessa_whatsapp_outbox WHERE client_id=$1`, id).Scan(&status)
	if status != "delivered" {
		t.Fatalf("early callback regressed: %s", status)
	}
	for range 2 {
		if worked, err := worker.processOne(ctx); err != nil || worked {
			t.Fatalf("restart resend: %v %v", worked, err)
		}
	}
	if sender.calls != 1 {
		t.Fatalf("sends=%d", sender.calls)
	}
}
func TestTessaControlDispatchSecurityFenceIntegration(t *testing.T) {
	ctx, r, id, store := tessaLinkFixture(t)
	c, err := r.Start(ctx, id, "+2348142751683", TessaWhatsAppNotice)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{tessaInbound("tessa_link", tessaChallengeToken(t, c), "2348142751683")}); err != nil {
		t.Fatal(err)
	}
	r.db.Exec(ctx, `UPDATE clients SET security_revision=security_revision+1 WHERE id=$1`, id)
	sender := &fakeTessaTextSender{}
	worker := NewTessaControlWorker(r, sender, nil)
	if _, err = worker.processOne(ctx); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 0 {
		t.Fatal("security-revoked grant sent a reply")
	}
}
func TestTessaControlParsingAndStatus(t *testing.T) {
	hash := sha256.Sum256([]byte("test"))
	token := strings.Repeat("a", len(hash))
	if got := classifyInboundControl("2348142751683", "text", "TESSA LINK "+token); got.kind != "tessa_link" {
		t.Fatalf("control=%+v", got)
	}
	if got := classifyInboundControl("2348142751683", "text", "TESSA DISCONNECT"); got.kind != "tessa_disconnect" {
		t.Fatal("disconnect not recognized")
	}
	now := time.Now()
	if shouldApplyTessaStatus("delivered", &now, "sent", now.Add(time.Second)) {
		t.Fatal("status regressed")
	}
	if shouldApplyTessaStatus("cancelled", nil, "delivered", now) {
		t.Fatal("cancelled status mutated")
	}
}

func TestTessaControlExpiryAndAmbiguousSendIntegration(t *testing.T) {
	for _, scenario := range []string{"window", "challenge", "replacement", "ambiguous", "attempts"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, r, id, store := tessaLinkFixture(t)
			c, err := r.Start(ctx, id, "+2348142751683", TessaWhatsAppNotice)
			if err != nil {
				t.Fatal(err)
			}
			senderNumber := "2348142751683"
			if scenario == "challenge" || scenario == "replacement" {
				senderNumber = "2348000000001"
			}
			if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{tessaInbound("tessa_link", tessaChallengeToken(t, c), senderNumber)}); err != nil {
				t.Fatal(err)
			}
			sender := &fakeTessaTextSender{}
			w := NewTessaControlWorker(r, sender, nil)
			switch scenario {
			case "window":
				_, err = r.db.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET window_expires_at=NOW()-INTERVAL '1 second' WHERE client_id=$1`, id)
			case "challenge":
				_, err = r.db.Exec(ctx, `UPDATE tessa_whatsapp_link_challenges SET expires_at=NOW()-INTERVAL '1 second' WHERE client_id=$1`, id)
			case "replacement":
				_, err = r.db.Exec(ctx, `UPDATE tessa_whatsapp_link_challenges SET created_at=NOW()-INTERVAL '2 minutes' WHERE client_id=$1`, id)
				if err == nil {
					_, err = r.Start(ctx, id, "+2348142751683", TessaWhatsAppNotice)
				}
			case "ambiguous":
				sender.err = errors.New("connection lost after dispatch")
			case "attempts":
				_, err = r.db.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET status='processing',attempt_count=5,available_at=NOW()-INTERVAL '1 second',lease_expires_at=NOW()-INTERVAL '1 second',lease_token=gen_random_uuid() WHERE client_id=$1`, id)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.processOne(ctx); err != nil {
				t.Fatal(err)
			}
			if scenario == "ambiguous" {
				if sender.calls != 1 {
					t.Fatalf("calls=%d", sender.calls)
				}
				if worked, err := w.processOne(ctx); worked || err != nil {
					t.Fatalf("ambiguous resent: %v %v", worked, err)
				}
				if _, err = r.db.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET reconcile_after=NOW()-INTERVAL '1 second' WHERE client_id=$1`, id); err != nil {
					t.Fatal(err)
				}
				if err = w.processStatuses(ctx); err != nil {
					t.Fatal(err)
				}
				var status string
				if err = r.db.QueryRow(ctx, `SELECT status FROM tessa_whatsapp_outbox WHERE client_id=$1`, id).Scan(&status); err != nil || status != "manual_review" {
					t.Fatalf("status=%s err=%v", status, err)
				}
			} else if sender.calls != 0 {
				t.Fatal("stale control sent")
			}
		})
	}
}

func TestTessaStatusRoutingOwnershipIntegration(t *testing.T) {
	ctx, r, id, store := tessaLinkFixture(t)
	c, err := r.Start(ctx, id, "+2348142751683", TessaWhatsAppNotice)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{tessaInbound("tessa_link", tessaChallengeToken(t, c), "2348142751683")}); err != nil {
		t.Fatal(err)
	}
	var outboxID uuid.UUID
	if err = r.db.QueryRow(ctx, `UPDATE tessa_whatsapp_outbox SET provider_message_id=$2 WHERE client_id=$1 RETURNING id`, id, "wamid."+id.String()).Scan(&outboxID); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct{ name, correlation, wamid, wantOwner, wantStatus string }{
		{"owned", outboxID.String(), "wamid." + id.String(), "tessa", "pending"},
		{"conflict", outboxID.String(), "wamid.wrong", "quarantined", "dead_letter"},
		{"unknown", "", "wamid.unknown." + id.String(), "unassigned", "pending"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			now := time.Now()
			dedupe := receiptDedupeKey("route", uuid.NewString())
			receipt := WebhookReceipt{DedupeKey: dedupe, BusinessID: "100", PhoneNumberID: r.phoneID, EventKind: "status", MessageID: scenario.wamid, MessageStatus: "sent", CorrelationID: scenario.correlation, ProviderTimestamp: &now, ProcessingStatus: "pending"}
			if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{receipt}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { r.db.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, dedupe) })
			// Simulate a previous worker dying before the routing migration was deployed.
			if _, err = r.db.Exec(ctx, `UPDATE meta_whatsapp_webhook_receipts SET processing_status='processing',lease_owner='old-worker',lease_token=gen_random_uuid(),lease_expires_at=NOW()-INTERVAL '1 second' WHERE dedupe_key=$1`, dedupe); err != nil {
				t.Fatal(err)
			}
			if err = RouteStatusReceipts(ctx, r.db); err != nil {
				t.Fatal(err)
			}
			var owner, status string
			if err = r.db.QueryRow(ctx, `SELECT processing_owner,processing_status FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, dedupe).Scan(&owner, &status); err != nil {
				t.Fatal(err)
			}
			if owner != scenario.wantOwner || status != scenario.wantStatus {
				t.Fatalf("owner/status=%s/%s", owner, status)
			}
		})
	}
}

func TestTessaStopPendingLinkIntegration(t *testing.T) {
	for _, scenario := range []string{"first_link", "replacement", "mismatched_sender"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, r, id, store := tessaLinkFixture(t)
			if scenario == "replacement" {
				first, err := r.Start(ctx, id, "+2348000000002", TessaWhatsAppNotice)
				if err != nil {
					t.Fatal(err)
				}
				if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{tessaInbound("tessa_link", tessaChallengeToken(t, first), "2348000000002")}); err != nil {
					t.Fatal(err)
				}
				if _, err = r.db.Exec(ctx, `UPDATE tessa_whatsapp_link_challenges SET created_at=NOW()-INTERVAL '2 minutes' WHERE client_id=$1`, id); err != nil {
					t.Fatal(err)
				}
			}
			c, err := r.Start(ctx, id, "+2348142751683", TessaWhatsAppNotice)
			if err != nil {
				t.Fatal(err)
			}
			token := tessaChallengeToken(t, c)
			sender := "2348142751683"
			if scenario == "mismatched_sender" {
				sender = "2348000000001"
				if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{tessaInbound("tessa_link", token, sender)}); err != nil {
					t.Fatal(err)
				}
			}
			if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{tessaInbound("stop", "", sender)}); err != nil {
				t.Fatal(err)
			}
			var pending bool
			if err = r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tessa_whatsapp_link_challenges WHERE client_id=$1 AND consumed_at IS NULL)`, id).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if pending != (scenario == "mismatched_sender") {
				t.Fatalf("pending=%v after STOP", pending)
			}
			var unsent int
			if err = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_whatsapp_outbox WHERE client_id=$1 AND destination=$2 AND status IN ('pending','retry','processing')`, id, "+"+sender).Scan(&unsent); err != nil {
				t.Fatal(err)
			}
			if unsent != 0 {
				t.Fatal("STOP left replies queued to the sender")
			}
			if scenario == "mismatched_sender" {
				return
			}
			if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{tessaInbound("tessa_link", token, sender)}); err != nil {
				t.Fatal(err)
			}
			state, err := r.State(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "replacement" {
				if state.Status != "connected" || state.Destination != maskE164("+2348000000002") {
					t.Fatalf("unrelated active connection changed: %+v", state)
				}
			} else if state.Status != "disconnected" {
				t.Fatal("STOP did not invalidate the old token")
			}
		})
	}
}

func TestTessaStatusMappingConflictAfterRoutingIntegration(t *testing.T) {
	ctx, r, id, store := tessaLinkFixture(t)
	c, err := r.Start(ctx, id, "+2348142751683", TessaWhatsAppNotice)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{tessaInbound("tessa_link", tessaChallengeToken(t, c), "2348142751683")}); err != nil {
		t.Fatal(err)
	}
	var outboxID uuid.UUID
	if err = r.db.QueryRow(ctx, `SELECT id FROM tessa_whatsapp_outbox WHERE client_id=$1`, id).Scan(&outboxID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	dedupe := receiptDedupeKey("late-map", uuid.NewString())
	wamid := "wamid.late-map." + id.String()
	statusReceipt := WebhookReceipt{DedupeKey: dedupe, BusinessID: "100", PhoneNumberID: r.phoneID, EventKind: "status", MessageID: wamid, MessageStatus: "delivered", CorrelationID: outboxID.String(), ProviderTimestamp: &now, ProcessingStatus: "pending"}
	if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{statusReceipt}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.db.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, dedupe) })
	if err = RouteStatusReceipts(ctx, r.db); err != nil {
		t.Fatal(err)
	}
	otherSource := tessaInbound("", "", "2348142751683")
	if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{otherSource}); err != nil {
		t.Fatal(err)
	}
	_, err = r.db.Exec(ctx, `INSERT INTO tessa_whatsapp_outbox(id,source_receipt_id,client_id,phone_number_id,destination,connection_revision,security_revision,kind,window_expires_at,provider_message_id)
	 SELECT gen_random_uuid(),s.id,d.client_id,d.phone_number_id,d.destination,d.connection_revision,d.security_revision,d.kind,d.window_expires_at,$3
	 FROM tessa_whatsapp_outbox d CROSS JOIN meta_whatsapp_webhook_receipts s WHERE d.id=$1 AND s.dedupe_key=$2`, outboxID, otherSource.DedupeKey, wamid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewTessaControlWorker(r, nil, nil).applyStatus(ctx); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err = r.db.QueryRow(ctx, `SELECT processing_owner FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, dedupe).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "quarantined" {
		t.Fatalf("owner=%s", owner)
	}
}

func TestTessaInboundDisconnectWhileDisabledIntegration(t *testing.T) {
	for _, command := range []string{"stop", "tessa_disconnect"} {
		t.Run(command, func(t *testing.T) {
			ctx, r, id, store := tessaLinkFixture(t)
			c, err := r.Start(ctx, id, "+2348142751683", TessaWhatsAppNotice)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{tessaInbound("tessa_link", tessaChallengeToken(t, c), "2348142751683")}); err != nil {
				t.Fatal(err)
			}
			r.enabled = false
			r.allowed = map[uuid.UUID]bool{}
			if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{tessaInbound(command, "", "2348142751683")}); err != nil {
				t.Fatal(err)
			}
			state, err := r.State(ctx, id)
			if err != nil || state.Status != "disconnected" {
				t.Fatalf("state=%+v err=%v", state, err)
			}
			var pending int
			if err = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_whatsapp_outbox WHERE client_id=$1 AND kind='linked' AND status IN ('pending','retry','processing')`, id).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if pending != 0 {
				t.Fatal("revocation left the linked acknowledgement queued")
			}
		})
	}
}

func TestTessaSecurityEventsAreAtomicAndDeduplicatedIntegration(t *testing.T) {
	ctx, r, id, store := tessaLinkFixture(t)
	c, err := r.Start(ctx, id, "+2348142751683", TessaWhatsAppNotice)
	if err != nil {
		t.Fatal(err)
	}
	link := tessaInbound("tessa_link", tessaChallengeToken(t, c), "2348142751683")
	for range 2 {
		if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{link}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = r.db.Exec(ctx, `UPDATE tessa_whatsapp_link_challenges SET created_at=NOW()-INTERVAL '2 minutes' WHERE client_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	c, err = r.Start(ctx, id, "+2348000000002", TessaWhatsAppNotice)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{tessaInbound("tessa_link", tessaChallengeToken(t, c), "2348000000002")}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = r.Disconnect(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := r.db.Query(ctx, `SELECT kind,recipient_email FROM tessa_whatsapp_security_events WHERE client_id=$1 ORDER BY connection_revision`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var kind, email string
		if err = rows.Scan(&kind, &email); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, kind)
		if email != id.String()+"@example.invalid" {
			t.Fatal("wrong verified recipient snapshot")
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(kinds, ",") != "linked,replaced,disconnected" {
		t.Fatalf("events=%v", kinds)
	}
}

func TestTessaSecurityEventWithoutEmailAndRollbackIntegration(t *testing.T) {
	ctx, r, id, store := tessaLinkFixture(t)
	if _, err := r.db.Exec(ctx, `UPDATE clients SET email_verified_at=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	c, err := r.Start(ctx, id, "+2348142751683", TessaWhatsAppNotice)
	if err != nil {
		t.Fatal(err)
	}
	inbound := tessaInbound("tessa_link", tessaChallengeToken(t, c), "2348142751683")
	tx, err := r.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Missing receipt FK forces failure after the grant and its security event were inserted.
	if err = r.applyControlTx(ctx, tx, inbound, uuid.New()); err == nil {
		t.Fatal("expected missing receipt failure")
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_whatsapp_security_events WHERE client_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled-back events=%d err=%v", count, err)
	}
	if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{inbound}); err != nil {
		t.Fatal(err)
	}
	var reason, email string
	if err = r.db.QueryRow(ctx, `SELECT skip_reason,recipient_email FROM tessa_whatsapp_security_events WHERE client_id=$1`, id).Scan(&reason, &email); err != nil {
		t.Fatal(err)
	}
	if reason != "no_verified_email" || email != "" {
		t.Fatal("unverified recipient scheduled")
	}
	if err = r.Disconnect(ctx, id); err != nil {
		t.Fatal(err)
	}
}
