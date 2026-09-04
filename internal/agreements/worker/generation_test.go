package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"booking/go-server/internal/agreements/domain"
	"booking/go-server/internal/agreements/repository"
	aiapi "booking/go-server/shared/ai_api"

	"github.com/google/uuid"
)

type generationStoreFake struct {
	jobs      []domain.TemplateGenerationJob
	completed *repository.CompleteGenerationJobParams
	failed    bool
	permanent bool
	failCode  string
}

type wakeGenerationStore struct {
	job       domain.TemplateGenerationJob
	claims    atomic.Int32
	firstPoll chan struct{}
	completed chan struct{}
}

type scheduledGenerationStore struct {
	*wakeGenerationStore
	delay time.Duration
}

func (store *scheduledGenerationStore) NextGenerationJobDelay(context.Context, time.Duration) time.Duration {
	return store.delay
}

func (s *wakeGenerationStore) ClaimGenerationJobs(context.Context, string, int, time.Duration) ([]domain.TemplateGenerationJob, error) {
	if s.claims.Add(1) == 1 {
		close(s.firstPoll)
		return nil, nil
	}
	if s.job.ID == uuid.Nil {
		return nil, nil
	}
	job := s.job
	s.job = domain.TemplateGenerationJob{}
	return []domain.TemplateGenerationJob{job}, nil
}

func (s *wakeGenerationStore) CompleteGenerationJob(context.Context, repository.CompleteGenerationJobParams) error {
	close(s.completed)
	return nil
}

func (*wakeGenerationStore) FailGenerationJob(context.Context, uuid.UUID, string, string, string, time.Time, bool) error {
	return nil
}

func (s *generationStoreFake) ClaimGenerationJobs(context.Context, string, int, time.Duration) ([]domain.TemplateGenerationJob, error) {
	jobs := s.jobs
	s.jobs = nil
	return jobs, nil
}

func (s *generationStoreFake) CompleteGenerationJob(_ context.Context, params repository.CompleteGenerationJobParams) error {
	s.completed = &params
	return nil
}

func (s *generationStoreFake) FailGenerationJob(_ context.Context, _ uuid.UUID, _ string, code, _ string, _ time.Time, permanent bool) error {
	s.failed = true
	s.permanent = permanent
	s.failCode = code
	return nil
}

type generationBuilderFake struct {
	prepared PreparedGenerationRequest
}

func (b generationBuilderFake) Build(context.Context, domain.TemplateGenerationJob) (PreparedGenerationRequest, error) {
	return b.prepared, nil
}

type generationGeneratorFake struct {
	response aiapi.GenerateAgreementDocumentResponse
	err      error
}

func (g generationGeneratorFake) GenerateAgreementDocument(context.Context, aiapi.GenerateAgreementDocumentRequest) (aiapi.GenerateAgreementDocumentResponse, error) {
	return g.response, g.err
}

func TestGenerationWorkerCompletesValidatedDocument(t *testing.T) {
	job := generationWorkerJob(t)
	store := &generationStoreFake{jobs: []domain.TemplateGenerationJob{job}}
	worker, err := NewGenerationWorker(
		store,
		generationGeneratorFake{response: generatedWorkerResponse()},
		generationBuilderFake{prepared: preparedWorkerRequest()},
		nil,
		GenerationWorkerConfig{},
	)
	if err != nil {
		t.Fatalf("NewGenerationWorker() error = %v", err)
	}
	worker.RunOnce(context.Background())
	if store.completed == nil || store.failed {
		t.Fatalf("completed = %+v, failed = %v", store.completed, store.failed)
	}
	if len(store.completed.Document.Blocks) != 2 || store.completed.Document.Blocks[0].ID == "" {
		t.Fatalf("stored document was not finalized: %+v", store.completed.Document)
	}
}

