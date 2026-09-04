package appdata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	aisvc "booking/go-server/internal/ai"
	"booking/go-server/internal/aierror"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	inboxAISemiPilotTurnContextVersion = 1
	inboxAISemiPilotPromptVersion      = 1
	inboxAISemiPilotPollInterval       = 350 * time.Millisecond
	inboxAISemiPilotLeaseDuration      = 3 * time.Minute
)

var errInboxAISemiPilotTurnStale = errors.New("semi-pilot turn is stale")

var (
	semiPilotEmailPattern         = regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`)
	semiPilotPhonePattern         = regexp.MustCompile(`(?i)(?:\+?234|0)(?:[\s()\-]*\d){10}\b`)
	semiPilotAddressIntroPattern  = regexp.MustCompile(`(?i)\b(?:my\s+address\s+is|address\s+is|i\s+live\s+at|i\s+stay\s+at|deliver\s+to|come\s+to|located\s+at|find\s+me\s+at|i(?:'m|\s+am)\s+at)\b[\s,:\-]*[^.\n;!?]{1,220}`)
	semiPilotAddressUnitPattern   = regexp.MustCompile(`(?i)\b(?:no\.?|number|house|flat|plot|block|unit|suite|shop|office|apartment|apt|km)\s*[A-Z0-9][^\n;!?]{1,180}`)
	semiPilotStreetAddressPattern = regexp.MustCompile(`(?i)\b\d{1,5}[A-Z]?\s+[\pL][\pL\d' .\-]{1,100}\s(?:street|st|road|rd|avenue|ave|close|crescent|drive|lane|way|boulevard|expressway|estate|layout|quarters)\b[^\n,;!?]{0,100}`)
	semiPilotLandmarkPattern      = regexp.MustCompile(`(?i)\b(?:opposite|beside|behind|adjacent\s+to|next\s+to|close\s+to|near)\s+[^.\n;!?]{2,180}`)
)

type inboxAISemiPilotDecider interface {
	GenerateSemiPilotTurnDecision(
		context.Context,
		aisvc.SemiPilotTurnInput,
	) (aisvc.SemiPilotTurnDecision, error)
}

type inboxAIAutopilotDecider interface {
	GenerateAutopilotTurnDecision(
		context.Context,
		aisvc.SemiPilotTurnInput,
	) (aisvc.SemiPilotTurnDecision, error)
}

type InboxAISemiPilotWorkerConfig struct {
	ModelProvider   string
	ModelName       string
	ModelConfigHash string
	MaxConcurrency  int
	PollInterval    time.Duration
	LeaseDuration   time.Duration
}

type InboxAISemiPilotWorker struct {
	repo         *Repository
	decider      inboxAISemiPilotDecider
	limiter      *InboxAIGenerationLimiter
	logger       *slog.Logger
	config       InboxAISemiPilotWorkerConfig
	workerPrefix string
}

func NewInboxAISemiPilotWorker(
	repo *Repository,
	decider inboxAISemiPilotDecider,
	limiter *InboxAIGenerationLimiter,
	logger *slog.Logger,
	config InboxAISemiPilotWorkerConfig,
) (*InboxAISemiPilotWorker, error) {
	if repo == nil || repo.db == nil {
		return nil, errors.New("semi-pilot repository is required")
	}
	if decider == nil {
		return nil, errors.New("semi-pilot decision service is required")
	}
	config.ModelProvider = strings.TrimSpace(config.ModelProvider)
	config.ModelName = strings.TrimSpace(config.ModelName)
	config.ModelConfigHash = strings.TrimSpace(config.ModelConfigHash)
	if config.ModelProvider == "" || config.ModelName == "" ||
		!isSHA256Hex(config.ModelConfigHash) {
		return nil, errors.New("semi-pilot model identity is invalid")
	}
	if config.MaxConcurrency < 1 {
		config.MaxConcurrency = 1
	}
	if config.MaxConcurrency > 32 {
		config.MaxConcurrency = 32
	}
	if config.PollInterval <= 0 {
		config.PollInterval = inboxAISemiPilotPollInterval
	}
	if config.LeaseDuration <= 0 {
		config.LeaseDuration = inboxAISemiPilotLeaseDuration
	}
	if logger == nil {
		logger = slog.Default()
	}
	if limiter == nil {
		limiter = NewInboxAIGenerationLimiter(config.MaxConcurrency)
	}
	return &InboxAISemiPilotWorker{
		repo: repo, decider: decider, limiter: limiter, logger: logger, config: config,
		workerPrefix: "inbox-semi-pilot-" + uuid.NewString(),
	}, nil
}

func (worker *InboxAISemiPilotWorker) Start(ctx context.Context, wakes ...<-chan struct{}) {
	if worker == nil {
		return
	}
	for index := 0; index < worker.config.MaxConcurrency; index++ {
		var wake <-chan struct{}
		if len(wakes) > 0 {
			wake = wakes[index%len(wakes)]
		}
		go worker.run(ctx, fmt.Sprintf("%s-%d", worker.workerPrefix, index+1), wake)
	}
}

func (worker *InboxAISemiPilotWorker) run(ctx context.Context, workerID string, wake <-chan struct{}) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-timer.C:
		}
		for {
			processed, err := worker.ProcessOne(ctx, workerID)
			if err != nil && !errors.Is(err, context.Canceled) {
				worker.logger.Warn("semi-pilot turn failed", "worker_id", workerID, "error", err)
			}
			if !processed || ctx.Err() != nil {
				break
			}
		}
		timer.Reset(worker.nextWakeDelay(ctx))
	}
}

