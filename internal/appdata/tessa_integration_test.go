package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"booking/go-server/internal/aierror"
	"booking/go-server/internal/tessa"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const tessaTestConfigHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type tessaIntegrationGenerator struct {
	mu    sync.Mutex
	calls int
}

type tessaSynthesisFailureGenerator struct{}

type tessaScheduleGenerator struct{}

type tessaRetryGenerator struct {
	mu           sync.Mutex
	planning     int
	synthesizing int
}

func (generator *tessaRetryGenerator) GenerateJSON(
	_ context.Context,
	_, _ string,
	destination any,
) error {
	generator.mu.Lock()
	defer generator.mu.Unlock()
	switch value := destination.(type) {
	case *tessa.Plan:
		generator.planning++
		query := "booking availability"
		if generator.planning > 1 {
			query = "a regenerated plan that must not run"
		}
		*value = tessa.Plan{
			Scope: "in_scope", Intent: "booking_help", AnswerMode: "tools",
			Tools: []tessa.ToolRequest{{Name: "search_tellbook_help", Query: query}},
		}
		return nil
	case *tessa.Answer:
		generator.synthesizing++
		if generator.synthesizing == 1 {
			return aierror.Transient("test Tessa synthesis", aierror.KindUnavailable, errors.New("unavailable"))
		}
		value.Parts = []tessa.AnswerPart{{Text: "Tellbook uses your availability settings when calculating booking times."}}
		return nil
	default:
		return fmt.Errorf("unexpected Tessa destination %T", destination)
	}
}

func (generator *tessaRetryGenerator) callCounts() (int, int) {
	generator.mu.Lock()
	defer generator.mu.Unlock()
	return generator.planning, generator.synthesizing
}

func (tessaScheduleGenerator) GenerateJSON(
	_ context.Context,
	_, prompt string,
	destination any,
) error {
	switch value := destination.(type) {
	case *tessa.Plan:
		*value = tessa.Plan{
			Scope: "in_scope", Intent: "schedule", AnswerMode: "tools",
			Tools: []tessa.ToolRequest{{Name: "get_schedule", Period: "custom", TimeScope: "period", From: "2026-09-01", To: "2026-09-01"}},
		}
	case *tessa.Answer:
		var input tessa.SynthesisInput
		if err := json.Unmarshal([]byte(prompt[strings.Index(prompt, "{"):]), &input); err != nil {
			return err
		}
		var result TessaBookingSearchResult
		if err := json.Unmarshal(input.Evidence[0].Result, &result); err != nil {
			return err
		}
		for _, booking := range result.Items {
			value.Parts = append(value.Parts, tessa.AnswerPart{EntityID: booking.BookingID, Text: "You have one consultation on your schedule."})
		}
	default:
		return fmt.Errorf("unexpected Tessa destination %T", destination)
	}
	return nil
}

func (tessaSynthesisFailureGenerator) GenerateJSON(
	_ context.Context,
	_, _ string,
	destination any,
) error {
	switch value := destination.(type) {
	case *tessa.Plan:
		*value = tessa.Plan{
			Scope: "in_scope", Intent: "booking_help", AnswerMode: "tools",
			Tools: []tessa.ToolRequest{{Name: "search_tellbook_help", Query: "booking availability"}},
		}
		return nil
	case *tessa.Answer:
		return aierror.Transient("test Tessa synthesis", aierror.KindUnavailable, errors.New("unavailable"))
	default:
		return fmt.Errorf("unexpected Tessa destination %T", destination)
	}
}

func (generator *tessaIntegrationGenerator) GenerateJSON(
	_ context.Context,
	_, _ string,
	destination any,
) error {
	generator.mu.Lock()
	defer generator.mu.Unlock()
	generator.calls++
	switch value := destination.(type) {
	case *tessa.Plan:
		*value = tessa.Plan{
			Scope: "in_scope", Intent: "booking_help", AnswerMode: "tools",
			Tools: []tessa.ToolRequest{{Name: "search_tellbook_help", Query: "booking availability"}},
		}
	case *tessa.Answer:
		value.Parts = []tessa.AnswerPart{{Text: "Tellbook uses your availability settings when calculating booking times."}}
	default:
		return fmt.Errorf("unexpected Tessa destination %T", destination)
	}
	return nil
}

