package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	aisvc "booking/go-server/internal/ai"
	"booking/go-server/internal/aierror"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type bookingLinkSemiPilotDecider struct {
	serviceID uuid.UUID
}

func (decider bookingLinkSemiPilotDecider) GenerateSemiPilotTurnDecision(
	_ context.Context,
	input aisvc.SemiPilotTurnInput,
) (aisvc.SemiPilotTurnDecision, error) {
	if len(input.ToolResults) == 0 {
		arguments, _ := json.Marshal(aisvc.SemiPilotBookingLinkArguments{
			ServiceID: decider.serviceID.String(),
		})
		return aisvc.SemiPilotTurnDecision{
			ProtocolVersion: aisvc.SemiPilotProtocolVersion,
			NextState:       "service_identified",
			ToolCall: &aisvc.SemiPilotToolCall{
				Name: "get_booking_link", Arguments: arguments,
			},
			MissingFacts: []string{},
		}, nil
	}
	return aisvc.SemiPilotTurnDecision{
		ProtocolVersion: aisvc.SemiPilotProtocolVersion,
		Reply:           "You can continue with the booking link below.",
		NextState:       "link_sent",
		MissingFacts:    []string{},
	}, nil
}

type blockingSemiPilotDecider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type retryingBookingLinkDecider struct {
	serviceID uuid.UUID
	mu        sync.Mutex
	calls     int
}

type handoffAfterLinkDecider struct {
	serviceID uuid.UUID
}

type retryRehydrationDecider struct {
	serviceID uuid.UUID
	mu        sync.Mutex
	calls     int
}

type repeatedServiceLookupDecider struct{}

type failingSemiPilotDecider struct {
	mu    sync.Mutex
	calls int
}

type scriptedAutopilotDecider struct{}

func (scriptedAutopilotDecider) GenerateSemiPilotTurnDecision(
	context.Context,
	aisvc.SemiPilotTurnInput,
) (aisvc.SemiPilotTurnDecision, error) {
	return aisvc.SemiPilotTurnDecision{}, errors.New("semi-pilot generation was used for autopilot")
}

func (scriptedAutopilotDecider) GenerateAutopilotTurnDecision(
	_ context.Context,
	input aisvc.SemiPilotTurnInput,
) (aisvc.SemiPilotTurnDecision, error) {
	if len(input.ToolResults) == 0 {
		arguments, _ := json.Marshal(aisvc.SemiPilotListRelevantServicesArguments{Query: ""})
		return aisvc.SemiPilotTurnDecision{
			ProtocolVersion: aisvc.SemiPilotProtocolVersion,
			NextState:       input.CurrentState,
			ToolCall: &aisvc.SemiPilotToolCall{
				Name: "list_relevant_services", Arguments: arguments,
			},
			MissingFacts: []string{},
		}, nil
	}
	return aisvc.SemiPilotTurnDecision{
		ProtocolVersion: aisvc.SemiPilotProtocolVersion,
		Reply:           "I found the provider’s current services. Use Book here when you’re ready to choose one and see live times.",
		NextState:       input.CurrentState,
		MissingFacts:    []string{},
	}, nil
}

func (decider *retryRehydrationDecider) GenerateSemiPilotTurnDecision(
	_ context.Context,
	input aisvc.SemiPilotTurnInput,
) (aisvc.SemiPilotTurnDecision, error) {
	decider.mu.Lock()
	defer decider.mu.Unlock()
	decider.calls++
	if decider.calls == 1 {
		arguments, _ := json.Marshal(aisvc.SemiPilotBookingLinkArguments{
			ServiceID: decider.serviceID.String(),
		})
		return aisvc.SemiPilotTurnDecision{
			ProtocolVersion: aisvc.SemiPilotProtocolVersion,
			NextState:       "service_identified",
			ToolCall: &aisvc.SemiPilotToolCall{
				Name: "get_booking_link", Arguments: arguments,
			},
			MissingFacts: []string{},
		}, nil
	}
	if decider.calls == 2 {
		return aisvc.SemiPilotTurnDecision{}, aierror.Transient("call model", aierror.KindUnavailable, nil)
	}
	if len(input.ToolResults) != 1 || input.ToolResults[0].Name != "get_booking_link" {
		return aisvc.SemiPilotTurnDecision{}, errors.New("persisted booking-link result was not restored")
	}
	return aisvc.SemiPilotTurnDecision{
		ProtocolVersion: aisvc.SemiPilotProtocolVersion,
		Reply:           "Continue with the restored booking link below.",
		NextState:       "link_sent",
		MissingFacts:    []string{},
	}, nil
}

func (repeatedServiceLookupDecider) GenerateSemiPilotTurnDecision(
	_ context.Context,
	input aisvc.SemiPilotTurnInput,
) (aisvc.SemiPilotTurnDecision, error) {
	if len(input.ToolResults) < 2 {
		query := "makeup"
		if len(input.ToolResults) == 1 {
			query = "bridal makeup"
		}
		arguments, _ := json.Marshal(aisvc.SemiPilotListRelevantServicesArguments{Query: query})
		return aisvc.SemiPilotTurnDecision{
			ProtocolVersion: aisvc.SemiPilotProtocolVersion,
			NextState:       "qualifying",
			ToolCall: &aisvc.SemiPilotToolCall{
				Name: "list_relevant_services", Arguments: arguments,
			},
			MissingFacts: []string{},
		}, nil
	}
	return aisvc.SemiPilotTurnDecision{
		ProtocolVersion: aisvc.SemiPilotProtocolVersion,
		Reply:           "I need one more detail before I choose the service.",
		NextState:       "qualifying",
		MissingFacts:    []string{},
	}, nil
}

