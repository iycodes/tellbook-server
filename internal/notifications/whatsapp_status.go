package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"booking/go-server/internal/whatsapp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type WhatsAppStatusReceipt struct {
	ID                uuid.UUID
	WAMID             string
	Status            string
	ProviderTimestamp time.Time
	ProviderErrorCode string
	CorrelationID     *uuid.UUID
	AttemptCount      int
	LeaseOwner        string
	LeaseToken        uuid.UUID
	CreatedAt         time.Time
}

func (r *Repository) ClaimWhatsAppStatusReceipts(
	ctx context.Context,
	owner string,
	batch int,
	lease time.Duration,
) ([]WhatsAppStatusReceipt, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, errors.New("WhatsApp status lease owner is required")
	}
	if batch < 1 || batch > 100 {
		batch = defaultClaimBatch
	}
	if lease <= 0 {
		lease = defaultLeaseDuration
	}
	if err := whatsapp.RouteStatusReceipts(ctx, r.db); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `
		WITH exhausted_candidates AS (
			SELECT id FROM meta_whatsapp_webhook_receipts
			WHERE event_kind='status' AND processing_owner='notification' AND processing_status IN ('pending','retry','processing')
			  AND (CASE WHEN processing_status='processing' THEN lease_expires_at ELSE available_at END)<=NOW()
			  AND attempt_count >= $4
			ORDER BY (CASE WHEN processing_status='processing' THEN lease_expires_at ELSE available_at END),created_at,id
			FOR UPDATE SKIP LOCKED LIMIT $1
		), exhausted AS (
			UPDATE meta_whatsapp_webhook_receipts receipt
			SET processing_status='dead_letter',lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				last_error_code='processing_retry_exhausted',processed_at=NOW()
			FROM exhausted_candidates WHERE receipt.id=exhausted_candidates.id
			RETURNING receipt.id
		), claimable AS (
			SELECT id FROM meta_whatsapp_webhook_receipts
			WHERE event_kind='status' AND processing_owner='notification' AND processing_status IN ('pending','retry','processing')
			  AND (CASE WHEN processing_status='processing' THEN lease_expires_at ELSE available_at END)<=NOW()
			  AND attempt_count < $4
			ORDER BY (CASE WHEN processing_status='processing' THEN lease_expires_at ELSE available_at END),created_at,id
			FOR UPDATE SKIP LOCKED LIMIT $1
		), claimed AS (
			UPDATE meta_whatsapp_webhook_receipts receipt
			SET processing_status='processing',attempt_count=attempt_count+1,lease_owner=$2,
				lease_token=gen_random_uuid(),lease_expires_at=NOW()+($3::bigint*INTERVAL '1 millisecond'),
				last_error_code=''
			FROM claimable WHERE receipt.id=claimable.id
			RETURNING receipt.id,receipt.wamid,receipt.message_status,receipt.provider_timestamp,
				receipt.provider_error_code,receipt.correlation_id,receipt.attempt_count,
				receipt.lease_owner,receipt.lease_token,receipt.created_at
		)
		SELECT id,wamid,message_status,provider_timestamp,provider_error_code,correlation_id,
			attempt_count,lease_owner,lease_token,created_at FROM claimed
	`, batch, owner, lease.Milliseconds(), maxWhatsAppStatusAttempts)
	if err != nil {
		return nil, fmt.Errorf("claim WhatsApp status receipts: %w", err)
	}
	defer rows.Close()
	receipts := make([]WhatsAppStatusReceipt, 0, batch)
	for rows.Next() {
		var receipt WhatsAppStatusReceipt
		var correlation string
		if err := rows.Scan(
			&receipt.ID, &receipt.WAMID, &receipt.Status, &receipt.ProviderTimestamp,
			&receipt.ProviderErrorCode, &correlation, &receipt.AttemptCount,
			&receipt.LeaseOwner, &receipt.LeaseToken, &receipt.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan WhatsApp status receipt: %w", err)
		}
		if correlation != "" {
			id, err := uuid.Parse(correlation)
			if err != nil {
				return nil, fmt.Errorf("scan WhatsApp status correlation: %w", err)
			}
			receipt.CorrelationID = &id
		}
		receipts = append(receipts, receipt)
	}
	return receipts, rows.Err()
}