func TestTessaRepositoryIdempotencyIsolationAndWorkerCompletion(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	clientID := insertTessaTestClient(t, ctx, pool)
	otherClientID := insertTessaTestClient(t, ctx, pool)
	repo := NewRepository(pool)

	if err := repo.CompleteTessaIntroduction(ctx, clientID, "test-v1", "test-v1"); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteTessaIntroduction(ctx, otherClientID, "test-v1", "test-v1"); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := repo.GetTessaBootstrap(ctx, clientID, "test-v1", 50)
	if err != nil || bootstrap.Thread == nil {
		t.Fatalf("bootstrap thread = %+v, error = %v", bootstrap.Thread, err)
	}
	threadID := uuid.MustParse(bootstrap.Thread.ID)
	messageRequestID := uuid.New()
	type sendResult struct {
		response TessaSendMessageResponse
		err      error
	}
	start := make(chan struct{})
	results := make(chan sendResult, 2)
	for range 2 {
		go func() {
			<-start
			response, sendErr := repo.SendTessaMessage(
				ctx, clientID, threadID, messageRequestID, "How does availability work?",
				"self_hosted", "test", tessaTestConfigHash, "test-v1",
			)
			results <- sendResult{response: response, err: sendErr}
		}()
	}
	close(start)
	firstResult, replayResult := <-results, <-results
	if firstResult.err != nil || replayResult.err != nil {
		t.Fatalf("concurrent send errors = %v / %v", firstResult.err, replayResult.err)
	}
	first, replay := firstResult.response, replayResult.response
	if replay.Message.ID != first.Message.ID || replay.Run.ID != first.Run.ID {
		t.Fatalf("concurrent send responses = %+v / %+v", first, replay)
	}
	if _, err := repo.SendTessaMessage(
		ctx, clientID, threadID, messageRequestID, "Changed content",
		"self_hosted", "test", tessaTestConfigHash, "test-v1",
	); !errors.Is(err, ErrTessaIdempotencyConflict) {
		t.Fatalf("changed replay error = %v, want idempotency conflict", err)
	}
	if _, err := repo.SendTessaMessage(
		ctx, otherClientID, threadID, uuid.New(), "Cross-tenant message",
		"self_hosted", "test", tessaTestConfigHash, "test-v1",
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant send error = %v, want not found", err)
	}

	generator := &tessaIntegrationGenerator{}
	service, err := tessa.NewService(tessa.Provider{
		Name: "self_hosted", Model: "test", Generator: generator, Timeout: time.Second,
	}, nil, 12000)
	if err != nil {
		t.Fatal(err)
	}
	help, err := tessa.LoadHelpIndex()
	if err != nil {
		t.Fatal(err)
	}
	limiter := NewInboxAIGenerationLimiter(1)
	heldSlot, err := limiter.TryAcquire(ctx)
	if err != nil || heldSlot == nil {
		t.Fatalf("hold inference slot = %+v, error = %v", heldSlot, err)
	}
	worker, err := NewTessaWorker(
		repo, service, help, limiter, slog.Default(),
		TessaWorkerConfig{MaxConcurrency: 1, TurnTimeout: 5 * time.Second, ConfigHash: tessaTestConfigHash, NoticeRevision: "test-v1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(ctx, "tessa-capacity"); err != nil || !processed {
		t.Fatalf("capacity defer processed = %v, error = %v", processed, err)
	}
	var startedEvents, attemptCount int
	var deferredStatus string
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM tessa_events WHERE run_id=$1 AND event_type='run.started'),
			attempt_count,status
		FROM tessa_runs WHERE id=$1
	`, uuid.MustParse(first.Run.ID)).Scan(&startedEvents, &attemptCount, &deferredStatus); err != nil {
		t.Fatal(err)
	}
	if startedEvents != 0 || attemptCount != 0 || deferredStatus != "queued" {
		t.Fatalf("deferred run events/attempt/status = %d/%d/%s", startedEvents, attemptCount, deferredStatus)
	}
	heldSlot.Release()
	if _, err := pool.Exec(ctx, `UPDATE tessa_runs SET available_at=NOW() WHERE id=$1`, uuid.MustParse(first.Run.ID)); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(ctx, "tessa-integration"); err != nil || !processed {
		t.Fatalf("worker processed = %v, error = %v", processed, err)
	}
	completed, err := repo.GetTessaBootstrap(ctx, clientID, "test-v1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if completed.CurrentRun != nil || len(completed.Messages) != 2 || completed.Messages[1].SenderType != "tessa" {
		t.Fatalf("completed bootstrap = %+v", completed)
	}
	var messageCount, runCount, answerCount, toolStepCount int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM tessa_messages WHERE client_id=$1),
			(SELECT COUNT(*) FROM tessa_runs WHERE client_id=$1),
			(SELECT COUNT(*) FROM tessa_messages WHERE client_id=$1 AND sender_type='tessa'),
			(SELECT COUNT(*) FROM tessa_run_steps step INNER JOIN tessa_runs run ON run.id=step.run_id
			 WHERE run.client_id=$1 AND step.stage='tool' AND step.status='succeeded')
	`, clientID).Scan(&messageCount, &runCount, &answerCount, &toolStepCount); err != nil {
		t.Fatal(err)
	}
	if messageCount != 2 || runCount != 1 || answerCount != 1 || toolStepCount != 1 {
		t.Fatalf("message/run/answer/tool counts = %d/%d/%d/%d", messageCount, runCount, answerCount, toolStepCount)
	}

	firstReplacementID, secondReplacementID := uuid.New(), uuid.New()
	firstReplacement, err := repo.CreateTessaThread(ctx, clientID, firstReplacementID, "test-v1")
	if err != nil {
		t.Fatal(err)
	}
	secondReplacement, err := repo.CreateTessaThread(ctx, clientID, secondReplacementID, "test-v1")
	if err != nil {
		t.Fatal(err)
	}
	replayedReplacement, err := repo.CreateTessaThread(ctx, clientID, firstReplacementID, "test-v1")
	if err != nil {
		t.Fatal(err)
	}
	if firstReplacement.ID == secondReplacement.ID || replayedReplacement.ID != secondReplacement.ID || replayedReplacement.Status != "active" {
		t.Fatalf("replacement replay = %+v, first = %+v, active = %+v", replayedReplacement, firstReplacement, secondReplacement)
	}
	archivedReplay, err := repo.SendTessaMessage(
		ctx, clientID, threadID, messageRequestID, "How does availability work?",
		"self_hosted", "test", tessaTestConfigHash, "test-v1",
	)
	if err != nil || archivedReplay.Message.ID != first.Message.ID {
		t.Fatalf("archived send replay = %+v, error = %v", archivedReplay, err)
	}
}