func (worker *InboxAISemiPilotWorker) nextWakeDelay(ctx context.Context) time.Duration {
	fallback := worker.config.PollInterval
	if fallback <= 0 {
		fallback = 25 * time.Second
	}
	fallback = time.Duration(float64(fallback) * (0.8 + rand.Float64()*0.4))
	var next time.Time
	err := worker.repo.db.QueryRow(ctx, `
		SELECT COALESCE(MIN(
			CASE WHEN status='processing' THEN lease_expires_at ELSE available_at END
		),NOW()+($1::bigint*INTERVAL '1 millisecond'))
		FROM inbox_ai_turn_jobs
		WHERE (status='queued' AND attempt_count<max_attempts) OR status='processing'
	`, fallback.Milliseconds()).Scan(&next)
	if err != nil {
		return fallback
	}
	delay := time.Until(next)
	if delay < 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	if delay > fallback {
		return fallback
	}
	return delay
}

type inboxAISemiPilotTurnJob struct {
	ID                     uuid.UUID
	ConversationID         uuid.UUID
	ClientID               uuid.UUID
	MarketplaceCustomerID  uuid.UUID
	SessionID              uuid.UUID
	TriggerMessageID       uuid.UUID
	TriggerMessageSequence int64
	AttemptCount           int
	MaxAttempts            int
	TurnNumber             int
}

func (worker *InboxAISemiPilotWorker) ProcessOne(
	ctx context.Context,
	workerIDs ...string,
) (bool, error) {
	workerID := worker.workerPrefix + "-manual"
	if len(workerIDs) > 0 && strings.TrimSpace(workerIDs[0]) != "" {
		workerID = strings.TrimSpace(workerIDs[0])
	}
	job, err := worker.claim(ctx, workerID)
	if err != nil || job.ID == uuid.Nil {
		return false, err
	}
	if err := worker.process(ctx, workerID, job); err != nil {
		if errors.Is(err, errInboxAISemiPilotTurnStale) {
			if fenceErr := worker.fenceStaleRun(ctx, job.ID); fenceErr != nil {
				return true, fenceErr
			}
			return true, nil
		}
		if retryErr := worker.retryOrFail(ctx, workerID, job, err); retryErr != nil {
			return true, fmt.Errorf("process semi-pilot turn: %v; persist failure: %w", err, retryErr)
		}
		return true, err
	}
	return true, nil
}

func (worker *InboxAISemiPilotWorker) fenceStaleRun(
	ctx context.Context,
	jobID uuid.UUID,
) error {
	if _, err := worker.repo.db.Exec(ctx, `
		UPDATE inbox_ai_runs
		SET status='failed', output_draft='', error_code='stale',
			latency_ms=GREATEST(0,EXTRACT(EPOCH FROM (NOW()-created_at))*1000)::int,
			completed_at=NOW()
		WHERE turn_job_id=$1 AND status='running'
	`, jobID); err != nil {
		return fmt.Errorf("fence stale semi-pilot run: %w", err)
	}
	return nil
}

