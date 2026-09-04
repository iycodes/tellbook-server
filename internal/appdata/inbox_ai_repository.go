package appdata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	aiapi "booking/go-server/shared/ai_api"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	inboxAIDraftContextVersion    = 2
	inboxAIDraftMessageLimit      = 30
	inboxAIDraftBookingLimit      = 5
	inboxAIDraftServiceLimit      = 20
	inboxAIDraftMessageCharacters = 4000
	inboxAIDraftTotalCharacters   = 48000
	inboxAIDraftNameCharacters    = 160
	inboxAIDraftLabelCharacters   = 500
	inboxAIDraftPolicyCharacters  = 800
	inboxAIDraftProviderBacklog   = 10
)

type InboxAIDraftContextSnapshot struct {
	Request               aiapi.InboxReplyDraftRequest
	ContextHash           string
	PromptInputHash       string
	LatestMessageSequence int64
	LatestMessageID       uuid.NullUUID
	InputCharacterCount   int
}

type ProviderInboxAIDraftRun struct {
	ID                    uuid.UUID
	Status                string
	Draft                 string
	NeedsProviderInput    bool
	Warnings              []aiapi.Warning
	LatestMessageSequence int64
	LatestMessageID       uuid.NullUUID
	CreatedAt             time.Time
	ProviderOutcome       string
	ErrorCode             string
}

