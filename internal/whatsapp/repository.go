package whatsapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type WebhookRepository struct {
	db         *pgxpool.Pool
	foundation *ContactFoundationRepository
	tessa      *TessaLinkRepository
}

func NewWebhookRepository(db *pgxpool.Pool, foundation *ContactFoundationRepository) *WebhookRepository {
	return &WebhookRepository{db: db, foundation: foundation}
}

func (repository *WebhookRepository) WithTessaLinks(tessa *TessaLinkRepository) *WebhookRepository {
	repository.tessa = tessa
	return repository
}

func (repository *WebhookRepository) StoreWebhookReceipts(ctx context.Context, receipts []WebhookReceipt) error {
	if repository == nil || repository.db == nil {
		return errors.New("Meta webhook repository is not configured")
	}
	if len(receipts) == 0 {
		return nil
	}
	return pgx.BeginFunc(ctx, repository.db, func(tx pgx.Tx) error {
		pendingInserted := false
		for _, receipt := range receipts {
			var receiptID uuid.UUID
			err := tx.QueryRow(ctx, `
				INSERT INTO meta_whatsapp_webhook_receipts (
					id, dedupe_key, waba_id, phone_number_id, event_kind, wamid,
					message_status, provider_timestamp, provider_error_code, processing_status,
					correlation_id, processed_at
				) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,
					CASE WHEN $10::text IN ('completed','dead_letter') THEN NOW() ELSE NULL END)
				ON CONFLICT (dedupe_key) DO NOTHING
				RETURNING id
			`, uuid.New(), receipt.DedupeKey, receipt.BusinessID, receipt.PhoneNumberID,
				receipt.EventKind, receipt.MessageID, receipt.MessageStatus,
				receipt.ProviderTimestamp, receipt.ProviderErrorCode, receipt.ProcessingStatus,
				receipt.CorrelationID,
			).Scan(&receiptID)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("insert Meta WhatsApp webhook receipt: %w", err)
			}
			if receipt.ProcessingStatus == "pending" {
				pendingInserted = true
			}
			if receipt.control.kind != "" {
				if repository.tessa != nil {
					if err := repository.tessa.applyControlTx(ctx, tx, receipt, receiptID); err != nil {
						return err
					}
				}
				if repository.foundation != nil {
					if err := repository.foundation.applyInboundControlTx(ctx, tx, receipt.control); err != nil {
						return err
					}
				}
			}
		}
		if pendingInserted {
			if _, err := tx.Exec(ctx, `SELECT pg_notify('tellbook_worker_core', 'whatsapp_status')`); err != nil {
				return fmt.Errorf("notify WhatsApp status worker: %w", err)
			}
		}
		return nil
	})
}