func TestGenerationWorkerRunsImmediatelyOnWake(t *testing.T) {
	store := &wakeGenerationStore{
		job: generationWorkerJob(t), firstPoll: make(chan struct{}), completed: make(chan struct{}),
	}
	worker, err := NewGenerationWorker(
		store,
		generationGeneratorFake{response: generatedWorkerResponse()},
		generationBuilderFake{prepared: preparedWorkerRequest()},
		nil,
		GenerationWorkerConfig{PollInterval: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan struct{}, 1)
	go worker.Start(ctx, wake)
	select {
	case <-store.firstPoll:
	case <-time.After(time.Second):
		t.Fatal("worker did not perform its initial safety poll")
	}
	wake <- struct{}{}
	select {
	case <-store.completed:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("worker did not run when notified")
	}
}

func TestGenerationWorkerWakesWhenScheduledRetryBecomesDue(t *testing.T) {
	base := &wakeGenerationStore{
		job: generationWorkerJob(t), firstPoll: make(chan struct{}), completed: make(chan struct{}),
	}
	store := &scheduledGenerationStore{wakeGenerationStore: base, delay: 20 * time.Millisecond}
	worker, err := NewGenerationWorker(
		store,
		generationGeneratorFake{response: generatedWorkerResponse()},
		generationBuilderFake{prepared: preparedWorkerRequest()},
		nil,
		GenerationWorkerConfig{PollInterval: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Start(ctx)
	select {
	case <-store.firstPoll:
	case <-time.After(time.Second):
		t.Fatal("worker did not perform its initial claim")
	}
	select {
	case <-store.completed:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("worker waited for its safety poll instead of the scheduled retry")
	}
}

func TestGenerationWorkerRetriesTransientProviderFailure(t *testing.T) {
	store := &generationStoreFake{jobs: []domain.TemplateGenerationJob{generationWorkerJob(t)}}
	worker, err := NewGenerationWorker(
		store,
		generationGeneratorFake{err: errors.New("provider unavailable")},
		generationBuilderFake{prepared: preparedWorkerRequest()},
		nil,
		GenerationWorkerConfig{},
	)
	if err != nil {
		t.Fatalf("NewGenerationWorker() error = %v", err)
	}
	worker.RunOnce(context.Background())
	if !store.failed || store.permanent || store.failCode != "ai_request_failed" {
		t.Fatalf("failure = %v permanent=%v code=%q", store.failed, store.permanent, store.failCode)
	}
}

func TestGenerationWorkerPermanentlyRejectsInvalidOutput(t *testing.T) {
	response := generatedWorkerResponse()
	response.DocumentSchema.Blocks[1].Method = aiapi.AgreementSignature
	store := &generationStoreFake{jobs: []domain.TemplateGenerationJob{generationWorkerJob(t)}}
	worker, err := NewGenerationWorker(
		store,
		generationGeneratorFake{response: response},
		generationBuilderFake{prepared: preparedWorkerRequest()},
		nil,
		GenerationWorkerConfig{},
	)
	if err != nil {
		t.Fatalf("NewGenerationWorker() error = %v", err)
	}
	worker.RunOnce(context.Background())
	if !store.failed || !store.permanent || store.failCode != "invalid_ai_output" {
		t.Fatalf("failure = %v permanent=%v code=%q", store.failed, store.permanent, store.failCode)
	}
}

func generationWorkerJob(t *testing.T) domain.TemplateGenerationJob {
	t.Helper()
	input, err := domain.EncodeGenerationInput(domain.GenerationInputFields, domain.FieldsGenerationInput{
		BusinessCategory: "Beauty",
		ServiceName:      "Lashes",
	})
	if err != nil {
		t.Fatalf("EncodeGenerationInput() error = %v", err)
	}
	return domain.TemplateGenerationJob{
		ID: uuid.New(), ClientID: uuid.New(), FamilyID: uuid.New(), VersionID: uuid.New(),
		ConfirmationMethod: domain.ConfirmationMethodConfirmation,
		InputKind:          domain.GenerationInputFields, InputJSON: json.RawMessage(input),
		Status: domain.JobStatusProcessing, AttemptCount: 1, MaxAttempts: 3,
	}
}

func preparedWorkerRequest() PreparedGenerationRequest {
	return PreparedGenerationRequest{
		Request: aiapi.GenerateAgreementDocumentRequest{
			Source:             aiapi.AgreementGenerationFromFields,
			SchemaVersion:      aiapi.AgreementDocumentSchemaVersion,
			ConfirmationMethod: aiapi.AgreementConfirmation,
			SupportedVariables: []aiapi.SupportedAgreementVariable{{Key: "BUSINESS_NAME"}},
			BusinessCategory:   "Beauty",
			ServiceName:        "Lashes",
		},
		Guard: StandardOutputGuard{},
	}
}

func generatedWorkerResponse() aiapi.GenerateAgreementDocumentResponse {
	return aiapi.GenerateAgreementDocumentResponse{
		IsServiceAgreement: true,
		DocumentType:       "service_agreement",
		SuggestedTitle:     "Lash Agreement",
		DocumentSchema: &aiapi.GeneratedDocumentSchema{
			SchemaVersion: aiapi.AgreementDocumentSchemaVersion,
			Blocks: []aiapi.GeneratedAgreementDocumentBlock{
				{Type: aiapi.AgreementBlockParagraph, Content: []aiapi.AgreementInlineNode{{Type: aiapi.AgreementInlineText, Text: "Terms apply."}}},
				{Type: aiapi.AgreementBlockAcceptance, Method: aiapi.AgreementConfirmation},
			},
		},
	}
}
