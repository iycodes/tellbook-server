package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"booking/go-server/internal/agreements/domain"
	"booking/go-server/internal/agreements/repository"
	aiapi "booking/shared/ai_api"

	"github.com/google/uuid"
)

type GenerationJobStore interface {
	ClaimGenerationJobs(context.Context, string, int, time.Duration) ([]domain.TemplateGenerationJob, error)
	CompleteGenerationJob(context.Context, repository.CompleteGenerationJobParams) error
	FailGenerationJob(context.Context, uuid.UUID, string, string, string, time.Time, bool) error
}

type AgreementDocumentGenerator interface {
	GenerateAgreementDocument(context.Context, aiapi.GenerateAgreementDocumentRequest) (aiapi.GenerateAgreementDocumentResponse, error)
}

type GenerationRequestBuilder interface {
	Build(context.Context, domain.TemplateGenerationJob) (PreparedGenerationRequest, error)
}

type OutputGuard interface {
	Validate(aiapi.GenerateAgreementDocumentResponse) error
}

type PreparedGenerationRequest struct {
	Request aiapi.GenerateAgreementDocumentRequest
	Guard   OutputGuard
}

type GenerationWorker struct {
	store          GenerationJobStore
	generator      AgreementDocumentGenerator
	requestBuilder GenerationRequestBuilder
	logger         *slog.Logger
	workerID       string
	pollInterval   time.Duration
	leaseDuration  time.Duration
	jobTimeout     time.Duration
	batchSize      int
}

type GenerationWorkerConfig struct {
	PollInterval  time.Duration
	LeaseDuration time.Duration
	JobTimeout    time.Duration
	BatchSize     int
}