func (r *Repository) LoadProviderInboxAIDraftContext(ctx context.Context, clientID, conversationID uuid.UUID) (InboxAIDraftContextSnapshot, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return InboxAIDraftContextSnapshot{}, fmt.Errorf("begin inbox ai context snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	request := aiapi.InboxReplyDraftRequest{GeneratedAt: time.Now().UTC()}
	var disabled bool
	var latestSequence int64
	var latestMessageID uuid.NullUUID
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(BTRIM(profile.business_name),''), client.full_name),
			COALESCE(NULLIF(BTRIM(profile.short_bio),''), NULLIF(BTRIM(profile.headline),''), ''),
			COALESCE(profile.timezone, ''),
			COALESCE(NULLIF(BTRIM(customer.full_name),''), 'Tellbook customer'),
			conversation.disabled_at IS NOT NULL,
			COALESCE(conversation.last_message_sequence,0), latest.id
		FROM inbox_conversations conversation
		INNER JOIN clients client ON client.id=conversation.client_id
		LEFT JOIN client_profiles profile ON profile.client_id=conversation.client_id
		INNER JOIN customers customer ON customer.id=conversation.customer_id
		LEFT JOIN inbox_messages latest ON latest.sequence=conversation.last_message_sequence
		WHERE conversation.id=$1 AND conversation.client_id=$2
	`, conversationID, clientID).Scan(
		&request.BusinessName, &request.BusinessSummary, &request.BusinessTimezone,
		&request.CustomerName, &disabled, &latestSequence, &latestMessageID,
	); errors.Is(err, pgx.ErrNoRows) {
		return InboxAIDraftContextSnapshot{}, ErrNotFound
	} else if err != nil {
		return InboxAIDraftContextSnapshot{}, fmt.Errorf("authorize inbox ai context: %w", err)
	}
	if disabled {
		return InboxAIDraftContextSnapshot{}, ErrInboxConversationDisabled
	}
	request.BusinessName = truncateRunes(strings.TrimSpace(request.BusinessName), inboxAIDraftNameCharacters)
	request.BusinessSummary = truncateRunes(strings.TrimSpace(request.BusinessSummary), 500)
	request.BusinessTimezone = truncateRunes(strings.TrimSpace(request.BusinessTimezone), 100)
	request.CustomerName = truncateRunes(strings.TrimSpace(request.CustomerName), inboxAIDraftNameCharacters)

	bookingRows, err := tx.Query(ctx, `
		SELECT booking.title, booking.status, booking.payment_status,
			booking.start_at, booking.end_at, booking.timezone,
			booking.fulfillment_mode,
			CASE booking.fulfillment_mode
				WHEN 'provider_location' THEN COALESCE(NULLIF(BTRIM(booking.provider_location_label),''), 'Provider location')
				WHEN 'virtual' THEN COALESCE(NULLIF(BTRIM(booking.virtual_delivery_label),''), 'Virtual appointment')
				ELSE 'Customer-provided location (exact address withheld from AI context)'
			END,
			booking.cancellation_policy_snapshot, booking.lateness_policy_snapshot
		FROM inbox_conversation_bookings link
		INNER JOIN bookings booking ON booking.id=link.booking_id
		WHERE link.conversation_id=$1 AND booking.client_id=$2
		ORDER BY
			CASE
				WHEN booking.status IN ('pending','booked','confirmed') AND booking.end_at >= NOW() THEN 0
				WHEN booking.end_at >= NOW() THEN 1
				ELSE 2
			END,
			CASE WHEN booking.end_at >= NOW() THEN booking.start_at END ASC,
			CASE WHEN booking.end_at < NOW() THEN booking.start_at END DESC,
			booking.id DESC
		LIMIT $3
	`, conversationID, clientID, inboxAIDraftBookingLimit)
	if err != nil {
		return InboxAIDraftContextSnapshot{}, fmt.Errorf("load inbox ai booking context: %w", err)
	}
	for bookingRows.Next() {
		var booking aiapi.InboxReplyDraftBooking
		if err := bookingRows.Scan(
			&booking.ServiceName, &booking.Status, &booking.PaymentStatus,
			&booking.StartsAt, &booking.EndsAt, &booking.Timezone,
			&booking.FulfillmentMode, &booking.Location,
			&booking.CancellationPolicy, &booking.LatenessPolicy,
		); err != nil {
			bookingRows.Close()
			return InboxAIDraftContextSnapshot{}, fmt.Errorf("scan inbox ai booking context: %w", err)
		}
		booking.ServiceName = truncateRunes(strings.TrimSpace(booking.ServiceName), inboxAIDraftNameCharacters)
		booking.Status = truncateRunes(strings.TrimSpace(booking.Status), 80)
		booking.PaymentStatus = truncateRunes(strings.TrimSpace(booking.PaymentStatus), 80)
		booking.Timezone = truncateRunes(strings.TrimSpace(booking.Timezone), 100)
		booking.FulfillmentMode = truncateRunes(strings.TrimSpace(booking.FulfillmentMode), 80)
		booking.Location = truncateRunes(strings.TrimSpace(booking.Location), inboxAIDraftLabelCharacters)
		booking.CancellationPolicy = truncateRunes(strings.TrimSpace(booking.CancellationPolicy), inboxAIDraftPolicyCharacters)
		booking.LatenessPolicy = truncateRunes(strings.TrimSpace(booking.LatenessPolicy), inboxAIDraftPolicyCharacters)
		request.Bookings = append(request.Bookings, booking)
	}
	if err := bookingRows.Err(); err != nil {
		bookingRows.Close()
		return InboxAIDraftContextSnapshot{}, fmt.Errorf("iterate inbox ai booking context: %w", err)
	}
	bookingRows.Close()

	serviceRows, err := tx.Query(ctx, `
		WITH recent_text AS (
			SELECT COALESCE(string_agg(LOWER(content), ' ' ORDER BY sequence), '') AS content
			FROM (
				SELECT sequence, content FROM inbox_messages
				WHERE conversation_id=$2 ORDER BY sequence DESC LIMIT 30
			) recent
		)
		SELECT service.title, service.category, service.description,
			service.duration_minutes, service.price_amount_minor, service.currency_code,
			COALESCE(profile.country_code,''), service.fulfillment_mode,
			service.cancellation_policy, service.lateness_policy
		FROM services service
		LEFT JOIN client_profiles profile ON profile.client_id=service.client_id
		CROSS JOIN recent_text
		WHERE service.client_id=$1
		  AND service.status='published' AND service.is_active AND NOT service.is_hidden
		ORDER BY CASE
			WHEN STRPOS(recent_text.content, LOWER(service.title)) > 0 THEN 0
			WHEN NULLIF(BTRIM(service.category),'') IS NOT NULL
			  AND STRPOS(recent_text.content, LOWER(service.category)) > 0 THEN 1
			ELSE 2
		END, service.sort_order, service.title, service.id
		LIMIT $3
	`, clientID, conversationID, inboxAIDraftServiceLimit)
	if err != nil {
		return InboxAIDraftContextSnapshot{}, fmt.Errorf("load inbox ai service context: %w", err)
	}
	for serviceRows.Next() {
		var service aiapi.InboxReplyDraftService
		var amountMinor int64
		var currencyCode, countryCode string
		if err := serviceRows.Scan(
			&service.ServiceName, &service.Category, &service.Description,
			&service.DurationMinutes, &amountMinor, &currencyCode, &countryCode,
			&service.FulfillmentMode, &service.CancellationPolicy, &service.LatenessPolicy,
		); err != nil {
			serviceRows.Close()
			return InboxAIDraftContextSnapshot{}, fmt.Errorf("scan inbox ai service context: %w", err)
		}
		if formatted, formatErr := formatMarketMoney(amountMinor, countryCode, currencyCode); formatErr == nil {
			service.DisplayPrice = formatted
		}
		service.ServiceName = truncateRunes(strings.TrimSpace(service.ServiceName), inboxAIDraftNameCharacters)
		service.Category = truncateRunes(strings.TrimSpace(service.Category), inboxAIDraftNameCharacters)
		service.Description = truncateRunes(strings.TrimSpace(service.Description), 240)
		service.FulfillmentMode = truncateRunes(strings.TrimSpace(service.FulfillmentMode), 80)
		service.CancellationPolicy = truncateRunes(strings.TrimSpace(service.CancellationPolicy), 400)
		service.LatenessPolicy = truncateRunes(strings.TrimSpace(service.LatenessPolicy), 400)
		request.Services = append(request.Services, service)
	}
	if err := serviceRows.Err(); err != nil {
		serviceRows.Close()
		return InboxAIDraftContextSnapshot{}, fmt.Errorf("iterate inbox ai service context: %w", err)
	}
	serviceRows.Close()

	messageRows, err := tx.Query(ctx, `
		SELECT sender_type, content
		FROM (
			SELECT sequence, sender_type, content
			FROM inbox_messages
			WHERE conversation_id=$1
			ORDER BY sequence DESC
			LIMIT $2
		) recent
		ORDER BY sequence ASC
	`, conversationID, inboxAIDraftMessageLimit)
	if err != nil {
		return InboxAIDraftContextSnapshot{}, fmt.Errorf("load inbox ai messages: %w", err)
	}
	var messages []aiapi.MessageTurn
	for messageRows.Next() {
		var senderType, content string
		if err := messageRows.Scan(&senderType, &content); err != nil {
			messageRows.Close()
			return InboxAIDraftContextSnapshot{}, fmt.Errorf("scan inbox ai message: %w", err)
		}
		role := "customer"
		if senderType == "provider" {
			role = "provider"
		} else if senderType == "system" {
			role = "system_event"
		}
		messages = append(messages, aiapi.MessageTurn{Role: role, Content: truncateRunes(content, inboxAIDraftMessageCharacters)})
	}
	if err := messageRows.Err(); err != nil {
		messageRows.Close()
		return InboxAIDraftContextSnapshot{}, fmt.Errorf("iterate inbox ai messages: %w", err)
	}
	messageRows.Close()
	request.Messages = newestMessagesWithinCharacterLimit(messages, inboxAIDraftTotalCharacters)

	promptPayload, err := json.Marshal(request)
	if err != nil {
		return InboxAIDraftContextSnapshot{}, fmt.Errorf("marshal inbox ai prompt input: %w", err)
	}
	promptHash := sha256.Sum256(promptPayload)
	stableRequest := request
	stableRequest.GeneratedAt = time.Time{}
	stablePayload, err := json.Marshal(stableRequest)
	if err != nil {
		return InboxAIDraftContextSnapshot{}, fmt.Errorf("marshal stable inbox ai context: %w", err)
	}
	contextHash := sha256.Sum256(stablePayload)
	if err := tx.Commit(ctx); err != nil {
		return InboxAIDraftContextSnapshot{}, fmt.Errorf("commit inbox ai context snapshot: %w", err)
	}
	return InboxAIDraftContextSnapshot{
		Request: request, ContextHash: hex.EncodeToString(contextHash[:]), PromptInputHash: hex.EncodeToString(promptHash[:]),
		LatestMessageSequence: latestSequence, LatestMessageID: latestMessageID,
		InputCharacterCount: utf8.RuneCount(promptPayload),
	}, nil
}

func (r *Repository) FindProviderInboxAIDraftRunByRequest(ctx context.Context, clientID, conversationID, requestID uuid.UUID) (*ProviderInboxAIDraftRun, error) {
	var run ProviderInboxAIDraftRun
	var warningsJSON []byte
	err := r.db.QueryRow(ctx, `
		SELECT run.id, run.status, run.output_draft, COALESCE(run.needs_provider_input,false),
			run.warnings, run.latest_message_sequence, latest.id, run.created_at, run.provider_outcome,
			run.error_code
		FROM inbox_ai_runs run
		LEFT JOIN inbox_messages latest
			ON latest.conversation_id=run.conversation_id AND latest.sequence=run.latest_message_sequence
		WHERE run.client_id=$1 AND run.conversation_id=$2 AND run.request_id=$3
	`, clientID, conversationID, requestID).Scan(
		&run.ID, &run.Status, &run.Draft, &run.NeedsProviderInput, &warningsJSON,
		&run.LatestMessageSequence, &run.LatestMessageID, &run.CreatedAt, &run.ProviderOutcome,
		&run.ErrorCode,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load inbox ai draft run: %w", err)
	}
	if err := json.Unmarshal(warningsJSON, &run.Warnings); err != nil {
		return nil, fmt.Errorf("decode inbox ai draft warnings: %w", err)
	}
	return &run, nil
}

func (r *Repository) GetProviderInboxAIDraftRun(
	ctx context.Context,
	clientID, conversationID, runID uuid.UUID,
) (*ProviderInboxAIDraftRun, error) {
	var run ProviderInboxAIDraftRun
	var warningsJSON []byte
	err := r.db.QueryRow(ctx, `
		SELECT run.id,run.status,run.output_draft,COALESCE(run.needs_provider_input,false),
			run.warnings,run.latest_message_sequence,latest.id,run.created_at,run.provider_outcome,
			run.error_code
		FROM inbox_ai_runs run
		LEFT JOIN inbox_messages latest
			ON latest.conversation_id=run.conversation_id AND latest.sequence=run.latest_message_sequence
		WHERE run.id=$1 AND run.client_id=$2 AND run.conversation_id=$3 AND run.mode='manual'
	`, runID, clientID, conversationID).Scan(
		&run.ID, &run.Status, &run.Draft, &run.NeedsProviderInput, &warningsJSON,
		&run.LatestMessageSequence, &run.LatestMessageID, &run.CreatedAt, &run.ProviderOutcome,
		&run.ErrorCode,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load inbox ai draft run: %w", err)
	}
	if err := json.Unmarshal(warningsJSON, &run.Warnings); err != nil {
		return nil, fmt.Errorf("decode inbox ai draft warnings: %w", err)
	}
	return &run, nil
}

func (r *Repository) StartProviderInboxAIDraftRun(
	ctx context.Context, clientID, conversationID, requestID uuid.UUID,
	modelProvider, modelName, modelConfigHash string, snapshot InboxAIDraftContextSnapshot,
) (uuid.UUID, error) {
	inputSnapshot, err := json.Marshal(snapshot.Request)
	if err != nil {
		return uuid.Nil, fmt.Errorf("encode inbox ai draft input: %w", err)
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("begin inbox ai draft enqueue: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended('inbox-ai-draft-backlog:'||$1::uuid::text,0))
	`, clientID); err != nil {
		return uuid.Nil, fmt.Errorf("lock inbox ai draft backlog: %w", err)
	}
	var backlog int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM inbox_ai_runs
		WHERE client_id=$1 AND mode='manual' AND status IN ('queued','processing')
	`, clientID).Scan(&backlog); err != nil {
		return uuid.Nil, fmt.Errorf("load inbox ai draft backlog: %w", err)
	}
	if backlog >= inboxAIDraftProviderBacklog {
		return uuid.Nil, ErrInboxAIServiceUnavailable
	}
	runID := uuid.New()
	err = tx.QueryRow(ctx, `
		INSERT INTO inbox_ai_runs (
			id, conversation_id, client_id, requested_by, request_id, status,
			model_provider, model_name, model_config_hash, prompt_version,
			context_version, context_hash, prompt_input_hash,
			latest_message_sequence, input_character_count, provider_outcome,
			input_snapshot, available_at
		)
		SELECT $1, conversation.id, conversation.client_id, $2, $4, 'queued',
			$5, $6, $7, $8, $9, $10, $11, $12, $13, 'pending', $14, NOW()
		FROM inbox_conversations conversation
		WHERE conversation.id=$3 AND conversation.client_id=$2
		ON CONFLICT (client_id, conversation_id, request_id) DO UPDATE SET
			status='queued', model_provider=EXCLUDED.model_provider, model_name=EXCLUDED.model_name,
			model_config_hash=EXCLUDED.model_config_hash, prompt_version=EXCLUDED.prompt_version,
			context_version=EXCLUDED.context_version, context_hash=EXCLUDED.context_hash,
			prompt_input_hash=EXCLUDED.prompt_input_hash,
			latest_message_sequence=EXCLUDED.latest_message_sequence,
			input_character_count=EXCLUDED.input_character_count,
			input_snapshot=EXCLUDED.input_snapshot, available_at=NOW(), attempt_count=0,
			lease_owner='', lease_expires_at=NULL,
			output_draft='', warnings='[]'::jsonb, error_code='', latency_ms=NULL,
			completed_at=NULL, needs_provider_input=NULL, provider_outcome='pending',
			provider_message_id=NULL, provider_final_content_hash=NULL, provider_outcome_at=NULL,
			created_at=NOW()
		WHERE inbox_ai_runs.status='failed'
		RETURNING inbox_ai_runs.id
	`, runID, clientID, conversationID, requestID, modelProvider, modelName, modelConfigHash,
		aiapi.InboxReplyDraftPromptVersion, inboxAIDraftContextVersion, snapshot.ContextHash,
		snapshot.PromptInputHash, snapshot.LatestMessageSequence, snapshot.InputCharacterCount,
		inputSnapshot,
	).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrInboxAIDraftInProgress
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("start inbox ai draft run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("commit inbox ai draft enqueue: %w", err)
	}
	return runID, nil
}

func (r *Repository) CompleteProviderInboxAIDraftRun(
	ctx context.Context,
	runID uuid.UUID,
	workerID string,
	attemptCount int,
	response aiapi.InboxReplyDraftResponse,
	latency time.Duration,
) error {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || attemptCount < 1 {
		return errors.New("complete inbox ai draft run: invalid lease fence")
	}
	if response.Warnings == nil {
		response.Warnings = []aiapi.Warning{}
	}
	warnings, err := json.Marshal(response.Warnings)
	if err != nil {
		return fmt.Errorf("marshal inbox ai warnings: %w", err)
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE inbox_ai_runs
		SET status='completed', output_draft=$2, warnings=$3, needs_provider_input=$4,
			provider_outcome='pending', latency_ms=$5, completed_at=NOW(),
			lease_owner='',lease_expires_at=NULL
		WHERE id=$1 AND status='processing' AND lease_owner=$6 AND attempt_count=$7
	`, runID, response.Draft, warnings, response.NeedsProviderInput, durationMilliseconds(latency), workerID, attemptCount)
	if err != nil {
		return fmt.Errorf("complete inbox ai draft run: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("complete inbox ai draft run: run is not active")
	}
	return nil
}

