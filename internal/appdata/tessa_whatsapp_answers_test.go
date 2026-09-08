package appdata

import (
	"context"
	"errors"
	"testing"
	"time"

	"booking/go-server/internal/whatsapp"
	"github.com/google/uuid"
)

func TestTessaWhatsAppAnswerOutboxAtomicIntegration(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[rollback], func(t *testing.T) {
			ctx, repo, clientID, _, worker, _ := tessaWhatsAppFixture(t)
			queueTessaWhatsApp(t, ctx, repo, clientID, "Explain bookings")
			if rollback {
				// A committed receipt cannot hide a failed answer/outbox transaction.
				if _, err := repo.db.Exec(ctx, `CREATE FUNCTION tessa_b4_test_reject() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='answer' THEN RAISE EXCEPTION 'forced answer intent rollback'; END IF; RETURN NEW; END $$;
                  CREATE TRIGGER tessa_b4_test_reject BEFORE INSERT ON tessa_whatsapp_outbox FOR EACH ROW EXECUTE FUNCTION tessa_b4_test_reject()`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					repo.db.Exec(ctx, `DROP TRIGGER IF EXISTS tessa_b4_test_reject ON tessa_whatsapp_outbox; DROP FUNCTION IF EXISTS tessa_b4_test_reject()`)
				})
			}
			_, err := worker.ProcessOne(ctx)
			if (err != nil) != rollback {
				t.Fatal("unexpected completion result", err)
			}
			var answers, intents int
			if err = repo.db.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM tessa_messages WHERE client_id=$1 AND sender_type='tessa'),
              (SELECT COUNT(*) FROM tessa_whatsapp_outbox WHERE client_id=$1 AND kind='answer')`, clientID).Scan(&answers, &intents); err != nil {
				t.Fatal(err)
			}
			want := 1
			if rollback {
				want = 0
			}
			if answers != want || intents != want {
				t.Fatal("answer and intent did not commit/rollback together", answers, intents)
			}
			if !rollback {
				if worked, err := worker.ProcessOne(ctx); err != nil || worked {
					t.Fatal("completed inference replayed", err)
				}
				var bound bool
				if err = repo.db.QueryRow(ctx, `SELECT d.assistant_message_id=m.id AND d.window_expires_at=i.source_timestamp+INTERVAL '24 hours'
                  FROM tessa_whatsapp_outbox d JOIN tessa_messages m ON m.id=d.assistant_message_id
                  JOIN tessa_whatsapp_ingress i ON i.run_id=m.run_id WHERE d.client_id=$1`, clientID).Scan(&bound); err != nil || !bound {
					t.Fatal("answer intent lost source/message binding", err)
				}
			}
		})
	}
}

type tessaTypingCapture struct {
	started chan string
	block   bool
}

func (s *tessaTypingCapture) SendTyping(ctx context.Context, source string) error {
	s.started <- source
	if s.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return errors.New("cosmetic failure")
}

func TestTessaWhatsAppTypingDoesNotGateGenerationIntegration(t *testing.T) {
	ctx, repo, clientID, _, worker, g := tessaWhatsAppFixture(t)
	id := queueTessaWhatsApp(t, ctx, repo, clientID, "Explain bookings")
	var source string
	if err := repo.db.QueryRow(ctx, `SELECT source_message_id FROM tessa_whatsapp_ingress WHERE id=$1`, id).Scan(&source); err != nil {
		t.Fatal(err)
	}
	sender := &tessaTypingCapture{started: make(chan string, 1), block: true}
	worker.WithWhatsAppTyping(sender)
	g.before = func() {
		select {
		case got := <-sender.started:
			if got != source {
				t.Fatal("wrong typing WAMID")
			}
		case <-time.After(time.Second):
			t.Fatal("typing not attempted alongside inference")
		}
	}
	start := time.Now()
	if _, err := worker.ProcessOne(ctx); err != nil {
		t.Fatal("typing failure affected generation", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("typing added a minimum generation delay")
	}
}

func TestTessaWhatsAppTypingAuthorityAndRetryIntegration(t *testing.T) {
	for _, scenario := range []string{"admitted", "web", "retry", "queued", "revoked", "expired", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, repo, clientID, _, worker, _ := tessaWhatsAppFixture(t)
			id := queueTessaWhatsApp(t, ctx, repo, clientID, "Explain bookings")
			if admitted, err := worker.admitWhatsApp(ctx); err != nil || !admitted {
				t.Fatal(err)
			}
			run, err := worker.claim(ctx, "typing-test")
			if err != nil || run.ID == uuid.Nil {
				t.Fatal(err)
			}
			sender := &tessaTypingCapture{started: make(chan string, 1)}
			worker.WithWhatsAppTyping(sender)
			switch scenario {
			case "web":
				run.SourceChannel = "web"
			case "retry":
				run.AttemptCount = 2
			case "queued":
				_, err = repo.db.Exec(ctx, `UPDATE tessa_runs SET status='queued',lease_owner='',lease_token=NULL,lease_expires_at=NULL WHERE id=$1`, run.ID)
			case "revoked":
				err = whatsapp.NewTessaLinkRepository(repo.db, "19990001", "+2348000000000", true, []string{clientID.String()}).Disconnect(ctx, clientID)
			case "expired":
				_, err = repo.db.Exec(ctx, `UPDATE tessa_whatsapp_ingress SET source_timestamp=NOW()-INTERVAL '25 hours' WHERE id=$1`, id)
			}
			if err != nil {
				t.Fatal(err)
			}
			active, cancel := context.WithCancel(ctx)
			if scenario == "cancelled" {
				cancel()
			}
			defer cancel()
			stop := worker.startWhatsAppTyping(active, run)
			if scenario == "admitted" {
				select {
				case <-sender.started:
				case <-time.After(time.Second):
					t.Fatal("typing not sent")
				}
			}
			// Give the bounded task its lifecycle, then join it before inspecting.
			if scenario != "admitted" {
				time.Sleep(20 * time.Millisecond)
			}
			stop()
			if len(sender.started) != 0 {
				t.Fatal("typing escaped its authority/lifecycle")
			}
		})
	}
}
