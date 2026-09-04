package appdata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"booking/go-server/internal/tessa"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrTessaIdempotencyConflict = errors.New("Tessa idempotency key conflicts with an earlier request")
	ErrTessaRunInProgress       = errors.New("Tessa is already processing a message")
	ErrTessaNoticeRevision      = errors.New("Tessa notice revision is stale")
)

const tessaEventChannel = "tellbook_tessa_events"

func (r *Repository) GetTessaBootstrap(
	ctx context.Context,
	clientID uuid.UUID,
	currentNoticeRevision string,
	limit int,
) (TessaBootstrapResponse, error) {
	if limit < 1 || limit > 50 {
		limit = 50
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return TessaBootstrapResponse{}, fmt.Errorf("begin Tessa bootstrap snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	response := TessaBootstrapResponse{Messages: make([]TessaMessage, 0)}
	var acknowledgedRevision string
	if err := tx.QueryRow(ctx, `
		SELECT introduction_completed_at, acknowledged_notice_revision, notice_acknowledged_at
		FROM tessa_preferences WHERE client_id=$1
	`, clientID).Scan(
		&response.Preferences.IntroductionCompletedAt,
		&acknowledgedRevision,
		&response.Preferences.NoticeAcknowledgedAt,
	); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return response, fmt.Errorf("load Tessa preferences: %w", err)
	}
	response.Preferences.IntroductionCompleted = response.Preferences.IntroductionCompletedAt != nil
	response.Preferences.AcknowledgedNoticeRevision = acknowledgedRevision
	response.Preferences.CurrentNoticeRevision = currentNoticeRevision
	response.Preferences.NoticeRequired = acknowledgedRevision != currentNoticeRevision

	var thread TessaThread
	var threadID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id, status, last_activity_at, created_at, archived_at
		FROM tessa_threads WHERE client_id=$1 AND status='active'
	`, clientID).Scan(
		&threadID, &thread.Status, &thread.LastActivityAt, &thread.CreatedAt, &thread.ArchivedAt,
	); errors.Is(err, pgx.ErrNoRows) {
		response.RealtimeCursor, err = latestTessaEventCursor(ctx, tx, clientID)
		if err != nil {
			return response, fmt.Errorf("load Tessa realtime cursor: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return response, fmt.Errorf("commit Tessa bootstrap snapshot: %w", err)
		}
		return response, nil
	} else if err != nil {
		return response, fmt.Errorf("load active Tessa thread: %w", err)
	}
	thread.ID = threadID.String()
	response.Thread = &thread

	items, hasMore, err := listTessaMessages(ctx, tx, clientID, threadID, 0, limit)
	if err != nil {
		return response, err
	}
	response.Messages = items
	response.HasMore = hasMore
	if hasMore && len(items) > 0 {
		response.BeforeSequence = strconv.FormatInt(items[0].Sequence, 10)
	}
	currentRun, err := getActiveTessaRun(ctx, tx, clientID, threadID)
	if err != nil {
		return response, err
	}
	response.CurrentRun = currentRun
	response.RealtimeCursor, err = latestTessaEventCursor(ctx, tx, clientID)
	if err != nil {
		return response, fmt.Errorf("load Tessa realtime cursor: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return response, fmt.Errorf("commit Tessa bootstrap snapshot: %w", err)
	}
	return response, nil
}

func (r *Repository) CompleteTessaIntroduction(
	ctx context.Context,
	clientID uuid.UUID,
	noticeRevision, currentNoticeRevision string,
) error {
	if strings.TrimSpace(noticeRevision) != currentNoticeRevision {
		return ErrTessaNoticeRevision
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin Tessa introduction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended('tessa-thread:' || $1::uuid::text,0))
	`, clientID); err != nil {
		return fmt.Errorf("lock Tessa introduction: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tessa_preferences (
			client_id, introduction_completed_at, acknowledged_notice_revision,
			notice_acknowledged_at, created_at, updated_at
		) VALUES ($1,NOW(),$2,NOW(),NOW(),NOW())
		ON CONFLICT (client_id) DO UPDATE SET
			introduction_completed_at=COALESCE(tessa_preferences.introduction_completed_at,NOW()),
			acknowledged_notice_revision=$2, notice_acknowledged_at=NOW(), updated_at=NOW()
	`, clientID, currentNoticeRevision); err != nil {
		return fmt.Errorf("complete Tessa introduction: %w", err)
	}
	if _, err := ensureActiveTessaThread(ctx, tx, clientID, uuid.New()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) CreateTessaThread(
	ctx context.Context,
	clientID, requestID uuid.UUID,
	currentNoticeRevision string,
) (TessaThread, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return TessaThread{}, fmt.Errorf("begin Tessa thread replacement: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended('tessa-thread:' || $1::uuid::text,0))
	`, clientID); err != nil {
		return TessaThread{}, fmt.Errorf("lock Tessa thread replacement: %w", err)
	}
	if err := requireTessaNoticeAcknowledgement(ctx, tx, clientID, currentNoticeRevision); err != nil {
		return TessaThread{}, err
	}
	if existing, found, err := loadTessaThreadByCreationRequest(ctx, tx, clientID, requestID); err != nil {
		return TessaThread{}, err
	} else if found {
		if existing.Status != "active" {
			existing, err = ensureActiveTessaThread(ctx, tx, clientID, uuid.New())
			if err != nil {
				return TessaThread{}, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return TessaThread{}, err
		}
		return existing, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE tessa_runs SET status='cancelled', stage='completed', lease_owner='', lease_token=NULL,
			lease_expires_at=NULL, error_code='thread_archived', cancelled_at=NOW()
		WHERE client_id=$1 AND status IN ('queued','processing')
	`, clientID); err != nil {
		return TessaThread{}, fmt.Errorf("cancel active Tessa run: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE tessa_threads SET status='archived', archived_at=NOW(), last_activity_at=NOW()
		WHERE client_id=$1 AND status='active'
	`, clientID); err != nil {
		return TessaThread{}, fmt.Errorf("archive active Tessa thread: %w", err)
	}
	thread, err := insertTessaThread(ctx, tx, clientID, requestID)
	if err != nil {
		return TessaThread{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TessaThread{}, fmt.Errorf("commit Tessa thread replacement: %w", err)
	}
	return thread, nil
}

func ensureActiveTessaThread(ctx context.Context, tx pgx.Tx, clientID, requestID uuid.UUID) (TessaThread, error) {
	var thread TessaThread
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id,status,last_activity_at,created_at,archived_at
		FROM tessa_threads WHERE client_id=$1 AND status='active'
	`, clientID).Scan(&id, &thread.Status, &thread.LastActivityAt, &thread.CreatedAt, &thread.ArchivedAt); err == nil {
		thread.ID = id.String()
		return thread, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return TessaThread{}, fmt.Errorf("load active Tessa thread: %w", err)
	}
	return insertTessaThread(ctx, tx, clientID, requestID)
}

func insertTessaThread(ctx context.Context, tx pgx.Tx, clientID, requestID uuid.UUID) (TessaThread, error) {
	id := uuid.New()
	var thread TessaThread
	if err := tx.QueryRow(ctx, `
		INSERT INTO tessa_threads (id,client_id,creation_request_id)
		VALUES ($1,$2,$3)
		RETURNING status,last_activity_at,created_at,archived_at
	`, id, clientID, requestID).Scan(
		&thread.Status, &thread.LastActivityAt, &thread.CreatedAt, &thread.ArchivedAt,
	); err != nil {
		return TessaThread{}, fmt.Errorf("create Tessa thread: %w", err)
	}
	thread.ID = id.String()
	if _, err := appendTessaEvent(ctx, tx, clientID, id, uuid.Nil, uuid.Nil, "thread.created", map[string]any{"thread_id": id.String()}); err != nil {
		return TessaThread{}, err
	}
	return thread, nil
}

func loadTessaThreadByCreationRequest(ctx context.Context, tx pgx.Tx, clientID, requestID uuid.UUID) (TessaThread, bool, error) {
	var thread TessaThread
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id,status,last_activity_at,created_at,archived_at
		FROM tessa_threads WHERE client_id=$1 AND creation_request_id=$2
	`, clientID, requestID).Scan(&id, &thread.Status, &thread.LastActivityAt, &thread.CreatedAt, &thread.ArchivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return TessaThread{}, false, nil
	}
	if err != nil {
		return TessaThread{}, false, fmt.Errorf("load idempotent Tessa thread: %w", err)
	}
	thread.ID = id.String()
	return thread, true, nil
}

func (r *Repository) SendTessaMessage(
	ctx context.Context,
	clientID, threadID, clientMessageID uuid.UUID,
	content, primaryProvider, primaryModel, configHash, currentNoticeRevision string,
) (TessaSendMessageResponse, error) {
	content = strings.TrimSpace(content)
	fingerprint := tessaMessageFingerprint(content)
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return TessaSendMessageResponse{}, fmt.Errorf("begin Tessa message: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if response, found, err := loadIdempotentTessaSend(
		ctx, tx, clientID, threadID, clientMessageID, fingerprint,
	); err != nil {
		return TessaSendMessageResponse{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return TessaSendMessageResponse{}, err
		}
		return response, nil
	}
	if err := requireTessaNoticeAcknowledgement(ctx, tx, clientID, currentNoticeRevision); err != nil {
		return TessaSendMessageResponse{}, err
	}
	var lastSequence int64
	if err := tx.QueryRow(ctx, `
		SELECT last_message_sequence FROM tessa_threads
		WHERE id=$1 AND client_id=$2 AND status='active' FOR UPDATE
	`, threadID, clientID).Scan(&lastSequence); errors.Is(err, pgx.ErrNoRows) {
		return TessaSendMessageResponse{}, ErrNotFound
	} else if err != nil {
		return TessaSendMessageResponse{}, fmt.Errorf("lock Tessa thread: %w", err)
	}
	if response, found, err := loadIdempotentTessaSend(
		ctx, tx, clientID, threadID, clientMessageID, fingerprint,
	); err != nil {
		return TessaSendMessageResponse{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return TessaSendMessageResponse{}, err
		}
		return response, nil
	}
	var activeRunID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id FROM tessa_runs WHERE thread_id=$1 AND status IN ('queued','processing')
	`, threadID).Scan(&activeRunID); err == nil {
		return TessaSendMessageResponse{}, ErrTessaRunInProgress
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return TessaSendMessageResponse{}, fmt.Errorf("check active Tessa run: %w", err)
	}
	messageID := uuid.New()
	runID := uuid.New()
	nextSequence := lastSequence + 1
	var message TessaMessage
	if err := tx.QueryRow(ctx, `
		INSERT INTO tessa_messages (
			id,thread_id,client_id,sequence,sender_type,source_channel,
			client_message_id,request_fingerprint,content
		) VALUES ($1,$2,$3,$4,'provider','web',$5,$6,$7)
		RETURNING created_at
	`, messageID, threadID, clientID, nextSequence, clientMessageID, fingerprint, content).Scan(
		&message.CreatedAt,
	); err != nil {
		return TessaSendMessageResponse{}, fmt.Errorf("insert Tessa message: %w", err)
	}
	inputHash := sha256.Sum256([]byte(content))
	var run TessaRun
	if err := tx.QueryRow(ctx, `
		INSERT INTO tessa_runs (
			id,thread_id,client_id,trigger_message_id,input_hash,schema_revision,
			config_hash,primary_provider,primary_model
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING status,stage,fallback_used,created_at,started_at,completed_at,cancelled_at
	`, runID, threadID, clientID, messageID, hex.EncodeToString(inputHash[:]), tessa.SchemaRevision,
		configHash, primaryProvider, primaryModel).Scan(
		&run.Status, &run.Stage, &run.FallbackUsed, &run.CreatedAt,
		&run.StartedAt, &run.CompletedAt, &run.CancelledAt,
	); err != nil {
		return TessaSendMessageResponse{}, fmt.Errorf("enqueue Tessa run: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE tessa_threads SET last_message_sequence=$2,last_activity_at=NOW() WHERE id=$1
	`, threadID, nextSequence); err != nil {
		return TessaSendMessageResponse{}, fmt.Errorf("advance Tessa thread: %w", err)
	}
	if _, err := appendTessaEvent(ctx, tx, clientID, threadID, messageID, uuid.Nil, "message.created", map[string]any{"message_id": messageID.String()}); err != nil {
		return TessaSendMessageResponse{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TessaSendMessageResponse{}, fmt.Errorf("commit Tessa message: %w", err)
	}
	message.ID, message.ThreadID, message.Sequence = messageID.String(), threadID.String(), nextSequence
	message.SenderType, message.SourceChannel, message.ClientMessageID, message.Content = "provider", "web", clientMessageID.String(), content
	message.Presentation, message.EntityReferences = TessaMessagePresentation{}, []TessaEntityReference{}
	run.ID, run.ThreadID, run.TriggerMessageID = runID.String(), threadID.String(), messageID.String()
	return TessaSendMessageResponse{Message: message, Run: run}, nil
}

type tessaNoticeQueryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func requireTessaNoticeAcknowledgement(
	ctx context.Context,
	querier tessaNoticeQueryRower,
	clientID uuid.UUID,
	currentNoticeRevision string,
) error {
	currentNoticeRevision = strings.TrimSpace(currentNoticeRevision)
	if currentNoticeRevision == "" {
		return ErrTessaNoticeRevision
	}
	var acknowledged bool
	if err := querier.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM tessa_preferences
			WHERE client_id=$1 AND introduction_completed_at IS NOT NULL
				AND acknowledged_notice_revision=$2
		)
	`, clientID, currentNoticeRevision).Scan(&acknowledged); err != nil {
		return fmt.Errorf("verify Tessa notice acknowledgement: %w", err)
	}
	if !acknowledged {
		return ErrTessaNoticeRevision
	}
	return nil
}

func loadIdempotentTessaSend(
	ctx context.Context,
	tx pgx.Tx,
	clientID, threadID, clientMessageID uuid.UUID,
	fingerprint string,
) (TessaSendMessageResponse, bool, error) {
	var existingFingerprint string
	var existingMessageID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id,request_fingerprint FROM tessa_messages
		WHERE thread_id=$1 AND client_id=$2 AND sender_type='provider' AND client_message_id=$3
	`, threadID, clientID, clientMessageID).Scan(&existingMessageID, &existingFingerprint); errors.Is(err, pgx.ErrNoRows) {
		return TessaSendMessageResponse{}, false, nil
	} else if err != nil {
		return TessaSendMessageResponse{}, false, fmt.Errorf("load idempotent Tessa message: %w", err)
	}
	if existingFingerprint != fingerprint {
		return TessaSendMessageResponse{}, false, ErrTessaIdempotencyConflict
	}
	response, err := loadTessaSendResponse(ctx, tx, clientID, threadID, existingMessageID)
	return response, err == nil, err
}

