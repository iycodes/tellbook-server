package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	aisvc "booking/go-server/internal/ai"
	aiapi "booking/go-server/shared/ai_api"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type InboxAIDraftWorkerConfig struct {
	MaxConcurrency int
	JobTimeout     time.Duration
}

type InboxAIDraftWorker struct {
	repo         *Repository
	ai           *aisvc.Client
	limiter      *InboxAIGenerationLimiter
	logger       *slog.Logger
	config       InboxAIDraftWorkerConfig
	workerPrefix string
}

type inboxAIDraftJob struct {
	ID           uuid.UUID
	ClientID     uuid.UUID
	Request      aiapi.InboxReplyDraftRequest
	AttemptCount int
	MaxAttempts  int
	CreatedAt    time.Time
}

func NewInboxAIDraftWorker(
	repo *Repository,
	ai *aisvc.Client,
	limiter *InboxAIGenerationLimiter,
	logger *slog.Logger,
	config InboxAIDraftWorkerConfig,
) (*InboxAIDraftWorker, error) {
	if repo == nil || repo.db == nil || ai == nil || !ai.DefaultAvailable() || limiter == nil ||
		config.MaxConcurrency < 1 || config.MaxConcurrency > 32 || config.JobTimeout <= 0 {
		return nil, errors.New("inbox ai draft worker configuration is invalid")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &InboxAIDraftWorker{
		repo: repo, ai: ai, limiter: limiter, logger: logger, config: config,
		workerPrefix: "inbox-ai-draft-" + uuid.NewString(),
	}, nil
}

func (worker *InboxAIDraftWorker) Start(ctx context.Context, wakes ...<-chan struct{}) {
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

func (worker *InboxAIDraftWorker) run(ctx context.Context, workerID string, wake <-chan struct{}) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-timer.C:
		}
		if err := worker.failExhausted(ctx); err != nil && !errors.Is(err, context.Canceled) {
			worker.logger.Warn("fail exhausted inbox AI draft jobs", "worker_id", workerID, "error", err)
		}
		for {
			processed, err := worker.processOne(ctx, workerID)
			if err != nil && !errors.Is(err, context.Canceled) {
				worker.logger.Warn("inbox AI draft job failed", "worker_id", workerID, "error", err)
			}
			if !processed || ctx.Err() != nil {
				break
			}
		}
		timer.Reset(worker.nextWakeDelay(ctx))
	}
}

func (worker *InboxAIDraftWorker) failExhausted(ctx context.Context) error {
	_, err := worker.repo.db.Exec(ctx, `
		UPDATE inbox_ai_runs
		SET status='failed',error_code='attempts_exhausted',latency_ms=0,completed_at=NOW(),
			provider_outcome='unavailable',lease_owner='',lease_expires_at=NULL
		WHERE mode='manual' AND attempt_count>=max_attempts AND (
			status='queued' OR (status='processing' AND lease_expires_at<NOW())
		)
	`)
	return err
}

func (worker *InboxAIDraftWorker) processOne(ctx context.Context, workerID string) (bool, error) {
	slot, err := worker.limiter.Acquire(ctx)
	if err != nil || slot == nil {
		return false, err
	}
	defer slot.Release()
	job, err := worker.claim(ctx, workerID)
	if err != nil || job.ID == uuid.Nil {
		return false, err
	}
	startedAt := time.Now()
	jobContext, cancel := context.WithTimeout(ctx, worker.config.JobTimeout)
	response, generationErr := worker.ai.GenerateInboxReplyDraft(jobContext, job.Request)
	cancel()
	latency := time.Since(startedAt)
	if generationErr != nil {
		if job.AttemptCount >= job.MaxAttempts {
			return true, worker.repo.FailProviderInboxAIDraftRun(
				ctx, job.ID, workerID, job.AttemptCount, "generation_failed", latency,
			)
		}
		return true, worker.retry(ctx, workerID, job, "generation_failed")
	}
	return true, worker.repo.CompleteProviderInboxAIDraftRun(
		ctx, job.ID, workerID, job.AttemptCount, response, latency,
	)
}