func (worker *WhatsAppWorker) processStatusCycle(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	receipts, err := worker.repository.ClaimWhatsAppStatusReceipts(
		ctx, worker.workerID, defaultClaimBatch, defaultLeaseDuration,
	)
	if err != nil {
		worker.logger.Error("claim WhatsApp status receipts", "error", err)
		return false
	}
	worker.processStatusBatch(ctx, receipts)
	return len(receipts) == defaultClaimBatch
}

func (worker *WhatsAppWorker) processStatusBatch(ctx context.Context, receipts []WhatsAppStatusReceipt) {
	for start := 0; start < len(receipts); start += worker.concurrency {
		end := min(start+worker.concurrency, len(receipts))
		var group sync.WaitGroup
		group.Add(end - start)
		for _, receipt := range receipts[start:end] {
			go func(receipt WhatsAppStatusReceipt) {
				defer group.Done()
				worker.processStatus(ctx, receipt)
			}(receipt)
		}
		group.Wait()
	}
}

func (worker *WhatsAppWorker) processStatus(ctx context.Context, receipt WhatsAppStatusReceipt) {
	finalizeContext, cancel := newWhatsAppFinalizationContext(ctx)
	defer cancel()
	outcome, err := worker.repository.ApplyWhatsAppStatusReceipt(finalizeContext, receipt)
	if err != nil {
		worker.logger.Warn("apply WhatsApp status receipt", "receipt_id", receipt.ID, "error", err)
		return
	}
	lag := worker.repository.now().Sub(receipt.ProviderTimestamp)
	if lag < 0 {
		lag = 0
	}
	if worker.metrics != nil {
		worker.metrics.ObserveNotificationWhatsAppStatus(receipt.Status, outcome, lag)
	}
}

func (r *Repository) ApplyWhatsAppStatusReceipt(
	ctx context.Context,
	receipt WhatsAppStatusReceipt,
) (string, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err := tx.QueryRow(ctx, `
		SELECT TRUE FROM meta_whatsapp_webhook_receipts
		WHERE id=$1 AND processing_status='processing' AND lease_owner=$2 AND lease_token=$3
		FOR UPDATE
	`, receipt.ID, receipt.LeaseOwner, receipt.LeaseToken).Scan(&locked); errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("WhatsApp status receipt lease was lost")
	} else if err != nil {
		return "", err
	}
	var correlation any
	if receipt.CorrelationID != nil {
		correlation = *receipt.CorrelationID
	}
	var deliveryID uuid.UUID
	var currentStatus, currentWAMID string
	var currentStatusAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT id,status,provider_message_id,provider_status_at
		FROM notification_deliveries
		WHERE channel='whatsapp' AND (
			(provider_message_id=$1 AND ($2::uuid IS NULL OR id=$2::uuid))
			OR ($2::uuid IS NOT NULL AND id=$2::uuid AND provider_message_id IN ('',$1))
		)
		ORDER BY (provider_message_id=$1) DESC
		LIMIT 1 FOR UPDATE
	`, receipt.WAMID, correlation).Scan(&deliveryID, &currentStatus, &currentWAMID, &currentStatusAt)
	if errors.Is(err, pgx.ErrNoRows) {
		if receipt.CorrelationID == nil {
			if err := completeWhatsAppReceiptTx(ctx, tx, receipt, "unmatched_external_status"); err != nil {
				return "", err
			}
			if err := tx.Commit(ctx); err != nil {
				return "", err
			}
			return "ignored", nil
		}
		outcome, err := retryWhatsAppReceiptTx(ctx, tx, receipt, "delivery_not_visible", r.now())
		if err != nil {
			return "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return outcome, nil
	}
	if err != nil {
		return "", fmt.Errorf("match WhatsApp delivery status: %w", err)
	}
	apply := shouldApplyWhatsAppStatus(currentStatus, currentStatusAt, receipt.Status, receipt.ProviderTimestamp)
	if apply {
		providerCode := boundedProviderCode(receipt.ProviderErrorCode)
		lastErrorCode := ""
		if receipt.Status == "failed" || receipt.Status == "deleted" {
			if providerCode == "" {
				providerCode = "meta_" + receipt.Status
			}
			lastErrorCode = boundedCode(providerCode)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE notification_deliveries SET
				status=$2,provider_message_id=CASE WHEN provider_message_id='' THEN $3 ELSE provider_message_id END,
				provider_status=$2,provider_status_at=$4,provider_error_code=$5,last_error_code=$6,
				reconcile_after=NULL,
				sent_at=CASE WHEN $2='sent' THEN $4 ELSE sent_at END,
				delivered_at=CASE WHEN $2='delivered' THEN $4 ELSE delivered_at END,
				read_at=CASE WHEN $2='read' THEN $4 ELSE read_at END,
				completed_at=CASE WHEN $2 IN ('delivered','read','failed','deleted') THEN $4 ELSE completed_at END,
				updated_at=NOW()
			WHERE id=$1
		`, deliveryID, receipt.Status, receipt.WAMID, receipt.ProviderTimestamp,
			providerCode, lastErrorCode); err != nil {
			return "", fmt.Errorf("apply WhatsApp delivery status: %w", err)
		}
	}
	if err := completeWhatsAppReceiptTx(ctx, tx, receipt, ""); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	if apply {
		return "applied", nil
	}
	return "stale", nil
}

