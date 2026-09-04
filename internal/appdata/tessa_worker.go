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
	"strings"
	"time"

	"booking/go-server/internal/aierror"
	"booking/go-server/internal/tessa"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"
)

const (
	tessaWorkerPollInterval   = 25 * time.Second
	tessaMinimumWorkerLease   = 2 * time.Minute
	tessaWorkerLeaseMargin    = 30 * time.Second
	tessaEntityReferenceLimit = 12
	tessaRecentContextLimit   = 11
	tessaSummaryMaximumBytes  = 6000
	tessaSummaryMessageBytes  = 360
)

type TessaWorkerConfig struct {
	MaxConcurrency int
	TurnTimeout    time.Duration
	ConfigHash     string
	NoticeRevision string
}

type TessaWorker struct {
	repo         *Repository
	service      *tessa.Service
	help         *tessa.HelpIndex
	limiter      *InboxAIGenerationLimiter
	logger       *slog.Logger
	config       TessaWorkerConfig
	workerPrefix string
}

type tessaClaimedRun struct {
	ID               uuid.UUID
	ThreadID         uuid.UUID
	ClientID         uuid.UUID
	TriggerMessageID uuid.UUID
	LeaseToken       uuid.UUID
	AttemptCount     int
	MaxAttempts      int
	ConfigHash       string
	CreatedAt        time.Time
}

var errTessaConfigChanged = errors.New("Tessa configuration changed before the run started")

func NewTessaWorker(
	repo *Repository,
	service *tessa.Service,
	help *tessa.HelpIndex,
	limiter *InboxAIGenerationLimiter,
	logger *slog.Logger,
	config TessaWorkerConfig,
) (*TessaWorker, error) {
	if repo == nil || repo.db == nil || service == nil || help == nil {
		return nil, errors.New("Tessa worker dependencies are required")
	}
	if config.MaxConcurrency < 1 || config.MaxConcurrency > 32 || config.TurnTimeout <= 0 ||
		len(config.ConfigHash) != 64 || strings.TrimSpace(config.NoticeRevision) == "" {
		return nil, errors.New("Tessa worker configuration is invalid")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if limiter == nil {
		limiter = NewInboxAIGenerationLimiter(config.MaxConcurrency)
	}
	return &TessaWorker{
		repo: repo, service: service, help: help, limiter: limiter, logger: logger, config: config,
		workerPrefix: "tessa-" + uuid.NewString(),
	}, nil
}

func (worker *TessaWorker) Start(ctx context.Context, wakes ...<-chan struct{}) {
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

func (worker *TessaWorker) run(ctx context.Context, workerID string, wake <-chan struct{}) {
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
				worker.logger.Warn("Tessa run failed", "worker_id", workerID, "error", err)
			}
			if !processed || ctx.Err() != nil {
				break
			}
		}
		timer.Reset(worker.nextWakeDelay(ctx))
	}
}

