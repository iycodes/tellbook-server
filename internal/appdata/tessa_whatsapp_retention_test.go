package appdata

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func tessaMaintenanceTask(t *testing.T, name string) dataMaintenanceTask {
	t.Helper()
	for _, task := range dataMaintenanceTasks {
		if task.name == name {
			return task
		}
	}
	t.Fatalf("missing maintenance task %s", name)
	return dataMaintenanceTask{}
}

func TestTessaWhatsAppRetentionPreservesDeliveryAndTranscriptIntegration(t *testing.T) {
	ctx, repo, clientID, _, worker, _ := tessaWhatsAppFixture(t)
	queueTessaWhatsApp(t, ctx, repo, clientID, "Explain bookings")
	if _, err := worker.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := repo.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	now := time.Now().UTC()
	for _, sql := range []string{
		`UPDATE tessa_whatsapp_outbox SET status='delivered',status_at=NOW() WHERE client_id=$1`,
		`UPDATE tessa_whatsapp_outbox SET updated_at=NOW()-INTERVAL '40 days' WHERE client_id=$1`,
		`UPDATE tessa_whatsapp_ingress SET completed_at=NOW()-INTERVAL '40 days' WHERE client_id=$1`,
		`UPDATE meta_whatsapp_webhook_receipts SET processed_at=NOW()-INTERVAL '40 days' WHERE id IN (SELECT source_receipt_id FROM tessa_whatsapp_ingress WHERE client_id=$1)`,
	} {
		if _, err = tx.Exec(ctx, sql, clientID); err != nil {
			t.Fatal(err)
		}
	}
	prune := func(name string, want int64) {
		t.Helper()
		n, err := pruneMaintenanceTask(ctx, tx, tessaMaintenanceTask(t, name), now, 1)
		if err != nil || n != want {
			t.Fatalf("%s: deleted=%d want=%d err=%v", name, n, want, err)
		}
	}
	prune("terminal_tessa_whatsapp_outbox", 0)
	prune("terminal_tessa_whatsapp_ingress", 0)
	prune("terminal_whatsapp_webhook_receipts", 0)
	if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET updated_at=NOW()-INTERVAL '91 days' WHERE client_id=$1`, clientID); err != nil {
		t.Fatal(err)
	}
	prune("terminal_tessa_whatsapp_outbox", 1)
	prune("terminal_tessa_whatsapp_ingress", 1)
	prune("terminal_whatsapp_webhook_receipts", 1)
	var messages int
	if err = tx.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_messages WHERE client_id=$1`, clientID).Scan(&messages); err != nil || messages != 2 {
		t.Fatal("transport retention deleted the canonical transcript", messages, err)
	}
	prune("terminal_tessa_whatsapp_outbox", 0)
}

func TestTessaWhatsAppRetentionPreservesUnresolvedWorkIntegration(t *testing.T) {
	for _, status := range []string{"pending", "processing", "retry", "dispatching", "unknown", "accepted", "manual_review"} {
		t.Run(status, func(t *testing.T) {
			ctx, repo, clientID, _, worker, _ := tessaWhatsAppFixture(t)
			queueTessaWhatsApp(t, ctx, repo, clientID, "Explain bookings")
			if _, err := worker.ProcessOne(ctx); err != nil {
				t.Fatal(err)
			}
			tx, err := repo.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET status=$2 WHERE client_id=$1`, clientID, status); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET updated_at=NOW()-INTERVAL '91 days' WHERE client_id=$1`, clientID); err != nil {
				t.Fatal(err)
			}
			n, err := pruneMaintenanceTask(ctx, tx, tessaMaintenanceTask(t, "terminal_tessa_whatsapp_outbox"), time.Now(), 1)
			want := int64(0)
			if status == "accepted" || status == "manual_review" {
				want = 1
			}
			if err != nil || n != want {
				t.Fatal("incorrect transport retention eligibility", status, n, err)
			}
		})
	}
}