func loadTessaSendResponse(ctx context.Context, tx pgx.Tx, clientID, threadID, messageID uuid.UUID) (TessaSendMessageResponse, error) {
	message, err := scanTessaMessage(tx.QueryRow(ctx, `
		SELECT id,thread_id,sequence,sender_type,source_channel,client_message_id,content,
			presentation,entity_references,run_id,created_at
		FROM tessa_messages WHERE id=$1 AND thread_id=$2 AND client_id=$3
	`, messageID, threadID, clientID))
	if err != nil {
		return TessaSendMessageResponse{}, err
	}
	run, err := scanTessaRun(tx.QueryRow(ctx, `
		SELECT id,thread_id,trigger_message_id,status,stage,error_code,fallback_used,
			created_at,started_at,completed_at,cancelled_at
		FROM tessa_runs WHERE trigger_message_id=$1 AND client_id=$2
	`, messageID, clientID))
	return TessaSendMessageResponse{Message: message, Run: run}, err
}

func (r *Repository) CancelTessaRun(ctx context.Context, clientID, runID uuid.UUID) (TessaRun, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return TessaRun{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var threadID uuid.UUID
	if err := tx.QueryRow(ctx, `
		UPDATE tessa_runs SET status='cancelled',stage='completed',lease_owner='',lease_token=NULL,
			lease_expires_at=NULL,error_code='provider_cancelled',cancelled_at=NOW()
		WHERE id=$1 AND client_id=$2 AND status IN ('queued','processing')
		RETURNING thread_id
	`, runID, clientID).Scan(&threadID); errors.Is(err, pgx.ErrNoRows) {
		return TessaRun{}, ErrNotFound
	} else if err != nil {
		return TessaRun{}, fmt.Errorf("cancel Tessa run: %w", err)
	}
	if _, err := appendTessaEvent(ctx, tx, clientID, threadID, uuid.Nil, runID, "run.cancelled", map[string]any{"run_id": runID.String()}); err != nil {
		return TessaRun{}, err
	}
	run, err := scanTessaRun(tx.QueryRow(ctx, `
		SELECT id,thread_id,trigger_message_id,status,stage,error_code,fallback_used,
			created_at,started_at,completed_at,cancelled_at
		FROM tessa_runs WHERE id=$1
	`, runID))
	if err != nil {
		return TessaRun{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TessaRun{}, err
	}
	return run, nil
}

func (r *Repository) ListOlderTessaMessages(ctx context.Context, clientID, threadID uuid.UUID, before int64, limit int) (TessaMessagePage, error) {
	if before <= 0 {
		return TessaMessagePage{}, ErrTessaInvalidCursor
	}
	if limit < 1 || limit > 50 {
		limit = 50
	}
	var owned bool
	if err := r.db.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM tessa_threads WHERE id=$1 AND client_id=$2)
	`, threadID, clientID).Scan(&owned); err != nil {
		return TessaMessagePage{}, fmt.Errorf("verify Tessa thread ownership: %w", err)
	}
	if !owned {
		return TessaMessagePage{}, ErrNotFound
	}
	items, hasMore, err := listTessaMessages(ctx, r.db, clientID, threadID, before, limit)
	if err != nil {
		return TessaMessagePage{}, err
	}
	page := TessaMessagePage{Items: items, HasMore: hasMore}
	if hasMore && len(items) > 0 {
		page.BeforeSequence = strconv.FormatInt(items[0].Sequence, 10)
	}
	return page, nil
}

var ErrTessaInvalidCursor = errors.New("Tessa cursor is invalid")

type tessaReadQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func listTessaMessages(ctx context.Context, querier tessaReadQuerier, clientID, threadID uuid.UUID, before int64, limit int) ([]TessaMessage, bool, error) {
	query := `
		SELECT id,thread_id,sequence,sender_type,source_channel,client_message_id,content,
			presentation,entity_references,run_id,created_at
		FROM tessa_messages
		WHERE thread_id=$1 AND client_id=$2 AND ($3::bigint=0 OR sequence<$3)
		ORDER BY sequence DESC LIMIT $4
	`
	rows, err := querier.Query(ctx, query, threadID, clientID, before, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list Tessa messages: %w", err)
	}
	defer rows.Close()
	items := make([]TessaMessage, 0, limit+1)
	for rows.Next() {
		message, scanErr := scanTessaMessage(rows)
		if scanErr != nil {
			return nil, false, scanErr
		}
		items = append(items, message)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	return items, hasMore, nil
}

type tessaScanner interface{ Scan(...any) error }

func scanTessaMessage(scanner tessaScanner) (TessaMessage, error) {
	var message TessaMessage
	var id, threadID uuid.UUID
	var clientMessageID, runID uuid.NullUUID
	var presentationJSON, referencesJSON []byte
	if err := scanner.Scan(
		&id, &threadID, &message.Sequence, &message.SenderType, &message.SourceChannel,
		&clientMessageID, &message.Content, &presentationJSON, &referencesJSON, &runID, &message.CreatedAt,
	); err != nil {
		return TessaMessage{}, err
	}
	message.ID, message.ThreadID = id.String(), threadID.String()
	if clientMessageID.Valid {
		message.ClientMessageID = clientMessageID.UUID.String()
	}
	if runID.Valid {
		message.RunID = runID.UUID.String()
	}
	message.Presentation = TessaMessagePresentation{}
	message.EntityReferences = []TessaEntityReference{}
	_ = json.Unmarshal(presentationJSON, &message.Presentation)
	_ = json.Unmarshal(referencesJSON, &message.EntityReferences)
	return message, nil
}

func scanTessaRun(scanner tessaScanner) (TessaRun, error) {
	var run TessaRun
	var id, threadID, triggerID uuid.UUID
	if err := scanner.Scan(
		&id, &threadID, &triggerID, &run.Status, &run.Stage, &run.ErrorCode,
		&run.FallbackUsed, &run.CreatedAt, &run.StartedAt, &run.CompletedAt, &run.CancelledAt,
	); err != nil {
		return TessaRun{}, err
	}
	run.ID, run.ThreadID, run.TriggerMessageID = id.String(), threadID.String(), triggerID.String()
	return run, nil
}

func getActiveTessaRun(ctx context.Context, querier tessaReadQuerier, clientID, threadID uuid.UUID) (*TessaRun, error) {
	run, err := scanTessaRun(querier.QueryRow(ctx, `
		SELECT id,thread_id,trigger_message_id,status,stage,error_code,fallback_used,
			created_at,started_at,completed_at,cancelled_at
		FROM tessa_runs WHERE client_id=$1 AND thread_id=$2 AND status IN ('queued','processing')
	`, clientID, threadID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load active Tessa run: %w", err)
	}
	return &run, nil
}

func (r *Repository) GetTessaBusinessSnapshot(ctx context.Context, clientID uuid.UUID) (TessaBusinessSnapshot, error) {
	var result TessaBusinessSnapshot
	if err := r.db.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(BTRIM(profile.business_name),''),client.full_name),
			COALESCE(profile.category,''),COALESCE(profile.country_code,''),
			COALESCE(profile.currency_code,''),COALESCE(profile.timezone,''),
			profile.market_configured_at IS NOT NULL,COALESCE(profile.marketplace_enabled,FALSE),
			COALESCE(profile.concurrent_booking_capacity,1)
		FROM clients client LEFT JOIN client_profiles profile ON profile.client_id=client.id
		WHERE client.id=$1
	`, clientID).Scan(
		&result.BusinessName, &result.Category, &result.CountryCode, &result.CurrencyCode,
		&result.Timezone, &result.MarketConfigured, &result.MarketplaceEnabled,
		&result.ConcurrentBookingCapacity,
	); errors.Is(err, pgx.ErrNoRows) {
		return TessaBusinessSnapshot{}, ErrNotFound
	} else if err != nil {
		return TessaBusinessSnapshot{}, fmt.Errorf("load Tessa business snapshot: %w", err)
	}
	return result, nil
}

