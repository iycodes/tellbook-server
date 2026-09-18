package appdata

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	dataMaintenanceInterval      = 15 * time.Minute
	dataMaintenanceBatchSize     = 1_000
	dataMaintenanceBatchesPerRun = 4
)

type DataMaintenanceWorker struct {
	db     *pgxpool.Pool
	logger *slog.Logger
	tasks  []dataMaintenanceTask
}

func NewDataMaintenanceWorker(db *pgxpool.Pool, logger *slog.Logger) *DataMaintenanceWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &DataMaintenanceWorker{db: db, logger: logger, tasks: dataMaintenanceTasks}
}

func (worker *DataMaintenanceWorker) Start(ctx context.Context) {
	if worker == nil || worker.db == nil {
		return
	}
	worker.runOnce(ctx)
	ticker := time.NewTicker(dataMaintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			worker.runOnce(ctx)
		}
	}
}

func (worker *DataMaintenanceWorker) runOnce(ctx context.Context) {
	for _, task := range worker.tasks {
		var total int64
		for range dataMaintenanceBatchesPerRun {
			deleted, err := pruneMaintenanceTask(ctx, worker.db, task, time.Now().UTC(), dataMaintenanceBatchSize)
			if err != nil {
				worker.logger.Warn("data maintenance task failed", "task", task.name, "error", err)
				break
			}
			total += deleted
			if deleted < dataMaintenanceBatchSize {
				break
			}
		}
		if total > 0 {
			worker.logger.Debug("data maintenance task completed", "task", task.name, "affected", total)
		}
	}
}

type dataMaintenanceExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type dataMaintenanceTask struct {
	name  string
	query string
}

func pruneMaintenanceTask(
	ctx context.Context,
	execer dataMaintenanceExecer,
	task dataMaintenanceTask,
	now time.Time,
	limit int,
) (int64, error) {
	if execer == nil || task.name == "" || task.query == "" {
		return 0, fmt.Errorf("invalid data maintenance task")
	}
	if limit < 1 || limit > dataMaintenanceBatchSize {
		limit = dataMaintenanceBatchSize
	}
	tag, err := execer.Exec(ctx, task.query, now, limit)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", task.name, err)
	}
	return tag.RowsAffected(), nil
}

