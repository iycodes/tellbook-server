package notifications

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type PlannerWorker struct {
	repository  *Repository
	logger      *slog.Logger
	workerID    string
	wake        <-chan struct{}
	concurrency int
}

func NewPlannerWorker(repository *Repository, logger *slog.Logger, wake <-chan struct{}, concurrency int) *PlannerWorker {
	if logger == nil {
		logger = slog.Default()
	}
	if concurrency < 1 || concurrency > 32 {
		concurrency = 4
	}
	return &PlannerWorker{
		repository: repository, logger: logger,
		workerID: "notification-planner-" + uuid.NewString(), wake: wake, concurrency: concurrency,
	}
}

func (worker *PlannerWorker) Start(ctx context.Context) {
	if worker == nil || worker.repository == nil {
		return
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-worker.wake:
		case <-timer.C:
		}
		worker.drain(ctx)
		timer.Reset(20 * time.Second)
	}
}

func (worker *PlannerWorker) drain(ctx context.Context) {
	for ctx.Err() == nil {
		events, err := worker.repository.ClaimEventJobs(
			ctx, worker.workerID, defaultClaimBatch, defaultLeaseDuration,
		)
		if err != nil {
			worker.logger.Error("claim notification event jobs", "error", err)
			return
		}
		worker.processEventBatch(ctx, events)
		scopes, err := worker.repository.ClaimScopeJobs(ctx, worker.workerID, 4, defaultLeaseDuration)
		if err != nil {
			worker.logger.Error("claim notification scope jobs", "error", err)
			return
		}
		for _, job := range scopes {
			if err := worker.repository.PlanScopeJob(ctx, job); err != nil {
				worker.failScope(ctx, job, err)
			}
		}
		inAppJobs, err := worker.repository.ClaimInAppJobs(
			ctx, worker.workerID, defaultClaimBatch, defaultLeaseDuration,
		)
		if err != nil {
			worker.logger.Error("claim in-app notification jobs", "error", err)
			return
		}
		for _, job := range inAppJobs {
			if err := worker.repository.CompleteInAppJob(ctx, job); err != nil {
				code := planningErrorCode(err)
				if failErr := worker.repository.FailInAppJob(ctx, job, code); failErr != nil {
					worker.logger.Error("reschedule in-app notification job", "job_id", job.ID, "error", failErr)
				}
			}
		}
		if len(events) < defaultClaimBatch && len(scopes) < 4 && len(inAppJobs) < defaultClaimBatch {
			return
		}
	}
}

func (worker *PlannerWorker) processEventBatch(ctx context.Context, jobs []EventJob) {
	for start := 0; start < len(jobs); start += worker.concurrency {
		end := min(start+worker.concurrency, len(jobs))
		var group sync.WaitGroup
		group.Add(end - start)
		for _, job := range jobs[start:end] {
			go func(job EventJob) {
				defer group.Done()
				if err := worker.repository.PlanEventJob(ctx, job); err != nil {
					worker.failEvent(ctx, job, err)
				}
			}(job)
		}
		group.Wait()
	}
}

func (worker *PlannerWorker) failEvent(ctx context.Context, job EventJob, cause error) {
	code := planningErrorCode(cause)
	if err := worker.repository.FailEventJob(ctx, job, code); err != nil {
		worker.logger.Error("reschedule notification event job", "event_id", job.BookingEventID, "error", err)
		return
	}
	worker.logger.Warn("notification event planning failed", "event_id", job.BookingEventID, "code", code)
}

func (worker *PlannerWorker) failScope(ctx context.Context, job ScopeJob, cause error) {
	code := planningErrorCode(cause)
	if err := worker.repository.FailScopeJob(ctx, job, code); err != nil {
		worker.logger.Error("reschedule notification scope job", "client_id", job.ClientID, "error", err)
		return
	}
	worker.logger.Warn("notification scope planning failed", "client_id", job.ClientID, "code", code)
}

func planningErrorCode(err error) string {
	if err == nil {
		return "planning_failed"
	}
	value := strings.ToLower(err.Error())
	value = strings.Map(func(character rune) rune {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' {
			return character
		}
		return '_'
	}, value)
	for strings.Contains(value, "__") {
		value = strings.ReplaceAll(value, "__", "_")
	}
	return boundedCode(strings.Trim(value, "_"))
}