func shouldApplyWhatsAppStatus(current string, currentAt *time.Time, incoming string, incomingAt time.Time) bool {
	if current == "cancelled" || current == "failed" || current == "deleted" {
		return false
	}
	incomingRank := whatsAppStatusRank(incoming)
	if incomingRank == 0 {
		return false
	}
	currentRank := whatsAppStatusRank(current)
	if currentRank > 0 && currentAt != nil && incomingAt.Before(*currentAt) {
		return false
	}
	if incoming == "failed" || incoming == "deleted" {
		return true
	}
	return incomingRank > currentRank
}

func whatsAppStatusRank(status string) int {
	switch status {
	case "sent":
		return 1
	case "delivered":
		return 2
	case "read":
		return 3
	case "failed", "deleted":
		return 4
	default:
		return 0
	}
}

func completeWhatsAppReceiptTx(
	ctx context.Context,
	tx pgx.Tx,
	receipt WhatsAppStatusReceipt,
	code string,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE meta_whatsapp_webhook_receipts
		SET processing_status='completed',lease_owner='',lease_token=NULL,lease_expires_at=NULL,
			last_error_code=$4,processed_at=NOW()
		WHERE id=$1 AND processing_status='processing' AND lease_owner=$2 AND lease_token=$3
	`, receipt.ID, receipt.LeaseOwner, receipt.LeaseToken, boundedCode(code))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("WhatsApp status receipt lease was lost")
	}
	return nil
}

func retryWhatsAppReceiptTx(
	ctx context.Context,
	tx pgx.Tx,
	receipt WhatsAppStatusReceipt,
	code string,
	now time.Time,
) (string, error) {
	status := "retry"
	var processedAt any
	availableAt := deliveryRetryAt(now, receipt.ID, receipt.AttemptCount)
	outcome := "retry"
	if receipt.AttemptCount >= maxWhatsAppStatusAttempts {
		status = "dead_letter"
		processedAt = now
		outcome = "dead_letter"
	}
	tag, err := tx.Exec(ctx, `
		UPDATE meta_whatsapp_webhook_receipts
		SET processing_status=$4,available_at=$5,lease_owner='',lease_token=NULL,lease_expires_at=NULL,
			last_error_code=$6,processed_at=$7
		WHERE id=$1 AND processing_status='processing' AND lease_owner=$2 AND lease_token=$3
	`, receipt.ID, receipt.LeaseOwner, receipt.LeaseToken, status, availableAt, boundedCode(code), processedAt)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", errors.New("WhatsApp status receipt lease was lost")
	}
	return outcome, nil
}

func boundedProviderCode(code string) string {
	code = strings.TrimSpace(code)
	if len(code) > 120 {
		return code[:120]
	}
	return code
}
