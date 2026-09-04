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
	inboxAIRunRetentionAge      = 90 * 24 * time.Hour
	inboxAIRunStaleAge          = 10 * time.Minute
	inboxAIRunMaintenancePeriod = 15 * time.Minute
	inboxAIRunRetentionBatch    = 1_000
)

type InboxAIRunRetentionWorker struct {
	db     *pgxpool.Pool
	logger *slog.Logger
}

func NewInboxAIRunRetentionWorker(db *pgxpool.Pool, logger *slog.Logger) *InboxAIRunRetentionWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &InboxAIRunRetentionWorker{db: db, logger: logger}
}

func (worker *InboxAIRunRetentionWorker) Start(ctx context.Context) {
	if worker == nil || worker.db == nil {
		return
	}
	worker.runOnce(ctx)
	ticker := time.NewTicker(inboxAIRunMaintenancePeriod)
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

func (worker *InboxAIRunRetentionWorker) runOnce(ctx context.Context) {
	recovered, deleted, err := maintainInboxAIRuns(
		ctx, worker.db, time.Now().UTC().Add(-inboxAIRunStaleAge),
		time.Now().UTC().Add(-inboxAIRunRetentionAge), inboxAIRunRetentionBatch,
	)
	if err != nil {
		worker.logger.Warn("maintain inbox ai runs failed", "error", err)
		return
	}
	if recovered > 0 || deleted > 0 {
		worker.logger.Debug("maintained inbox ai records", "recovered_runs", recovered,
			"deleted_records", deleted)
	}
}

type inboxAIMaintenanceExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func maintainInboxAIRuns(ctx context.Context, execer inboxAIMaintenanceExecer, staleCutoff, retentionCutoff time.Time, limit int) (int64, int64, error) {
	if limit < 1 || limit > inboxAIRunRetentionBatch {
		limit = inboxAIRunRetentionBatch
	}
	recovered, err := execer.Exec(ctx, `
		UPDATE inbox_ai_runs
		SET status='failed', error_code='abandoned', latency_ms=GREATEST(0, EXTRACT(EPOCH FROM (NOW()-created_at))*1000)::integer,
			completed_at=NOW(), provider_outcome='unavailable'
		WHERE status='running' AND created_at < $1
		  AND NOT EXISTS (
			SELECT 1 FROM inbox_ai_turn_jobs job
			WHERE job.id=inbox_ai_runs.turn_job_id
			  AND job.status='processing' AND job.lease_expires_at>NOW()
		  )
	`, staleCutoff)
	if err != nil {
		return 0, 0, fmt.Errorf("recover stale inbox ai runs: %w", err)
	}
	deleted, err := execer.Exec(ctx, `
		WITH expired AS (
			SELECT id FROM inbox_ai_runs
			WHERE created_at < $1
			ORDER BY created_at, id
			LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM inbox_ai_runs run USING expired WHERE run.id=expired.id
	`, retentionCutoff, limit)
	if err != nil {
		return recovered.RowsAffected(), 0, fmt.Errorf("prune inbox ai runs: %w", err)
	}
	deletedJobs, err := execer.Exec(ctx, `
		WITH expired AS (
			SELECT id FROM inbox_ai_turn_jobs
			WHERE status IN ('completed','failed','cancelled') AND completed_at < $1
			ORDER BY completed_at, id
			LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM inbox_ai_turn_jobs job USING expired WHERE job.id=expired.id
	`, retentionCutoff, limit)
	if err != nil {
		return recovered.RowsAffected(), deleted.RowsAffected(),
			fmt.Errorf("prune inbox ai turn jobs: %w", err)
	}
	return recovered.RowsAffected(), deleted.RowsAffected() + deletedJobs.RowsAffected(), nil
}