func NewGenerationWorker(
	store GenerationJobStore,
	generator AgreementDocumentGenerator,
	requestBuilder GenerationRequestBuilder,
	logger *slog.Logger,
	config GenerationWorkerConfig,
) (*GenerationWorker, error) {
	if store == nil || generator == nil || requestBuilder == nil {
		return nil, errors.New("agreement generation worker dependencies are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if config.PollInterval <= 0 {
		config.PollInterval = time.Second
	}
	if config.JobTimeout <= 0 {
		config.JobTimeout = 3 * time.Minute
	}
	if config.LeaseDuration < config.JobTimeout+30*time.Second {
		config.LeaseDuration = config.JobTimeout + 30*time.Second
	}
	if config.BatchSize <= 0 || config.BatchSize > 10 {
		config.BatchSize = 5
	}
	return &GenerationWorker{
		store: store, generator: generator, requestBuilder: requestBuilder, logger: logger,
		workerID: "agreement-generation-" + uuid.NewString(), pollInterval: config.PollInterval,
		leaseDuration: config.LeaseDuration, jobTimeout: config.JobTimeout, batchSize: config.BatchSize,
	}, nil
}

func (w *GenerationWorker) Start(ctx context.Context) {
	if w == nil {
		return
	}
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

func (w *GenerationWorker) RunOnce(ctx context.Context) {
	jobs, err := w.store.ClaimGenerationJobs(ctx, w.workerID, w.batchSize, w.leaseDuration)
	if err != nil {
		w.logger.Error("claim agreement generation jobs failed", "error", err)
		return
	}
	for _, job := range jobs {
		jobCtx, cancel := context.WithTimeout(ctx, w.jobTimeout)
		err := w.process(jobCtx, job)
		cancel()
		if err == nil {
			continue
		}
		jobErr := classifyGenerationError(err)
		retryAt := time.Now().UTC().Add(generationRetryDelay(job.AttemptCount))
		if failErr := w.store.FailGenerationJob(
			ctx,
			job.ID,
			w.workerID,
			jobErr.Code,
			jobErr.Error(),
			retryAt,
			jobErr.Permanent,
		); failErr != nil {
			w.logger.Error("fail agreement generation job", "job_id", job.ID, "error", failErr)
			continue
		}
		w.logger.Warn(
			"agreement generation job failed",
			"job_id", job.ID,
			"attempt", job.AttemptCount,
			"error_code", jobErr.Code,
			"permanent", jobErr.Permanent,
		)
	}
}

func (w *GenerationWorker) process(ctx context.Context, job domain.TemplateGenerationJob) error {
	prepared, err := w.requestBuilder.Build(ctx, job)
	if err != nil {
		return err
	}
	if prepared.Guard == nil {
		return permanentGenerationError("invalid_generation_policy", "agreement generation output guard is missing")
	}
	if prepared.Request.ConfirmationMethod != job.ConfirmationMethod.AIAPIValue() {
		return permanentGenerationError("confirmation_method_mismatch", "prepared confirmation method does not match the stored template family")
	}
	if err := prepared.Request.Validate(); err != nil {
		return permanentGenerationError("invalid_generation_input", err.Error())
	}
	response, err := w.generator.GenerateAgreementDocument(ctx, prepared.Request)
	if err != nil {
		return transientGenerationError("ai_request_failed", err.Error())
	}
	if !response.IsServiceAgreement {
		message := strings.TrimSpace(response.Reason)
		if message == "" {
			message = "The uploaded document was not recognized as a service agreement."
		}
		return permanentGenerationError("not_service_agreement", message)
	}
	if strings.TrimSpace(response.DocumentType) != "service_agreement" {
		return permanentGenerationError("invalid_ai_output", "AI returned an unsupported document type")
	}
	response.SuggestedTitle = strings.TrimSpace(response.SuggestedTitle)
	response.SuggestedDescription = strings.TrimSpace(response.SuggestedDescription)
	if response.SuggestedTitle == "" || len([]rune(response.SuggestedTitle)) > 160 || len([]rune(response.SuggestedDescription)) > 500 {
		return permanentGenerationError("invalid_ai_output", "AI returned invalid agreement metadata")
	}
	response.Warnings, err = normalizeGenerationWarnings(response.Warnings)
	if err != nil {
		return permanentGenerationError("invalid_ai_output", err.Error())
	}
	if response.DocumentSchema == nil {
		return permanentGenerationError("invalid_ai_output", "AI returned no agreement document")
	}
	if err := prepared.Guard.Validate(response); err != nil {
		return permanentGenerationError("unsafe_ai_output", err.Error())
	}
	document, err := domain.FinalizeGeneratedDocument(
		*response.DocumentSchema,
		job.ConfirmationMethod,
		domain.AgreementVariableKeySet(),
	)
	if err != nil {
		return permanentGenerationError("invalid_ai_output", err.Error())
	}
	if err := w.store.CompleteGenerationJob(ctx, repository.CompleteGenerationJobParams{
		JobID:                job.ID,
		WorkerID:             w.workerID,
		SuggestedTitle:       response.SuggestedTitle,
		SuggestedDescription: response.SuggestedDescription,
		Document:             document,
		Warnings:             response.Warnings,
	}); err != nil {
		if errors.Is(err, repository.ErrLeaseLost) {
			return permanentGenerationError("job_lease_lost", err.Error())
		}
		return transientGenerationError("store_generated_document_failed", err.Error())
	}
	return nil
}

func normalizeGenerationWarnings(warnings []aiapi.Warning) ([]aiapi.Warning, error) {
	if len(warnings) > 20 {
		return nil, errors.New("AI returned too many review warnings")
	}
	result := make([]aiapi.Warning, 0, len(warnings))
	seen := make(map[string]struct{}, len(warnings))
	for _, warning := range warnings {
		warning.Code = strings.TrimSpace(warning.Code)
		warning.Message = strings.TrimSpace(warning.Message)
		if warning.Code == "" || warning.Message == "" || len(warning.Code) > 80 || len([]rune(warning.Message)) > 500 {
			return nil, errors.New("AI returned an invalid review warning")
		}
		key := warning.Code + "\x00" + warning.Message
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, warning)
	}
	return result, nil
}

type GenerationError struct {
	Code      string
	Permanent bool
	err       error
}

func (e *GenerationError) Error() string {
	return e.err.Error()
}

func (e *GenerationError) Unwrap() error {
	return e.err
}

func permanentGenerationError(code, message string) error {
	return &GenerationError{Code: code, Permanent: true, err: errors.New(message)}
}

func transientGenerationError(code, message string) error {
	return &GenerationError{Code: code, err: errors.New(message)}
}

func classifyGenerationError(err error) *GenerationError {
	var generationErr *GenerationError
	if errors.As(err, &generationErr) {
		return generationErr
	}
	return &GenerationError{Code: "generation_failed", err: fmt.Errorf("agreement generation failed: %w", err)}
}

func generationRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Duration(1<<(attempt-1)) * 5 * time.Second
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}