func appendTessaEvent(
	ctx context.Context,
	execer interface {
		QueryRow(context.Context, string, ...any) pgx.Row
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	},
	clientID, threadID, messageID, runID uuid.UUID,
	eventType string,
	payload map[string]any,
) (int64, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var messageValue, runValue any
	if messageID != uuid.Nil {
		messageValue = messageID
	}
	if runID != uuid.Nil {
		runValue = runID
	}
	var sequence int64
	if err := execer.QueryRow(ctx, `
		INSERT INTO tessa_events (client_id,thread_id,message_id,run_id,event_type,payload)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb) RETURNING sequence
	`, clientID, threadID, messageValue, runValue, eventType, payloadJSON).Scan(&sequence); err != nil {
		return 0, fmt.Errorf("append Tessa event: %w", err)
	}
	if _, err := execer.Exec(ctx, `SELECT pg_notify('tellbook_tessa_events',$1)`, fmt.Sprintf("%d|%s", sequence, clientID)); err != nil {
		return 0, fmt.Errorf("notify Tessa event: %w", err)
	}
	return sequence, nil
}

func latestTessaEventCursor(ctx context.Context, querier tessaReadQuerier, clientID uuid.UUID) (string, error) {
	var sequence int64
	if err := querier.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),0) FROM tessa_events WHERE client_id=$1`, clientID).Scan(&sequence); err != nil {
		return "", err
	}
	return encodeInboxSequenceCursor(sequence), nil
}

func tessaMessageFingerprint(content string) string {
	hash := sha256.Sum256([]byte(strings.TrimSpace(content)))
	return hex.EncodeToString(hash[:])
}
