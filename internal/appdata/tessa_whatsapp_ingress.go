package appdata

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"booking/go-server/internal/whatsapp"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type TessaWhatsAppIngress struct {
	notice string
}

func NewTessaWhatsAppIngress(notice string) *TessaWhatsAppIngress {
	return &TessaWhatsAppIngress{notice: notice}
}

// Called only from signed ingress in its receipt transaction. It stores no model
// run until the preceding turn is terminal, so queued follow-ups see that answer.
func (s *TessaWhatsAppIngress) StoreTessaWhatsAppMessageTx(ctx context.Context, tx pgx.Tx, in whatsapp.TessaInboundMessage) error {
	content := strings.TrimSpace(in.Content)
	if _, err := tx.Exec(ctx, `SELECT id FROM clients WHERE id=$1 FOR UPDATE`, in.ClientID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('tessa-thread:' || $1::uuid::text,0))`, in.ClientID); err != nil {
		return err
	}
	// Read authority and the timestamp from the same durable signed receipt.
	var authorized, currentWindow, future bool
	var sourceTimestamp *time.Time
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tessa_whatsapp_connections g JOIN clients c ON c.id=g.client_id
      WHERE g.client_id=$1 AND g.phone_number_id=$2 AND g.destination=$3 AND g.revision=$4 AND g.security_revision=$5
	  AND c.security_revision=g.security_revision AND g.status='active' AND g.expires_at>NOW() AND g.notice_revision=$6),
      provider_timestamp,COALESCE(provider_timestamp+INTERVAL '24 hours'>NOW(),false),
      COALESCE(provider_timestamp>NOW()+INTERVAL '1 minute',false)
      FROM meta_whatsapp_webhook_receipts WHERE id=$7 AND phone_number_id=$2 AND wamid=$8 AND event_kind='inbound_message'`, in.ClientID, in.PhoneNumberID, in.Sender, in.ConnectionRevision, in.SecurityRevision, whatsapp.TessaWhatsAppNotice, in.ReceiptID, in.MessageID).Scan(&authorized, &sourceTimestamp, &currentWindow, &future)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !authorized || sourceTimestamp == nil {
		return nil
	}
	in.SourceTimestamp = *sourceTimestamp
	// Meta ids are opaque; use a namespace-derived UUID, never UUID-parse a wamid.
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("tellbook:tessa:whatsapp:"+in.PhoneNumberID+":"+in.MessageID))
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tessa_whatsapp_ingress WHERE id=$1)`, id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	status, reason := "pending", ""
	if !utf8.ValidString(content) || content == "" || utf8.RuneCountInString(content) > 4000 {
		status, reason = "rejected", "invalid_text"
	}
	if err := requireTessaNoticeAcknowledgement(ctx, tx, in.ClientID, s.notice); errors.Is(err, ErrTessaNoticeRevision) {
		status, reason = "rejected", "notice_required"
	} else if err != nil {
		return err
	}
	if !currentWindow {
		status, reason = "expired", "queue_expired"
	} else if future {
		status, reason = "rejected", "invalid_timestamp"
	}
	var threadID *uuid.UUID
	if status == "pending" {
		thread, err := ensureActiveTessaThread(ctx, tx, in.ClientID, uuid.New())
		if err != nil {
			return err
		}
		id := uuid.MustParse(thread.ID)
		threadID = &id
		if _, err := tx.Exec(ctx, `SELECT id FROM tessa_threads WHERE id=$1 FOR UPDATE`, id); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_whatsapp_ingress WHERE client_id=$1 AND status='pending'`, in.ClientID).Scan(&pending); err != nil {
			return err
		}
		if pending >= 5 {
			status, reason = "rejected", "queue_full"
		}
	}
	var body any
	if status == "pending" {
		body = content
	}
	_, err = tx.Exec(ctx, `INSERT INTO tessa_whatsapp_ingress(id,source_receipt_id,client_id,thread_id,phone_number_id,destination,connection_revision,security_revision,source_message_id,source_timestamp,content,status,reason,expires_at,completed_at)
      VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,LEAST(NOW()+INTERVAL '5 minutes',$10::timestamptz+INTERVAL '24 hours'),CASE WHEN $12<>'pending' THEN NOW() ELSE NULL END)`, id, in.ReceiptID, in.ClientID, threadID, in.PhoneNumberID, in.Sender, in.ConnectionRevision, in.SecurityRevision, in.MessageID, in.SourceTimestamp, body, status, reason)
	if err != nil {
		return err
	}
	if status != "pending" {
		kind := "queue_unavailable"
		if reason == "queue_full" {
			kind = "queue_busy"
		}
		if status == "expired" {
			kind = "queue_expired"
		}
		return enqueueTessaQueueNoticeTx(ctx, tx, id, kind)
	}
	_, err = tx.Exec(ctx, `SELECT pg_notify('tellbook_worker_ai','tessa_whatsapp_ingress')`)
	return err
}

func enqueueTessaQueueNoticeTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, kind string) error {
	_, err := tx.Exec(ctx, `INSERT INTO tessa_whatsapp_outbox(id,source_receipt_id,client_id,phone_number_id,destination,connection_revision,security_revision,kind,window_expires_at)
      SELECT gen_random_uuid(),q.source_receipt_id,q.client_id,q.phone_number_id,q.destination,q.connection_revision,q.security_revision,$2,q.source_timestamp+INTERVAL '24 hours'
      FROM tessa_whatsapp_ingress q WHERE q.id=$1 AND NOT EXISTS(SELECT 1 FROM tessa_whatsapp_outbox d WHERE d.client_id=q.client_id
        AND d.kind IN ('queue_busy','queue_expired','queue_unavailable') AND d.created_at>NOW()-INTERVAL '1 minute') ON CONFLICT(source_receipt_id) DO NOTHING`, id, kind)
	if err == nil {
		_, err = tx.Exec(ctx, `SELECT pg_notify('tellbook_worker_core','tessa_whatsapp')`)
	}
	return err
}

// The account/advisory/thread locks also serialize web submissions and reset.
// No persistent lease is needed: admission is one short, DB-only transaction.
func (worker *TessaWorker) admitWhatsApp(ctx context.Context) (bool, error) {
	if worker.config.WhatsAppPhoneNumberID == "" {
		return false, nil
	}
	tx, err := worker.repo.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var clientID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT q.client_id FROM tessa_whatsapp_ingress q JOIN tessa_threads t ON t.id=q.thread_id
	  JOIN clients c ON c.id=q.client_id
	  WHERE q.status='pending' AND q.phone_number_id=$1 AND NOT EXISTS(SELECT 1 FROM tessa_whatsapp_ingress earlier WHERE earlier.client_id=q.client_id AND earlier.phone_number_id=$1 AND earlier.status='pending' AND earlier.sequence<q.sequence)
	  AND (q.expires_at<=NOW() OR t.status<>'active' OR NOT EXISTS(SELECT 1 FROM tessa_runs r WHERE r.thread_id=q.thread_id AND r.status IN ('queued','processing')))
      ORDER BY q.sequence LIMIT 1 FOR UPDATE OF c SKIP LOCKED`, worker.config.WhatsAppPhoneNumberID).Scan(&clientID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('tessa-thread:' || $1::uuid::text,0))`, clientID); err != nil {
		return false, err
	}
	var id, threadID uuid.UUID
	var content string
	var expired, valid bool
	err = tx.QueryRow(ctx, `SELECT q.id,q.thread_id,q.content,q.expires_at<=NOW(),COALESCE(g.status='active' AND g.revision=q.connection_revision
      AND g.phone_number_id=q.phone_number_id AND g.destination=q.destination AND g.security_revision=q.security_revision AND c.security_revision=q.security_revision
      AND g.expires_at>NOW() AND g.notice_revision=$2,false)
      FROM tessa_whatsapp_ingress q JOIN clients c ON c.id=q.client_id LEFT JOIN tessa_whatsapp_connections g ON g.client_id=q.client_id
      WHERE q.client_id=$1 AND q.phone_number_id=$3 AND q.status='pending' ORDER BY q.sequence LIMIT 1 FOR UPDATE OF q`, clientID, whatsapp.TessaWhatsAppNotice, worker.config.WhatsAppPhoneNumberID).Scan(&id, &threadID, &content, &expired, &valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var sequence int64
	var threadStatus string
	if err = tx.QueryRow(ctx, `SELECT last_message_sequence,status FROM tessa_threads WHERE id=$1 FOR UPDATE`, threadID).Scan(&sequence, &threadStatus); err != nil {
		return false, err
	}
	reason := ""
	if expired {
		reason = "queue_expired"
	} else if threadStatus != "active" {
		reason = "thread_archived"
	} else if !valid || !worker.whatsAppAllowed(clientID) {
		reason = "connection_changed"
	}
	if reason == "" {
		if err = requireTessaNoticeAcknowledgement(ctx, tx, clientID, worker.config.NoticeRevision); errors.Is(err, ErrTessaNoticeRevision) {
			reason = "notice_required"
		} else if err != nil {
			return false, err
		}
	}
	if reason != "" {
		status := "cancelled"
		if expired {
			status = "expired"
		}
		if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_ingress SET status=$2,reason=$3,content=NULL,completed_at=NOW() WHERE id=$1`, id, status, reason); err != nil {
			return false, err
		}
		kind := "queue_unavailable"
		if expired {
			kind = "queue_expired"
		}
		if err = enqueueTessaQueueNoticeTx(ctx, tx, id, kind); err != nil {
			return false, err
		}
		return true, tx.Commit(ctx)
	}
	var busy bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tessa_runs WHERE thread_id=$1 AND status IN ('queued','processing'))`, threadID).Scan(&busy); err != nil {
		return false, err
	}
	if busy {
		return false, nil
	}
	primary := worker.service.Primary()
	response, err := insertTessaTurnTx(ctx, tx, clientID, threadID, id, content, primary.Name, primary.Model, worker.config.ConfigHash, "whatsapp", sequence)
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_ingress SET status='admitted',content=NULL,message_id=$2,run_id=$3,completed_at=NOW() WHERE id=$1`, id, response.Message.ID, response.Run.ID); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

var errTessaWhatsAppAuthority = errors.New("Tessa WhatsApp authority changed")

func (worker *TessaWorker) whatsAppAllowed(clientID uuid.UUID) bool {
	return worker.whatsAppProviders[clientID]
}

func (worker *TessaWorker) checkWhatsAppAuthority(ctx context.Context, q tessaNoticeQueryRower, run tessaClaimedRun) error {
	if run.SourceChannel == "web" {
		return nil
	}
	var channel string
	var valid bool
	err := q.QueryRow(ctx, `SELECT m.source_channel,EXISTS(SELECT 1 FROM tessa_whatsapp_ingress i JOIN tessa_whatsapp_connections g ON g.client_id=i.client_id
      JOIN clients c ON c.id=i.client_id JOIN tessa_threads t ON t.id=i.thread_id WHERE i.run_id=r.id AND i.status='admitted'
      AND i.client_id=r.client_id AND i.thread_id=r.thread_id AND i.message_id=r.trigger_message_id AND i.phone_number_id=$2
      AND g.status='active' AND g.revision=i.connection_revision AND g.security_revision=i.security_revision AND c.security_revision=i.security_revision
      AND g.phone_number_id=i.phone_number_id AND g.destination=i.destination AND g.expires_at>NOW() AND g.notice_revision=$3 AND t.status='active'
      AND r.status='processing' AND r.lease_token=$4 AND r.lease_expires_at>NOW())
      FROM tessa_runs r JOIN tessa_messages m ON m.id=r.trigger_message_id WHERE r.id=$1`, run.ID, worker.config.WhatsAppPhoneNumberID, whatsapp.TessaWhatsAppNotice, run.LeaseToken).Scan(&channel, &valid)
	if err != nil {
		return err
	}
	if channel == "whatsapp" && (!valid || !worker.whatsAppAllowed(run.ClientID)) {
		return errTessaWhatsAppAuthority
	}
	return nil
}