func TestTessaRepositoryRequiresCurrentNoticeBeforeMutation(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	clientID := insertTessaTestClient(t, ctx, pool)
	repo := NewRepository(pool)
	if err := repo.CompleteTessaIntroduction(ctx, clientID, "notice-v1", "notice-v1"); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := repo.GetTessaBootstrap(ctx, clientID, "notice-v2", 50)
	if err != nil || bootstrap.Thread == nil || !bootstrap.Preferences.NoticeRequired {
		t.Fatalf("bootstrap = %+v, error = %v", bootstrap, err)
	}
	threadID := uuid.MustParse(bootstrap.Thread.ID)
	if _, err := repo.SendTessaMessage(
		ctx, clientID, threadID, uuid.New(), "Can Tessa process this?",
		"self_hosted", "test", tessaTestConfigHash, "notice-v2",
	); !errors.Is(err, ErrTessaNoticeRevision) {
		t.Fatalf("stale-notice send error = %v", err)
	}
	if _, err := repo.CreateTessaThread(ctx, clientID, uuid.New(), "notice-v2"); !errors.Is(err, ErrTessaNoticeRevision) {
		t.Fatalf("stale-notice thread error = %v", err)
	}
	var messages, runs int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM tessa_messages WHERE client_id=$1),
			(SELECT COUNT(*) FROM tessa_runs WHERE client_id=$1)
	`, clientID).Scan(&messages, &runs); err != nil {
		t.Fatal(err)
	}
	if messages != 0 || runs != 0 {
		t.Fatalf("stale notice persisted messages/runs = %d/%d", messages, runs)
	}
}

func TestTessaWorkerFencesStaleNoticeAndConfiguration(t *testing.T) {
	for _, fixture := range []struct {
		name, workerNotice, workerHash, expectedCode string
	}{
		{name: "notice changed", workerNotice: "test-v2", workerHash: tessaTestConfigHash, expectedCode: "notice_changed"},
		{name: "configuration changed", workerNotice: "test-v1", workerHash: strings.Repeat("b", 64), expectedCode: "config_changed"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx, pool := openTessaIntegrationPool(t)
			clientID := insertTessaTestClient(t, ctx, pool)
			repo := NewRepository(pool)
			threadID, runID := enqueueTessaIntegrationRun(t, ctx, repo, clientID)
			generator := &tessaIntegrationGenerator{}
			worker := newTessaIntegrationWorkerWithConfig(
				t, repo, generator, nil, fixture.workerHash, fixture.workerNotice,
			)
			if processed, err := worker.ProcessOne(ctx, "tessa-fence"); err != nil || !processed {
				t.Fatalf("processed = %v, error = %v", processed, err)
			}
			var status, code string
			var answerCount int
			if err := pool.QueryRow(ctx, `
				SELECT run.status,run.error_code,
					(SELECT COUNT(*) FROM tessa_messages WHERE thread_id=$2 AND sender_type='tessa')
				FROM tessa_runs run WHERE run.id=$1
			`, runID, threadID).Scan(&status, &code, &answerCount); err != nil {
				t.Fatal(err)
			}
			generator.mu.Lock()
			calls := generator.calls
			generator.mu.Unlock()
			if status != "failed" || code != fixture.expectedCode || answerCount != 0 || calls != 0 {
				t.Fatalf("status/code/answers/model calls = %s/%s/%d/%d", status, code, answerCount, calls)
			}
		})
	}
}

func TestTessaConversationCompactsOlderMessagesAndBoundsLiveContext(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	clientID := insertTessaTestClient(t, ctx, pool)
	repo := NewRepository(pool)
	if err := repo.CompleteTessaIntroduction(ctx, clientID, "test-v1", "test-v1"); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := repo.GetTessaBootstrap(ctx, clientID, "test-v1", 50)
	if err != nil || bootstrap.Thread == nil {
		t.Fatalf("bootstrap = %+v, error = %v", bootstrap, err)
	}
	threadID := uuid.MustParse(bootstrap.Thread.ID)
	worker := newTessaIntegrationWorker(t, repo, &tessaIntegrationGenerator{}, nil)
	for index := 0; index < 7; index++ {
		response, sendErr := repo.SendTessaMessage(
			ctx, clientID, threadID, uuid.New(), fmt.Sprintf("Availability follow-up %d", index+1),
			"self_hosted", "test", tessaTestConfigHash, "test-v1",
		)
		if sendErr != nil {
			t.Fatal(sendErr)
		}
		if processed, processErr := worker.ProcessOne(ctx, fmt.Sprintf("tessa-summary-%d", index)); processErr != nil || !processed {
			t.Fatalf("turn %d processed = %v, error = %v", index, processed, processErr)
		}
		var contextHash string
		if err := pool.QueryRow(ctx, `SELECT context_hash FROM tessa_runs WHERE id=$1`, uuid.MustParse(response.Run.ID)).Scan(&contextHash); err != nil {
			t.Fatal(err)
		}
		if len(contextHash) != 64 {
			t.Fatalf("turn %d context hash = %q", index, contextHash)
		}
	}
	var summary string
	var summaryThrough int64
	if err := pool.QueryRow(ctx, `
		SELECT summary,summary_through_sequence FROM tessa_threads WHERE id=$1
	`, threadID).Scan(&summary, &summaryThrough); err != nil {
		t.Fatal(err)
	}
	if summaryThrough == 0 || summary == "" || len(summary) > tessaSummaryMaximumBytes ||
		!strings.Contains(summary, "Provider:") || !strings.Contains(summary, "Tessa:") {
		t.Fatalf("summary through=%d bytes=%d value=%q", summaryThrough, len(summary), summary)
	}
	response, err := repo.SendTessaMessage(
		ctx, clientID, threadID, uuid.New(), "And what about next week?",
		"self_hosted", "test", tessaTestConfigHash, "test-v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	question, _, loadedSummary, recent, err := worker.loadContext(ctx, tessaClaimedRun{
		ID: uuid.MustParse(response.Run.ID), ThreadID: threadID, ClientID: clientID,
		TriggerMessageID: uuid.MustParse(response.Message.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if question != "And what about next week?" || loadedSummary != summary || len(recent) != tessaRecentContextLimit {
		t.Fatalf("context question=%q summary match=%v recent=%d", question, loadedSummary == summary, len(recent))
	}
}

func TestTessaRetentionPrunesEventsAndArchivedThreadsAndExpiresCursor(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	clientID := insertTessaTestClient(t, ctx, pool)
	repo := NewRepository(pool)
	if err := repo.CompleteTessaIntroduction(ctx, clientID, "test-v1", "test-v1"); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := repo.GetTessaBootstrap(ctx, clientID, "test-v1", 50)
	if err != nil || bootstrap.Thread == nil {
		t.Fatalf("bootstrap = %+v, error = %v", bootstrap, err)
	}
	archivedThreadID := uuid.MustParse(bootstrap.Thread.ID)
	if _, err := repo.CreateTessaThread(ctx, clientID, uuid.New(), "test-v1"); err != nil {
		t.Fatal(err)
	}
	var cursorSequence int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),0) FROM tessa_events WHERE client_id=$1`, clientID).Scan(&cursorSequence); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-100 * 24 * time.Hour)
	if _, err := pool.Exec(ctx, `UPDATE tessa_threads SET archived_at=$2 WHERE id=$1`, archivedThreadID, old); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tessa_events SET created_at=$2 WHERE client_id=$1`, clientID, old); err != nil {
		t.Fatal(err)
	}
	events, threads, err := maintainTessaRetention(
		ctx, pool, time.Now().UTC().Add(-tessaEventRetentionAge),
		time.Now().UTC().Add(-tessaArchivedThreadRetention), tessaRetentionBatch,
	)
	if err != nil {
		t.Fatal(err)
	}
	if events == 0 || threads != 1 {
		t.Fatalf("deleted events/threads = %d/%d", events, threads)
	}
	var archivedExists, activeExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tessa_threads WHERE id=$1)`, archivedThreadID).Scan(&archivedExists); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tessa_threads WHERE client_id=$1 AND status='active')`, clientID).Scan(&activeExists); err != nil {
		t.Fatal(err)
	}
	if archivedExists || !activeExists {
		t.Fatalf("archived exists=%v active exists=%v", archivedExists, activeExists)
	}
	drain, err := repo.ListTessaEventsAfter(ctx, clientID, encodeInboxSequenceCursor(cursorSequence), 50)
	if err != nil || !drain.Reset {
		t.Fatalf("expired cursor drain = %+v, error = %v", drain, err)
	}
}

func TestTessaWorkerFencesCancelledLease(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	clientID := insertTessaTestClient(t, ctx, pool)
	repo := NewRepository(pool)
	threadID, runID := enqueueTessaIntegrationRun(t, ctx, repo, clientID)
	worker := newTessaIntegrationWorker(t, repo, &tessaIntegrationGenerator{}, nil)
	claimed, err := worker.claim(ctx, "tessa-stale-worker")
	if err != nil || claimed.ID != runID {
		t.Fatalf("claimed run = %+v, error = %v", claimed, err)
	}
	if err := worker.markStarted(ctx, "tessa-stale-worker", claimed); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CancelTessaRun(ctx, clientID, runID); err != nil {
		t.Fatal(err)
	}
	if err := worker.process(ctx, "tessa-stale-worker", claimed); err == nil {
		t.Fatal("cancelled worker published without a lease error")
	}
	var answers int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM tessa_messages
		WHERE thread_id=$1 AND sender_type='tessa'
	`, threadID).Scan(&answers); err != nil {
		t.Fatal(err)
	}
	if answers != 0 {
		t.Fatalf("cancelled worker published %d answers", answers)
	}
}