func (worker *InboxAIDraftWorker) nextWakeDelay(ctx context.Context) time.Duration {
	fallback := time.Duration(20+rand.IntN(11)) * time.Second
	var next time.Time
	err := worker.repo.db.QueryRow(ctx, `
		SELECT COALESCE(MIN(
			CASE WHEN status='processing' THEN lease_expires_at ELSE available_at END
		), NOW()+($1::bigint*INTERVAL '1 millisecond'))
		FROM inbox_ai_runs
		WHERE mode='manual' AND status IN ('queued','processing') AND attempt_count<max_attempts
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

func (worker *InboxAIDraftWorker) claim(ctx context.Context, workerID string) (inboxAIDraftJob, error) {
	tx, err := worker.repo.db.Begin(ctx)
	if err != nil {
		return inboxAIDraftJob{}, err
	}
	defer tx.Rollback(ctx)
	var job inboxAIDraftJob
	var input json.RawMessage
	err = tx.QueryRow(ctx, `
		WITH candidates AS (
			SELECT queued.id
			FROM inbox_ai_runs queued
			WHERE queued.mode='manual' AND queued.available_at<=NOW() AND (
				(queued.status='queued' AND queued.attempt_count<queued.max_attempts)
				OR (queued.status='processing' AND queued.lease_expires_at<NOW()
					AND queued.attempt_count<queued.max_attempts)
			) AND NOT EXISTS (
				SELECT 1 FROM inbox_ai_runs active
				WHERE active.client_id=queued.client_id AND active.mode='manual'
				  AND active.status='processing' AND active.lease_expires_at>=NOW()
				  AND active.id<>queued.id
			)
			ORDER BY queued.available_at,queued.created_at,queued.id
			FOR UPDATE OF queued SKIP LOCKED LIMIT 1
		)
		SELECT run.id,run.client_id,run.input_snapshot,run.attempt_count,run.max_attempts,run.created_at
		FROM inbox_ai_runs run JOIN candidates ON candidates.id=run.id
	`).Scan(&job.ID, &job.ClientID, &input, &job.AttemptCount, &job.MaxAttempts, &job.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return inboxAIDraftJob{}, tx.Commit(ctx)
	}
	if err != nil {
		return inboxAIDraftJob{}, fmt.Errorf("claim inbox ai draft: %w", err)
	}
	if err := json.Unmarshal(input, &job.Request); err != nil {
		return inboxAIDraftJob{}, fmt.Errorf("decode inbox ai draft input: %w", err)
	}
	job.AttemptCount++
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_runs
		SET status='processing',attempt_count=$2,lease_owner=$3,
			lease_expires_at=NOW()+($4::bigint*INTERVAL '1 millisecond')
		WHERE id=$1
	`, job.ID, job.AttemptCount, workerID, (worker.config.JobTimeout + 30*time.Second).Milliseconds()); err != nil {
		return inboxAIDraftJob{}, fmt.Errorf("lease inbox ai draft: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return inboxAIDraftJob{}, err
	}
	return job, nil
}

func (worker *InboxAIDraftWorker) retry(
	ctx context.Context,
	workerID string,
	job inboxAIDraftJob,
	errorCode string,
) error {
	base := 2 * time.Second * time.Duration(1<<min(max(job.AttemptCount-1, 0), 5))
	delay := time.Duration(float64(base) * (0.8 + rand.Float64()*0.4))
	tag, err := worker.repo.db.Exec(ctx, `
		UPDATE inbox_ai_runs
		SET status='queued',available_at=NOW()+($4::bigint*INTERVAL '1 millisecond'),
			lease_owner='',lease_expires_at=NULL
		WHERE id=$1 AND status='processing' AND lease_owner=$2 AND attempt_count=$3
	`, job.ID, workerID, job.AttemptCount, delay.Milliseconds())
	if err != nil {
		return fmt.Errorf("retry inbox ai draft: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New(strings.TrimSpace(errorCode) + ": inbox ai draft lease was lost")
	}
	return nil
}
