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
	tessaEventRetentionAge       = 30 * 24 * time.Hour
	tessaArchivedThreadRetention = 90 * 24 * time.Hour
	tessaRetentionInterval       = 15 * time.Minute
	tessaRetentionBatch          = 5_000
	tessaRetentionBatchesPerRun  = 4
)

type TessaRetentionWorker struct {
	db     *pgxpool.Pool
	logger *slog.Logger
}

func NewTessaRetentionWorker(db *pgxpool.Pool, logger *slog.Logger) *TessaRetentionWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &TessaRetentionWorker{db: db, logger: logger}
}

func (worker *TessaRetentionWorker) Start(ctx context.Context) {
	if worker == nil || worker.db == nil {
		return
	}
	worker.runOnce(ctx)
	ticker := time.NewTicker(tessaRetentionInterval)
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

func (worker *TessaRetentionWorker) runOnce(ctx context.Context) {
	now := time.Now().UTC()
	events, threads, err := drainTessaRetention(
		ctx, worker.db,
		now.Add(-tessaEventRetentionAge),
		now.Add(-tessaArchivedThreadRetention),
		tessaRetentionBatch, tessaRetentionBatchesPerRun,
	)
	if err != nil {
		worker.logger.Warn("maintain Tessa retention failed", "error", err)
		return
	}
	if events > 0 || threads > 0 {
		worker.logger.Debug("maintained Tessa retention", "deleted_events", events, "deleted_threads", threads)
	}
}

func drainTessaRetention(
	ctx context.Context,
	execer tessaRetentionExecer,
	eventCutoff, archivedThreadCutoff time.Time,
	limit, maximumBatches int,
) (int64, int64, error) {
	if limit < 1 || limit > tessaRetentionBatch {
		limit = tessaRetentionBatch
	}
	if maximumBatches < 1 || maximumBatches > tessaRetentionBatchesPerRun {
		maximumBatches = tessaRetentionBatchesPerRun
	}
	var totalEvents, totalThreads int64
	for range maximumBatches {
		events, threads, err := maintainTessaRetention(
			ctx, execer, eventCutoff, archivedThreadCutoff, limit,
		)
		totalEvents += events
		totalThreads += threads
		if err != nil {
			return totalEvents, totalThreads, err
		}
		if events < int64(limit) && threads < int64(limit) {
			break
		}
	}
	return totalEvents, totalThreads, nil
}

type tessaRetentionExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func maintainTessaRetention(
	ctx context.Context,
	execer tessaRetentionExecer,
	eventCutoff, archivedThreadCutoff time.Time,
	limit int,
) (int64, int64, error) {
	if limit < 1 || limit > tessaRetentionBatch {
		limit = tessaRetentionBatch
	}
	events, err := execer.Exec(ctx, `
		WITH expired AS (
			SELECT sequence FROM tessa_events
			WHERE created_at < $1
			ORDER BY created_at,sequence
			LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM tessa_events event USING expired
		WHERE event.sequence=expired.sequence
	`, eventCutoff, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("prune Tessa events: %w", err)
	}
	threads, err := execer.Exec(ctx, `
		WITH expired AS (
			SELECT id FROM tessa_threads
			WHERE status='archived' AND archived_at < $1
			ORDER BY archived_at,id
			LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM tessa_threads thread USING expired
		WHERE thread.id=expired.id
	`, archivedThreadCutoff, limit)
	if err != nil {
		return events.RowsAffected(), 0, fmt.Errorf("prune archived Tessa threads: %w", err)
	}
	return events.RowsAffected(), threads.RowsAffected(), nil
}
