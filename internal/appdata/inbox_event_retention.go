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
	inboxEventRetentionAge      = 30 * 24 * time.Hour
	inboxEventRetentionInterval = 15 * time.Minute
	inboxEventRetentionBatch    = 5_000
)

type InboxEventRetentionWorker struct {
	db     *pgxpool.Pool
	logger *slog.Logger
}

func NewInboxEventRetentionWorker(
	db *pgxpool.Pool,
	logger *slog.Logger,
) *InboxEventRetentionWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &InboxEventRetentionWorker{db: db, logger: logger}
}

func (worker *InboxEventRetentionWorker) Start(ctx context.Context) {
	if worker == nil || worker.db == nil {
		return
	}
	worker.runOnce(ctx)
	ticker := time.NewTicker(inboxEventRetentionInterval)
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

func (worker *InboxEventRetentionWorker) runOnce(ctx context.Context) {
	deleted, err := pruneExpiredInboxEvents(
		ctx,
		worker.db,
		time.Now().UTC().Add(-inboxEventRetentionAge),
		inboxEventRetentionBatch,
	)
	if err != nil {
		worker.logger.Warn("prune expired inbox events failed", "error", err)
		return
	}
	if deleted > 0 {
		worker.logger.Debug("pruned expired inbox events", "count", deleted)
	}
}

type inboxRetentionExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func pruneExpiredInboxEvents(
	ctx context.Context,
	execer inboxRetentionExecer,
	cutoff time.Time,
	limit int,
) (int64, error) {
	if limit < 1 || limit > inboxEventRetentionBatch {
		limit = inboxEventRetentionBatch
	}
	tag, err := execer.Exec(ctx, `
		WITH expired AS (
			SELECT sequence
			FROM inbox_events
			WHERE created_at < $1
			ORDER BY sequence
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM inbox_events event
		USING expired
		WHERE event.sequence=expired.sequence
	`, cutoff, limit)
	if err != nil {
		return 0, fmt.Errorf("prune expired inbox events: %w", err)
	}
	return tag.RowsAffected(), nil
}
