package appdata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"booking/go-server/internal/whatsapp"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type tessaWhatsAppGenerator struct {
	base   tessaIntegrationGenerator
	inputs []string
	mu     sync.Mutex
	before func()
}

func (g *tessaWhatsAppGenerator) GenerateJSON(ctx context.Context, system, user string, out any) error {
	g.mu.Lock()
	g.inputs = append(g.inputs, user)
	g.mu.Unlock()
	if g.before != nil {
		g.before()
		g.before = nil
	}
	return g.base.GenerateJSON(ctx, system, user, out)
}

func tessaWhatsAppFixture(t *testing.T) (context.Context, *Repository, uuid.UUID, uuid.UUID, *TessaWorker, *tessaWhatsAppGenerator) {
	ctx, db := openTessaIntegrationPool(t)
	clientID := insertTessaTestClient(t, ctx, db)
	repo := NewRepository(db)
	if err := repo.CompleteTessaIntroduction(ctx, clientID, "test-v1", "test-v1"); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := repo.GetTessaBootstrap(ctx, clientID, "test-v1", 50)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `INSERT INTO tessa_whatsapp_connections(client_id,phone_number_id,destination,status,security_revision,notice_revision,expires_at)
      SELECT id,'19990001','+2348142751683','active',security_revision,$2,NOW()+INTERVAL '30 days' FROM clients WHERE id=$1`, clientID, whatsapp.TessaWhatsAppNotice)
	if err != nil {
		t.Fatal(err)
	}
	g := &tessaWhatsAppGenerator{}
	base := newTessaIntegrationWorker(t, repo, g, nil)
	config := base.config
	config.WhatsAppPhoneNumberID = "19990001"
	worker, err := NewTessaWorker(repo, base.service, base.help, nil, nil, config)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, repo, clientID, uuid.MustParse(bootstrap.Thread.ID), worker, g
}

func queueTessaWhatsApp(t *testing.T, ctx context.Context, repo *Repository, clientID uuid.UUID, content string, mutate ...func(*whatsapp.TessaInboundMessage)) uuid.UUID {
	return queueTessaWhatsAppAt(t, ctx, repo, clientID, content, time.Now().UTC(), mutate...)
}