func (r *Repository) FailProviderInboxAIDraftRun(
	ctx context.Context,
	runID uuid.UUID,
	workerID string,
	attemptCount int,
	errorCode string,
	latency time.Duration,
) error {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || attemptCount < 1 {
		return errors.New("fail inbox ai draft run: invalid lease fence")
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE inbox_ai_runs
		SET status='failed', error_code=$2, latency_ms=$3, completed_at=NOW(), provider_outcome='unavailable',
			lease_owner='',lease_expires_at=NULL
		WHERE id=$1 AND status='processing' AND lease_owner=$4 AND attempt_count=$5
	`, runID, errorCode, durationMilliseconds(latency), workerID, attemptCount)
	if err != nil {
		return fmt.Errorf("fail inbox ai draft run: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("fail inbox ai draft run: run is not active")
	}
	return nil
}

func (r *Repository) DiscardProviderInboxAIDraftRun(ctx context.Context, clientID, conversationID, runID uuid.UUID, reason string) error {
	if reason != "discarded" && reason != "stale" {
		return ErrInboxAIDraftInvalid
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE inbox_ai_runs SET provider_outcome=$4, provider_outcome_at=NOW()
		WHERE id=$1 AND client_id=$2 AND conversation_id=$3 AND status='completed'
		  AND provider_outcome IN ('pending',$4)
	`, runID, clientID, conversationID, reason)
	if err != nil {
		return fmt.Errorf("discard inbox ai draft run: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrInboxAIDraftInvalid
	}
	return nil
}

func truncateRunes(value string, limit int) string {
	if limit < 1 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func newestMessagesWithinCharacterLimit(messages []aiapi.MessageTurn, limit int) []aiapi.MessageTurn {
	if limit <= 0 {
		return nil
	}
	start, used := len(messages), 0
	for start > 0 {
		length := utf8.RuneCountInString(messages[start-1].Content)
		if used+length > limit && start < len(messages) {
			break
		}
		if length > limit-used {
			messages[start-1].Content = truncateRunes(messages[start-1].Content, limit-used)
			start--
			break
		}
		used += length
		start--
	}
	return messages[start:]
}

func durationMilliseconds(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	return value.Milliseconds()
}