func (decider *failingSemiPilotDecider) GenerateSemiPilotTurnDecision(
	context.Context,
	aisvc.SemiPilotTurnInput,
) (aisvc.SemiPilotTurnDecision, error) {
	decider.mu.Lock()
	decider.calls++
	decider.mu.Unlock()
	return aisvc.SemiPilotTurnDecision{}, aierror.Transient("call model", aierror.KindUnavailable, nil)
}

func (decider handoffAfterLinkDecider) GenerateSemiPilotTurnDecision(
	_ context.Context,
	input aisvc.SemiPilotTurnInput,
) (aisvc.SemiPilotTurnDecision, error) {
	if len(input.ToolResults) == 0 {
		arguments, _ := json.Marshal(aisvc.SemiPilotBookingLinkArguments{
			ServiceID: decider.serviceID.String(),
		})
		return aisvc.SemiPilotTurnDecision{
			ProtocolVersion: aisvc.SemiPilotProtocolVersion,
			NextState:       "service_identified",
			ToolCall: &aisvc.SemiPilotToolCall{
				Name: "get_booking_link", Arguments: arguments,
			},
			MissingFacts: []string{},
		}, nil
	}
	return safeSemiPilotHandoffDecision(
		"I’ll hand this over to the provider.", "unsupported_request",
	), nil
}

func (decider *retryingBookingLinkDecider) GenerateSemiPilotTurnDecision(
	_ context.Context,
	input aisvc.SemiPilotTurnInput,
) (aisvc.SemiPilotTurnDecision, error) {
	decider.mu.Lock()
	defer decider.mu.Unlock()
	decider.calls++
	if decider.calls == 2 {
		return aisvc.SemiPilotTurnDecision{}, aierror.Transient("call model", aierror.KindUnavailable, nil)
	}
	if len(input.ToolResults) == 0 {
		arguments, _ := json.Marshal(aisvc.SemiPilotBookingLinkArguments{
			ServiceID: decider.serviceID.String(),
		})
		return aisvc.SemiPilotTurnDecision{
			ProtocolVersion: aisvc.SemiPilotProtocolVersion,
			NextState:       "service_identified",
			ToolCall: &aisvc.SemiPilotToolCall{
				Name: "get_booking_link", Arguments: arguments,
			},
			MissingFacts: []string{},
		}, nil
	}
	return aisvc.SemiPilotTurnDecision{
		ProtocolVersion: aisvc.SemiPilotProtocolVersion,
		Reply:           "Continue with the booking link below.",
		NextState:       "link_sent",
		MissingFacts:    []string{},
	}, nil
}

func (decider *blockingSemiPilotDecider) GenerateSemiPilotTurnDecision(
	ctx context.Context,
	_ aisvc.SemiPilotTurnInput,
) (aisvc.SemiPilotTurnDecision, error) {
	decider.once.Do(func() { close(decider.started) })
	select {
	case <-ctx.Done():
		return aisvc.SemiPilotTurnDecision{}, ctx.Err()
	case <-decider.release:
	}
	return aisvc.SemiPilotTurnDecision{
		ProtocolVersion: aisvc.SemiPilotProtocolVersion,
		Reply:           "This stale reply must never be committed.",
		NextState:       "qualifying",
		MissingFacts:    []string{},
	}, nil
}

type inboxAITestPolicySnapshot struct {
	exists          bool
	defaultMode     string
	enabledServices []uuid.UUID
	paused          bool
	maxTurns        int
	inactivity      int
	revision        int64
	updatedBy       uuid.UUID
	createdAt       time.Time
	updatedAt       time.Time
}

