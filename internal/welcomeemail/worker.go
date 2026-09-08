package welcomeemail

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"booking/go-server/internal/mailer"

	"github.com/google/uuid"
)

const (
	defaultConcurrency  = 2
	claimBatch          = 32
	claimLease          = 90 * time.Second
	finalizationTimeout = 5 * time.Second
)

type WorkerMetrics interface {
	ObserveWelcomeEmailClaim(audience string, latency time.Duration)
	ObserveWelcomeEmailOutcome(audience, outcome string)
}

type Worker struct {
	repository      *Repository
	sender          mailer.Sender
	logger          *slog.Logger
	wake            <-chan struct{}
	metrics         WorkerMetrics
	workerID        string
	concurrency     int
	deliveryTimeout time.Duration
	leaseDuration   time.Duration
}

func NewWorker(repository *Repository, sender mailer.Sender, logger *slog.Logger, wake <-chan struct{}, metrics WorkerMetrics, concurrency int, deliveryTimeout time.Duration) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	if concurrency < 1 || concurrency > 32 {
		concurrency = defaultConcurrency
	}
	if deliveryTimeout <= 0 {
		deliveryTimeout = 30 * time.Second
	}
	leaseDuration := deliveryTimeout + 30*time.Second
	if leaseDuration < claimLease {
		leaseDuration = claimLease
	}
	return &Worker{repository: repository, sender: sender, logger: logger, wake: wake, metrics: metrics,
		workerID: "welcome-email-" + uuid.NewString(), concurrency: concurrency,
		deliveryTimeout: deliveryTimeout, leaseDuration: leaseDuration}
}

func (worker *Worker) Start(ctx context.Context) {
	if worker == nil || worker.repository == nil || worker.sender == nil || !worker.sender.Enabled() {
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

func (worker *Worker) drain(ctx context.Context) {
	for ctx.Err() == nil {
		jobs, err := worker.repository.ClaimJobs(ctx, worker.workerID, claimBatch, worker.leaseDuration)
		if err != nil {
			worker.logger.Error("claim welcome email jobs", "error", err)
			return
		}
		worker.processBatch(ctx, jobs)
		if len(jobs) < claimBatch {
			return
		}
	}
}

func (worker *Worker) processBatch(ctx context.Context, jobs []Job) {
	for start := 0; start < len(jobs); start += worker.concurrency {
		end := min(start+worker.concurrency, len(jobs))
		var group sync.WaitGroup
		group.Add(end - start)
		for _, job := range jobs[start:end] {
			go func(job Job) {
				defer group.Done()
				worker.processOne(ctx, job)
			}(job)
		}
		group.Wait()
	}
}

func (worker *Worker) processOne(ctx context.Context, job Job) {
	latency := worker.repository.now().Sub(job.NextAttemptAt)
	if latency < 0 {
		latency = 0
	}
	worker.observeClaim(job.Audience, latency)
	deliveryContext, cancel := context.WithTimeout(ctx, worker.deliveryTimeout)
	err := worker.sender.Send(deliveryContext, mailer.Message{
		ToEmail: job.RecipientEmail, ToName: job.RecipientName, Subject: job.Subject,
		HTML: job.HTMLBody, Text: job.TextBody,
		MessageID: fmt.Sprintf("<welcome-%s@mail.tellbook.app>", job.ID),
	})
	cancel()
	finalizeContext, finalizeCancel := context.WithTimeout(context.WithoutCancel(ctx), finalizationTimeout)
	defer finalizeCancel()
	if err == nil {
		if err := worker.repository.MarkAccepted(finalizeContext, job); err != nil {
			worker.logger.Error("record accepted welcome email", "job_id", job.ID, "error", err)
			return
		}
		worker.observeOutcome(job.Audience, "accepted")
		return
	}
	disposition, _ := mailer.ClassifyTransportError(err)
	code := "smtp_transient"
	switch disposition {
	case mailer.DispositionPermanent:
		code = "smtp_permanent"
	case mailer.DispositionAmbiguous:
		code = "smtp_outcome_unknown"
	}
	if err := worker.repository.RecordFailure(finalizeContext, job, disposition, code); err != nil {
		worker.logger.Error("record welcome email failure", "job_id", job.ID, "error", err)
		return
	}
	worker.observeOutcome(job.Audience, failureOutcome(disposition, job.AttemptCount))
}

func failureOutcome(disposition mailer.TransportDisposition, attempt int) string {
	switch disposition {
	case mailer.DispositionRetryable:
		if attempt >= maxAttempts {
			return "retry_exhausted"
		}
		return "retry"
	case mailer.DispositionPermanent:
		return "failed"
	default:
		return "manual_review"
	}
}

func (worker *Worker) observeClaim(audience string, latency time.Duration) {
	if worker.metrics != nil {
		worker.metrics.ObserveWelcomeEmailClaim(audience, latency)
	}
}

func (worker *Worker) observeOutcome(audience, outcome string) {
	if worker.metrics != nil {
		worker.metrics.ObserveWelcomeEmailOutcome(audience, outcome)
	}
}