func (worker *InboxAISemiPilotWorker) claim(
	ctx context.Context,
	workerID string,
) (inboxAISemiPilotTurnJob, error) {
	tx, err := worker.repo.db.Begin(ctx)
	if err != nil {
		return inboxAISemiPilotTurnJob{}, fmt.Errorf("begin semi-pilot turn claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var job inboxAISemiPilotTurnJob
	if err := tx.QueryRow(ctx, `
		WITH candidates AS (
			SELECT queued.id
			FROM inbox_ai_turn_jobs queued
			WHERE queued.available_at<=NOW()
			  AND (
				(queued.status='queued' AND queued.attempt_count<queued.max_attempts)
				OR (queued.status='processing' AND queued.lease_expires_at<NOW())
			  )
			  AND NOT EXISTS (
				SELECT 1 FROM inbox_ai_turn_jobs active
				WHERE active.client_id=queued.client_id AND active.status='processing'
				  AND active.lease_expires_at>=NOW() AND active.id<>queued.id
			  )
			ORDER BY queued.available_at,queued.created_at,queued.id
			FOR UPDATE OF queued SKIP LOCKED LIMIT 1
		)
		SELECT job.id,job.conversation_id,job.client_id,job.marketplace_customer_id,job.session_id,
			job.trigger_message_id,job.trigger_message_sequence,job.attempt_count,job.max_attempts,job.turn_number
		FROM inbox_ai_turn_jobs job JOIN candidates ON candidates.id=job.id
	`).Scan(
		&job.ID, &job.ConversationID, &job.ClientID, &job.MarketplaceCustomerID,
		&job.SessionID, &job.TriggerMessageID, &job.TriggerMessageSequence,
		&job.AttemptCount, &job.MaxAttempts, &job.TurnNumber,
	); errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return inboxAISemiPilotTurnJob{}, fmt.Errorf("commit empty semi-pilot claim: %w", err)
		}
		return inboxAISemiPilotTurnJob{}, nil
	} else if err != nil {
		return inboxAISemiPilotTurnJob{}, fmt.Errorf("select semi-pilot turn: %w", err)
	}
	if job.AttemptCount < job.MaxAttempts {
		job.AttemptCount++
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs
		SET status='processing', attempt_count=$2, lease_owner=$3,
			lease_expires_at=NOW()+($4::bigint * INTERVAL '1 millisecond'),
			error_code='', updated_at=NOW()
		WHERE id=$1
	`, job.ID, job.AttemptCount, workerID, worker.config.LeaseDuration.Milliseconds()); err != nil {
		return inboxAISemiPilotTurnJob{}, fmt.Errorf("claim semi-pilot turn: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return inboxAISemiPilotTurnJob{}, fmt.Errorf("commit semi-pilot turn claim: %w", err)
	}
	return job, nil
}

type inboxAISemiPilotPreparedTurn struct {
	Job                     inboxAISemiPilotTurnJob
	RunID                   uuid.UUID
	Mode                    string
	CurrentState            string
	CustomerMessage         string
	SelectedServiceID       string
	ProviderTimezone        string
	RecentMessages          []aisvc.SemiPilotContextMessage
	PolicyRevision          int64
	ControlRevision         int64
	TurnNumber              int
	BudgetExhausted         bool
	NeedsDisclosure         bool
	InactivityTimeoutMinute int
	ToolResults             []aisvc.SemiPilotContextToolResult
	ToolCallCount           int
	BookingLinkActionID     uuid.UUID
}

func (worker *InboxAISemiPilotWorker) prepare(
	ctx context.Context,
	workerID string,
	job inboxAISemiPilotTurnJob,
) (inboxAISemiPilotPreparedTurn, error) {
	tx, err := worker.repo.db.Begin(ctx)
	if err != nil {
		return inboxAISemiPilotPreparedTurn{}, fmt.Errorf("begin semi-pilot turn preparation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	turn := inboxAISemiPilotPreparedTurn{Job: job}
	var latestSequence int64
	var disabled bool
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(last_message_sequence,0), disabled_at IS NOT NULL
		FROM inbox_conversations
		WHERE id=$1 AND client_id=$2 AND marketplace_customer_id=$3
		FOR UPDATE
	`, job.ConversationID, job.ClientID, job.MarketplaceCustomerID).Scan(
		&latestSequence, &disabled,
	); errors.Is(err, pgx.ErrNoRows) {
		return turn, worker.cancelPreparedTurn(ctx, tx, job.ID, workerID, "conversation_missing")
	} else if err != nil {
		return turn, fmt.Errorf("lock semi-pilot conversation: %w", err)
	}
	if disabled || latestSequence != job.TriggerMessageSequence {
		return turn, worker.cancelPreparedTurn(ctx, tx, job.ID, workerID, "message_superseded")
	}
	policy, err := loadInboxAIPolicyRecord(ctx, tx, job.ClientID)
	if err != nil {
		return turn, err
	}
	control, err := loadInboxAIConversationControl(
		ctx, tx, job.ClientID, job.ConversationID, policy,
		worker.repo.inboxAIAutomationAvailable(job.ClientID),
	)
	if err != nil {
		return turn, err
	}
	if (control.EffectiveMode != InboxAIModeSemiPilot && control.EffectiveMode != InboxAIModeAutopilot) ||
		control.State != "active" ||
		policy.paused || len(policy.enabledServiceIDs) == 0 {
		return turn, worker.cancelPreparedTurn(ctx, tx, job.ID, workerID, "automation_fenced")
	}
	var selectedService uuid.NullUUID
	var lastAIMessage uuid.NullUUID
	var sessionMode, sessionState string
	var sessionPolicyRevision, sessionControlRevision int64
	var turnCount, maxTurns int
	var expiresAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT mode, state, selected_service_id, last_ai_message_id,
			policy_revision, control_revision, turn_count, max_turns_snapshot, expires_at
		FROM inbox_ai_sessions WHERE id=$1 AND conversation_id=$2 FOR UPDATE
	`, job.SessionID, job.ConversationID).Scan(
		&sessionMode, &sessionState, &selectedService, &lastAIMessage,
		&sessionPolicyRevision, &sessionControlRevision, &turnCount, &maxTurns, &expiresAt,
	); errors.Is(err, pgx.ErrNoRows) {
		return turn, worker.cancelPreparedTurn(ctx, tx, job.ID, workerID, "session_missing")
	} else if err != nil {
		return turn, fmt.Errorf("lock semi-pilot session: %w", err)
	}
	if sessionMode != control.EffectiveMode || isTerminalInboxAISessionState(sessionState) ||
		sessionPolicyRevision != policy.revision || sessionControlRevision != control.Revision ||
		!expiresAt.After(time.Now()) {
		return turn, worker.cancelPreparedTurn(ctx, tx, job.ID, workerID, "session_fenced")
	}
	// Every command that can fence a turn locks conversation/session before the
	// job. Keep the worker on that same order to avoid takeover/completion
	// deadlocks.
	var status, leaseOwner string
	if err := tx.QueryRow(ctx, `
		SELECT status, lease_owner FROM inbox_ai_turn_jobs WHERE id=$1 FOR UPDATE
	`, job.ID).Scan(&status, &leaseOwner); errors.Is(err, pgx.ErrNoRows) {
		return turn, errInboxAISemiPilotTurnStale
	} else if err != nil {
		return turn, fmt.Errorf("lock semi-pilot turn job: %w", err)
	}
	if status != "processing" || leaseOwner != workerID {
		return turn, errInboxAISemiPilotTurnStale
	}
	turn.BudgetExhausted = job.TurnNumber == 0 && turnCount >= maxTurns
	if job.TurnNumber > 0 {
		turnCount = job.TurnNumber
	} else if !turn.BudgetExhausted {
		turnCount++
		if _, err := tx.Exec(ctx, `
			UPDATE inbox_ai_sessions
			SET turn_count=$2, revision=revision+1, updated_at=NOW()
			WHERE id=$1
		`, job.SessionID, turnCount); err != nil {
			return turn, fmt.Errorf("reserve semi-pilot turn budget: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE inbox_ai_turn_jobs SET turn_number=$2, updated_at=NOW() WHERE id=$1
		`, job.ID, turnCount); err != nil {
			return turn, fmt.Errorf("record semi-pilot turn number: %w", err)
		}
		turn.Job.TurnNumber = turnCount
	}
	turn.TurnNumber = turnCount
	turn.CurrentState = sessionState
	turn.Mode = sessionMode
	turn.PolicyRevision = policy.revision
	turn.ControlRevision = control.Revision
	turn.NeedsDisclosure = !lastAIMessage.Valid
	turn.InactivityTimeoutMinute = policy.inactivityTimeoutMinutes
	if selectedService.Valid {
		turn.SelectedServiceID = selectedService.UUID.String()
	}
	if err := tx.QueryRow(ctx, `
		SELECT content FROM inbox_messages
		WHERE id=$1 AND conversation_id=$2 AND sender_type='marketplace_customer'
		  AND sequence=$3
	`, job.TriggerMessageID, job.ConversationID, job.TriggerMessageSequence).Scan(
		&turn.CustomerMessage,
	); errors.Is(err, pgx.ErrNoRows) {
		return turn, worker.cancelPreparedTurn(ctx, tx, job.ID, workerID, "trigger_missing")
	} else if err != nil {
		return turn, fmt.Errorf("load semi-pilot trigger message: %w", err)
	}
	turn.CustomerMessage = redactSemiPilotCustomerText(turn.CustomerMessage)
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(timezone,''),'Africa/Lagos')
		FROM client_profiles WHERE client_id=$1
	`, job.ClientID).Scan(&turn.ProviderTimezone); err != nil {
		return turn, fmt.Errorf("load semi-pilot provider timezone: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT sender_type, content FROM (
			SELECT sender_type, content, sequence
			FROM inbox_messages
			WHERE conversation_id=$1 AND sequence<$2
			ORDER BY sequence DESC LIMIT 12
		) recent ORDER BY sequence
	`, job.ConversationID, job.TriggerMessageSequence)
	if err != nil {
		return turn, fmt.Errorf("load semi-pilot recent messages: %w", err)
	}
	turn.RecentMessages = make([]aisvc.SemiPilotContextMessage, 0, 12)
	for rows.Next() {
		var sender, content string
		if err := rows.Scan(&sender, &content); err != nil {
			rows.Close()
			return turn, fmt.Errorf("scan semi-pilot recent message: %w", err)
		}
		role := sender
		content = redactSemiPilotCustomerText(content)
		if sender == "marketplace_customer" {
			role = "customer"
		}
		turn.RecentMessages = append(turn.RecentMessages, aisvc.SemiPilotContextMessage{
			Role: role, Content: truncateRunes(content, 2000),
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return turn, fmt.Errorf("iterate semi-pilot recent messages: %w", err)
	}
	rows.Close()
	rows, err = tx.Query(ctx, `
		SELECT id, action_name, status, safe_result
		FROM inbox_ai_actions
		WHERE turn_job_id=$1
		ORDER BY started_at, id
	`, job.ID)
	if err != nil {
		return turn, fmt.Errorf("load semi-pilot turn actions: %w", err)
	}
	turn.ToolResults = make([]aisvc.SemiPilotContextToolResult, 0, aisvc.SemiPilotMaxToolCallsPerTurn)
	for rows.Next() {
		var actionID uuid.UUID
		var actionName, actionStatus string
		var result json.RawMessage
		if err := rows.Scan(&actionID, &actionName, &actionStatus, &result); err != nil {
			rows.Close()
			return turn, fmt.Errorf("scan semi-pilot turn action: %w", err)
		}
		turn.ToolCallCount++
		if turn.ToolCallCount > aisvc.SemiPilotMaxToolCallsPerTurn {
			rows.Close()
			return turn, errors.New("semi-pilot persisted tool-call budget exceeded")
		}
		if actionStatus != "succeeded" {
			continue
		}
		if !validPersistedAutomatedToolResult(sessionMode, actionName, result) {
			rows.Close()
			return turn, errors.New("semi-pilot persisted tool result is invalid")
		}
		turn.ToolResults = append(turn.ToolResults, aisvc.SemiPilotContextToolResult{
			Name: actionName, Result: result,
		})
		if actionName == "get_booking_link" {
			turn.BookingLinkActionID = actionID
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return turn, fmt.Errorf("iterate semi-pilot turn actions: %w", err)
	}
	rows.Close()

	input := aisvc.SemiPilotTurnInput{
		CurrentState: turn.CurrentState, CustomerMessage: truncateRunes(turn.CustomerMessage, 2000),
		SelectedServiceID: turn.SelectedServiceID, ProviderTimezone: turn.ProviderTimezone,
		RecentMessages: turn.RecentMessages, ToolResults: turn.ToolResults,
	}
	encodedInput, _ := json.Marshal(input)
	inputHash := sha256.Sum256(encodedInput)
	turn.RunID = uuid.New()
	if err := tx.QueryRow(ctx, `
		INSERT INTO inbox_ai_runs (
			id, conversation_id, client_id, requested_by, request_id, mode, trigger_type,
			status, model_provider, model_name, model_config_hash, prompt_version,
			context_version, context_hash, prompt_input_hash, latest_message_sequence,
			input_character_count, provider_outcome, turn_job_id
		) VALUES (
				$1,$2,$3,$3,$4,$13,'customer_message','running',$5,$6,$7,$8,
			$9,$10,$10,$11,$12,'unavailable',$4
		)
		ON CONFLICT (turn_job_id) WHERE turn_job_id IS NOT NULL DO UPDATE SET
			status='running', model_provider=EXCLUDED.model_provider,
			model_name=EXCLUDED.model_name, model_config_hash=EXCLUDED.model_config_hash,
			context_hash=EXCLUDED.context_hash, prompt_input_hash=EXCLUDED.prompt_input_hash,
			latest_message_sequence=EXCLUDED.latest_message_sequence,
			input_character_count=EXCLUDED.input_character_count,
			output_draft='', structured_decision='{}'::jsonb, tool_call_count=0,
			resulting_message_id=NULL, error_code='', latency_ms=NULL, completed_at=NULL,
			created_at=NOW()
		RETURNING id
	`, turn.RunID, job.ConversationID, job.ClientID, job.ID,
		worker.config.ModelProvider, worker.config.ModelName, worker.config.ModelConfigHash,
		inboxAISemiPilotPromptVersion, inboxAISemiPilotTurnContextVersion,
		hex.EncodeToString(inputHash[:]), job.TriggerMessageSequence,
		utf8.RuneCount(encodedInput), sessionMode,
	).Scan(&turn.RunID); err != nil {
		return turn, fmt.Errorf("start semi-pilot run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return turn, fmt.Errorf("commit semi-pilot turn preparation: %w", err)
	}
	return turn, nil
}

func validPersistedAutomatedToolResult(mode, name string, result json.RawMessage) bool {
	if len(result) == 0 || len(result) > 4*1024 || !json.Valid(result) {
		return false
	}
	switch name {
	case "list_relevant_services", "get_service_details", "get_booking_link":
		return mode == InboxAIModeSemiPilot || name != "get_booking_link"
	default:
		return false
	}
}

func redactSemiPilotCustomerText(value string) string {
	value = semiPilotEmailPattern.ReplaceAllString(value, "[email redacted]")
	value = semiPilotPhonePattern.ReplaceAllString(value, "[phone redacted]")
	value = semiPilotAddressIntroPattern.ReplaceAllString(value, "[address redacted]")
	value = semiPilotAddressUnitPattern.ReplaceAllString(value, "[address redacted]")
	value = semiPilotStreetAddressPattern.ReplaceAllString(value, "[address redacted]")
	value = semiPilotLandmarkPattern.ReplaceAllString(value, "[address redacted]")
	return strings.TrimSpace(value)
}

func (worker *InboxAISemiPilotWorker) cancelPreparedTurn(
	ctx context.Context,
	tx pgx.Tx,
	jobID uuid.UUID,
	workerID, code string,
) error {
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs
		SET status='cancelled', error_code=$3, lease_owner='', lease_expires_at=NULL,
			completed_at=NOW(), updated_at=NOW()
		WHERE id=$1 AND status='processing' AND lease_owner=$2
	`, jobID, workerID, code); err != nil {
		return fmt.Errorf("cancel stale semi-pilot turn: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit stale semi-pilot turn: %w", err)
	}
	return errInboxAISemiPilotTurnStale
}

func (worker *InboxAISemiPilotWorker) process(
	ctx context.Context,
	workerID string,
	job inboxAISemiPilotTurnJob,
) error {
	turn, err := worker.prepare(ctx, workerID, job)
	if err != nil {
		return err
	}
	startedAt := time.Now()
	if turn.BudgetExhausted {
		return worker.repo.completeInboxAISemiPilotTurn(ctx, completeInboxAISemiPilotTurnCommand{
			Turn: turn, WorkerID: workerID, Decision: safeSemiPilotHandoffDecision(
				"I’ve reached the limit for this automated chat, so I’ll hand this over to the provider.",
				"model_or_tool_failure",
			), Latency: time.Since(startedAt),
		})
	}
	if job.AttemptCount >= job.MaxAttempts {
		return worker.repo.completeInboxAISemiPilotTurn(ctx, completeInboxAISemiPilotTurnCommand{
			Turn: turn, WorkerID: workerID, Decision: safeSemiPilotHandoffDecision(
				"I’m unable to finish this automatically right now, so I’ll hand this over to the provider.",
				"model_or_tool_failure",
			), Latency: time.Since(startedAt),
		})
	}
	slot, err := worker.limiter.TryAcquire(ctx)
	if err != nil {
		return err
	}
	if slot == nil {
		return worker.deferForCapacity(ctx, workerID, job.ID)
	}
	defer slot.Release()

	input := aisvc.SemiPilotTurnInput{
		CurrentState: turn.CurrentState, CustomerMessage: truncateRunes(turn.CustomerMessage, 2000),
		SelectedServiceID: turn.SelectedServiceID, ProviderTimezone: turn.ProviderTimezone,
		RecentMessages: turn.RecentMessages,
		ToolResults:    append([]aisvc.SemiPilotContextToolResult(nil), turn.ToolResults...),
	}
	var finalDecision aisvc.SemiPilotTurnDecision
	bookingLinkActionID := turn.BookingLinkActionID
	toolCount := turn.ToolCallCount
	for {
		if err := worker.renewLease(ctx, workerID, job.ID); err != nil {
			return err
		}
		decision, err := worker.generateWithLeaseHeartbeat(ctx, workerID, job.ID, turn.Mode, input)
		if err != nil {
			if errors.Is(err, context.Canceled) ||
				(errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil) {
				return err
			}
			if shouldRetryInboxAIGeneration(err, job.AttemptCount, job.MaxAttempts) {
				return fmt.Errorf("generate semi-pilot decision: %w", err)
			}
			finalDecision = safeSemiPilotHandoffDecision(
				"I’m unable to finish this automatically right now, so I’ll hand it over to the provider.",
				"model_or_tool_failure",
			)
			break
		}
		if decision.ToolCall == nil {
			if turn.Mode == InboxAIModeAutopilot {
				finalDecision = normalizeAutopilotFinalDecision(decision, input.CurrentState)
			} else {
				finalDecision = normalizeSemiPilotFinalDecision(
					decision, input.CurrentState, bookingLinkActionID != uuid.Nil,
				)
			}
			break
		}
		if toolCount >= aisvc.SemiPilotMaxToolCallsPerTurn {
			finalDecision = safeSemiPilotHandoffDecision(
				"I need the provider to take this from here.", "model_or_tool_failure",
			)
			break
		}
		toolCount++
		if err := worker.renewLease(ctx, workerID, job.ID); err != nil {
			return err
		}
		actionResult, err := worker.executeTool(ctx, turn, *decision.ToolCall, toolCount)
		if errors.Is(err, ErrInboxAIControlBlocked) {
			return worker.cancel(ctx, workerID, job.ID, "tool_fenced")
		}
		if err != nil {
			reason := "model_or_tool_failure"
			message := "I couldn’t complete that check, so I’ll hand this over to the provider."
			if errors.Is(err, ErrInboxAIServiceUnavailable) || errors.Is(err, ErrNotFound) {
				reason = "service_not_found"
				message = "I couldn’t find a matching bookable service, so I’ll hand this over to the provider."
			}
			finalDecision = safeSemiPilotHandoffDecision(message, reason)
			break
		}
		input.CurrentState = actionResult.State
		if selectedServiceIDFromToolResult(actionResult.Result) != "" {
			input.SelectedServiceID = selectedServiceIDFromToolResult(actionResult.Result)
		}
		input.ToolResults = append(input.ToolResults, aisvc.SemiPilotContextToolResult{
			Name: actionResult.ActionName, Result: actionResult.Result,
		})
		if actionResult.ActionName == "get_booking_link" {
			bookingLinkActionID = uuid.MustParse(actionResult.ActionID)
		}
	}
	return worker.repo.completeInboxAISemiPilotTurn(ctx, completeInboxAISemiPilotTurnCommand{
		Turn: turn, WorkerID: workerID, Decision: finalDecision,
		ToolCallCount: toolCount, BookingLinkActionID: bookingLinkActionID,
		Latency: time.Since(startedAt),
	})
}

func shouldRetryInboxAIGeneration(err error, attemptCount, maxAttempts int) bool {
	return aierror.IsRetryable(err) && attemptCount < maxAttempts
}

func (worker *InboxAISemiPilotWorker) generateWithLeaseHeartbeat(
	ctx context.Context,
	workerID string,
	jobID uuid.UUID,
	mode string,
	input aisvc.SemiPilotTurnInput,
) (aisvc.SemiPilotTurnDecision, error) {
	modelCtx, cancelModel := context.WithCancel(ctx)
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan error, 1)
	interval := worker.config.LeaseDuration / 3
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				heartbeatDone <- nil
				return
			case <-ticker.C:
				if err := worker.renewLease(heartbeatCtx, workerID, jobID); err != nil {
					if heartbeatCtx.Err() != nil {
						heartbeatDone <- nil
					} else {
						heartbeatDone <- err
						cancelModel()
					}
					return
				}
			}
		}
	}()
	var decision aisvc.SemiPilotTurnDecision
	var decisionErr error
	if mode == InboxAIModeAutopilot {
		decider, ok := worker.decider.(inboxAIAutopilotDecider)
		if !ok {
			decisionErr = errors.New("autopilot decision service is unavailable")
		} else {
			decision, decisionErr = decider.GenerateAutopilotTurnDecision(modelCtx, input)
		}
	} else {
		decision, decisionErr = worker.decider.GenerateSemiPilotTurnDecision(modelCtx, input)
	}
	stopHeartbeat()
	heartbeatErr := <-heartbeatDone
	cancelModel()
	if heartbeatErr != nil {
		if errors.Is(heartbeatErr, errInboxAISemiPilotTurnStale) {
			return aisvc.SemiPilotTurnDecision{}, heartbeatErr
		}
		return aisvc.SemiPilotTurnDecision{}, aierror.Transient(
			"renew inbox AI generation lease", aierror.KindUnavailable, heartbeatErr,
		)
	}
	return decision, decisionErr
}

func normalizeAutopilotFinalDecision(
	decision aisvc.SemiPilotTurnDecision,
	authoritativeState string,
) aisvc.SemiPilotTurnDecision {
	if decision.NextState != "handoff" {
		decision.NextState = authoritativeState
	}
	return decision
}

func normalizeSemiPilotFinalDecision(
	decision aisvc.SemiPilotTurnDecision,
	authoritativeState string,
	bookingLinkCreated bool,
) aisvc.SemiPilotTurnDecision {
	if decision.NextState == "handoff" || bookingLinkCreated {
		return decision
	}
	// A generated reply is not authority to advance or regress workflow state.
	// Read tools own qualifying/service/link readiness; completion is only valid
	// after a link was already sent in a previous committed turn.
	if decision.NextState == "completed" && authoritativeState == "link_sent" {
		return decision
	}
	decision.NextState = authoritativeState
	return decision
}

func (worker *InboxAISemiPilotWorker) executeTool(
	ctx context.Context,
	turn inboxAISemiPilotPreparedTurn,
	call aisvc.SemiPilotToolCall,
	callOrdinal int,
) (InboxAISemiPilotReadActionResult, error) {
	key := uuid.NewSHA1(
		uuid.NameSpaceOID,
		[]byte(fmt.Sprintf("%s:%s:%d", turn.Mode, turn.Job.ID, callOrdinal)),
	)
	command := ExecuteSemiPilotReadActionCommand{
		ClientID: turn.Job.ClientID.String(), ConversationID: turn.Job.ConversationID.String(),
		IdempotencyKey: key.String(), ActionName: call.Name,
		ExpectedMessageSequence: turn.Job.TriggerMessageSequence,
		TurnJobID:               turn.Job.ID.String(),
	}
	switch call.Name {
	case "list_relevant_services":
		var arguments aisvc.SemiPilotListRelevantServicesArguments
		if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
			return InboxAISemiPilotReadActionResult{}, err
		}
		command.Query = arguments.Query
	case "get_service_details":
		var arguments aisvc.SemiPilotServiceArguments
		if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
			return InboxAISemiPilotReadActionResult{}, err
		}
		command.ServiceID = arguments.ServiceID
	case "get_booking_link":
		var arguments aisvc.SemiPilotBookingLinkArguments
		if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
			return InboxAISemiPilotReadActionResult{}, err
		}
		command.ServiceID = arguments.ServiceID
	}
	if turn.Mode == InboxAIModeAutopilot {
		return worker.repo.executeAutopilotReadAction(ctx, command)
	}
	return worker.repo.ExecuteSemiPilotReadAction(ctx, command)
}

func safeSemiPilotHandoffDecision(reply, reason string) aisvc.SemiPilotTurnDecision {
	return aisvc.SemiPilotTurnDecision{
		ProtocolVersion: aisvc.SemiPilotProtocolVersion,
		Reply:           strings.TrimSpace(reply), NextState: "handoff",
		ToolCall: nil, MissingFacts: []string{}, HandoffReason: reason,
	}
}

func selectedServiceIDFromToolResult(raw json.RawMessage) string {
	var envelope struct {
		Service *struct {
			ID string `json:"id"`
		} `json:"service"`
		ServiceID string `json:"service_id"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ""
	}
	if envelope.Service != nil {
		return envelope.Service.ID
	}
	return envelope.ServiceID
}

func (worker *InboxAISemiPilotWorker) renewLease(
	ctx context.Context,
	workerID string,
	jobID uuid.UUID,
) error {
	tag, err := worker.repo.db.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs
		SET lease_expires_at=NOW()+($3::bigint * INTERVAL '1 millisecond'), updated_at=NOW()
		WHERE id=$1 AND status='processing' AND lease_owner=$2
	`, jobID, workerID, worker.config.LeaseDuration.Milliseconds())
	if err != nil {
		return fmt.Errorf("renew semi-pilot lease: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errInboxAISemiPilotTurnStale
	}
	return nil
}

func (worker *InboxAISemiPilotWorker) deferForCapacity(
	ctx context.Context,
	workerID string,
	jobID uuid.UUID,
) error {
	tag, err := worker.repo.db.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs
		SET status='queued', attempt_count=GREATEST(attempt_count-1,0),
			available_at=NOW()+INTERVAL '250 milliseconds', lease_owner='',
			lease_expires_at=NULL, updated_at=NOW()
		WHERE id=$1 AND status='processing' AND lease_owner=$2
	`, jobID, workerID)
	if err != nil {
		return fmt.Errorf("defer semi-pilot capacity: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errInboxAISemiPilotTurnStale
	}
	return nil
}

func (worker *InboxAISemiPilotWorker) cancel(
	ctx context.Context,
	workerID string,
	jobID uuid.UUID,
	code string,
) error {
	tag, err := worker.repo.db.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs
		SET status='cancelled', error_code=$3, lease_owner='', lease_expires_at=NULL,
			completed_at=NOW(), updated_at=NOW()
		WHERE id=$1 AND status='processing' AND lease_owner=$2
	`, jobID, workerID, code)
	if err != nil {
		return fmt.Errorf("cancel semi-pilot turn: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errInboxAISemiPilotTurnStale
	}
	return errInboxAISemiPilotTurnStale
}

func (worker *InboxAISemiPilotWorker) retryOrFail(
	ctx context.Context,
	workerID string,
	job inboxAISemiPilotTurnJob,
	processErr error,
) error {
	terminal := job.AttemptCount >= job.MaxAttempts
	if terminal {
		tag, err := worker.repo.db.Exec(ctx, `
			UPDATE inbox_ai_turn_jobs
			SET available_at=NOW()+INTERVAL '8 seconds', lease_expires_at=NOW(), updated_at=NOW()
			WHERE id=$1 AND status='processing' AND lease_owner=$2
		`, job.ID, workerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errInboxAISemiPilotTurnStale
		}
		return nil
	}
	status := "queued"
	code := "processing_retry"
	completedAt := any(nil)
	availableAt := time.Now().UTC().Add(semiPilotTurnBackoff(job.AttemptCount))
	tx, err := worker.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs
		SET status=$3, available_at=$4, error_code=CASE WHEN $3='failed' THEN $5 ELSE '' END,
			lease_owner='', lease_expires_at=NULL, completed_at=$6, updated_at=NOW()
		WHERE id=$1 AND status='processing' AND lease_owner=$2
	`, job.ID, workerID, status, availableAt, code, completedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errInboxAISemiPilotTurnStale
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_runs
		SET status='failed', output_draft='', error_code=$2,
			latency_ms=GREATEST(0,EXTRACT(EPOCH FROM (NOW()-created_at))*1000)::int,
			completed_at=NOW()
		WHERE turn_job_id=$1 AND status='running'
	`, job.ID, code); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func semiPilotTurnBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Duration(1<<(attempt-1)) * time.Second
	if delay > 8*time.Second {
		return 8 * time.Second
	}
	return delay
}

func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
