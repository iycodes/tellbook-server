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
}

func NewDataMaintenanceWorker(db *pgxpool.Pool, logger *slog.Logger) *DataMaintenanceWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &DataMaintenanceWorker{db: db, logger: logger}
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
	for _, task := range dataMaintenanceTasks {
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
			worker.logger.Debug("data maintenance task completed", "task", task.name, "deleted", total)
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
	{name: "resolved_locations", query: `
		WITH expired AS (
			SELECT id FROM resolved_locations WHERE expires_at<$1
			ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM resolved_locations item USING expired WHERE item.id=expired.id
	`},
	{name: "provider_pending_registrations", query: `
		WITH expired AS (
			SELECT id FROM auth_pending_registrations WHERE expires_at<$1
			ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM auth_pending_registrations item USING expired WHERE item.id=expired.id
	`},
	{name: "provider_password_resets", query: `
		WITH expired AS (
			SELECT id FROM auth_password_reset_tokens WHERE expires_at<$1
			ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM auth_password_reset_tokens item USING expired WHERE item.id=expired.id
	`},
	{name: "provider_refresh_sessions", query: `
		WITH expired AS (
			SELECT id FROM auth_refresh_sessions WHERE expires_at<$1
			ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM auth_refresh_sessions item USING expired WHERE item.id=expired.id
	`},
	{name: "marketplace_auth_challenges", query: `
		WITH expired AS (
			SELECT id FROM marketplace_auth_challenges
			WHERE expires_at<$1::timestamptz-INTERVAL '24 hours'
			ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
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
			WHERE status IN ('failed','deleted','cancelled','read','delivered')
			  AND completed_at<$1::timestamptz-INTERVAL '90 days'
			ORDER BY completed_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM notification_deliveries delivery USING expired
		WHERE delivery.id=expired.id
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