func TestInboxAISemiPilotWorkerSendsCanonicalBookingCardWithoutCreatingBooking(t *testing.T) {
	ctx, pool := openInboxAISemiPilotTestPool(t)
	clientID, serviceID := pickInboxAISemiPilotService(t, ctx, pool)
	repo, conversationID, marketplaceCustomerID, restore := configureInboxAISemiPilotTest(
		t, ctx, pool, clientID, serviceID,
	)
	defer restore()

	var bookingsBefore int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM bookings WHERE client_id=$1`, clientID).Scan(
		&bookingsBefore,
	); err != nil {
		t.Fatal(err)
	}
	response, err := repo.SendMarketplaceMessage(
		ctx, marketplaceCustomerID, conversationID, uuid.New(),
		"I’m ready to book this service.", uuid.NullUUID{},
	)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewInboxAISemiPilotWorker(
		repo,
		bookingLinkSemiPilotDecider{serviceID: serviceID},
		NewInboxAIGenerationLimiter(1),
		nil,
		InboxAISemiPilotWorkerConfig{
			ModelProvider: "test", ModelName: "scripted",
			ModelConfigHash: strings.Repeat("a", 64), MaxConcurrency: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.ProcessOne(ctx, "booking-link-test-worker")
	if err != nil || !processed {
		t.Fatalf("process semi-pilot turn: processed=%v error=%v", processed, err)
	}
	detail, err := repo.GetMarketplaceConversationDetail(
		ctx, marketplaceCustomerID, conversationID, 20,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Messages) != 2 {
		t.Fatalf("messages=%d, want customer plus AI reply", len(detail.Messages))
	}
	aiMessage := detail.Messages[1]
	if aiMessage.SenderType != "ai" ||
		!strings.HasPrefix(aiMessage.Content, "I’m Tellbook AI") ||
		aiMessage.Presentation == nil || aiMessage.Presentation.Kind != "booking_link" {
		t.Fatalf("unexpected AI booking-link message: %#v", aiMessage)
	}
	var card struct {
		ServiceID string `json:"service_id"`
		Href      string `json:"href"`
	}
	if err := json.Unmarshal(aiMessage.Presentation.Data, &card); err != nil {
		t.Fatal(err)
	}
	if card.ServiceID != serviceID.String() || !strings.Contains(card.Href, serviceID.String()) {
		t.Fatalf("booking card=%+v, want canonical service %s", card, serviceID)
	}
	var bookingsAfter int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM bookings WHERE client_id=$1`, clientID).Scan(
		&bookingsAfter,
	); err != nil {
		t.Fatal(err)
	}
	if bookingsAfter != bookingsBefore {
		t.Fatalf("semi-pilot created a booking: before=%d after=%d", bookingsBefore, bookingsAfter)
	}
	var jobStatus, runStatus, sessionState string
	var triggerSequence, processedSequence int64
	var resultingMessageID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT job.status, run.status, session.state, job.trigger_message_sequence,
			session.last_processed_message_sequence, run.resulting_message_id
		FROM inbox_ai_turn_jobs job
		INNER JOIN inbox_ai_runs run ON run.turn_job_id=job.id
		INNER JOIN inbox_ai_sessions session ON session.id=job.session_id
		WHERE job.trigger_message_id=$1
	`, uuid.MustParse(response.Message.ID)).Scan(
		&jobStatus, &runStatus, &sessionState, &triggerSequence,
		&processedSequence, &resultingMessageID,
	); err != nil {
		t.Fatal(err)
	}
	if jobStatus != "completed" || runStatus != "completed" || sessionState != "link_sent" ||
		triggerSequence != processedSequence || resultingMessageID.String() != aiMessage.ID {
		t.Fatalf(
			"turn lineage status=%s run=%s session=%s trigger=%d processed=%d result=%s",
			jobStatus, runStatus, sessionState, triggerSequence, processedSequence, resultingMessageID,
		)
	}
}

func TestInboxAIAutopilotCustomerMessageRunsLiveBoundedTurn(t *testing.T) {
	ctx, pool := openInboxAISemiPilotTestPool(t)
	clientID, serviceID := pickInboxAISemiPilotService(t, ctx, pool)
	repo, conversationID, marketplaceCustomerID, restore := configureInboxAISemiPilotTest(
		t, ctx, pool, clientID, serviceID,
	)
	defer restore()
	if _, err := pool.Exec(ctx, `
		UPDATE inbox_ai_policies SET default_mode='autopilot', revision=revision+1, updated_at=NOW()
		WHERE client_id=$1
	`, clientID); err != nil {
		t.Fatal(err)
	}
	var bookingsBefore int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM bookings WHERE client_id=$1`, clientID).Scan(&bookingsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SendMarketplaceMessage(
		ctx, marketplaceCustomerID, conversationID, uuid.New(),
		"Which service can I book?", uuid.NullUUID{},
	); err != nil {
		t.Fatal(err)
	}
	worker, err := NewInboxAISemiPilotWorker(
		repo, scriptedAutopilotDecider{}, NewInboxAIGenerationLimiter(1), nil,
		InboxAISemiPilotWorkerConfig{
			ModelProvider: "test", ModelName: "scripted-autopilot",
			ModelConfigHash: strings.Repeat("b", 64), MaxConcurrency: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.ProcessOne(ctx, "autopilot-live-test-worker")
	if err != nil || !processed {
		t.Fatalf("process autopilot turn: processed=%v error=%v", processed, err)
	}
	detail, err := repo.GetMarketplaceConversationDetail(ctx, marketplaceCustomerID, conversationID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Messages) != 2 || detail.Messages[1].SenderType != "ai" ||
		!strings.Contains(detail.Messages[1].Content, "Use Book here") {
		t.Fatalf("autopilot transcript = %#v", detail.Messages)
	}
	var runMode, jobStatus, actionName string
	if err := pool.QueryRow(ctx, `
		SELECT run.mode, job.status, action.action_name
		FROM inbox_ai_turn_jobs job
		INNER JOIN inbox_ai_runs run ON run.turn_job_id=job.id
		INNER JOIN inbox_ai_actions action ON action.turn_job_id=job.id
		WHERE job.conversation_id=$1
	`, conversationID).Scan(&runMode, &jobStatus, &actionName); err != nil {
		t.Fatal(err)
	}
	if runMode != InboxAIModeAutopilot || jobStatus != "completed" || actionName != "list_relevant_services" {
		t.Fatalf("autopilot audit mode=%q job=%q action=%q", runMode, jobStatus, actionName)
	}
	var bookingsAfter int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM bookings WHERE client_id=$1`, clientID).Scan(&bookingsAfter); err != nil {
		t.Fatal(err)
	}
	if bookingsAfter != bookingsBefore {
		t.Fatalf("autopilot conversation created booking before confirmation: before=%d after=%d", bookingsBefore, bookingsAfter)
	}
}

func TestInboxAISemiPilotWorkerFencesNewerCustomerAndProviderMessages(t *testing.T) {
	ctx, pool := openInboxAISemiPilotTestPool(t)
	clientID, serviceID := pickInboxAISemiPilotService(t, ctx, pool)
	repo, conversationID, marketplaceCustomerID, restore := configureInboxAISemiPilotTest(
		t, ctx, pool, clientID, serviceID,
	)
	defer restore()

	first, err := repo.SendMarketplaceMessage(
		ctx, marketplaceCustomerID, conversationID, uuid.New(), "First question", uuid.NullUUID{},
	)
	if err != nil {
		t.Fatal(err)
	}
	decider := &blockingSemiPilotDecider{started: make(chan struct{}), release: make(chan struct{})}
	worker, err := NewInboxAISemiPilotWorker(
		repo, decider, NewInboxAIGenerationLimiter(1), nil,
		InboxAISemiPilotWorkerConfig{
			ModelProvider: "test", ModelName: "blocking",
			ModelConfigHash: strings.Repeat("b", 64), MaxConcurrency: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, processErr := worker.ProcessOne(ctx, "stale-customer-test-worker")
		result <- processErr
	}()
	select {
	case <-decider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("semi-pilot decision did not start")
	}
	if _, err := repo.SendMarketplaceMessage(
		ctx, marketplaceCustomerID, conversationID, uuid.New(), "Newer question", uuid.NullUUID{},
	); err != nil {
		t.Fatal(err)
	}
	close(decider.release)
	if err := <-result; err != nil {
		t.Fatalf("stale turn processing returned error: %v", err)
	}
	var firstStatus, firstError string
	if err := pool.QueryRow(ctx, `
		SELECT status,error_code FROM inbox_ai_turn_jobs WHERE trigger_message_id=$1
	`, uuid.MustParse(first.Message.ID)).Scan(&firstStatus, &firstError); err != nil {
		t.Fatal(err)
	}
	if firstStatus != "cancelled" || firstError != "superseded" {
		t.Fatalf("superseded turn status=%s error=%s", firstStatus, firstError)
	}
	var aiMessageCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM inbox_messages WHERE conversation_id=$1 AND sender_type='ai'
	`, conversationID).Scan(&aiMessageCount); err != nil {
		t.Fatal(err)
	}
	if aiMessageCount != 0 {
		t.Fatalf("stale customer turn committed %d AI messages", aiMessageCount)
	}

	// Cancel the newer queued turn so the next claimed turn is deterministic.
	if _, err := pool.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs
		SET status='cancelled', error_code='test_cleanup', completed_at=NOW()
		WHERE conversation_id=$1 AND status='queued'
	`, conversationID); err != nil {
		t.Fatal(err)
	}
	third, err := repo.SendMarketplaceMessage(
		ctx, marketplaceCustomerID, conversationID, uuid.New(), "One more question", uuid.NullUUID{},
	)
	if err != nil {
		t.Fatal(err)
	}
	providerDecider := &blockingSemiPilotDecider{started: make(chan struct{}), release: make(chan struct{})}
	providerWorker, err := NewInboxAISemiPilotWorker(
		repo, providerDecider, NewInboxAIGenerationLimiter(1), nil,
		InboxAISemiPilotWorkerConfig{
			ModelProvider: "test", ModelName: "blocking",
			ModelConfigHash: strings.Repeat("c", 64), MaxConcurrency: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	providerResult := make(chan error, 1)
	go func() {
		_, processErr := providerWorker.ProcessOne(ctx, "provider-takeover-test-worker")
		providerResult <- processErr
	}()
	select {
	case <-providerDecider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider-fence decision did not start")
	}
	if _, err := repo.SendProviderMessage(
		ctx, clientID, conversationID, uuid.New(), "I’ll take it from here.", uuid.NullUUID{},
	); err != nil {
		t.Fatal(err)
	}
	close(providerDecider.release)
	if err := <-providerResult; err != nil {
		t.Fatalf("provider-fenced turn processing returned error: %v", err)
	}
	var thirdStatus, thirdError, controlState string
	if err := pool.QueryRow(ctx, `
		SELECT job.status,job.error_code,control.state
		FROM inbox_ai_turn_jobs job
		INNER JOIN inbox_ai_conversation_controls control
		  ON control.conversation_id=job.conversation_id
		WHERE job.trigger_message_id=$1
	`, uuid.MustParse(third.Message.ID)).Scan(&thirdStatus, &thirdError, &controlState); err != nil {
		t.Fatal(err)
	}
	if thirdStatus != "cancelled" || thirdError != "provider_takeover" ||
		controlState != "provider_takeover" {
		t.Fatalf("provider fence status=%s error=%s control=%s", thirdStatus, thirdError, controlState)
	}
}

func TestInboxAISemiPilotHeartbeatProtectsLeaseAndLiveRunRetention(t *testing.T) {
	ctx, pool := openInboxAISemiPilotTestPool(t)
	clientID, serviceID := pickInboxAISemiPilotService(t, ctx, pool)
	repo, conversationID, marketplaceCustomerID, restore := configureInboxAISemiPilotTest(
		t, ctx, pool, clientID, serviceID,
	)
	defer restore()

	message, err := repo.SendMarketplaceMessage(
		ctx, marketplaceCustomerID, conversationID, uuid.New(), "Please check this.", uuid.NullUUID{},
	)
	if err != nil {
		t.Fatal(err)
	}
	decider := &blockingSemiPilotDecider{started: make(chan struct{}), release: make(chan struct{})}
	worker, err := NewInboxAISemiPilotWorker(
		repo, decider, NewInboxAIGenerationLimiter(1), nil,
		InboxAISemiPilotWorkerConfig{
			ModelProvider: "test", ModelName: "heartbeat",
			ModelConfigHash: strings.Repeat("3", 64), MaxConcurrency: 1,
			LeaseDuration: 300 * time.Millisecond,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, processErr := worker.ProcessOne(ctx, "heartbeat-owner")
		result <- processErr
	}()
	select {
	case <-decider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("heartbeat decision did not start")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE inbox_ai_runs SET created_at=NOW()-INTERVAL '11 minutes'
		WHERE turn_job_id=(
			SELECT id FROM inbox_ai_turn_jobs WHERE trigger_message_id=$1
		)
	`, uuid.MustParse(message.Message.ID)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(750 * time.Millisecond)
	recovered, _, err := maintainInboxAIRuns(
		ctx, pool, time.Now().UTC().Add(-10*time.Minute), time.Unix(0, 0), 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 0 {
		t.Fatalf("retention abandoned %d actively leased runs", recovered)
	}
	competingWorker, err := NewInboxAISemiPilotWorker(
		repo, &failingSemiPilotDecider{}, NewInboxAIGenerationLimiter(1), nil,
		InboxAISemiPilotWorkerConfig{
			ModelProvider: "test", ModelName: "competitor",
			ModelConfigHash: strings.Repeat("4", 64), MaxConcurrency: 1,
			LeaseDuration: 300 * time.Millisecond,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := competingWorker.ProcessOne(ctx, "heartbeat-competitor"); err != nil || processed {
		t.Fatalf("active lease was reclaimed: processed=%v error=%v", processed, err)
	}
	close(decider.release)
	if err := <-result; err != nil {
		t.Fatalf("heartbeat-owned turn failed: %v", err)
	}
	var jobStatus, runStatus string
	if err := pool.QueryRow(ctx, `
		SELECT job.status,run.status FROM inbox_ai_turn_jobs job
		INNER JOIN inbox_ai_runs run ON run.turn_job_id=job.id
		WHERE job.trigger_message_id=$1
	`, uuid.MustParse(message.Message.ID)).Scan(&jobStatus, &runStatus); err != nil {
		t.Fatal(err)
	}
	if jobStatus != "completed" || runStatus != "completed" {
		t.Fatalf("heartbeat completion job=%s run=%s", jobStatus, runStatus)
	}
}

func TestInboxAISemiPilotCustomerHandoffCancelsQueuedTurn(t *testing.T) {
	ctx, pool := openInboxAISemiPilotTestPool(t)
	clientID, serviceID := pickInboxAISemiPilotService(t, ctx, pool)
	repo, conversationID, marketplaceCustomerID, restore := configureInboxAISemiPilotTest(
		t, ctx, pool, clientID, serviceID,
	)
	defer restore()

	message, err := repo.SendMarketplaceMessage(
		ctx, marketplaceCustomerID, conversationID, uuid.New(),
		"Please let me speak with the provider.", uuid.NullUUID{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CustomerInboxAIHandoff(
		ctx, marketplaceCustomerID, conversationID, "talk_to_provider",
	); err != nil {
		t.Fatal(err)
	}
	var status, errorCode, controlState, sessionState string
	if err := pool.QueryRow(ctx, `
		SELECT job.status,job.error_code,control.state,session.state
		FROM inbox_ai_turn_jobs job
		INNER JOIN inbox_ai_conversation_controls control
		  ON control.conversation_id=job.conversation_id
		INNER JOIN inbox_ai_sessions session ON session.id=job.session_id
		WHERE job.trigger_message_id=$1
	`, uuid.MustParse(message.Message.ID)).Scan(
		&status, &errorCode, &controlState, &sessionState,
	); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || errorCode != "customer_handoff" ||
		controlState != "handoff" || sessionState != "handoff" {
		t.Fatalf(
			"customer handoff job=%s error=%s control=%s session=%s",
			status, errorCode, controlState, sessionState,
		)
	}
}

func TestInboxAISemiPilotRetryReplaysToolWithoutExpandingBudget(t *testing.T) {
	ctx, pool := openInboxAISemiPilotTestPool(t)
	clientID, serviceID := pickInboxAISemiPilotService(t, ctx, pool)
	repo, conversationID, marketplaceCustomerID, restore := configureInboxAISemiPilotTest(
		t, ctx, pool, clientID, serviceID,
	)
	defer restore()

	message, err := repo.SendMarketplaceMessage(
		ctx, marketplaceCustomerID, conversationID, uuid.New(),
		"Please send the booking link.", uuid.NullUUID{},
	)
	if err != nil {
		t.Fatal(err)
	}
	decider := &retryingBookingLinkDecider{serviceID: serviceID}
	worker, err := NewInboxAISemiPilotWorker(
		repo, decider, NewInboxAIGenerationLimiter(1), nil,
		InboxAISemiPilotWorkerConfig{
			ModelProvider: "test", ModelName: "retrying",
			ModelConfigHash: strings.Repeat("d", 64), MaxConcurrency: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	processed, firstErr := worker.ProcessOne(ctx, "retry-test-worker")
	if !processed || firstErr == nil {
		t.Fatalf("first attempt processed=%v error=%v, want persisted retry", processed, firstErr)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs SET available_at=NOW()
		WHERE trigger_message_id=$1 AND status='queued'
	`, uuid.MustParse(message.Message.ID)); err != nil {
		t.Fatal(err)
	}
	processed, err = worker.ProcessOne(ctx, "retry-test-worker")
	if err != nil || !processed {
		t.Fatalf("retry processed=%v error=%v", processed, err)
	}
	var attemptCount, actionCount, auditedToolCount int
	var jobStatus string
	if err := pool.QueryRow(ctx, `
		SELECT job.attempt_count,job.status,
			(SELECT COUNT(*) FROM inbox_ai_actions action WHERE action.turn_job_id=job.id),
			run.tool_call_count
		FROM inbox_ai_turn_jobs job
		INNER JOIN inbox_ai_runs run ON run.turn_job_id=job.id
		WHERE job.trigger_message_id=$1
	`, uuid.MustParse(message.Message.ID)).Scan(
		&attemptCount, &jobStatus, &actionCount, &auditedToolCount,
	); err != nil {
		t.Fatal(err)
	}
	if attemptCount != 2 || jobStatus != "completed" || actionCount != 1 ||
		auditedToolCount != 1 {
		t.Fatalf(
			"retry lineage attempts=%d status=%s actions=%d audited_tools=%d",
			attemptCount, jobStatus, actionCount, auditedToolCount,
		)
	}
}

func TestInboxAISemiPilotRetryRestoresToolResultAndBookingCard(t *testing.T) {
	ctx, pool := openInboxAISemiPilotTestPool(t)
	clientID, serviceID := pickInboxAISemiPilotService(t, ctx, pool)
	repo, conversationID, marketplaceCustomerID, restore := configureInboxAISemiPilotTest(
		t, ctx, pool, clientID, serviceID,
	)
	defer restore()

	message, err := repo.SendMarketplaceMessage(
		ctx, marketplaceCustomerID, conversationID, uuid.New(),
		"Please send the booking link.", uuid.NullUUID{},
	)
	if err != nil {
		t.Fatal(err)
	}
	decider := &retryRehydrationDecider{serviceID: serviceID}
	worker, err := NewInboxAISemiPilotWorker(
		repo, decider, NewInboxAIGenerationLimiter(1), nil,
		InboxAISemiPilotWorkerConfig{
			ModelProvider: "test", ModelName: "retry-rehydration",
			ModelConfigHash: strings.Repeat("f", 64), MaxConcurrency: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if processed, firstErr := worker.ProcessOne(ctx, "retry-rehydrate-worker"); !processed || firstErr == nil {
		t.Fatalf("first attempt processed=%v error=%v", processed, firstErr)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs SET available_at=NOW()
		WHERE trigger_message_id=$1 AND status='queued'
	`, uuid.MustParse(message.Message.ID)); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(ctx, "retry-rehydrate-worker"); err != nil || !processed {
		t.Fatalf("retry processed=%v error=%v", processed, err)
	}
	detail, err := repo.GetMarketplaceConversationDetail(ctx, marketplaceCustomerID, conversationID, 20)
	if err != nil {
		t.Fatal(err)
	}
	aiMessage := detail.Messages[len(detail.Messages)-1]
	if aiMessage.Presentation == nil || aiMessage.Presentation.Kind != "booking_link" {
		t.Fatalf("rehydrated retry did not commit booking card: %#v", aiMessage)
	}
}

func TestInboxAISemiPilotAllowsRepeatedToolWithDifferentArguments(t *testing.T) {
	ctx, pool := openInboxAISemiPilotTestPool(t)
	clientID, serviceID := pickInboxAISemiPilotService(t, ctx, pool)
	repo, conversationID, marketplaceCustomerID, restore := configureInboxAISemiPilotTest(
		t, ctx, pool, clientID, serviceID,
	)
	defer restore()

	message, err := repo.SendMarketplaceMessage(
		ctx, marketplaceCustomerID, conversationID, uuid.New(), "I need makeup.", uuid.NullUUID{},
	)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewInboxAISemiPilotWorker(
		repo, repeatedServiceLookupDecider{}, NewInboxAIGenerationLimiter(1), nil,
		InboxAISemiPilotWorkerConfig{
			ModelProvider: "test", ModelName: "repeated-tool",
			ModelConfigHash: strings.Repeat("1", 64), MaxConcurrency: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(ctx, "repeated-tool-worker"); err != nil || !processed {
		t.Fatalf("repeated-tool turn processed=%v error=%v", processed, err)
	}
	var actionCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM inbox_ai_actions action
		INNER JOIN inbox_ai_turn_jobs job ON job.id=action.turn_job_id
		WHERE job.trigger_message_id=$1 AND action.action_name='list_relevant_services'
	`, uuid.MustParse(message.Message.ID)).Scan(&actionCount); err != nil {
		t.Fatal(err)
	}
	if actionCount != 2 {
		t.Fatalf("repeated tool actions=%d, want 2", actionCount)
	}
}

func TestInboxAISemiPilotFinalAttemptCommitsVisibleHandoff(t *testing.T) {
	ctx, pool := openInboxAISemiPilotTestPool(t)
	clientID, serviceID := pickInboxAISemiPilotService(t, ctx, pool)
	repo, conversationID, marketplaceCustomerID, restore := configureInboxAISemiPilotTest(
		t, ctx, pool, clientID, serviceID,
	)
	defer restore()

	message, err := repo.SendMarketplaceMessage(
		ctx, marketplaceCustomerID, conversationID, uuid.New(), "Can you help me?", uuid.NullUUID{},
	)
	if err != nil {
		t.Fatal(err)
	}
	decider := &failingSemiPilotDecider{}
	worker, err := NewInboxAISemiPilotWorker(
		repo, decider, NewInboxAIGenerationLimiter(1), nil,
		InboxAISemiPilotWorkerConfig{
			ModelProvider: "test", ModelName: "always-fails",
			ModelConfigHash: strings.Repeat("2", 64), MaxConcurrency: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		processed, processErr := worker.ProcessOne(ctx, "terminal-handoff-worker")
		if !processed {
			t.Fatalf("attempt %d did not process a job", attempt)
		}
		if attempt < 3 && processErr == nil {
			t.Fatalf("attempt %d unexpectedly succeeded", attempt)
		}
		if attempt == 3 && processErr != nil {
			t.Fatalf("terminal handoff failed: %v", processErr)
		}
		if attempt < 3 {
			if _, err := pool.Exec(ctx, `
				UPDATE inbox_ai_turn_jobs SET available_at=NOW()
				WHERE trigger_message_id=$1 AND status='queued'
			`, uuid.MustParse(message.Message.ID)); err != nil {
				t.Fatal(err)
			}
		}
	}
	decider.mu.Lock()
	modelCalls := decider.calls
	decider.mu.Unlock()
	if modelCalls != 2 {
		t.Fatalf("model calls=%d, want two inference attempts plus deterministic fallback", modelCalls)
	}
	detail, err := repo.GetMarketplaceConversationDetail(ctx, marketplaceCustomerID, conversationID, 20)
	if err != nil {
		t.Fatal(err)
	}
	aiMessage := detail.Messages[len(detail.Messages)-1]
	if aiMessage.SenderType != "ai" || !strings.Contains(aiMessage.Content, "hand this over") {
		t.Fatalf("terminal handoff message=%#v", aiMessage)
	}
}

func TestInboxAISemiPilotHandoffTakesPrecedenceOverPreparedLink(t *testing.T) {
	ctx, pool := openInboxAISemiPilotTestPool(t)
	clientID, serviceID := pickInboxAISemiPilotService(t, ctx, pool)
	repo, conversationID, marketplaceCustomerID, restore := configureInboxAISemiPilotTest(
		t, ctx, pool, clientID, serviceID,
	)
	defer restore()

	if _, err := repo.SendMarketplaceMessage(
		ctx, marketplaceCustomerID, conversationID, uuid.New(),
		"This needs the provider instead.", uuid.NullUUID{},
	); err != nil {
		t.Fatal(err)
	}
	worker, err := NewInboxAISemiPilotWorker(
		repo, handoffAfterLinkDecider{serviceID: serviceID},
		NewInboxAIGenerationLimiter(1), nil,
		InboxAISemiPilotWorkerConfig{
			ModelProvider: "test", ModelName: "handoff-after-link",
			ModelConfigHash: strings.Repeat("e", 64), MaxConcurrency: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(ctx, "handoff-link-test-worker"); err != nil || !processed {
		t.Fatalf("handoff turn processed=%v error=%v", processed, err)
	}
	detail, err := repo.GetMarketplaceConversationDetail(
		ctx, marketplaceCustomerID, conversationID, 20,
	)
	if err != nil {
		t.Fatal(err)
	}
	aiMessage := detail.Messages[len(detail.Messages)-1]
	if aiMessage.SenderType != "ai" || aiMessage.Presentation != nil {
		t.Fatalf("handoff leaked prepared booking card: %#v", aiMessage)
	}
	var controlState, sessionState string
	if err := pool.QueryRow(ctx, `
		SELECT control.state,session.state
		FROM inbox_ai_conversation_controls control
		INNER JOIN inbox_ai_sessions session
		  ON session.conversation_id=control.conversation_id
		WHERE control.conversation_id=$1
	`, conversationID).Scan(&controlState, &sessionState); err != nil {
		t.Fatal(err)
	}
	if controlState != "handoff" || sessionState != "handoff" {
		t.Fatalf("handoff control=%s session=%s", controlState, sessionState)
	}
}

func TestInboxAISemiPilotRateBudgetHandsOffWithoutBlockingNativeMessages(t *testing.T) {
	ctx, pool := openInboxAISemiPilotTestPool(t)
	clientID, serviceID := pickInboxAISemiPilotService(t, ctx, pool)
	repo, conversationID, marketplaceCustomerID, restore := configureInboxAISemiPilotTest(
		t, ctx, pool, clientID, serviceID,
	)
	defer restore()

	for index := 0; index <= inboxAISemiPilotCustomerTurnsPerMinute; index++ {
		if _, err := repo.SendMarketplaceMessage(
			ctx, marketplaceCustomerID, conversationID, uuid.New(),
			fmt.Sprintf("Message %d", index+1), uuid.NullUUID{},
		); err != nil {
			t.Fatalf("native message %d failed at automation budget: %v", index+1, err)
		}
	}
	var messageCount, jobCount, liveJobCount int
	var controlState, controlReason string
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM inbox_messages WHERE conversation_id=$1),
			(SELECT COUNT(*) FROM inbox_ai_turn_jobs WHERE conversation_id=$1),
			(SELECT COUNT(*) FROM inbox_ai_turn_jobs
			 WHERE conversation_id=$1 AND status IN ('queued','processing')),
			control.state,control.reason
		FROM inbox_ai_conversation_controls control WHERE control.conversation_id=$1
	`, conversationID).Scan(
		&messageCount, &jobCount, &liveJobCount, &controlState, &controlReason,
	); err != nil {
		t.Fatal(err)
	}
	if messageCount != inboxAISemiPilotCustomerTurnsPerMinute+1 ||
		jobCount != inboxAISemiPilotCustomerTurnsPerMinute || liveJobCount != 0 ||
		controlState != "handoff" || controlReason != "rate_limit" {
		t.Fatalf(
			"rate budget messages=%d jobs=%d live=%d control=%s reason=%s",
			messageCount, jobCount, liveJobCount, controlState, controlReason,
		)
	}
}

func TestInboxAISemiPilotProviderRateBudgetIsAtomicAcrossConversations(t *testing.T) {
	ctx, pool := openInboxAISemiPilotTestPool(t)
	clientID, serviceID := pickInboxAISemiPilotService(t, ctx, pool)
	repo, firstConversationID, firstCustomerID, restore := configureInboxAISemiPilotTest(
		t, ctx, pool, clientID, serviceID,
	)
	defer restore()

	type participant struct {
		conversationID uuid.UUID
		customerID     uuid.UUID
	}
	participants := make([]participant, 0, inboxAISemiPilotProviderTurnsPerMinute+1)
	participants = append(participants, participant{
		conversationID: firstConversationID,
		customerID:     firstCustomerID,
	})
	additionalCustomers := make([]uuid.UUID, 0, inboxAISemiPilotProviderTurnsPerMinute)
	for index := 1; index <= inboxAISemiPilotProviderTurnsPerMinute; index++ {
		customerID := uuid.New()
		if _, err := pool.Exec(ctx, `
			INSERT INTO marketplace_customers (id,full_name,email,email_verified_at)
			VALUES ($1,'Concurrent Rate Customer',$2,NOW())
		`, customerID, fmt.Sprintf("concurrent-rate-%s@example.com", customerID)); err != nil {
			t.Fatal(err)
		}
		additionalCustomers = append(additionalCustomers, customerID)
		conversation, err := repo.GetOrCreateMarketplaceProviderConversation(ctx, customerID, clientID)
		if err != nil {
			t.Fatal(err)
		}
		participants = append(participants, participant{
			conversationID: uuid.MustParse(conversation.Detail.Conversation.ID),
			customerID:     customerID,
		})
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=ANY($1::uuid[])`, additionalCustomers)
	}()

	sendConcurrently := func(items []participant, offset int) {
		t.Helper()
		start := make(chan struct{})
		errorsCh := make(chan error, len(items))
		var wait sync.WaitGroup
		for index, item := range items {
			wait.Add(1)
			go func(index int, item participant) {
				defer wait.Done()
				<-start
				_, err := repo.SendMarketplaceMessage(
					ctx, item.customerID, item.conversationID, uuid.New(),
					fmt.Sprintf("Concurrent message %d", offset+index+1), uuid.NullUUID{},
				)
				errorsCh <- err
			}(index, item)
		}
		close(start)
		wait.Wait()
		close(errorsCh)
		for err := range errorsCh {
			if err != nil {
				t.Fatalf("native concurrent message failed: %v", err)
			}
		}
	}

	// Fill and drain the lower live-backlog budget first so the second concurrent
	// wave specifically contests the per-minute provider budget.
	firstWave := participants[:inboxAISemiPilotProviderBacklogLimit]
	sendConcurrently(firstWave, 0)
	firstWaveConversationIDs := make([]uuid.UUID, 0, len(firstWave))
	for _, item := range firstWave {
		firstWaveConversationIDs = append(firstWaveConversationIDs, item.conversationID)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs
		SET status='completed',lease_owner='',lease_expires_at=NULL,
			completed_at=NOW(),updated_at=NOW()
		WHERE conversation_id=ANY($1::uuid[]) AND status='queued'
	`, firstWaveConversationIDs); err != nil {
		t.Fatal(err)
	}
	sendConcurrently(participants[inboxAISemiPilotProviderBacklogLimit:], len(firstWave))
	conversationIDs := make([]uuid.UUID, 0, len(participants))
	for _, item := range participants {
		conversationIDs = append(conversationIDs, item.conversationID)
	}
	var messageCount, jobCount, handoffCount int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM inbox_messages WHERE conversation_id=ANY($1::uuid[])),
			(SELECT COUNT(*) FROM inbox_ai_turn_jobs WHERE conversation_id=ANY($1::uuid[])),
			(SELECT COUNT(*) FROM inbox_ai_conversation_controls
			 WHERE conversation_id=ANY($1::uuid[]) AND state='handoff' AND reason='rate_limit')
	`, conversationIDs).Scan(&messageCount, &jobCount, &handoffCount); err != nil {
		t.Fatal(err)
	}
	if messageCount != len(participants) || jobCount != inboxAISemiPilotProviderTurnsPerMinute || handoffCount != 1 {
		t.Fatalf(
			"atomic provider budget messages=%d jobs=%d handoffs=%d",
			messageCount, jobCount, handoffCount,
		)
	}
}

func openInboxAISemiPilotTestPool(t *testing.T) (context.Context, *pgxpool.Pool) {
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

func pickInboxAISemiPilotService(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	var clientID, serviceID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT service.client_id,service.id
		FROM services service
		INNER JOIN client_profiles profile ON profile.client_id=service.client_id
		WHERE service.status='published' AND service.is_active AND NOT service.is_hidden
		  AND profile.marketplace_enabled AND profile.market_configured_at IS NOT NULL
		ORDER BY service.created_at,service.id LIMIT 1
	`).Scan(&clientID, &serviceID); err != nil {
		t.Fatal(err)
	}
	return clientID, serviceID
}

func configureInboxAISemiPilotTest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	clientID, serviceID uuid.UUID,
) (*Repository, uuid.UUID, uuid.UUID, func()) {
	t.Helper()
	repo := NewRepository(pool)
	repo.ConfigureInboxAIAutomation(true, []string{clientID.String()}, 0)
	var previous inboxAITestPolicySnapshot
	err := pool.QueryRow(ctx, `
		SELECT default_mode,enabled_service_ids,paused,max_turns,
			inactivity_timeout_minutes,revision,updated_by,created_at,updated_at
		FROM inbox_ai_policies WHERE client_id=$1
	`, clientID).Scan(
		&previous.defaultMode, &previous.enabledServices, &previous.paused,
		&previous.maxTurns, &previous.inactivity, &previous.revision,
		&previous.updatedBy, &previous.createdAt, &previous.updatedAt,
	)
	if err == nil {
		previous.exists = true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO inbox_ai_policies (
			client_id,default_mode,enabled_service_ids,paused,max_turns,
			inactivity_timeout_minutes,revision,updated_by
		) VALUES ($1,'semi_pilot',ARRAY[$2]::uuid[],FALSE,8,45,1,$1)
		ON CONFLICT (client_id) DO UPDATE SET
			default_mode='semi_pilot',enabled_service_ids=ARRAY[$2]::uuid[],
			paused=FALSE,max_turns=8,inactivity_timeout_minutes=45,
			revision=inbox_ai_policies.revision+1,updated_by=$1,updated_at=NOW()
	`, clientID, serviceID); err != nil {
		t.Fatal(err)
	}
	marketplaceCustomerID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO marketplace_customers (id,full_name,email,email_verified_at)
		VALUES ($1,'Turn Worker Customer',$2,NOW())
	`, marketplaceCustomerID, fmt.Sprintf("turn-worker-%s@example.com", marketplaceCustomerID)); err != nil {
		t.Fatal(err)
	}
	conversation, err := repo.GetOrCreateMarketplaceProviderConversation(
		ctx, marketplaceCustomerID, clientID,
	)
	if err != nil {
		t.Fatal(err)
	}
	conversationID := uuid.MustParse(conversation.Detail.Conversation.ID)
	restore := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM inbox_conversations WHERE id=$1`, conversationID)
		_, _ = pool.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=$1`, marketplaceCustomerID)
		if previous.exists {
			_, _ = pool.Exec(ctx, `
				UPDATE inbox_ai_policies SET
					default_mode=$2,enabled_service_ids=$3,paused=$4,max_turns=$5,
					inactivity_timeout_minutes=$6,revision=$7,updated_by=$8,
					created_at=$9,updated_at=$10
				WHERE client_id=$1
			`, clientID, previous.defaultMode, previous.enabledServices, previous.paused,
				previous.maxTurns, previous.inactivity, previous.revision, previous.updatedBy,
				previous.createdAt, previous.updatedAt)
		} else {
			_, _ = pool.Exec(ctx, `DELETE FROM inbox_ai_policies WHERE client_id=$1`, clientID)
		}
	}
	return repo, conversationID, marketplaceCustomerID, restore
}
