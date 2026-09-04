package appdata

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const tessaEventDrainLimit = 100

type tessaStoredEvent struct {
	Sequence  int64
	ThreadID  uuid.UUID
	MessageID uuid.NullUUID
	RunID     uuid.NullUUID
	Type      string
	CreatedAt time.Time
}

func (r *Repository) ListTessaEventsAfter(ctx context.Context, clientID uuid.UUID, after string, limit int) (TessaEventDrain, error) {
	if limit < 1 || limit > tessaEventDrainLimit {
		limit = tessaEventDrainLimit
	}
	afterSequence, err := decodeInboxSyncCursor(after)
	if err != nil {
		return TessaEventDrain{}, ErrTessaInvalidCursor
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return TessaEventDrain{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var globalMax, clientMin, clientMax int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),0) FROM tessa_events`).Scan(&globalMax); err != nil {
		return TessaEventDrain{}, err
	}
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MIN(sequence),0),COALESCE(MAX(sequence),0)
		FROM tessa_events WHERE client_id=$1
	`, clientID).Scan(&clientMin, &clientMax); err != nil {
		return TessaEventDrain{}, err
	}
	if afterSequence > globalMax ||
		(afterSequence > 0 && (clientMax == 0 || afterSequence > clientMax ||
			(clientMin > 0 && afterSequence < clientMin-1))) {
		_ = tx.Commit(ctx)
		return TessaEventDrain{
			Events: []TessaRealtimeEvent{}, Cursor: encodeInboxSequenceCursor(clientMax),
			LatestCursor: encodeInboxSequenceCursor(clientMax), Reset: true,
		}, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT sequence,thread_id,message_id,run_id,event_type,created_at
		FROM tessa_events WHERE client_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3
	`, clientID, afterSequence, limit+1)
	if err != nil {
		return TessaEventDrain{}, err
	}
	stored := make([]tessaStoredEvent, 0, limit+1)
	messageIDs := make([]uuid.UUID, 0, limit)
	runIDs := make([]uuid.UUID, 0, limit)
	for rows.Next() {
		var event tessaStoredEvent
		if err := rows.Scan(&event.Sequence, &event.ThreadID, &event.MessageID, &event.RunID, &event.Type, &event.CreatedAt); err != nil {
			rows.Close()
			return TessaEventDrain{}, err
		}
		stored = append(stored, event)
		if event.MessageID.Valid {
			messageIDs = append(messageIDs, event.MessageID.UUID)
		}
		if event.RunID.Valid {
			runIDs = append(runIDs, event.RunID.UUID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return TessaEventDrain{}, err
	}
	rows.Close()
	hasMore := len(stored) > limit
	if hasMore {
		stored = stored[:limit]
	}
	messages, err := loadTessaMessagesByID(ctx, tx, clientID, messageIDs)
	if err != nil {
		return TessaEventDrain{}, err
	}
	runs, err := loadTessaRunsByID(ctx, tx, clientID, runIDs)
	if err != nil {
		return TessaEventDrain{}, err
	}
	events := make([]TessaRealtimeEvent, 0, len(stored))
	cursor := afterSequence
	for _, storedEvent := range stored {
		cursor = storedEvent.Sequence
		event := TessaRealtimeEvent{
			Cursor: encodeInboxSequenceCursor(storedEvent.Sequence), Type: storedEvent.Type,
			ThreadID: storedEvent.ThreadID.String(), CreatedAt: storedEvent.CreatedAt,
		}
		if storedEvent.MessageID.Valid {
			if message, exists := messages[storedEvent.MessageID.UUID]; exists {
				copy := message
				event.Message = &copy
			}
		}
		if storedEvent.RunID.Valid {
			if run, exists := runs[storedEvent.RunID.UUID]; exists {
				copy := run
				event.Run = &copy
			}
		}
		events = append(events, event)
	}
	if err := tx.Commit(ctx); err != nil {
		return TessaEventDrain{}, err
	}
	return TessaEventDrain{
		Events: events, Cursor: encodeInboxSequenceCursor(cursor),
		LatestCursor: encodeInboxSequenceCursor(clientMax), HasMore: hasMore,
	}, nil
}

func loadTessaMessagesByID(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]TessaMessage, error) {
	result := make(map[uuid.UUID]TessaMessage, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id,thread_id,sequence,sender_type,source_channel,client_message_id,content,
			presentation,entity_references,run_id,created_at
		FROM tessa_messages WHERE client_id=$1 AND id=ANY($2::uuid[])
	`, clientID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		message, err := scanTessaMessage(rows)
		if err != nil {
			return nil, err
		}
		id, _ := uuid.Parse(message.ID)
		result[id] = message
	}
	return result, rows.Err()
}

func loadTessaRunsByID(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]TessaRun, error) {
	result := make(map[uuid.UUID]TessaRun, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id,thread_id,trigger_message_id,status,stage,error_code,fallback_used,
			created_at,started_at,completed_at,cancelled_at
		FROM tessa_runs WHERE client_id=$1 AND id=ANY($2::uuid[])
	`, clientID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		run, err := scanTessaRun(rows)
		if err != nil {
			return nil, err
		}
		id, _ := uuid.Parse(run.ID)
		result[id] = run
	}
	return result, rows.Err()
}
