package whatsapp

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type TextSender interface {
	SendText(context.Context, string, string, string) (SendResult, error)
	SendURLButton(context.Context, string, string, URLButton, string) (SendResult, error)
}
type TessaControlWorker struct {
	repo   *TessaLinkRepository
	sender TextSender
	logger *slog.Logger
}

func NewTessaControlWorker(repo *TessaLinkRepository, sender TextSender, logger *slog.Logger) *TessaControlWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &TessaControlWorker{repo: repo, sender: sender, logger: logger}
}
func (w *TessaControlWorker) Start(ctx context.Context, wake <-chan struct{}) {
	timer := time.NewTicker(5 * time.Second)
	defer timer.Stop()
	for {
		if err := w.processStatuses(ctx); err != nil && ctx.Err() == nil {
			w.logger.Error("process Tessa WhatsApp status", "error", err)
		}
		if w.sender != nil && w.repo.enabled {
			for n := 0; n < 10; n++ {
				worked, err := w.processOne(ctx)
				if err != nil {
					w.logger.Error("process Tessa WhatsApp control", "error", err)
					break
				}
				if !worked {
					break
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-timer.C:
		}
	}
}

type tessaControlJob struct {
	ID, Token         uuid.UUID
	Destination, Kind string
	Attempts          int
	Body              string
	Button            *URLButton
}

func (w *TessaControlWorker) processOne(ctx context.Context) (bool, error) {
	var j tessaControlJob
	err := w.repo.db.QueryRow(ctx, `WITH due AS (
	 SELECT id FROM tessa_whatsapp_outbox WHERE phone_number_id=$1 AND status IN ('pending','retry','processing')
	 AND available_at<=NOW() ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1
	) UPDATE tessa_whatsapp_outbox d SET status='processing',lease_token=gen_random_uuid(),lease_expires_at=NOW()+INTERVAL '1 minute',
	 available_at=NOW()+INTERVAL '1 minute',attempt_count=attempt_count+1 FROM due WHERE d.id=due.id
	 RETURNING d.id,d.lease_token,d.destination,d.kind,d.attempt_count`, w.repo.phoneID).Scan(&j.ID, &j.Token, &j.Destination, &j.Kind, &j.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	authorized, err := w.authorize(ctx, &j)
	if err != nil || !authorized {
		return true, err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	text := tessaControlText(j.Kind)
	if w.repo.conversationAvailable() {
		if j.Kind == "linked" {
			text = "Your WhatsApp is connected to Tessa. Ask about your Tellbook bookings, services or setup here. Open Tessa in Tellbook first if you need to accept its current notice. Send TESSA DISCONNECT to disconnect this number."
		}
		if j.Kind == "onboarding_menu" {
			text = "Hi, I'm Tessa, Tellbook's assistant for service providers. Reply CONNECT to connect your Tellbook account, or CREATE to create an account."
		}
	}
	if j.Kind == "answer" {
		text = j.Body
	}
	if j.Kind == "onboarding_signup" {
		text += " " + w.repo.webURL
	}
	var result SendResult
	var sendErr error
	if j.Button != nil {
		result, sendErr = w.sender.SendURLButton(sendCtx, j.Destination, text, *j.Button, j.ID.String())
	} else {
		result, sendErr = w.sender.SendText(sendCtx, j.Destination, text, j.ID.String())
	}
	cancel()
	status, code, delay := "accepted", "", time.Duration(0)
	if sendErr != nil {
		status, code = "unknown", "ambiguous_send"
		var graph *GraphError
		var transport *TransportError
		var request *RequestError
		switch {
		case errors.As(sendErr, &request):
			status, code = "failed", "invalid_request"
		case errors.As(sendErr, &graph):
			status, code = "failed", string(graph.Class)
			if graph.Class == ErrorClassTransient || graph.Class == ErrorClassRateLimit {
				status = "retry"
				delay = graph.RetryAfter
			}
		case errors.As(sendErr, &transport) && !transport.Ambiguous:
			status, code = "retry", "pre_dispatch_transport"
		}
		if status == "retry" && j.Attempts >= 5 {
			status, code = "failed", "attempts_exhausted"
		}
		if status == "retry" && delay < time.Duration(j.Attempts)*30*time.Second {
			delay = time.Duration(j.Attempts) * 30 * time.Second
		}
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	_, err = w.repo.db.Exec(finishCtx, `WITH account AS MATERIALIZED (
      SELECT c.id FROM clients c JOIN tessa_whatsapp_outbox d ON d.client_id=c.id WHERE d.id=$1 FOR UPDATE OF c
    ) UPDATE tessa_whatsapp_outbox SET status=$3,last_error_code=$4,
	 provider_message_id=CASE WHEN $5<>'' THEN $5 ELSE provider_message_id END,available_at=NOW()+($6::bigint*INTERVAL '1 millisecond'),
	 completed_at=CASE WHEN $3 IN ('accepted','failed') THEN NOW() ELSE NULL END,
	 lease_expires_at=NULL WHERE id=$1 AND lease_token=$2 AND status='dispatching'
     AND (client_id IS NULL OR client_id IN (SELECT id FROM account))`, j.ID, j.Token, status, code, result.MessageID, delay.Milliseconds())
	return true, err
}
func (w *TessaControlWorker) authorize(ctx context.Context, j *tessaControlJob) (bool, error) {
	tx, err := w.repo.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var clientID, onboardingID *uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT client_id,onboarding_id FROM tessa_whatsapp_outbox WHERE id=$1`, j.ID).Scan(&clientID, &onboardingID); err != nil {
		return false, err
	}
	var valid, window bool
	answerValid := true
	if onboardingID != nil {
		// Session-before-outbox order matches onboarding and revocation.
		if _, err = tx.Exec(ctx, `SELECT id FROM tessa_whatsapp_onboarding WHERE id=$1 FOR SHARE`, onboardingID); err != nil {
			return false, err
		}
		err = tx.QueryRow(ctx, `SELECT d.window_expires_at>NOW(),s.revision=d.onboarding_revision AND s.stage<>'closed' AND s.expires_at>NOW()
		 FROM tessa_whatsapp_outbox d JOIN tessa_whatsapp_onboarding s ON s.id=d.onboarding_id
		 WHERE d.id=$1 AND d.lease_token=$2 AND d.status='processing' AND d.lease_expires_at>NOW() FOR UPDATE OF d`, j.ID, j.Token).Scan(&window, &valid)
		valid = valid && w.repo.emailLinks != nil && w.repo.enabled
	} else {
		// Follow the same account-before-outbox lock order as disconnect/replacement.
		if _, err = tx.Exec(ctx, `SELECT id FROM clients WHERE id=$1 FOR UPDATE`, clientID); err != nil {
			return false, err
		}
		if j.Kind == "answer" {
			// Fence reset before locking the outbox, matching account/thread order
			// in answer completion. Only committed answers can be dispatched.
			var content string
			var presentation []byte
			err = tx.QueryRow(ctx, `SELECT m.content,m.presentation,t.status='active' AND r.status='completed'
              AND d.core_notice_revision=$2 AND p.acknowledged_notice_revision=$2 AND p.introduction_completed_at IS NOT NULL
              AND EXISTS(SELECT 1 FROM tessa_whatsapp_ingress i WHERE i.run_id=r.id AND i.status='admitted'
                AND i.source_receipt_id=d.source_receipt_id AND i.client_id=d.client_id AND i.thread_id=d.thread_id
                AND i.phone_number_id=d.phone_number_id AND i.destination=d.destination
                AND i.connection_revision=d.connection_revision AND i.security_revision=d.security_revision)
              FROM tessa_whatsapp_outbox d JOIN tessa_messages m ON m.id=d.assistant_message_id AND m.client_id=d.client_id AND m.thread_id=d.thread_id
              JOIN tessa_threads t ON t.id=m.thread_id JOIN tessa_runs r ON r.id=m.run_id
              JOIN tessa_preferences p ON p.client_id=d.client_id
              WHERE d.id=$1 AND m.sender_type='tessa' AND m.source_channel='whatsapp' FOR SHARE OF t`, j.ID, w.repo.answerNotice).Scan(&content, &presentation, &answerValid)
			if errors.Is(err, pgx.ErrNoRows) {
				answerValid = false
			} else if err != nil {
				return false, err
			}
			answerValid = answerValid && w.repo.answerNotice != "" && w.repo.answerOrigin != ""
			if answerValid {
				var rendered renderedTessaAnswer
				rendered, err = renderTessaAnswer(content, presentation, w.repo.answerOrigin)
				j.Body, j.Button = rendered.Body, rendered.Button
				if err != nil {
					answerValid = false
				}
			}
		}
		err = tx.QueryRow(ctx, `SELECT d.window_expires_at>NOW(),d.security_revision=c.security_revision AND
	 d.connection_revision=COALESCE(g.revision,0) AND (d.kind NOT IN ('answer','linked','queue_busy','queue_expired','queue_unavailable') OR (g.status='active' AND g.expires_at>NOW() AND g.notice_revision=$3 AND g.phone_number_id=d.phone_number_id AND g.destination=d.destination))
	 AND (d.kind NOT IN ('mismatch','unavailable') OR EXISTS (
	 SELECT 1 FROM tessa_whatsapp_link_challenges q WHERE q.id=d.challenge_id AND q.expires_at>NOW()
	 AND q.security_revision=c.security_revision AND q.notice_revision=$3))
	 FROM tessa_whatsapp_outbox d JOIN clients c ON c.id=d.client_id LEFT JOIN tessa_whatsapp_connections g ON g.client_id=d.client_id
	 WHERE d.id=$1 AND d.lease_token=$2 AND d.status='processing' AND d.lease_expires_at>NOW()
	 FOR UPDATE OF d FOR SHARE OF c`, j.ID, j.Token, TessaWhatsAppNotice).Scan(&window, &valid)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !valid || !answerValid || !window || !w.repo.enabled {
		status, code := "cancelled", "grant_changed"
		if !window {
			status, code = "expired", "customer_service_window_closed"
		}
		_, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET status=$2,last_error_code=$3,completed_at=NOW() WHERE id=$1`, j.ID, status, code)
		if err != nil {
			return false, err
		}
		return false, tx.Commit(ctx)
	}
	// Lease recovery consumes the same attempt budget as explicit safe retries.
	// A process crash must not allow a sixth network dispatch.
	if j.Attempts > 5 {
		_, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET status='failed',last_error_code='attempts_exhausted',completed_at=NOW() WHERE id=$1`, j.ID)
		if err != nil {
			return false, err
		}
		return false, tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET status='dispatching',reconcile_after=NOW()+INTERVAL '15 minutes' WHERE id=$1`, j.ID)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
func tessaControlText(kind string) string {
	switch kind {
	case "queue_busy":
		return "Tessa already has several messages waiting. This message was not added; please wait before sending it again."
	case "queue_expired":
		return "Your message waited too long and was not processed. Please send it again when you're ready."
	case "queue_unavailable":
		return "This message could not be processed. Open Tessa in Tellbook to review your access and notices, then try again."
	case "onboarding_menu":
		return "Hi, I'm Tessa, Tellbook's assistant for service providers. Reply CONNECT to connect your Tellbook account, or CREATE to create an account. WhatsApp conversations are not available yet; linking prepares your connection."
	case "onboarding_consent":
		return "Connecting gives this WhatsApp number access to your provider account's Tessa assistant. Web and WhatsApp share conversation context. Messages pass through WhatsApp/Meta, Tellbook and Tessa's configured AI providers and may include private booking details. Only connect a number you control; anyone with access to it may use Tessa. You can disconnect in Tellbook or send TESSA DISCONNECT. Reply AGREE to accept and continue, or STOP to cancel."
	case "onboarding_email":
		return "Enter your verified Tellbook provider account email. Never send your password."
	case "onboarding_code":
		return "If that email is eligible, we'll send a Tessa linking code. Enter the six-digit code here; it expires 10 minutes after the request. Wait at least 60 seconds before replying RESEND. To use another email, reply CONNECT."
	case "onboarding_invalid":
		return "The connection could not be verified. Enter the latest linking code, or reply RESEND after 60 seconds. If it has expired or you need another email, reply CONNECT."
	case "onboarding_signup":
		return "Create your provider account on Tellbook, then connect WhatsApp from your dashboard:"
	case "onboarding_failed":
		return "This connection could not be completed. Manage your Tessa connection from your Tellbook dashboard, or reply CONNECT to start again."
	case "linked":
		return "Your WhatsApp is connected to Tessa. WhatsApp conversations are not available yet; continue chatting with Tessa in Tellbook for now. Send TESSA DISCONNECT to disconnect this number."
	case "disconnected":
		return "This WhatsApp number is disconnected from Tessa. You can reconnect from your Tellbook dashboard."
	case "mismatch":
		return "This WhatsApp number does not match the linking request. Return to Tellbook, choose the number you want to use, and create a fresh link."
	default:
		return "This linking request could not be completed. Please manage your Tessa connection from your Tellbook dashboard."
	}
}
func (w *TessaControlWorker) processStatuses(ctx context.Context) error {
	if err := RouteStatusReceipts(ctx, w.repo.db); err != nil {
		return err
	}
	for n := 0; n < 100; n++ {
		worked, err := w.applyStatus(ctx)
		if err != nil {
			return err
		}
		if !worked {
			break
		}
	}
	_, err := w.repo.db.Exec(ctx, `WITH due AS MATERIALIZED (
      SELECT id,client_id FROM tessa_whatsapp_outbox WHERE phone_number_id=$1 AND status IN ('dispatching','unknown')
      AND reconcile_after<=NOW() ORDER BY reconcile_after,id LIMIT 100
    ), accounts AS MATERIALIZED (
      SELECT c.id FROM clients c WHERE c.id IN (SELECT client_id FROM due) ORDER BY c.id FOR UPDATE SKIP LOCKED
    ), locked AS (
      SELECT d.id FROM tessa_whatsapp_outbox d JOIN due ON due.id=d.id
      WHERE (d.client_id IS NULL OR d.client_id IN (SELECT id FROM accounts)) AND d.status IN ('dispatching','unknown')
      AND d.reconcile_after<=NOW() FOR UPDATE OF d SKIP LOCKED
    ) UPDATE tessa_whatsapp_outbox d SET status='manual_review',last_error_code='unresolved_send',completed_at=NOW() FROM locked WHERE d.id=locked.id`, w.repo.phoneID)
	return err
}
func (w *TessaControlWorker) applyStatus(ctx context.Context) (bool, error) {
	tx, err := w.repo.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var receiptID uuid.UUID
	var wamid, status, correlation string
	var at time.Time
	err = tx.QueryRow(ctx, `SELECT id,wamid,message_status,correlation_id,provider_timestamp FROM meta_whatsapp_webhook_receipts
	 WHERE processing_owner='tessa' AND processing_status IN ('pending','retry') AND available_at<=NOW()
	 AND phone_number_id=$1 ORDER BY available_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, w.repo.phoneID).Scan(&receiptID, &wamid, &status, &correlation, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var deliveryID uuid.UUID
	// Serialize delivery publication per account so event sequence allocation
	// cannot overtake another uncommitted delivery event. Match revoke's lock order.
	if _, err = tx.Exec(ctx, `SELECT c.id FROM clients c WHERE c.id IN (
      SELECT client_id FROM tessa_whatsapp_outbox WHERE phone_number_id=$1 AND (id=NULLIF($2,'')::uuid OR provider_message_id=$3)
    ) ORDER BY c.id FOR UPDATE`, w.repo.phoneID, correlation, wamid); err != nil {
		return false, err
	}
	var current, currentWAMID string
	var currentAt *time.Time
	var matches int
	err = tx.QueryRow(ctx, `WITH matches AS MATERIALIZED (
	 SELECT id,status,provider_message_id,status_at FROM tessa_whatsapp_outbox WHERE phone_number_id=$1
	 AND (id=NULLIF($2,'')::uuid OR provider_message_id=$3) ORDER BY id FOR UPDATE
	) SELECT id,status,provider_message_id,status_at,(SELECT COUNT(*) FROM matches) FROM matches LIMIT 1`, w.repo.phoneID, correlation, wamid).Scan(&deliveryID, &current, &currentWAMID, &currentAt, &matches)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `UPDATE meta_whatsapp_webhook_receipts SET processing_status='completed',processed_at=NOW(),last_error_code='delivery_removed' WHERE id=$1`, receiptID)
		if err != nil {
			return false, err
		}
		return true, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	if matches != 1 || (correlation != "" && correlation != deliveryID.String()) || (currentWAMID != "" && currentWAMID != wamid) {
		_, err = tx.Exec(ctx, `UPDATE meta_whatsapp_webhook_receipts SET processing_owner='quarantined',processing_status='dead_letter',processed_at=NOW(),last_error_code='status_owner_conflict' WHERE id=$1`, receiptID)
		if err != nil {
			return false, err
		}
		return true, tx.Commit(ctx)
	}
	if shouldApplyTessaStatus(current, currentAt, status, at) {
		if status == "deleted" {
			status = "failed"
		}
		_, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET status=$2,provider_message_id=$3,status_at=$4,completed_at=NOW() WHERE id=$1`, deliveryID, status, wamid, at)
		if err != nil {
			return false, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE meta_whatsapp_webhook_receipts SET processing_status='completed',processed_at=NOW(),attempt_count=attempt_count+1 WHERE id=$1`, receiptID)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
func shouldApplyTessaStatus(current string, currentAt *time.Time, next string, at time.Time) bool {
	if current == "failed" || current == "cancelled" || current == "expired" {
		return false
	}
	if currentAt != nil && at.Before(*currentAt) {
		return false
	}
	if next == "failed" || next == "deleted" {
		return currentAt == nil || at.After(*currentAt)
	}
	rank := map[string]int{"sent": 1, "delivered": 2, "read": 3}
	return rank[next] > 0 && rank[next] > rank[current]
}