func TestTessaWorkerFallsBackAfterCommittedEvidence(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	clientID := insertTessaTestClient(t, ctx, pool)
	repo := NewRepository(pool)
	_, runID := enqueueTessaIntegrationRun(t, ctx, repo, clientID)
	fallback := &tessaIntegrationGenerator{}
	worker := newTessaIntegrationWorker(t, repo, tessaSynthesisFailureGenerator{}, fallback)
	if processed, err := worker.ProcessOne(ctx, "tessa-fallback-worker"); err != nil || !processed {
		t.Fatalf("fallback worker processed = %v, error = %v", processed, err)
	}
	var fallbackUsed bool
	var finalProvider string
	var toolSteps int
	if err := pool.QueryRow(ctx, `
		SELECT run.fallback_used,run.final_provider,
			(SELECT COUNT(*) FROM tessa_run_steps step
			 WHERE step.run_id=run.id AND step.stage='tool' AND step.status='succeeded')
		FROM tessa_runs run WHERE run.id=$1
	`, runID).Scan(&fallbackUsed, &finalProvider, &toolSteps); err != nil {
		t.Fatal(err)
	}
	if !fallbackUsed || finalProvider != "hosted" || toolSteps != 1 {
		t.Fatalf("fallback/provider/tool steps = %v/%s/%d", fallbackUsed, finalProvider, toolSteps)
	}
}