var dataMaintenanceTasks = []dataMaintenanceTask{
	{name: "provider_customer_contact_challenges", query: `
		WITH expired AS (
			SELECT id FROM provider_customer_contact_challenges WHERE expires_at<$1
			ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM provider_customer_contact_challenges item USING expired WHERE item.id=expired.id
	`},
	{name: "resolved_locations", query: `
		WITH expired AS (
			SELECT id FROM resolved_locations WHERE expires_at<$1
			ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM resolved_locations item USING expired WHERE item.id=expired.id
	`},
	{name: "provider_refresh_sessions", query: `
		WITH expired AS (
			SELECT id FROM auth_refresh_sessions WHERE expires_at<$1
			ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM auth_refresh_sessions item USING expired WHERE item.id=expired.id
	`},
	{name: "provider_auth_challenges", query: `
		WITH expired AS (
			SELECT id FROM provider_auth_challenges
			WHERE COALESCE(verify_expires_at,delivery_deadline)<$1::timestamptz-INTERVAL '24 hours'
			ORDER BY COALESCE(verify_expires_at,delivery_deadline),id
			LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM provider_auth_challenges item USING expired WHERE item.id=expired.id
	`},
	{name: "terminal_auth_code_delivery_jobs", query: `
			WITH expired AS (
			SELECT id FROM auth_code_delivery_jobs
			WHERE status IN ('accepted','sent','delivered','unknown','failed','expired')
			  AND COALESCE(completed_at,updated_at)<$1::timestamptz-INTERVAL '24 hours'
			ORDER BY COALESCE(completed_at,updated_at),id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
			DELETE FROM auth_code_delivery_jobs item USING expired WHERE item.id=expired.id
		`},
	{name: "auth_password_reset_grants", query: `
			WITH expired AS (
				SELECT id FROM auth_password_reset_grants WHERE expires_at<$1
				ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
			)
			DELETE FROM auth_password_reset_grants item USING expired WHERE item.id=expired.id
		`},
	{name: "marketplace_auth_challenges", query: `
		WITH expired AS (
			SELECT id FROM marketplace_auth_challenges
			WHERE COALESCE(verify_expires_at,delivery_deadline)<$1::timestamptz-INTERVAL '24 hours'
			ORDER BY COALESCE(verify_expires_at,delivery_deadline),id
			LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM marketplace_auth_challenges item USING expired WHERE item.id=expired.id
	`},
	{name: "marketplace_auth_sessions", query: `
		WITH expired AS (
			SELECT id FROM marketplace_auth_sessions WHERE expires_at<$1
			ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM marketplace_auth_sessions item USING expired WHERE item.id=expired.id
	`},
	{name: "unconsumed_booking_quotes", query: `
		WITH expired AS (
			SELECT quote.id FROM booking_quotes quote
			WHERE quote.booking_id IS NULL AND quote.consumed_at IS NULL
			  AND quote.expires_at<$1::timestamptz-INTERVAL '24 hours'
			  AND NOT EXISTS (
				SELECT 1 FROM inbox_ai_booking_sessions session WHERE session.quote_id=quote.id
			  )
			ORDER BY quote.expires_at,quote.id LIMIT $2 FOR UPDATE OF quote SKIP LOCKED
		)
		DELETE FROM booking_quotes quote USING expired WHERE quote.id=expired.id
	`},
	{name: "unconsumed_booking_change_quotes", query: `
		WITH expired AS (
			SELECT quote.id FROM booking_change_quotes quote
			WHERE quote.consumed_at IS NULL AND quote.expires_at<$1::timestamptz-INTERVAL '24 hours'
			  AND NOT EXISTS (
				SELECT 1 FROM booking_change_commands command WHERE command.quote_id=quote.id
			  )
			ORDER BY quote.expires_at,quote.id LIMIT $2 FOR UPDATE OF quote SKIP LOCKED
		)
		DELETE FROM booking_change_quotes quote USING expired WHERE quote.id=expired.id
	`},
	{name: "completed_agreement_jobs", query: `
		WITH expired AS (
			SELECT id FROM agreement_jobs
			WHERE status='completed' AND completed_at<$1::timestamptz-INTERVAL '30 days'
			ORDER BY completed_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM agreement_jobs job USING expired WHERE job.id=expired.id
	`},
	{name: "terminal_agreement_generation_jobs", query: `
		WITH expired AS (
			SELECT id FROM agreement_template_generation_jobs
			WHERE status IN ('completed','failed') AND completed_at<$1::timestamptz-INTERVAL '90 days'
			ORDER BY completed_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM agreement_template_generation_jobs job USING expired WHERE job.id=expired.id
	`},
	{name: "completed_financial_jobs", query: `
		WITH expired AS (
			SELECT id FROM financial_jobs
			WHERE status IN ('completed','cancelled') AND completed_at<$1::timestamptz-INTERVAL '30 days'
			ORDER BY completed_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM financial_jobs job USING expired WHERE job.id=expired.id
	`},
	{name: "terminal_notification_event_jobs", query: `
		WITH expired AS (
			SELECT booking_event_id FROM notification_event_jobs
			WHERE status IN ('completed','dead_letter')
			  AND completed_at<$1::timestamptz-INTERVAL '30 days'
			ORDER BY completed_at,booking_event_id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM notification_event_jobs job USING expired
		WHERE job.booking_event_id=expired.booking_event_id
	`},
	{name: "terminal_notification_scope_jobs", query: `
		WITH expired AS (
			SELECT client_id,preference_revision FROM notification_scope_replan_jobs
			WHERE status IN ('completed','dead_letter','superseded')
			  AND completed_at<$1::timestamptz-INTERVAL '30 days'
			ORDER BY completed_at,client_id,preference_revision LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM notification_scope_replan_jobs job USING expired
		WHERE job.client_id=expired.client_id
		  AND job.preference_revision=expired.preference_revision
	`},
	{name: "terminal_notification_deliveries", query: `
		WITH expired AS (
			SELECT id FROM notification_deliveries
			WHERE (status IN ('failed','deleted','cancelled','read','delivered')
			       OR (channel='email' AND status='accepted'))
              -- These rows are the booking-level deduplication record, including after rescheduling.
              AND notification_type NOT IN ('booking_step_reminder','booking_completed')
			  AND completed_at<$1::timestamptz-INTERVAL '90 days'
			ORDER BY completed_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM notification_deliveries delivery USING expired
		WHERE delivery.id=expired.id
	`},
	{name: "terminal_welcome_email_jobs", query: `
		WITH expired AS (
			SELECT id FROM welcome_email_jobs
			WHERE status IN ('accepted','failed')
			  AND completed_at<$1::timestamptz-INTERVAL '90 days'
			ORDER BY completed_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM welcome_email_jobs job USING expired WHERE job.id=expired.id
	`},
	{name: "orphaned_notification_dispatches", query: `
		WITH expired AS (
			SELECT id FROM notification_deliveries
			WHERE status IN ('dispatching','unknown') AND reconcile_after<$1
			ORDER BY reconcile_after,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		UPDATE notification_deliveries delivery
		SET status='manual_review',reconcile_after=NULL,
			provider_status='manual_review',provider_status_at=$1,
			last_error_code='dispatch_outcome_unknown',updated_at=$1
		FROM expired WHERE delivery.id=expired.id
	`},
	// Remove children before their receipt/challenge parents. Parent cleanup must
	// never cascade through runnable work or shorten answer-delivery retention.
	{name: "terminal_tessa_whatsapp_outbox", query: `
		WITH expired AS (
			SELECT id FROM tessa_whatsapp_outbox
			WHERE status IN ('accepted','sent','delivered','read','failed','cancelled','expired','manual_review')
			  AND updated_at<$1::timestamptz-INTERVAL '90 days'
			ORDER BY updated_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM tessa_whatsapp_outbox item USING expired WHERE item.id=expired.id
	`},
	{name: "terminal_tessa_whatsapp_ingress", query: `
		WITH expired AS (
			SELECT q.id FROM tessa_whatsapp_ingress q
			WHERE q.status<>'pending' AND q.completed_at<$1::timestamptz-INTERVAL '30 days'
			  AND NOT EXISTS (SELECT 1 FROM tessa_runs r WHERE r.id=q.run_id AND r.status IN ('queued','processing'))
			  AND NOT EXISTS (SELECT 1 FROM tessa_whatsapp_outbox d WHERE d.source_receipt_id=q.source_receipt_id)
			ORDER BY q.completed_at,q.id LIMIT $2 FOR UPDATE OF q SKIP LOCKED
		)
		DELETE FROM tessa_whatsapp_ingress item USING expired WHERE item.id=expired.id
	`},
	{name: "expired_tessa_whatsapp_onboarding", query: `
		WITH expired AS (
			SELECT s.id FROM tessa_whatsapp_onboarding s
			WHERE s.expires_at<$1::timestamptz-INTERVAL '24 hours'
			  AND NOT EXISTS (SELECT 1 FROM tessa_whatsapp_outbox d WHERE d.onboarding_id=s.id)
			ORDER BY s.expires_at,s.id LIMIT $2 FOR UPDATE OF s SKIP LOCKED
		)
		DELETE FROM tessa_whatsapp_onboarding item USING expired WHERE item.id=expired.id
	`},
	{name: "expired_tessa_whatsapp_link_challenges", query: `
		WITH expired AS (
			SELECT q.id FROM tessa_whatsapp_link_challenges q
			WHERE q.expires_at<$1::timestamptz-INTERVAL '24 hours'
			  AND NOT EXISTS (SELECT 1 FROM tessa_whatsapp_outbox d WHERE d.challenge_id=q.id)
			ORDER BY q.expires_at,q.id LIMIT $2 FOR UPDATE OF q SKIP LOCKED
		)
		DELETE FROM tessa_whatsapp_link_challenges item USING expired WHERE item.id=expired.id
	`},
	{name: "expired_tessa_whatsapp_email_challenges", query: `
		WITH expired AS (
			SELECT q.id FROM tessa_whatsapp_email_challenges q
			WHERE q.expires_at<$1::timestamptz-INTERVAL '24 hours'
			  AND NOT EXISTS (SELECT 1 FROM auth_code_delivery_jobs j WHERE j.tessa_link_challenge_id=q.id)
			  AND NOT EXISTS (SELECT 1 FROM tessa_whatsapp_onboarding s WHERE s.challenge_id=q.id)
			ORDER BY q.expires_at,q.id LIMIT $2 FOR UPDATE OF q SKIP LOCKED
		)
		DELETE FROM tessa_whatsapp_email_challenges item USING expired WHERE item.id=expired.id
	`},
	{name: "terminal_tessa_whatsapp_security_events", query: `
		WITH expired AS (
			SELECT e.id FROM tessa_whatsapp_security_events e
			WHERE e.created_at<$1::timestamptz-INTERVAL '90 days'
			  AND e.email_deadline<$1
			  AND NOT EXISTS (SELECT 1 FROM auth_code_delivery_jobs j WHERE j.tessa_security_event_id=e.id)
			ORDER BY e.created_at,e.id LIMIT $2 FOR UPDATE OF e SKIP LOCKED
		)
		DELETE FROM tessa_whatsapp_security_events item USING expired WHERE item.id=expired.id
	`},
	{name: "terminal_whatsapp_webhook_receipts", query: `
		WITH expired AS (
			SELECT id FROM meta_whatsapp_webhook_receipts
			WHERE processing_status IN ('completed','dead_letter')
			  AND processed_at<$1::timestamptz-INTERVAL '30 days'
			  AND NOT EXISTS (SELECT 1 FROM tessa_whatsapp_outbox d WHERE d.source_receipt_id=meta_whatsapp_webhook_receipts.id)
			  AND NOT EXISTS (SELECT 1 FROM tessa_whatsapp_ingress q WHERE q.source_receipt_id=meta_whatsapp_webhook_receipts.id)
			  AND NOT EXISTS (SELECT 1 FROM tessa_whatsapp_email_challenges q WHERE q.source_receipt_id=meta_whatsapp_webhook_receipts.id)
			ORDER BY processed_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM meta_whatsapp_webhook_receipts receipt USING expired
		WHERE receipt.id=expired.id
	`},
	{name: "terminal_notification_in_app_jobs", query: `
		WITH expired AS (
			SELECT id FROM notification_in_app_jobs
			WHERE status IN ('completed','dead_letter','cancelled')
			  AND completed_at<$1::timestamptz-INTERVAL '30 days'
			ORDER BY completed_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM notification_in_app_jobs job USING expired WHERE job.id=expired.id
	`},
	{name: "expired_daily_metric_jobs", query: `
		WITH expired AS (
			SELECT client_id,metric_date,currency_code FROM provider_daily_metric_jobs
			WHERE metric_date<($1::date-400)
			ORDER BY metric_date,client_id,currency_code LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM provider_daily_metric_jobs job USING expired
		WHERE job.client_id=expired.client_id AND job.metric_date=expired.metric_date
		  AND job.currency_code=expired.currency_code
	`},
	{name: "expired_daily_metrics", query: `
		WITH expired AS (
			SELECT client_id,metric_date,currency_code FROM provider_daily_metrics
			WHERE metric_date<($1::date-400)
			ORDER BY metric_date,client_id,currency_code LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM provider_daily_metrics metric USING expired
		WHERE metric.client_id=expired.client_id AND metric.metric_date=expired.metric_date
		  AND metric.currency_code=expired.currency_code
	`},
}