func queueTessaWhatsAppAt(t *testing.T, ctx context.Context, repo *Repository, clientID uuid.UUID, content string, at time.Time, mutate ...func(*whatsapp.TessaInboundMessage)) uuid.UUID {
	t.Helper()
	receiptID := uuid.New()
	wamid := "wamid.opaque." + uuid.NewString()
	hash := sha256.Sum256([]byte(wamid))
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("tellbook:tessa:whatsapp:19990001:"+wamid))
	err := pgx.BeginFunc(ctx, repo.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO meta_whatsapp_webhook_receipts(id,dedupe_key,waba_id,phone_number_id,event_kind,wamid,provider_timestamp,processing_status,processed_at)
          VALUES($1,$2,'100','19990001','inbound_message',$3,$4,'completed',NOW())`, receiptID, hex.EncodeToString(hash[:]), wamid, at); err != nil {
			return err
		}
		var revision, security int64
		if err := tx.QueryRow(ctx, `SELECT revision,security_revision FROM tessa_whatsapp_connections WHERE client_id=$1`, clientID).Scan(&revision, &security); err != nil {
			return err
		}
		input := whatsapp.TessaInboundMessage{ReceiptID: receiptID, ClientID: clientID, PhoneNumberID: "19990001", Sender: "+2348142751683", MessageID: wamid, Content: content, ConnectionRevision: revision, SecurityRevision: security, SourceTimestamp: at}
		for _, change := range mutate {
			change(&input)
		}
		ingress := NewTessaWhatsAppIngress("test-v1")
		if err := ingress.StoreTessaWhatsAppMessageTx(ctx, tx, input); err != nil {
			return err
		}
		// Replaying the same namespace/source id cannot enqueue twice.
		return ingress.StoreTessaWhatsAppMessageTx(ctx, tx, input)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		repo.db.Exec(context.Background(), `DELETE FROM meta_whatsapp_webhook_receipts WHERE id=$1`, receiptID)
	})
	return id
}

func TestTessaWhatsAppReceiptTimestampIsAuthoritativeIntegration(t *testing.T) {
	ctx, repo, clientID, _, _, _ := tessaWhatsAppFixture(t)
	id := queueTessaWhatsApp(t, ctx, repo, clientID, "Explain bookings", func(in *whatsapp.TessaInboundMessage) {
		in.SourceTimestamp = in.SourceTimestamp.Add(48 * time.Hour)
	})
	var matches bool
	if err := repo.db.QueryRow(ctx, `SELECT q.source_timestamp=r.provider_timestamp FROM tessa_whatsapp_ingress q
      JOIN meta_whatsapp_webhook_receipts r ON r.id=q.source_receipt_id WHERE q.id=$1`, id).Scan(&matches); err != nil || !matches {
		t.Fatal("queue timestamp was not derived from its durable signed receipt", err)
	}
}

func TestTessaWhatsAppPlanningAndSynthesisReceiveOriginalQuestionTimeIntegration(t *testing.T) {
	ctx, repo, clientID, _, worker, g := tessaWhatsAppFixture(t)
	at := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	queueTessaWhatsAppAt(t, ctx, repo, clientID, "Explain bookings", at)
	if _, err := worker.ProcessOne(ctx, "question-clock"); err != nil {
		t.Fatal(err)
	}
	if len(g.inputs) == 0 || !strings.Contains(g.inputs[0], at.In(timezoneLocation("Africa/Lagos")).Format(time.RFC3339)) {
		t.Fatal("source question clock missing from planning input")
	}
	if len(g.inputs) != 2 {
		t.Fatal("source question clock missing from synthesis input")
	}
	_, inputJSON, ok := strings.Cut(g.inputs[1], "Input JSON:\n")
	var input struct {
		QuestionAt  string `json:"question_at"`
		CurrentDate string `json:"current_date"`
	}
	if !ok || json.Unmarshal([]byte(inputJSON), &input) != nil {
		t.Fatal("invalid synthesis input")
	}
	sentAt, err := time.Parse(time.RFC3339, input.QuestionAt)
	if err != nil || !sentAt.Equal(at) || input.CurrentDate != at.In(timezoneLocation("Africa/Lagos")).Format("2006-01-02") {
		t.Fatal("synthesis question clock mismatch", input, err)
	}
}

func TestTessaAccountLockPrecedesThreadLocksIntegration(t *testing.T) {
	for _, operation := range []string{"send", "reset", "introduction"} {
		t.Run(operation, func(t *testing.T) {
			ctx, repo, clientID, threadID, _, _ := tessaWhatsAppFixture(t)
			if operation == "introduction" {
				if _, err := repo.db.Exec(ctx, `DELETE FROM tessa_preferences WHERE client_id=$1`, clientID); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := repo.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			var pid int
			if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM clients WHERE id=$1 FOR UPDATE`, clientID).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			workCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			go func() {
				var err error
				switch operation {
				case "send":
					_, err = repo.SendTessaMessage(workCtx, clientID, threadID, uuid.New(), "Explain bookings", "self_hosted", "test", tessaTestConfigHash, "test-v1")
				case "reset":
					_, err = repo.CreateTessaThread(workCtx, clientID, uuid.New(), "test-v1")
				case "introduction":
					err = repo.CompleteTessaIntroduction(workCtx, clientID, "test-v1", "test-v1")
				}
				done <- err
			}()
			// Wait for a real lock wait, not a timing guess about goroutine scheduling.
			deadline := time.Now().Add(2 * time.Second)
			for {
				var waiting bool
				if err = repo.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("operation never waited for account lock")
				}
				time.Sleep(5 * time.Millisecond)
			}
			var acquired bool
			if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('tessa-thread:' || $1::uuid::text,0))`, clientID).Scan(&acquired); err != nil || !acquired {
				t.Fatal("operation took thread advisory lock before account lock", err)
			}
			if _, err = tx.Exec(ctx, `SELECT id FROM tessa_threads WHERE id=$1 FOR UPDATE NOWAIT`, threadID); err != nil {
				t.Fatal("operation took thread row lock before account lock", err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTessaWhatsAppDelayedReceiptIntegration(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		age    time.Duration
		status string
	}{
		{"delayed", -15 * time.Minute, "pending"},
		{"outside_reply_window", -25 * time.Hour, "expired"},
		{"future", 2 * time.Hour, "rejected"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, repo, clientID, _, _, _ := tessaWhatsAppFixture(t)
			id := queueTessaWhatsAppAt(t, ctx, repo, clientID, "Explain bookings", time.Now().Add(scenario.age))
			var status string
			var hasContent bool
			if err := repo.db.QueryRow(ctx, `SELECT status,content IS NOT NULL FROM tessa_whatsapp_ingress WHERE id=$1`, id).Scan(&status, &hasContent); err != nil || status != scenario.status || hasContent != (status == "pending") {
				t.Fatal("delayed/invalid receipt was silently lost or retained private content", status, err)
			}
		})
	}
}

func TestTessaWhatsAppAdmissionSkipsLockedAccountIntegration(t *testing.T) {
	ctx, repo, clientID, _, worker, _ := tessaWhatsAppFixture(t)
	queueTessaWhatsApp(t, ctx, repo, clientID, "Explain bookings")
	tx, err := repo.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM clients WHERE id=$1 FOR UPDATE`, clientID); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if admitted, err := worker.admitWhatsApp(bounded); err != nil || admitted {
		t.Fatal("admission blocked on an account owned by another transaction", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if admitted, err := worker.admitWhatsApp(ctx); err != nil || !admitted {
		t.Fatal("unlocked account was not admitted", err)
	}
}

func TestTessaWhatsAppAdmissionNamespaceIntegration(t *testing.T) {
	ctx, repo, clientID, _, worker, _ := tessaWhatsAppFixture(t)
	old := queueTessaWhatsApp(t, ctx, repo, clientID, "Old connector question")
	if _, err := repo.db.Exec(ctx, `UPDATE tessa_whatsapp_ingress SET phone_number_id='19990002' WHERE id=$1`, old); err != nil {
		t.Fatal(err)
	}
	current := queueTessaWhatsApp(t, ctx, repo, clientID, "Current connector question")
	if admitted, err := worker.admitWhatsApp(ctx); err != nil || !admitted {
		t.Fatal("another namespace blocked current connector", err)
	}
	var correct bool
	if err := repo.db.QueryRow(ctx, `SELECT (SELECT status='pending' FROM tessa_whatsapp_ingress WHERE id=$1)
      AND (SELECT status='admitted' FROM tessa_whatsapp_ingress WHERE id=$2)`, old, current).Scan(&correct); err != nil || !correct {
		t.Fatal("worker mutated another connector's queue", err)
	}
}

func TestTessaWhatsAppSharedQueueContextAndFairnessIntegration(t *testing.T) {
	ctx, repo, clientID, threadID, worker, g := tessaWhatsAppFixture(t)
	if _, err := repo.SendTessaMessage(ctx, clientID, threadID, uuid.New(), "Explain availability.", "self_hosted", "test", tessaTestConfigHash, "test-v1"); err != nil {
		t.Fatal(err)
	}
	first := queueTessaWhatsApp(t, ctx, repo, clientID, "How does that affect my bookings?")
	second := queueTessaWhatsApp(t, ctx, repo, clientID, "And what about the next day?")
	if worked, err := worker.admitWhatsApp(ctx); err != nil || worked {
		t.Fatal("WhatsApp bypassed the active web run", err)
	}
	if _, err := worker.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SendTessaMessage(ctx, clientID, threadID, uuid.New(), "jump the queue", "self_hosted", "test", tessaTestConfigHash, "test-v1"); !errors.Is(err, ErrTessaRunInProgress) {
		t.Fatal("web submission starved waiting WhatsApp", err)
	}
	for range 2 {
		if worked, err := worker.ProcessOne(ctx); err != nil || !worked {
			t.Fatal("queued turn was not processed", err)
		}
	}
	bootstrap, err := repo.GetTessaBootstrap(ctx, clientID, "test-v1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(bootstrap.Messages) != 6 {
		t.Fatalf("messages=%d", len(bootstrap.Messages))
	}
	for i, m := range bootstrap.Messages {
		want := "web"
		if i >= 2 {
			want = "whatsapp"
		}
		if m.SourceChannel != want || m.Sequence != int64(i+1) {
			t.Fatal("source or sequence was lost")
		}
	}
	if bootstrap.Messages[2].Content != "How does that affect my bookings?" || bootstrap.Messages[4].Content != "And what about the next day?" {
		t.Fatal("committed receipt order was not preserved")
	}
	for _, id := range []uuid.UUID{first, second} {
		var admitted bool
		if err := repo.db.QueryRow(ctx, `SELECT status='admitted' AND content IS NULL AND message_id IS NOT NULL AND run_id IS NOT NULL FROM tessa_whatsapp_ingress WHERE id=$1`, id).Scan(&admitted); err != nil || !admitted {
			t.Fatal("admission not finalized", err)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.inputs) != 6 || !strings.Contains(g.inputs[2], "Tellbook uses your availability settings") || !strings.Contains(g.inputs[4], "How does that affect my bookings?") {
		t.Fatal("follow-up did not receive preceding context")
	}
}

func TestTessaWhatsAppBoundedQueueExpiryAndResetIntegration(t *testing.T) {
	for _, scenario := range []string{"overflow", "expiry", "reset", "revocation", "security_revision", "notice"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, repo, clientID, _, worker, _ := tessaWhatsAppFixture(t)
			id := queueTessaWhatsApp(t, ctx, repo, clientID, "Explain bookings")
			var err error
			switch scenario {
			case "overflow":
				for i := 0; i < 5; i++ {
					queueTessaWhatsApp(t, ctx, repo, clientID, fmt.Sprintf("Question %d", i))
				}
				var pending, rejected int
				if err = repo.db.QueryRow(ctx, `SELECT COUNT(*) FILTER(WHERE status='pending'),COUNT(*) FILTER(WHERE status='rejected') FROM tessa_whatsapp_ingress WHERE client_id=$1`, clientID).Scan(&pending, &rejected); err != nil || pending != 5 || rejected != 1 {
					t.Fatal("queue was not bounded", err)
				}
				var notices int
				if err = repo.db.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_whatsapp_outbox WHERE client_id=$1 AND kind='queue_busy'`, clientID).Scan(&notices); err != nil || notices != 1 {
					t.Fatal("overflow notification not bounded", err)
				}
				return
			case "expiry":
				_, err = repo.db.Exec(ctx, `UPDATE tessa_whatsapp_ingress SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, id)
			case "reset":
				_, err = repo.CreateTessaThread(ctx, clientID, uuid.New(), "test-v1")
			case "revocation":
				err = whatsapp.NewTessaLinkRepository(repo.db, "19990001", "+2348000000000", true).Disconnect(ctx, clientID)
			case "security_revision":
				_, err = repo.db.Exec(ctx, `UPDATE clients SET security_revision=security_revision+1 WHERE id=$1`, clientID)
			case "notice":
				_, err = repo.db.Exec(ctx, `UPDATE tessa_preferences SET acknowledged_notice_revision='old' WHERE client_id=$1`, clientID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = worker.admitWhatsApp(ctx); err != nil {
				t.Fatal(err)
			}
			var cleared bool
			if err = repo.db.QueryRow(ctx, `SELECT status IN ('expired','cancelled') AND content IS NULL AND run_id IS NULL FROM tessa_whatsapp_ingress WHERE id=$1`, id).Scan(&cleared); err != nil || !cleared {
				t.Fatal("invalid queued request was admitted", err)
			}
		})
	}
}

func TestTessaWhatsAppConcurrentAdmissionAndAuthorityIntegration(t *testing.T) {
	ctx, repo, clientID, _, worker, g := tessaWhatsAppFixture(t)
	queueTessaWhatsApp(t, ctx, repo, clientID, "Explain bookings")
	results := make(chan error, 2)
	for range 2 {
		go func() { _, err := worker.admitWhatsApp(ctx); results <- err }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	var runs int
	if err := repo.db.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_runs WHERE client_id=$1`, clientID).Scan(&runs); err != nil || runs != 1 {
		t.Fatal("concurrent admission duplicated a run", err)
	}
	g.before = func() {
		if err := whatsapp.NewTessaLinkRepository(repo.db, "19990001", "+2348000000000", true).Disconnect(ctx, clientID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := worker.ProcessOne(ctx); err == nil {
		t.Fatal("revocation during inference did not stop the turn")
	}
	var answers int
	if err := repo.db.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_messages WHERE client_id=$1 AND sender_type='tessa'`, clientID).Scan(&answers); err != nil || answers != 0 {
		t.Fatal("answer committed after revocation", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.inputs) != 1 {
		t.Fatal("model continued after revocation")
	}
}

func TestTessaWhatsAppRetryAfterWorkerRestartIntegration(t *testing.T) {
	ctx, repo, clientID, _, configured, _ := tessaWhatsAppFixture(t)
	g := &tessaRetryGenerator{}
	base := newTessaIntegrationWorker(t, repo, g, nil)
	worker, err := NewTessaWorker(repo, base.service, base.help, nil, nil, configured.config)
	if err != nil {
		t.Fatal(err)
	}
	id := queueTessaWhatsApp(t, ctx, repo, clientID, "Explain availability")
	if _, err = worker.ProcessOne(ctx); err == nil {
		t.Fatal("test synthesis did not request retry")
	}
	if _, err = repo.db.Exec(ctx, `UPDATE tessa_runs SET available_at=NOW() WHERE id=(SELECT run_id FROM tessa_whatsapp_ingress WHERE id=$1)`, id); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewTessaWorker(repo, base.service, base.help, nil, nil, configured.config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	plans, syntheses := g.callCounts()
	if plans != 1 || syntheses != 2 {
		t.Fatal("restart did not reuse committed planning evidence")
	}
	var runs, tools int
	if err = repo.db.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_runs WHERE client_id=$1`, clientID).Scan(&runs); err != nil || runs != 1 {
		t.Fatal("retry created another run", err)
	}
	if err = repo.db.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_run_steps WHERE stage='tool' AND run_id=(SELECT run_id FROM tessa_whatsapp_ingress WHERE id=$1)`, id).Scan(&tools); err != nil || tools != 1 {
		t.Fatal("retry did not reuse committed tool evidence", err)
	}
}