func TestTessaWorkerRetryReplaysCommittedPlanAndEvidence(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	clientID := insertTessaTestClient(t, ctx, pool)
	repo := NewRepository(pool)
	_, runID := enqueueTessaIntegrationRun(t, ctx, repo, clientID)
	generator := &tessaRetryGenerator{}
	worker := newTessaIntegrationWorker(t, repo, generator, nil)

	if processed, err := worker.ProcessOne(ctx, "tessa-retry-first"); err == nil || !processed {
		t.Fatalf("first attempt processed = %v, error = %v; want a queued transient failure", processed, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tessa_runs SET available_at=NOW() WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(ctx, "tessa-retry-second"); err != nil || !processed {
		t.Fatalf("retry processed = %v, error = %v", processed, err)
	}

	planningCalls, synthesisCalls := generator.callCounts()
	var status string
	var planningSteps, toolSteps, answerCount int
	if err := pool.QueryRow(ctx, `
		SELECT run.status,
			(SELECT COUNT(*) FROM tessa_run_steps WHERE run_id=run.id AND stage='planning'),
			(SELECT COUNT(*) FROM tessa_run_steps WHERE run_id=run.id AND stage='tool'),
			(SELECT COUNT(*) FROM tessa_messages WHERE run_id=run.id AND sender_type='tessa')
		FROM tessa_runs run WHERE run.id=$1
	`, runID).Scan(&status, &planningSteps, &toolSteps, &answerCount); err != nil {
		t.Fatal(err)
	}
	if planningCalls != 1 || synthesisCalls != 2 || status != "completed" ||
		planningSteps != 1 || toolSteps != 1 || answerCount != 1 {
		t.Fatalf(
			"planning/synthesis/status/steps/answers = %d/%d/%s/%d:%d/%d",
			planningCalls, synthesisCalls, status, planningSteps, toolSteps, answerCount,
		)
	}
}

func TestTessaWorkerRetryKeepsCommittedSynthesisFallback(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	clientID := insertTessaTestClient(t, ctx, pool)
	repo := NewRepository(pool)
	_, runID := enqueueTessaIntegrationRun(t, ctx, repo, clientID)
	fallback := &tessaRetryGenerator{}
	worker := newTessaIntegrationWorker(t, repo, tessaSynthesisFailureGenerator{}, fallback)

	if processed, err := worker.ProcessOne(ctx, "tessa-fallback-retry-first"); err == nil || !processed {
		t.Fatalf("first attempt processed = %v, error = %v; want a queued fallback failure", processed, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tessa_runs SET available_at=NOW() WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(ctx, "tessa-fallback-retry-second"); err != nil || !processed {
		t.Fatalf("retry processed = %v, error = %v", processed, err)
	}

	planningCalls, synthesisCalls := fallback.callCounts()
	var status, finalProvider, fallbackReason string
	var fallbackUsed bool
	if err := pool.QueryRow(ctx, `
		SELECT status,final_provider,fallback_used,fallback_reason FROM tessa_runs WHERE id=$1
	`, runID).Scan(&status, &finalProvider, &fallbackUsed, &fallbackReason); err != nil {
		t.Fatal(err)
	}
	if planningCalls != 0 || synthesisCalls != 2 || status != "completed" ||
		finalProvider != "hosted" || !fallbackUsed || fallbackReason == "" {
		t.Fatalf(
			"fallback planning/synthesis/status/provider/used/reason = %d/%d/%s/%s/%v/%q",
			planningCalls, synthesisCalls, status, finalProvider, fallbackUsed, fallbackReason,
		)
	}
}

func enqueueTessaIntegrationRun(
	t *testing.T,
	ctx context.Context,
	repo *Repository,
	clientID uuid.UUID,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	if err := repo.CompleteTessaIntroduction(ctx, clientID, "test-v1", "test-v1"); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := repo.GetTessaBootstrap(ctx, clientID, "test-v1", 50)
	if err != nil || bootstrap.Thread == nil {
		t.Fatalf("bootstrap thread = %+v, error = %v", bootstrap.Thread, err)
	}
	threadID := uuid.MustParse(bootstrap.Thread.ID)
	response, err := repo.SendTessaMessage(
		ctx, clientID, threadID, uuid.New(), "How does availability work?",
		"self_hosted", "test", tessaTestConfigHash, "test-v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	return threadID, uuid.MustParse(response.Run.ID)
}

func newTessaIntegrationWorker(
	t *testing.T,
	repo *Repository,
	primaryGenerator interface {
		GenerateJSON(context.Context, string, string, any) error
	},
	fallbackGenerator interface {
		GenerateJSON(context.Context, string, string, any) error
	},
) *TessaWorker {
	return newTessaIntegrationWorkerWithConfig(
		t, repo, primaryGenerator, fallbackGenerator, tessaTestConfigHash, "test-v1",
	)
}

func newTessaIntegrationWorkerWithConfig(
	t *testing.T,
	repo *Repository,
	primaryGenerator interface {
		GenerateJSON(context.Context, string, string, any) error
	},
	fallbackGenerator interface {
		GenerateJSON(context.Context, string, string, any) error
	},
	configHash, noticeRevision string,
) *TessaWorker {
	t.Helper()
	primary := tessa.Provider{
		Name: "self_hosted", Model: "test", Generator: primaryGenerator, Timeout: time.Second,
	}
	var fallback *tessa.Provider
	if fallbackGenerator != nil {
		fallback = &tessa.Provider{
			Name: "hosted", Model: "fallback-test", Generator: fallbackGenerator, Timeout: time.Second,
		}
	}
	service, err := tessa.NewService(primary, fallback, 12000)
	if err != nil {
		t.Fatal(err)
	}
	help, err := tessa.LoadHelpIndex()
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewTessaWorker(
		repo, service, help, NewInboxAIGenerationLimiter(1), slog.Default(),
		TessaWorkerConfig{MaxConcurrency: 1, TurnTimeout: 5 * time.Second, ConfigHash: configHash, NoticeRevision: noticeRevision},
	)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func openTessaIntegrationPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}

func insertTessaTestClient(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	clientID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO clients (id,full_name,email,password_hash,created_at,updated_at)
		VALUES ($1,'Tessa Integration',$2,'test-only',NOW(),NOW())
	`, clientID, "tessa-integration-"+clientID.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM clients WHERE id=$1`, clientID)
	})
	return clientID
}

func TestTessaBookingReadsAreTenantScopedAndContactFree(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	clientID := insertTessaTestClient(t, ctx, pool)
	otherClientID := insertTessaTestClient(t, ctx, pool)
	customerID, bookingID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO customers (id,client_id,full_name,email,phone)
		VALUES ($1,$2,'Ada Test','private@example.com','+2348000000000')
	`, customerID, clientID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bookings (
			id,client_id,customer_id,title,status,payment_status,agreement_status,
			start_at,end_at,occupied_start_at,occupied_end_at,currency_code,country_code,notes
		) VALUES (
			$3,$2,$1,'Consultation','booked','paid_in_full','not_required',
			'2026-09-01 09:00:00+00','2026-09-01 10:00:00+00',
			'2026-09-01 09:00:00+00','2026-09-01 10:00:00+00','NGN','NG','private note'
		)
	`, customerID, clientID, bookingID); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(pool)
	detail, err := repo.GetTessaBooking(ctx, clientID, bookingID)
	if err != nil || detail.BookingID != bookingID.String() || detail.CustomerName != "Ada Test" {
		t.Fatalf("detail = %+v, error = %v", detail, err)
	}
	if _, err := repo.GetTessaBooking(ctx, otherClientID, bookingID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant error = %v, want not found", err)
	}
	from, to, _ := tessaDateRange("2026-09-01", "2026-09-01", "Africa/Lagos")
	checkedAt := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	day, err := repo.SearchTessaBookings(ctx, clientID, from, to, TessaBookingFilter{}, 8, false)
	if err != nil || len(day.Items) != 1 {
		t.Fatal(day, err)
	}
	next, err := repo.SearchTessaBookings(ctx, clientID, from, to, TessaBookingFilter{StartsNotBefore: &checkedAt}, 8, false)
	if err != nil || len(next.Items) != 0 {
		t.Fatal("past start returned as upcoming", next, err)
	}
	payload, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	for _, privateValue := range []string{"private@example.com", "+2348000000000", "private note"} {
		if strings.Contains(encoded, privateValue) {
			t.Fatalf("safe detail leaked %q: %s", privateValue, encoded)
		}
	}
	customers, err := repo.SearchTessaCustomers(ctx, clientID, "Ada", 8)
	if err != nil || len(customers.Items) != 1 || customers.Items[0].CustomerID != customerID.String() {
		t.Fatalf("customer search = %+v, error = %v", customers, err)
	}
	customerSummary, err := repo.GetTessaCustomerBookingSummary(ctx, clientID, customerID)
	if err != nil || customerSummary.TotalBookings != 1 {
		t.Fatalf("customer summary = %+v, error = %v", customerSummary, err)
	}
	if _, err := repo.GetTessaCustomerBookingSummary(ctx, otherClientID, customerID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant customer summary error = %v, want not found", err)
	}
	payload, err = json.Marshal(struct {
		Search  TessaCustomerSearchResult   `json:"search"`
		Summary TessaCustomerBookingSummary `json:"summary"`
	}{customers, customerSummary})
	if err != nil {
		t.Fatal(err)
	}
	encoded = string(payload)
	for _, privateValue := range []string{"private@example.com", "+2348000000000", "private note"} {
		if strings.Contains(encoded, privateValue) {
			t.Fatalf("safe customer evidence leaked %q: %s", privateValue, encoded)
		}
	}
}

func TestTessaScheduleToolPersistsSafeNavigationEvidence(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	clientID := insertTessaTestClient(t, ctx, pool)
	customerID, bookingID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO customers (id,client_id,full_name,email,phone)
		VALUES ($1,$2,'Ada Test','private@example.com','+2348000000000')
	`, customerID, clientID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bookings (
			id,client_id,customer_id,title,status,payment_status,agreement_status,
			start_at,end_at,occupied_start_at,occupied_end_at,currency_code,country_code,notes
		) VALUES (
			$3,$2,$1,'Consultation','booked','paid_in_full','not_required',
			'2026-09-01 09:00:00+00','2026-09-01 10:00:00+00',
			'2026-09-01 09:00:00+00','2026-09-01 10:00:00+00','NGN','NG','private note'
		)
	`, customerID, clientID, bookingID); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(pool)
	if err := repo.CompleteTessaIntroduction(ctx, clientID, "test-v1", "test-v1"); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := repo.GetTessaBootstrap(ctx, clientID, "test-v1", 50)
	if err != nil || bootstrap.Thread == nil {
		t.Fatalf("bootstrap = %+v, error = %v", bootstrap, err)
	}
	response, err := repo.SendTessaMessage(
		ctx, clientID, uuid.MustParse(bootstrap.Thread.ID), uuid.New(), "What is my schedule?",
		"self_hosted", "test", tessaTestConfigHash, "test-v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	worker := newTessaIntegrationWorker(t, repo, tessaScheduleGenerator{}, nil)
	if processed, err := worker.ProcessOne(ctx, "tessa-schedule"); err != nil || !processed {
		t.Fatalf("processed = %v, error = %v", processed, err)
	}
	completed, err := repo.GetTessaBootstrap(ctx, clientID, "test-v1", 50)
	if err != nil || len(completed.Messages) != 2 {
		t.Fatalf("completed = %+v, error = %v", completed, err)
	}
	answer := completed.Messages[1]
	if answer.Presentation.Kind != "navigation_actions" || len(answer.EntityReferences) != 1 ||
		answer.EntityReferences[0].ID != bookingID.String() {
		t.Fatalf("answer navigation = %+v / %+v", answer.Presentation, answer.EntityReferences)
	}
	followup, err := repo.SendTessaMessage(ctx, clientID, uuid.MustParse(bootstrap.Thread.ID), uuid.New(), "Has that booking been paid?", "self_hosted", "test", tessaTestConfigHash, "test-v1")
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, recent, err := worker.loadContext(ctx, tessaClaimedRun{ClientID: clientID, ThreadID: uuid.MustParse(bootstrap.Thread.ID), TriggerMessageID: uuid.MustParse(followup.Message.ID)})
	if err != nil || len(recent) != 2 || len(recent[1].References) != 1 || recent[1].References[0].ID != bookingID.String() {
		t.Fatal("follow-up lost committed booking reference", recent, err)
	}
	previous, err := worker.loadPreviousTessaTools(ctx, tessaClaimedRun{ClientID: clientID, ThreadID: uuid.MustParse(bootstrap.Thread.ID), TriggerMessageID: uuid.MustParse(followup.Message.ID)})
	if err != nil || len(previous) != 1 || previous[0].Name != "get_schedule" {
		t.Fatal("follow-up lost exact query scope", previous, err)
	}
	isolated, err := worker.loadPreviousTessaTools(ctx, tessaClaimedRun{ClientID: uuid.New(), ThreadID: uuid.MustParse(bootstrap.Thread.ID), TriggerMessageID: uuid.MustParse(followup.Message.ID)})
	if err != nil || len(isolated) != 0 {
		t.Fatal("previous query scope crossed tenants", isolated, err)
	}
	before, err := worker.loadPreviousTessaTools(ctx, tessaClaimedRun{ClientID: clientID, ThreadID: uuid.MustParse(bootstrap.Thread.ID), TriggerMessageID: uuid.MustParse(response.Message.ID)})
	if err != nil || len(before) != 0 {
		t.Fatal("future answer changed an earlier run's query context", before, err)
	}
	var safeResult string
	if err := pool.QueryRow(ctx, `
		SELECT safe_result::text FROM tessa_run_steps
		WHERE run_id=$1 AND stage='tool'
	`, uuid.MustParse(response.Run.ID)).Scan(&safeResult); err != nil {
		t.Fatal(err)
	}
	for _, privateValue := range []string{"private@example.com", "+2348000000000", "private note"} {
		if strings.Contains(safeResult, privateValue) {
			t.Fatalf("safe evidence leaked %q: %s", privateValue, safeResult)
		}
	}
}

func TestTessaOperationalReadsReconcileWithCanonicalProviderData(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	clientID := insertTessaTestClient(t, ctx, pool)
	otherClientID := insertTessaTestClient(t, ctx, pool)
	handle := "tessa-operations-" + strings.ToLower(strings.ReplaceAll(clientID.String(), "-", ""))[:12]
	if _, err := pool.Exec(ctx, `
		INSERT INTO client_profile_handles (handle_slug,client_id) VALUES ($2,$1)
	`, clientID, handle); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO client_profiles (
			client_id,business_name,handle_slug,category,headline,short_bio,public_location_label,
			city,region,timezone,country_code,currency_code,locale,market_configured_at,marketplace_enabled
		) VALUES ($1,'Tessa Operations',$2,'Consulting','Book a consultation','Focused service',
			'Lagos','Lagos','Lagos','Africa/Lagos','NG','NGN','en-NG',NOW(),TRUE)
	`, clientID, handle); err != nil {
		t.Fatal(err)
	}
	locationID, serviceID, customerID, bookingID, reviewID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO business_locations (
			id,client_id,label,formatted_address,address_source,resolution_status,timezone,is_primary,is_active
		) VALUES ($1,$2,'Studio','Lagos, Nigeria','manual','text_only','Africa/Lagos',TRUE,TRUE)
	`, locationID, clientID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO services (
			id,client_id,title,slug,description,duration_minutes,price_amount_minor,is_active,status,
			currency_code,fulfillment_mode,agreement_timing,standalone_signature_required
		) VALUES ($2,$1,'Operations Consultation','operations-consultation','A focused planning session',
			60,2500000,TRUE,'published','NGN','virtual',NULL,FALSE)
	`, clientID, serviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO customers (id,client_id,full_name,email,phone)
		VALUES ($1,$2,'Ada Operations','private-operations@example.com','+2348111111111')
	`, customerID, clientID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bookings (
			id,client_id,customer_id,service_id,title,status,payment_status,agreement_status,
			start_at,end_at,occupied_start_at,occupied_end_at,currency_code,country_code,notes,
			base_service_amount_minor,discounted_service_amount_minor,total_amount_minor
		) VALUES ($4,$1,$2,$3,'Operations Consultation','completed','paid_in_full','not_required',
			'2026-08-15 09:00:00+00','2026-08-15 10:00:00+00','2026-08-15 09:00:00+00',
			'2026-08-15 10:00:00+00','NGN','NG','private operations note',2500000,2500000,2500000)
	`, clientID, customerID, serviceID, bookingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO provider_reviews (
			id,client_id,customer_id,booking_id,service_id,author_name,rating,review_text,status,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,'Ada Operations',5,'Very helpful session.','approved',
			'2026-08-16 12:00:00+00','2026-08-16 12:00:00+00')
	`, reviewID, clientID, customerID, bookingID, serviceID); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(pool)
	services, err := repo.SearchTessaServices(ctx, clientID, "Operations", "published", 8)
	if err != nil || len(services.Items) != 1 || services.Items[0].ServiceID != serviceID.String() {
		t.Fatalf("services = %+v, error = %v", services, err)
	}
	if _, err := repo.GetTessaService(ctx, otherClientID, serviceID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant service error = %v, want not found", err)
	}
	from, to, err := tessaDateRange("2026-08-01", "2026-08-31", "Africa/Lagos")
	if err != nil {
		t.Fatal(err)
	}
	payment, err := repo.GetTessaPaymentSummary(ctx, clientID, from, to)
	if err != nil || !payment.Configured || payment.CurrencyCode != "NGN" {
		t.Fatalf("payment summary = %+v, error = %v", payment, err)
	}
	payout, err := repo.GetTessaPayoutSummary(ctx, clientID, from, to)
	if err != nil || !payout.MarketConfigured || payout.CurrencyCode != "NGN" ||
		payout.From != "2026-08-01" || payout.To != "2026-08-31" || payout.DestinationConfigured {
		t.Fatalf("payout summary = %+v, error = %v", payout, err)
	}
	metrics, err := repo.GetTessaBookingMetrics(ctx, clientID, from, to, true)
	if err != nil || !metrics.Configured || metrics.Current.Metrics.TotalBookings != 1 ||
		metrics.Current.Metrics.CompletedBookings != 1 || metrics.Previous == nil || metrics.Delta == nil {
		t.Fatalf("booking metrics = %+v, error = %v", metrics, err)
	}
	inbox, err := repo.GetTessaInboxSummary(ctx, clientID)
	if err != nil || inbox.OpenConversations != 0 || inbox.UnreadMessages != 0 {
		t.Fatalf("inbox summary = %+v, error = %v", inbox, err)
	}
	reviews, err := repo.GetTessaReviewSummary(ctx, clientID, from, to)
	if err != nil || reviews.Count != 1 || reviews.Rating != 5 || len(reviews.Recent) != 1 {
		t.Fatalf("review summary = %+v, error = %v", reviews, err)
	}
	profile, err := repo.GetTessaPublicProfileStatus(ctx, clientID)
	if err != nil || !profile.MarketplaceEnabled || profile.PublishedServices != 1 ||
		profile.BusinessLocations != 1 || profile.PublicPath != "/p/"+handle {
		t.Fatalf("profile status = %+v, error = %v", profile, err)
	}
	payload, err := json.Marshal(struct {
		Services TessaServiceSearchResult `json:"services"`
		Metrics  TessaBookingMetrics      `json:"metrics"`
		Reviews  TessaReviewSummary       `json:"reviews"`
	}{services, metrics, reviews})
	if err != nil {
		t.Fatal(err)
	}
	for _, privateValue := range []string{"private-operations@example.com", "+2348111111111", "private operations note"} {
		if strings.Contains(string(payload), privateValue) {
			t.Fatalf("operational evidence leaked %q: %s", privateValue, payload)
		}
	}
}