func TestTessaWhatsAppRetentionPreservesAdmittedRunIntegration(t *testing.T) {
	ctx, repo, clientID, _, worker, _ := tessaWhatsAppFixture(t)
	queueTessaWhatsApp(t, ctx, repo, clientID, "Explain bookings")
	if worked, err := worker.admitWhatsApp(ctx); err != nil || !worked {
		t.Fatal("admit", worked, err)
	}
	tx, err := repo.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_ingress SET completed_at=NOW()-INTERVAL '40 days' WHERE client_id=$1`, clientID); err != nil {
		t.Fatal(err)
	}
	n, err := pruneMaintenanceTask(ctx, tx, tessaMaintenanceTask(t, "terminal_tessa_whatsapp_ingress"), time.Now(), 1)
	if err != nil || n != 0 {
		t.Fatal("retention removed a queued run's source authority", n, err)
	}
}

func TestTessaWhatsAppAccountDeletionClearsPrivateQueueIntegration(t *testing.T) {
	ctx, repo, clientID, _, worker, _ := tessaWhatsAppFixture(t)
	queueTessaWhatsApp(t, ctx, repo, clientID, "Explain bookings")
	if _, err := worker.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	queueTessaWhatsApp(t, ctx, repo, clientID, "Which bookings need attention?")
	if _, err := repo.db.Exec(ctx, `DELETE FROM clients WHERE id=$1`, clientID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"tessa_whatsapp_connections", "tessa_whatsapp_ingress", "tessa_whatsapp_outbox", "tessa_messages", "tessa_runs", "tessa_threads", "tessa_events"} {
		var count int
		if err := repo.db.QueryRow(ctx, "SELECT COUNT(*) FROM "+table+" WHERE client_id=$1", clientID).Scan(&count); err != nil || count != 0 {
			t.Fatal("account deletion left private Tessa data", table, count, err)
		}
	}
	if worked, err := worker.ProcessOne(ctx); err != nil || worked {
		t.Fatal("deleted account still had runnable work", worked, err)
	}
}

func TestTessaWhatsAppResetCancelsOnlyUnsentAnswersIntegration(t *testing.T) {
	for _, status := range []string{"pending", "retry", "processing", "dispatching", "unknown", "accepted", "delivered"} {
		t.Run(status, func(t *testing.T) {
			ctx, repo, clientID, _, worker, _ := tessaWhatsAppFixture(t)
			queueTessaWhatsApp(t, ctx, repo, clientID, "Explain bookings")
			if _, err := worker.ProcessOne(ctx); err != nil {
				t.Fatal(err)
			}
			queueTessaWhatsApp(t, ctx, repo, clientID, "Which bookings need attention?")
			if _, err := repo.db.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET status=$2 WHERE client_id=$1 AND kind='answer'`, clientID, status); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.CreateTessaThread(ctx, clientID, uuid.New(), "test-v1"); err != nil {
				t.Fatal(err)
			}
			want := status
			if status == "pending" || status == "retry" || status == "processing" {
				want = "cancelled"
			}
			var got string
			if err := repo.db.QueryRow(ctx, `SELECT status FROM tessa_whatsapp_outbox WHERE client_id=$1 AND kind='answer'`, clientID).Scan(&got); err != nil || got != want {
				t.Fatal("reset lost the send boundary", got, want, err)
			}
			var privatePending int
			if err := repo.db.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_whatsapp_ingress WHERE client_id=$1 AND content IS NOT NULL`, clientID).Scan(&privatePending); err != nil || privatePending != 0 {
				t.Fatal("reset retained queued private content", privatePending, err)
			}
		})
	}
}

func TestTessaWhatsAppChallengeRetentionSkipsLocksIntegration(t *testing.T) {
	ctx, repo, clientID, _, _, _ := tessaWhatsAppFixture(t)
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	for _, id := range ids {
		if _, err := repo.db.Exec(ctx, `INSERT INTO tessa_whatsapp_link_challenges(id,client_id,token_hash,phone_number_id,destination,security_revision,notice_revision,expires_at,consumed_at)
		VALUES($1,$2,decode(repeat(replace($1::uuid::text,'-',''),2),'hex'),'19990001','+2348142751683',1,'test',NOW()-INTERVAL '2 days',NOW())`, id, clientID); err != nil {
			t.Fatal(err)
		}
	}
	locked, err := repo.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Rollback(ctx)
	if _, err = locked.Exec(ctx, `SELECT id FROM tessa_whatsapp_link_challenges WHERE id=$1 FOR UPDATE`, ids[0]); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err = pgx.BeginFunc(bounded, repo.db, func(tx pgx.Tx) error {
		n, err := pruneMaintenanceTask(bounded, tx, tessaMaintenanceTask(t, "expired_tessa_whatsapp_link_challenges"), time.Now(), 1)
		if err == nil && n != 1 {
			t.Fatalf("bounded cleanup deleted %d rows, want 1", n)
		}
		return err
	}); err != nil {
		t.Fatal("cleanup blocked on a locked challenge", err)
	}
}

func TestTessaWhatsAppParentRetentionPreservesControlRepliesIntegration(t *testing.T) {
	for _, parent := range []string{"challenge", "onboarding"} {
		t.Run(parent, func(t *testing.T) {
			ctx, repo, clientID, _, _, _ := tessaWhatsAppFixture(t)
			ingressID := queueTessaWhatsApp(t, ctx, repo, clientID, "Explain bookings")
			tx, err := repo.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			id := uuid.New()
			task := "expired_tessa_whatsapp_link_challenges"
			if parent == "challenge" {
				_, err = tx.Exec(ctx, `INSERT INTO tessa_whatsapp_link_challenges(id,client_id,token_hash,phone_number_id,destination,security_revision,notice_revision,expires_at,consumed_at)
				VALUES($1,$2,decode(repeat('a',64),'hex'),'19990001','+2348142751683',1,'test',NOW()-INTERVAL '2 days',NOW())`, id, clientID)
				if err == nil {
					_, err = tx.Exec(ctx, `INSERT INTO tessa_whatsapp_outbox(id,source_receipt_id,challenge_id,client_id,phone_number_id,destination,connection_revision,security_revision,kind,window_expires_at)
					SELECT gen_random_uuid(),source_receipt_id,$2,client_id,phone_number_id,destination,connection_revision,security_revision,'mismatch',NOW()+INTERVAL '1 hour' FROM tessa_whatsapp_ingress WHERE id=$1`, ingressID, id)
				}
			} else {
				task = "expired_tessa_whatsapp_onboarding"
				_, err = tx.Exec(ctx, `INSERT INTO tessa_whatsapp_onboarding(id,phone_number_id,destination,stage,expires_at,last_inbound_at)
				VALUES($1,'19990001','+2348000000001','closed',NOW()-INTERVAL '2 days',NOW()-INTERVAL '2 days')`, id)
				if err == nil {
					_, err = tx.Exec(ctx, `INSERT INTO tessa_whatsapp_outbox(id,source_receipt_id,onboarding_id,onboarding_revision,phone_number_id,destination,connection_revision,security_revision,kind,window_expires_at)
					SELECT gen_random_uuid(),source_receipt_id,$2,1,phone_number_id,'+2348000000001',0,0,'onboarding_menu',NOW()+INTERVAL '1 hour' FROM tessa_whatsapp_ingress WHERE id=$1`, ingressID, id)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			n, err := pruneMaintenanceTask(ctx, tx, tessaMaintenanceTask(t, task), time.Now(), 1)
			if err != nil || n != 0 {
				t.Fatal("cleanup cascaded through a control reply", n, err)
			}
			if _, err = tx.Exec(ctx, `DELETE FROM tessa_whatsapp_outbox WHERE challenge_id=$1 OR onboarding_id=$1`, id); err != nil {
				t.Fatal(err)
			}
			n, err = pruneMaintenanceTask(ctx, tx, tessaMaintenanceTask(t, task), time.Now(), 1)
			if err != nil || n != 1 {
				t.Fatal("unreferenced expired parent not pruned", n, err)
			}
		})
	}
}