func (worker *TessaWorker) nextWakeDelay(ctx context.Context) time.Duration {
	fallback := time.Duration(20+rand.IntN(11)) * time.Second
	var next time.Time
	err := worker.repo.db.QueryRow(ctx, `
		SELECT COALESCE(MIN(
			CASE WHEN status='processing' THEN lease_expires_at ELSE available_at END
		),NOW()+($1::bigint*INTERVAL '1 millisecond'))
		FROM tessa_runs
		WHERE status IN ('queued','processing') AND attempt_count<max_attempts
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

func (worker *TessaWorker) ProcessOne(ctx context.Context, workerIDs ...string) (bool, error) {
	workerID := worker.workerPrefix + "-manual"
	if len(workerIDs) > 0 && strings.TrimSpace(workerIDs[0]) != "" {
		workerID = strings.TrimSpace(workerIDs[0])
	}
	run, err := worker.claim(ctx, workerID)
	if err != nil || run.ID == uuid.Nil {
		return false, err
	}
	if err := requireTessaNoticeAcknowledgement(
		ctx, worker.repo.db, run.ClientID, worker.config.NoticeRevision,
	); err != nil {
		return true, worker.retryOrFail(ctx, workerID, run, err)
	}
	if run.ConfigHash != worker.config.ConfigHash {
		return true, worker.retryOrFail(ctx, workerID, run, errTessaConfigChanged)
	}
	slot, err := worker.limiter.TryAcquire(ctx)
	if err != nil {
		return true, worker.retryOrFail(ctx, workerID, run, err)
	}
	if slot == nil {
		return true, worker.deferForCapacity(ctx, workerID, run)
	}
	defer slot.Release()
	if err := worker.markStarted(ctx, workerID, run); err != nil {
		if persistErr := worker.retryOrFail(ctx, workerID, run, err); persistErr != nil {
			return true, fmt.Errorf("start Tessa run: %v; persist failure: %w", err, persistErr)
		}
		return true, err
	}

	turnContext, cancel := context.WithTimeout(ctx, worker.config.TurnTimeout)
	defer cancel()
	if err := worker.process(turnContext, workerID, run); err != nil {
		if persistErr := worker.retryOrFail(ctx, workerID, run, err); persistErr != nil {
			return true, fmt.Errorf("process Tessa run: %v; persist failure: %w", err, persistErr)
		}
		return true, err
	}
	return true, nil
}

func (worker *TessaWorker) claim(ctx context.Context, workerID string) (tessaClaimedRun, error) {
	tx, err := worker.repo.db.Begin(ctx)
	if err != nil {
		return tessaClaimedRun{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var run tessaClaimedRun
	if err := tx.QueryRow(ctx, `
		WITH candidates AS (
			SELECT queued.id
			FROM tessa_runs queued
			WHERE queued.available_at<=NOW() AND (
				(queued.status='queued' AND queued.attempt_count<queued.max_attempts)
				OR (queued.status='processing' AND queued.lease_expires_at<NOW() AND queued.attempt_count<queued.max_attempts)
			) AND NOT EXISTS (
				SELECT 1 FROM tessa_runs active
				WHERE active.client_id=queued.client_id AND active.status='processing'
				  AND active.lease_expires_at>=NOW() AND active.id<>queued.id
			)
			ORDER BY queued.available_at,queued.created_at,queued.id
			FOR UPDATE OF queued SKIP LOCKED LIMIT 1
		)
		SELECT run.id,run.thread_id,run.client_id,run.trigger_message_id,
			run.attempt_count,run.max_attempts,run.config_hash,run.created_at
		FROM tessa_runs run JOIN candidates ON candidates.id=run.id
	`).Scan(
		&run.ID, &run.ThreadID, &run.ClientID, &run.TriggerMessageID,
		&run.AttemptCount, &run.MaxAttempts, &run.ConfigHash, &run.CreatedAt,
	); errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return tessaClaimedRun{}, err
		}
		return tessaClaimedRun{}, nil
	} else if err != nil {
		return tessaClaimedRun{}, fmt.Errorf("claim Tessa run: %w", err)
	}
	run.AttemptCount++
	run.LeaseToken = uuid.New()
	if _, err := tx.Exec(ctx, `
		UPDATE tessa_runs SET status='processing',stage='planning',lease_owner=$2,lease_token=$3,
			lease_expires_at=NOW()+($4::bigint*INTERVAL '1 millisecond'),attempt_count=$5,
			started_at=COALESCE(started_at,NOW()),queue_latency_ms=COALESCE(
				queue_latency_ms,GREATEST(0,EXTRACT(EPOCH FROM (NOW()-created_at))*1000)::int
			),error_code=''
		WHERE id=$1
	`, run.ID, workerID, run.LeaseToken, worker.leaseDuration().Milliseconds(), run.AttemptCount); err != nil {
		return tessaClaimedRun{}, fmt.Errorf("lease Tessa run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return tessaClaimedRun{}, err
	}
	return run, nil
}

func (worker *TessaWorker) markStarted(ctx context.Context, workerID string, run tessaClaimedRun) error {
	tx, err := worker.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var active bool
	if err := tx.QueryRow(ctx, `
		SELECT TRUE FROM tessa_runs
		WHERE id=$1 AND status='processing' AND lease_owner=$2 AND lease_token=$3
		FOR UPDATE
	`, run.ID, workerID, run.LeaseToken).Scan(&active); errors.Is(err, pgx.ErrNoRows) {
		return errors.New("Tessa run lease was lost")
	} else if err != nil {
		return err
	}
	if _, err := appendTessaEvent(ctx, tx, run.ClientID, run.ThreadID, uuid.Nil, run.ID, "run.started", map[string]any{
		"run_id": run.ID.String(), "stage": "planning",
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (worker *TessaWorker) process(ctx context.Context, workerID string, run tessaClaimedRun) error {
	question, timezone, summary, recent, err := worker.loadContext(ctx, run)
	if err != nil {
		return err
	}
	contextPayload, err := json.Marshal(struct {
		Question string                 `json:"question"`
		Summary  string                 `json:"summary"`
		Recent   []tessa.ContextMessage `json:"recent"`
		Timezone string                 `json:"timezone"`
	}{question, summary, recent, timezone})
	if err != nil {
		return fmt.Errorf("encode Tessa context hash: %w", err)
	}
	contextHash := sha256.Sum256(contextPayload)
	contextHashValue := hex.EncodeToString(contextHash[:])
	tag, err := worker.repo.db.Exec(ctx, `
		UPDATE tessa_runs SET context_hash=$5
		WHERE id=$1 AND status='processing' AND lease_owner=$2 AND lease_token=$3
			AND (context_hash='' OR context_hash=$5) AND thread_id=$4
	`, run.ID, workerID, run.LeaseToken, run.ThreadID, contextHashValue)
	if err != nil {
		return fmt.Errorf("record Tessa context hash: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("Tessa run context changed or lease was lost")
	}
	location := timezoneLocation(timezone)
	now := time.Now().In(location)
	input := tessa.PlanInput{
		Question: question, ConversationSummary: summary, RecentMessages: recent,
		CurrentDate: now.Format("2006-01-02"), CurrentTime: now.Format("15:04:05"), Timezone: location.String(),
	}
	decision, err := worker.loadOrCreatePlanningDecision(ctx, run, input)
	if err != nil {
		return err
	}
	plan, activeProvider := decision.Plan, decision.Provider
	fallbackUsed, fallbackReason := decision.FallbackUsed, decision.FallbackReason
	input.CurrentDate, input.CurrentTime, input.Timezone = decision.CurrentDate, decision.CurrentTime, decision.Timezone
	if plan.AnswerMode == "direct" {
		if err := worker.setStage(ctx, workerID, run, "answering"); err != nil {
			return err
		}
		return worker.complete(ctx, workerID, run, plan.DirectResponse, TessaMessagePresentation{}, nil, activeProvider, fallbackUsed, fallbackReason)
	}
	stage := tessaToolStage(plan.Tools)
	if err := worker.setStage(ctx, workerID, run, stage); err != nil {
		return err
	}
	execution, err := worker.executeTools(ctx, run, plan.Tools, input.Timezone)
	if err != nil {
		return err
	}
	if err := worker.setStage(ctx, workerID, run, "answering"); err != nil {
		return err
	}
	answer, err := worker.service.GenerateAnswer(ctx, activeProvider, tessa.SynthesisInput{
		Question: question, ConversationSummary: summary, RecentMessages: recent, Evidence: execution.Evidence,
		CurrentDate: input.CurrentDate, Timezone: input.Timezone,
	})
	if err != nil && !fallbackUsed && shouldFallbackTessa(err) {
		if fallback, ok := worker.service.Fallback(); ok {
			fallbackUsed, fallbackReason, activeProvider = true, tessaSafeFailureCode(err), fallback
			if err := worker.recordSynthesisProviderTransition(ctx, run, activeProvider, fallbackReason); err != nil {
				return err
			}
			answer, err = worker.service.GenerateAnswer(ctx, activeProvider, tessa.SynthesisInput{
				Question: question, ConversationSummary: summary, RecentMessages: recent, Evidence: execution.Evidence,
				CurrentDate: input.CurrentDate, Timezone: input.Timezone,
			})
		}
	}
	if err != nil {
		return err
	}
	stepCode := ""
	if fallbackUsed {
		stepCode = fallbackReason
	}
	if err := worker.recordModelStep(ctx, run, 100, "synthesis", activeProvider, stepCode); err != nil {
		return err
	}
	return worker.complete(ctx, workerID, run, answer.Content, execution.Presentation, execution.References, activeProvider, fallbackUsed, fallbackReason)
}

type tessaPersistedPlanningDecision struct {
	Plan        tessa.Plan `json:"plan"`
	CurrentDate string     `json:"current_date"`
	CurrentTime string     `json:"current_time"`
	Timezone    string     `json:"timezone"`
}

type tessaPlanningDecision struct {
	tessaPersistedPlanningDecision
	Provider       tessa.Provider
	FallbackUsed   bool
	FallbackReason string
}

func (worker *TessaWorker) loadOrCreatePlanningDecision(
	ctx context.Context,
	run tessaClaimedRun,
	input tessa.PlanInput,
) (tessaPlanningDecision, error) {
	if decision, found, err := worker.loadPlanningDecision(ctx, run); err != nil || found {
		return decision, err
	}

	activeProvider := worker.service.Primary()
	plan, err := worker.service.GeneratePlan(ctx, activeProvider, input)
	fallbackReason := ""
	if err != nil && shouldFallbackTessa(err) {
		if fallback, ok := worker.service.Fallback(); ok {
			fallbackReason, activeProvider = tessaSafeFailureCode(err), fallback
			plan, err = worker.service.GeneratePlan(ctx, activeProvider, input)
		}
	}
	if err != nil {
		return tessaPlanningDecision{}, err
	}
	persisted := tessaPersistedPlanningDecision{
		Plan: plan, CurrentDate: input.CurrentDate, CurrentTime: input.CurrentTime, Timezone: input.Timezone,
	}
	payload, err := marshalTessaSafeResult(persisted)
	if err != nil {
		return tessaPlanningDecision{}, fmt.Errorf("encode Tessa planning decision: %w", err)
	}
	if _, err := worker.repo.db.Exec(ctx, `
		INSERT INTO tessa_run_steps (
			id,run_id,sequence,stage,provider,model,status,error_code,
			safe_result,safe_result_count,completed_at
		) VALUES ($1,$2,1,'planning',$3,$4,'succeeded',$5,$6::jsonb,$7,NOW())
		ON CONFLICT (run_id,sequence) DO NOTHING
	`, uuid.New(), run.ID, activeProvider.Name, activeProvider.Model, fallbackReason, payload, len(plan.Tools)); err != nil {
		return tessaPlanningDecision{}, fmt.Errorf("commit Tessa planning decision: %w", err)
	}
	decision, found, err := worker.loadPlanningDecision(ctx, run)
	if err != nil {
		return tessaPlanningDecision{}, err
	}
	if !found {
		return tessaPlanningDecision{}, errors.New("committed Tessa planning decision was not found")
	}
	return decision, nil
}

func (worker *TessaWorker) loadPlanningDecision(
	ctx context.Context,
	run tessaClaimedRun,
) (tessaPlanningDecision, bool, error) {
	var payload json.RawMessage
	var providerName, model, fallbackReason string
	if err := worker.repo.db.QueryRow(ctx, `
		SELECT safe_result,provider,model,error_code FROM tessa_run_steps
		WHERE run_id=$1 AND sequence=1 AND stage='planning' AND status='succeeded'
	`, run.ID).Scan(&payload, &providerName, &model, &fallbackReason); errors.Is(err, pgx.ErrNoRows) {
		return tessaPlanningDecision{}, false, nil
	} else if err != nil {
		return tessaPlanningDecision{}, false, err
	}
	var persisted tessaPersistedPlanningDecision
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return tessaPlanningDecision{}, false, fmt.Errorf("decode committed Tessa planning decision: %w", err)
	}
	plan, err := tessa.NormalizePlan(persisted.Plan)
	if err != nil {
		return tessaPlanningDecision{}, false, fmt.Errorf("validate committed Tessa planning decision: %w", err)
	}
	if persisted.CurrentDate == "" || persisted.CurrentTime == "" || persisted.Timezone == "" {
		return tessaPlanningDecision{}, false, errors.New("committed Tessa planning context is incomplete")
	}
	provider, fallbackUsed, err := worker.planningProvider(providerName, model)
	if err != nil {
		return tessaPlanningDecision{}, false, err
	}
	var synthesisProviderName, synthesisModel, synthesisFallbackReason string
	if err := worker.repo.db.QueryRow(ctx, `
		SELECT provider,model,error_code FROM tessa_run_steps
		WHERE run_id=$1 AND sequence=100 AND stage='synthesis'
	`, run.ID).Scan(&synthesisProviderName, &synthesisModel, &synthesisFallbackReason); err == nil {
		provider, fallbackUsed, err = worker.planningProvider(synthesisProviderName, synthesisModel)
		if err != nil {
			return tessaPlanningDecision{}, false, err
		}
		if synthesisFallbackReason != "" {
			fallbackReason = synthesisFallbackReason
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return tessaPlanningDecision{}, false, err
	}
	persisted.Plan = plan
	return tessaPlanningDecision{
		tessaPersistedPlanningDecision: persisted,
		Provider:                       provider, FallbackUsed: fallbackUsed, FallbackReason: fallbackReason,
	}, true, nil
}

func (worker *TessaWorker) recordSynthesisProviderTransition(
	ctx context.Context,
	run tessaClaimedRun,
	provider tessa.Provider,
	reason string,
) error {
	_, err := worker.repo.db.Exec(ctx, `
		INSERT INTO tessa_run_steps (
			id,run_id,sequence,stage,provider,model,status,error_code
		) VALUES ($1,$2,100,'synthesis',$3,$4,'running',$5)
		ON CONFLICT (run_id,sequence) DO UPDATE SET provider=EXCLUDED.provider,
			model=EXCLUDED.model,status='running',error_code=EXCLUDED.error_code,completed_at=NULL
	`, uuid.New(), run.ID, provider.Name, provider.Model, reason)
	return err
}

func (worker *TessaWorker) planningProvider(name, model string) (tessa.Provider, bool, error) {
	primary := worker.service.Primary()
	if name == primary.Name && model == primary.Model {
		return primary, false, nil
	}
	if fallback, ok := worker.service.Fallback(); ok && name == fallback.Name && model == fallback.Model {
		return fallback, true, nil
	}
	return tessa.Provider{}, false, errors.New("committed Tessa planning provider is unavailable")
}

func tessaToolStage(requests []tessa.ToolRequest) string {
	stage := "checking_help"
	for _, request := range requests {
		switch request.Name {
		case "get_availability":
			return "checking_availability"
		case "get_schedule":
			stage = "checking_schedule"
		case "search_bookings", "get_booking", "get_booking_attention_summary":
			if stage != "checking_schedule" {
				stage = "checking_bookings"
			}
		case "get_business_snapshot":
			if stage == "checking_help" {
				stage = "checking_business"
			}
		case "search_services", "get_service", "search_customers", "get_customer_booking_summary",
			"get_payment_summary", "get_payout_summary", "get_booking_metrics", "get_inbox_summary",
			"get_review_summary", "get_public_profile_status":
			if stage == "checking_help" {
				stage = "checking_business"
			}
		case "get_booking_payment_status":
			if stage != "checking_schedule" {
				stage = "checking_bookings"
			}
		}
	}
	return stage
}

func (worker *TessaWorker) loadContext(ctx context.Context, run tessaClaimedRun) (string, string, string, []tessa.ContextMessage, error) {
	var question, timezone, summary string
	if err := worker.repo.db.QueryRow(ctx, `
		SELECT message.content,COALESCE(profile.timezone,'Africa/Lagos'),thread.summary
		FROM tessa_messages message
		INNER JOIN tessa_threads thread ON thread.id=message.thread_id AND thread.client_id=message.client_id
		LEFT JOIN client_profiles profile ON profile.client_id=message.client_id
		WHERE message.id=$1 AND message.thread_id=$2 AND message.client_id=$3
	`, run.TriggerMessageID, run.ThreadID, run.ClientID).Scan(&question, &timezone, &summary); err != nil {
		return "", "", "", nil, fmt.Errorf("load Tessa question: %w", err)
	}
	rows, err := worker.repo.db.Query(ctx, `
		SELECT sender_type,content FROM (
			SELECT sender_type,content,sequence FROM tessa_messages
			WHERE thread_id=$1 AND client_id=$2 AND sequence<(
				SELECT sequence FROM tessa_messages WHERE id=$3
			) ORDER BY sequence DESC LIMIT $4
		) recent ORDER BY sequence
	`, run.ThreadID, run.ClientID, run.TriggerMessageID, tessaRecentContextLimit)
	if err != nil {
		return "", "", "", nil, err
	}
	defer rows.Close()
	recent := make([]tessa.ContextMessage, 0, tessaRecentContextLimit)
	for rows.Next() {
		var sender, content string
		if err := rows.Scan(&sender, &content); err != nil {
			return "", "", "", nil, err
		}
		role := "assistant"
		if sender == "provider" {
			role = "provider"
		}
		recent = append(recent, tessa.ContextMessage{Role: role, Content: truncateRunes(content, 2000)})
	}
	if err := rows.Err(); err != nil {
		return "", "", "", nil, err
	}
	return question, timezone, truncateTessaToolText(summary, tessaSummaryMaximumBytes), recent, nil
}

func compactTessaConversation(
	ctx context.Context,
	tx pgx.Tx,
	threadID, clientID uuid.UUID,
	existing string,
	summaryThrough, lastSequence int64,
) (string, int64, bool, error) {
	cutoff := lastSequence - tessaRecentContextLimit
	if cutoff <= summaryThrough {
		return existing, summaryThrough, false, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT sequence,sender_type,content
		FROM tessa_messages
		WHERE thread_id=$1 AND client_id=$2 AND sequence>$3 AND sequence<=$4
		ORDER BY sequence
	`, threadID, clientID, summaryThrough, cutoff)
	if err != nil {
		return "", 0, false, fmt.Errorf("load Tessa messages for compaction: %w", err)
	}
	defer rows.Close()
	lines := make([]string, 0, 16)
	if trimmed := strings.TrimSpace(existing); trimmed != "" {
		lines = append(lines, strings.Split(trimmed, "\n")...)
	}
	for rows.Next() {
		var sequence int64
		var sender, content string
		if err := rows.Scan(&sequence, &sender, &content); err != nil {
			return "", 0, false, err
		}
		label := "Tessa"
		if sender == "provider" {
			label = "Provider"
		} else if sender == "system" {
			label = "System"
		}
		content = truncateTessaToolText(strings.Join(strings.Fields(content), " "), tessaSummaryMessageBytes)
		lines = append(lines, fmt.Sprintf("%d %s: %s", sequence, label, content))
	}
	if err := rows.Err(); err != nil {
		return "", 0, false, err
	}
	for len(lines) > 0 && len(strings.Join(lines, "\n")) > tessaSummaryMaximumBytes {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n"), cutoff, true, nil
}

type tessaToolExecution struct {
	Evidence     []tessa.Evidence
	Presentation TessaMessagePresentation
	References   []TessaEntityReference
}

func (worker *TessaWorker) executeTools(ctx context.Context, run tessaClaimedRun, requests []tessa.ToolRequest, timezone string) (tessaToolExecution, error) {
	type result struct {
		index   int
		request tessa.ToolRequest
		payload json.RawMessage
		count   int
		err     error
	}
	results := make([]result, len(requests))
	group, groupContext := errgroup.WithContext(ctx)
	for index := range requests {
		index := index
		results[index] = result{index: index, request: requests[index]}
		group.Go(func() error {
			payload, count, err := worker.loadOrExecuteTool(groupContext, run, index, requests[index], timezone)
			results[index].payload, results[index].count, results[index].err = payload, count, err
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return tessaToolExecution{}, err
	}
	evidence := make([]tessa.Evidence, 0, len(results))
	for _, item := range results {
		evidence = append(evidence, tessa.Evidence{Tool: item.request.Name, Result: item.payload})
	}
	presentation, references := tessaToolMetadata(evidence)
	return tessaToolExecution{Evidence: evidence, Presentation: presentation, References: references}, nil
}

func (worker *TessaWorker) loadOrExecuteTool(ctx context.Context, run tessaClaimedRun, index int, request tessa.ToolRequest, timezone string) (json.RawMessage, int, error) {
	key := tessaToolActionKey(index, request)
	var stored json.RawMessage
	var storedCount int
	if err := worker.repo.db.QueryRow(ctx, `
		SELECT safe_result,safe_result_count FROM tessa_run_steps
		WHERE run_id=$1 AND idempotency_key=$2 AND status='succeeded'
	`, run.ID, key).Scan(&stored, &storedCount); err == nil {
		return stored, storedCount, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, err
	}
	var value any
	count := 1
	switch request.Name {
	case "search_tellbook_help":
		matches := worker.help.Search(request.Query, 2)
		count, value = len(matches), map[string]any{
			"revision": worker.help.Revision(), "matches": matches,
		}
	case "get_business_snapshot":
		snapshot, err := worker.repo.GetTessaBusinessSnapshot(ctx, run.ClientID)
		if err != nil {
			return nil, 0, err
		}
		value = snapshot
	case "search_bookings":
		from, to, err := tessaDateRange(request.From, request.To, timezone)
		if err != nil {
			return nil, 0, err
		}
		result, err := worker.repo.SearchTessaBookings(ctx, run.ClientID, from, to, request.Statuses, request.Query, request.Limit, true)
		if err != nil {
			return nil, 0, err
		}
		count, value = len(result.Items), result
	case "get_booking":
		bookingID, _ := uuid.Parse(request.BookingID)
		result, err := worker.repo.GetTessaBooking(ctx, run.ClientID, bookingID)
		if errors.Is(err, ErrNotFound) {
			count, value = 0, map[string]any{"found": false}
		} else if err != nil {
			return nil, 0, err
		} else {
			value = map[string]any{"found": true, "booking": result}
		}
	case "get_schedule":
		from, to, err := tessaDateRange(request.From, request.To, timezone)
		if err != nil {
			return nil, 0, err
		}
		result, err := worker.repo.SearchTessaBookings(ctx, run.ClientID, from, to, nil, "", tessaBookingResultLimit, false)
		if err != nil {
			return nil, 0, err
		}
		count, value = len(result.Items), result
	case "get_availability":
		from, to, err := tessaDateRange(request.From, request.To, timezone)
		if err != nil {
			return nil, 0, err
		}
		serviceID := uuid.Nil
		if request.ServiceID != "" {
			serviceID, _ = uuid.Parse(request.ServiceID)
		}
		result, err := worker.repo.GetTessaAvailability(ctx, run.ClientID, serviceID, from, tessaCalendarDays(from, to))
		if errors.Is(err, ErrNotFound) {
			count, value = 0, map[string]any{"found": false}
		} else if err != nil {
			return nil, 0, err
		} else {
			count = tessaAvailabilitySlotCount(result)
			value = result
		}
	case "get_booking_attention_summary":
		from, to, err := tessaDateRange(request.From, request.To, timezone)
		if err != nil {
			return nil, 0, err
		}
		result, err := worker.repo.GetTessaBookingAttentionSummary(ctx, run.ClientID, from, to)
		if err != nil {
			return nil, 0, err
		}
		count, value = result.Total, result
	case "search_services":
		result, err := worker.repo.SearchTessaServices(
			ctx, run.ClientID, request.Query, request.Status, request.Limit,
		)
		if err != nil {
			return nil, 0, err
		}
		count, value = len(result.Items), result
	case "get_service":
		serviceID, _ := uuid.Parse(request.ServiceID)
		result, err := worker.repo.GetTessaService(ctx, run.ClientID, serviceID)
		if errors.Is(err, ErrNotFound) {
			count, value = 0, map[string]any{"found": false}
		} else if err != nil {
			return nil, 0, err
		} else {
			value = map[string]any{"found": true, "service": result}
		}
	case "search_customers":
		result, err := worker.repo.SearchTessaCustomers(ctx, run.ClientID, request.Query, request.Limit)
		if err != nil {
			return nil, 0, err
		}
		count, value = len(result.Items), result
	case "get_customer_booking_summary":
		customerID, _ := uuid.Parse(request.CustomerID)
		result, err := worker.repo.GetTessaCustomerBookingSummary(ctx, run.ClientID, customerID)
		if errors.Is(err, ErrNotFound) {
			count, value = 0, map[string]any{"found": false}
		} else if err != nil {
			return nil, 0, err
		} else {
			value = map[string]any{"found": true, "customer": result}
		}
	case "get_payment_summary":
		from, to, err := tessaDateRange(request.From, request.To, timezone)
		if err != nil {
			return nil, 0, err
		}
		result, err := worker.repo.GetTessaPaymentSummary(ctx, run.ClientID, from, to)
		if err != nil {
			return nil, 0, err
		}
		count, value = result.PaymentCount, result
	case "get_booking_payment_status":
		bookingID, _ := uuid.Parse(request.BookingID)
		result, err := worker.repo.GetTessaBookingPaymentStatus(ctx, run.ClientID, bookingID)
		if errors.Is(err, ErrNotFound) {
			count, value = 0, map[string]any{"found": false}
		} else if err != nil {
			return nil, 0, err
		} else {
			value = map[string]any{"found": true, "payment": result}
		}
	case "get_payout_summary":
		from, to, err := tessaDateRange(request.From, request.To, timezone)
		if err != nil {
			return nil, 0, err
		}
		result, err := worker.repo.GetTessaPayoutSummary(ctx, run.ClientID, from, to)
		if err != nil {
			return nil, 0, err
		}
		count, value = result.PeriodPayoutCount, result
	case "get_booking_metrics":
		from, to, err := tessaDateRange(request.From, request.To, timezone)
		if err != nil {
			return nil, 0, err
		}
		result, err := worker.repo.GetTessaBookingMetrics(ctx, run.ClientID, from, to, request.ComparePrevious)
		if err != nil {
			return nil, 0, err
		}
		count, value = result.Current.Metrics.TotalBookings, result
	case "get_inbox_summary":
		result, err := worker.repo.GetTessaInboxSummary(ctx, run.ClientID)
		if err != nil {
			return nil, 0, err
		}
		count, value = result.OpenConversations, result
	case "get_review_summary":
		from, to, err := tessaDateRange(request.From, request.To, timezone)
		if err != nil {
			return nil, 0, err
		}
		result, err := worker.repo.GetTessaReviewSummary(ctx, run.ClientID, from, to)
		if err != nil {
			return nil, 0, err
		}
		count, value = result.Count, result
	case "get_public_profile_status":
		result, err := worker.repo.GetTessaPublicProfileStatus(ctx, run.ClientID)
		if err != nil {
			return nil, 0, err
		}
		value = result
	default:
		return nil, 0, errors.New("Tessa tool is not allowlisted")
	}
	payload, err := marshalTessaSafeResult(value)
	if err != nil {
		return nil, 0, err
	}
	count = tessaStoredResultCount(request.Name, payload, count)
	argumentBytes, _ := json.Marshal(request)
	argumentHash := sha256.Sum256(argumentBytes)
	if _, err := worker.repo.db.Exec(ctx, `
		INSERT INTO tessa_run_steps (
			id,run_id,sequence,stage,idempotency_key,tool_name,safe_argument_hash,
			status,safe_result,safe_result_count,completed_at
		) VALUES ($1,$2,$3,'tool',$4,$5,$6,'succeeded',$7::jsonb,$8,NOW())
		ON CONFLICT (run_id,idempotency_key) WHERE idempotency_key IS NOT NULL DO UPDATE SET
			status='succeeded',safe_result=EXCLUDED.safe_result,
			safe_result_count=EXCLUDED.safe_result_count,error_code='',completed_at=NOW()
	`, uuid.New(), run.ID, 10+index, key, request.Name, hex.EncodeToString(argumentHash[:]), payload, count); err != nil {
		return nil, 0, fmt.Errorf("commit Tessa safe tool evidence: %w", err)
	}
	return payload, count, nil
}

func (worker *TessaWorker) recordModelStep(ctx context.Context, run tessaClaimedRun, sequence int, stage string, provider tessa.Provider, code string) error {
	_, err := worker.repo.db.Exec(ctx, `
		INSERT INTO tessa_run_steps (
			id,run_id,sequence,stage,provider,model,status,error_code,completed_at
		) VALUES ($1,$2,$3,$4,$5,$6,'succeeded',$7,NOW())
		ON CONFLICT (run_id,sequence) DO UPDATE SET provider=EXCLUDED.provider,
			model=EXCLUDED.model,status='succeeded',error_code=EXCLUDED.error_code,completed_at=NOW()
	`, uuid.New(), run.ID, sequence, stage, provider.Name, provider.Model, code)
	return err
}

func (worker *TessaWorker) setStage(ctx context.Context, workerID string, run tessaClaimedRun, stage string) error {
	tx, err := worker.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE tessa_runs SET stage=$4
		WHERE id=$1 AND status='processing' AND lease_owner=$2 AND lease_token=$3
	`, run.ID, workerID, run.LeaseToken, stage)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("Tessa run lease was lost")
	}
	if _, err := appendTessaEvent(ctx, tx, run.ClientID, run.ThreadID, uuid.Nil, run.ID, "run.stage_changed", map[string]any{
		"run_id": run.ID.String(), "stage": stage,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (worker *TessaWorker) complete(
	ctx context.Context,
	workerID string,
	run tessaClaimedRun,
	content string,
	presentation TessaMessagePresentation,
	references []TessaEntityReference,
	provider tessa.Provider,
	fallbackUsed bool,
	fallbackReason string,
) error {
	content = strings.TrimSpace(content)
	if content == "" || len([]rune(content)) > 4000 {
		return errors.New("Tessa completion content is invalid")
	}
	tx, err := worker.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var lastSequence, summaryThrough int64
	var summary string
	if err := tx.QueryRow(ctx, `
		SELECT thread.last_message_sequence,thread.summary,thread.summary_through_sequence
		FROM tessa_threads thread INNER JOIN tessa_runs run ON run.thread_id=thread.id
		WHERE run.id=$1 AND run.client_id=$2 AND run.status='processing'
			AND run.lease_owner=$3 AND run.lease_token=$4 AND thread.status='active'
		FOR UPDATE OF thread,run
	`, run.ID, run.ClientID, workerID, run.LeaseToken).Scan(&lastSequence, &summary, &summaryThrough); errors.Is(err, pgx.ErrNoRows) {
		return errors.New("Tessa run was cancelled or superseded")
	} else if err != nil {
		return err
	}
	messageID := uuid.New()
	sequence := lastSequence + 1
	presentationJSON, err := json.Marshal(presentation)
	if err != nil {
		return err
	}
	referencesJSON, err := json.Marshal(references)
	if err != nil {
		return err
	}
	if references == nil {
		references = []TessaEntityReference{}
		referencesJSON = []byte("[]")
	}
	var createdAt time.Time
	if err := tx.QueryRow(ctx, `
		INSERT INTO tessa_messages (
			id,thread_id,client_id,sequence,sender_type,source_channel,content,presentation,entity_references,run_id
		) VALUES ($1,$2,$3,$4,'tessa','web',$5,$6::jsonb,$7::jsonb,$8) RETURNING created_at
	`, messageID, run.ThreadID, run.ClientID, sequence, content, presentationJSON, referencesJSON, run.ID).Scan(&createdAt); err != nil {
		return fmt.Errorf("insert Tessa answer: %w", err)
	}
	latency := int(time.Since(run.CreatedAt).Milliseconds())
	if _, err := tx.Exec(ctx, `
		UPDATE tessa_runs SET status='completed',stage='completed',lease_owner='',lease_token=NULL,
			lease_expires_at=NULL,final_provider=$2,final_model=$3,fallback_used=$4,
			fallback_reason=$5,error_code='',generation_latency_ms=GREATEST(0,$6-COALESCE(queue_latency_ms,0)),
			total_latency_ms=$6,completed_at=NOW()
		WHERE id=$1
	`, run.ID, provider.Name, provider.Model, fallbackUsed, fallbackReason, latency); err != nil {
		return err
	}
	compactedSummary, compactedThrough, compacted, err := compactTessaConversation(
		ctx, tx, run.ThreadID, run.ClientID, summary, summaryThrough, sequence,
	)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE tessa_threads SET last_message_sequence=$2,last_activity_at=$3,
			summary=CASE WHEN $4 THEN $5 ELSE summary END,
			summary_through_sequence=CASE WHEN $4 THEN $6 ELSE summary_through_sequence END,
			summary_revision=CASE WHEN $4 THEN summary_revision+1 ELSE summary_revision END
		WHERE id=$1
	`, run.ThreadID, sequence, createdAt, compacted, compactedSummary, compactedThrough); err != nil {
		return err
	}
	if _, err := appendTessaEvent(ctx, tx, run.ClientID, run.ThreadID, messageID, run.ID, "message.created", map[string]any{
		"message_id": messageID.String(), "run_id": run.ID.String(),
	}); err != nil {
		return err
	}
	if _, err := appendTessaEvent(ctx, tx, run.ClientID, run.ThreadID, uuid.Nil, run.ID, "run.completed", map[string]any{
		"run_id": run.ID.String(),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (worker *TessaWorker) deferForCapacity(ctx context.Context, workerID string, run tessaClaimedRun) error {
	tag, err := worker.repo.db.Exec(ctx, `
		UPDATE tessa_runs SET status='queued',stage='queued',lease_owner='',lease_token=NULL,
			lease_expires_at=NULL,attempt_count=GREATEST(0,attempt_count-1),
			available_at=NOW()+INTERVAL '1 second'
		WHERE id=$1 AND status='processing' AND lease_owner=$2 AND lease_token=$3
	`, run.ID, workerID, run.LeaseToken)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("Tessa run lease was lost")
	}
	return nil
}

func (worker *TessaWorker) retryOrFail(ctx context.Context, workerID string, run tessaClaimedRun, processErr error) error {
	if errors.Is(processErr, context.Canceled) && ctx.Err() != nil {
		return processErr
	}
	code := tessaSafeFailureCode(processErr)
	retry := aierror.IsRetryable(processErr) && run.AttemptCount < run.MaxAttempts
	tx, err := worker.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	status, stage := "failed", "completed"
	availableAt := time.Now().UTC()
	completedAt := any(time.Now().UTC())
	if retry {
		status, stage, completedAt = "queued", "queued", nil
		availableAt = availableAt.Add(tessaRunBackoff(run.AttemptCount))
	}
	if _, err := tx.Exec(ctx, `
		UPDATE tessa_run_steps SET status='failed',
			error_code=CASE WHEN error_code='' THEN $2 ELSE error_code END,completed_at=NOW()
		WHERE run_id=$1 AND status='running'
	`, run.ID, code); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE tessa_runs SET status=$4,stage=$5,lease_owner='',lease_token=NULL,
			lease_expires_at=NULL,available_at=$6,error_code=$7,completed_at=$8
		WHERE id=$1 AND status='processing' AND lease_owner=$2 AND lease_token=$3
	`, run.ID, workerID, run.LeaseToken, status, stage, availableAt, code, completedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("Tessa run lease was lost")
	}
	if !retry {
		if _, err := appendTessaEvent(ctx, tx, run.ClientID, run.ThreadID, uuid.Nil, run.ID, "run.failed", map[string]any{
			"run_id": run.ID.String(), "error_code": code,
		}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (worker *TessaWorker) leaseDuration() time.Duration {
	lease := worker.config.TurnTimeout + tessaWorkerLeaseMargin
	if lease < tessaMinimumWorkerLease {
		return tessaMinimumWorkerLease
	}
	return lease
}

func shouldFallbackTessa(err error) bool {
	return aierror.IsRetryable(err) || aierror.IsRepairable(err) ||
		strings.Contains(err.Error(), "validate Tessa plan")
}

func tessaSafeFailureCode(err error) string {
	if errors.Is(err, ErrTessaNoticeRevision) {
		return "notice_changed"
	}
	if errors.Is(err, errTessaConfigChanged) {
		return "config_changed"
	}
	var providerError *aierror.Error
	if errors.As(err, &providerError) {
		switch providerError.Kind {
		case aierror.KindTimeout:
			return "provider_timeout"
		case aierror.KindRateLimited:
			return "provider_rate_limited"
		case aierror.KindUnavailable, aierror.KindTransport:
			return "provider_unavailable"
		case aierror.KindInvalidOutput, aierror.KindMalformedResponse:
			return "invalid_output"
		default:
			return "provider_failure"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "turn_timeout"
	}
	return "processing_failed"
}

func tessaRunBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Duration(1<<(attempt-1)) * time.Second
	if delay > 8*time.Second {
		return 8 * time.Second
	}
	return delay
}

func tessaToolActionKey(index int, request tessa.ToolRequest) string {
	arguments, _ := json.Marshal(request)
	hash := sha256.Sum256(append([]byte(fmt.Sprintf("%d\x00", index)), arguments...))
	return fmt.Sprintf("tool:%d:%s", index, hex.EncodeToString(hash[:12]))
}

func tessaDateRange(fromValue, toValue, timezone string) (time.Time, time.Time, error) {
	location := timezoneLocation(timezone)
	from, err := time.ParseInLocation("2006-01-02", fromValue, location)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := time.ParseInLocation("2006-01-02", toValue, location)
	if err != nil || to.Before(from) {
		return time.Time{}, time.Time{}, errors.New("invalid Tessa date range")
	}
	return from, to.AddDate(0, 0, 1), nil
}

func tessaAvailabilitySlotCount(result TessaAvailabilityResult) int {
	count := 0
	for _, service := range result.Services {
		for _, day := range service.Dates {
			count += len(day.Slots)
		}
	}
	return count
}

func tessaCalendarDays(from, to time.Time) int {
	days := 0
	for current := from; current.Before(to); current = current.AddDate(0, 0, 1) {
		days++
	}
	return days
}

func marshalTessaSafeResult(value any) (json.RawMessage, error) {
	for {
		payload, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if len(payload) <= 4096 {
			return payload, nil
		}
		switch result := value.(type) {
		case TessaBookingSearchResult:
			if len(result.Items) == 0 {
				return nil, errors.New("Tessa tool result exceeds its safe bound")
			}
			result.Items = result.Items[:len(result.Items)-1]
			result.HasMore = true
			value = result
		case TessaBookingAttentionSummary:
			if len(result.Examples) == 0 {
				return nil, errors.New("Tessa tool result exceeds its safe bound")
			}
			result.Examples = result.Examples[:len(result.Examples)-1]
			value = result
		case TessaAvailabilityResult:
			trimmed := false
			for serviceIndex := range result.Services {
				boundedTitle := truncateRunes(strings.TrimSpace(result.Services[serviceIndex].ServiceTitle), 120)
				if boundedTitle != result.Services[serviceIndex].ServiceTitle {
					result.Services[serviceIndex].ServiceTitle = boundedTitle
					trimmed = true
				}
			}
			for serviceIndex := len(result.Services) - 1; serviceIndex >= 0 && !trimmed; serviceIndex-- {
				service := &result.Services[serviceIndex]
				for dayIndex := len(service.Dates) - 1; dayIndex >= 0; dayIndex-- {
					day := &service.Dates[dayIndex]
					if len(day.Slots) == 0 {
						continue
					}
					day.Slots = day.Slots[:len(day.Slots)-1]
					if len(day.Slots) == 0 {
						service.Dates = append(service.Dates[:dayIndex], service.Dates[dayIndex+1:]...)
					}
					trimmed = true
					break
				}
			}
			if !trimmed {
				return nil, errors.New("Tessa tool result exceeds its safe bound")
			}
			result.HasMore = true
			value = result
		case TessaServiceSearchResult:
			for index := range result.Items {
				result.Items[index].Name = truncateTessaToolText(result.Items[index].Name, 120)
				result.Items[index].SectionName = truncateTessaToolText(result.Items[index].SectionName, 80)
			}
			if len(result.Items) == 0 {
				return nil, errors.New("Tessa tool result exceeds its safe bound")
			}
			result.Items = result.Items[:len(result.Items)-1]
			result.HasMore = true
			value = result
		case TessaCustomerSearchResult:
			for index := range result.Items {
				result.Items[index].Name = truncateTessaToolText(result.Items[index].Name, 80)
				result.Items[index].Tier = truncateTessaToolText(result.Items[index].Tier, 40)
				result.Items[index].Status = truncateTessaToolText(result.Items[index].Status, 40)
			}
			if len(result.Items) == 0 {
				return nil, errors.New("Tessa tool result exceeds its safe bound")
			}
			result.Items = result.Items[:len(result.Items)-1]
			result.HasMore = true
			value = result
		case TessaCustomerBookingSummary:
			if len(result.RecentBookings) == 0 {
				return nil, errors.New("Tessa tool result exceeds its safe bound")
			}
			result.RecentBookings = result.RecentBookings[:len(result.RecentBookings)-1]
			value = result
		case TessaReviewSummary:
			for index := range result.Recent {
				result.Recent[index].AuthorName = truncateTessaToolText(result.Recent[index].AuthorName, 80)
				result.Recent[index].ServiceTitle = truncateTessaToolText(result.Recent[index].ServiceTitle, 120)
				result.Recent[index].ReviewExcerpt = truncateTessaToolText(result.Recent[index].ReviewExcerpt, 240)
			}
			if len(result.Recent) == 0 {
				return nil, errors.New("Tessa tool result exceeds its safe bound")
			}
			result.Recent = result.Recent[:len(result.Recent)-1]
			value = result
		default:
			return nil, errors.New("Tessa tool result exceeds its safe bound")
		}
	}
}

func tessaStoredResultCount(toolName string, payload json.RawMessage, fallback int) int {
	switch toolName {
	case "search_bookings", "get_schedule":
		var result TessaBookingSearchResult
		if json.Unmarshal(payload, &result) == nil {
			return len(result.Items)
		}
	case "get_availability":
		var result TessaAvailabilityResult
		if json.Unmarshal(payload, &result) == nil {
			return tessaAvailabilitySlotCount(result)
		}
	case "search_services":
		var result TessaServiceSearchResult
		if json.Unmarshal(payload, &result) == nil {
			return len(result.Items)
		}
	case "search_customers":
		var result TessaCustomerSearchResult
		if json.Unmarshal(payload, &result) == nil {
			return len(result.Items)
		}
	}
	return fallback
}

func tessaToolMetadata(evidence []tessa.Evidence) (TessaMessagePresentation, []TessaEntityReference) {
	actions := []TessaNavigationAction{}
	references := []TessaEntityReference{}
	seenReferences := map[string]struct{}{}
	seenActions := map[string]struct{}{}
	addEntity := func(kind, id, label, routeID, actionLabel string) {
		if id == "" {
			return
		}
		referenceKey := kind + ":" + id
		label = truncateTessaToolText(label, 120)
		if label == "" {
			label = kind
		}
		if _, exists := seenReferences[referenceKey]; !exists && len(references) < tessaEntityReferenceLimit {
			seenReferences[referenceKey] = struct{}{}
			references = append(references, TessaEntityReference{
				Kind: kind, ID: id, Label: label, RouteID: routeID,
			})
		}
		key := routeID + ":" + id
		if len(actions) < 4 {
			if _, exists := seenActions[key]; !exists {
				seenActions[key] = struct{}{}
				actions = append(actions, TessaNavigationAction{RouteID: routeID, EntityID: id, Label: actionLabel})
			}
		}
	}
	addRoute := func(routeID, label string) {
		if _, exists := seenActions[routeID]; exists || len(actions) >= 4 {
			return
		}
		seenActions[routeID] = struct{}{}
		actions = append(actions, TessaNavigationAction{RouteID: routeID, Label: label})
	}
	addBooking := func(item TessaBookingSummary) {
		addEntity("booking", item.BookingID, item.ServiceTitle, "booking_details", "View booking")
	}
	for _, item := range evidence {
		switch item.Tool {
		case "search_bookings", "get_schedule":
			var result TessaBookingSearchResult
			if json.Unmarshal(item.Result, &result) == nil {
				for _, booking := range result.Items {
					addBooking(booking)
				}
				addRoute("bookings", "Open bookings")
			}
		case "get_booking":
			var result struct {
				Found   bool               `json:"found"`
				Booking TessaBookingDetail `json:"booking"`
			}
			if json.Unmarshal(item.Result, &result) == nil && result.Found {
				addBooking(result.Booking.TessaBookingSummary)
			}
		case "get_booking_attention_summary":
			var result TessaBookingAttentionSummary
			if json.Unmarshal(item.Result, &result) == nil {
				for _, booking := range result.Examples {
					addBooking(booking)
				}
				addRoute("bookings", "Open bookings")
			}
		case "search_services":
			var result TessaServiceSearchResult
			if json.Unmarshal(item.Result, &result) == nil {
				for _, service := range result.Items {
					addEntity("service", service.ServiceID, service.Name, "service_details", "View service")
				}
				addRoute("services", "Open services")
			}
		case "get_service":
			var result struct {
				Found   bool               `json:"found"`
				Service TessaServiceDetail `json:"service"`
			}
			if json.Unmarshal(item.Result, &result) == nil && result.Found {
				addEntity("service", result.Service.ServiceID, result.Service.Name, "service_details", "View service")
			}
		case "search_customers":
			var result TessaCustomerSearchResult
			if json.Unmarshal(item.Result, &result) == nil {
				for _, customer := range result.Items {
					addEntity("customer", customer.CustomerID, customer.Name, "customer_details", "View customer")
				}
				addRoute("customers", "Open customers")
			}
		case "get_customer_booking_summary":
			var result struct {
				Found    bool                        `json:"found"`
				Customer TessaCustomerBookingSummary `json:"customer"`
			}
			if json.Unmarshal(item.Result, &result) == nil && result.Found {
				addEntity("customer", result.Customer.Customer.CustomerID, result.Customer.Customer.Name, "customer_details", "View customer")
				for _, booking := range result.Customer.RecentBookings {
					addBooking(booking)
				}
			}
		case "get_booking_payment_status":
			var result struct {
				Found   bool                      `json:"found"`
				Payment TessaBookingPaymentStatus `json:"payment"`
			}
			if json.Unmarshal(item.Result, &result) == nil && result.Found {
				addEntity("booking", result.Payment.BookingID, result.Payment.ServiceTitle, "booking_details", "View booking")
			}
			addRoute("payments", "Open payments")
		case "get_payment_summary":
			addRoute("payments", "Open payments")
		case "get_payout_summary":
			addRoute("payouts", "Open payouts")
		case "get_booking_metrics":
			addRoute("stats", "Open stats")
		case "get_inbox_summary":
			addRoute("inbox", "Open inbox")
		case "get_review_summary":
			addRoute("reviews", "Open reviews")
		case "get_public_profile_status":
			addRoute("business_profile", "Open business profile")
		}
	}
	if len(actions) == 0 {
		return TessaMessagePresentation{}, references
	}
	return TessaMessagePresentation{Kind: "navigation_actions", Version: 1, Actions: actions}, references
}

func timezoneLocation(value string) *time.Location {
	location, err := time.LoadLocation(strings.TrimSpace(value))
	if err != nil {
		location, _ = time.LoadLocation("Africa/Lagos")
	}
	if location == nil {
		return time.UTC
	}
	return location
}
