package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestInboxAISemiPilotPolicyControlAndReadTools(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repo := NewRepository(pool)

	var clientID, serviceID uuid.UUID
	var serviceTitle, handle string
	var servicePrice int64
	if err := pool.QueryRow(ctx, `
		SELECT service.client_id, service.id, service.title, profile.handle_slug,
			service.price_amount_minor
		FROM services service
		INNER JOIN client_profiles profile ON profile.client_id=service.client_id
		WHERE service.status='published' AND service.is_active AND NOT service.is_hidden
		  AND NOT service.standalone_signature_required
		  AND profile.marketplace_enabled AND profile.market_configured_at IS NOT NULL
		ORDER BY service.created_at, service.id
		LIMIT 1
	`).Scan(&clientID, &serviceID, &serviceTitle, &handle, &servicePrice); err != nil {
		t.Fatal(err)
	}
	repo.ConfigureInboxAIAutomation(true, []string{clientID.String()})

	type policySnapshot struct {
		defaultMode       string
		enabledServiceIDs []uuid.UUID
		paused            bool
		maxTurns          int
		inactivity        int
		revision          int64
		updatedBy         uuid.UUID
		createdAt         time.Time
		updatedAt         time.Time
	}
	var previous policySnapshot
	previousPolicyExists := true
	if err := pool.QueryRow(ctx, `
		SELECT default_mode, enabled_service_ids, paused, max_turns,
			inactivity_timeout_minutes, revision, updated_by, created_at, updated_at
		FROM inbox_ai_policies WHERE client_id=$1
	`, clientID).Scan(
		&previous.defaultMode, &previous.enabledServiceIDs, &previous.paused, &previous.maxTurns,
		&previous.inactivity, &previous.revision, &previous.updatedBy,
		&previous.createdAt, &previous.updatedAt,
	); errors.Is(err, pgx.ErrNoRows) {
		previousPolicyExists = false
	} else if err != nil {
		t.Fatal(err)
	}

	marketplaceCustomerID := uuid.New()
	email := fmt.Sprintf("semi-pilot-%s@example.com", uuid.NewString())
	if _, err := pool.Exec(ctx, `
		INSERT INTO marketplace_customers (id,full_name,email,email_verified_at)
		VALUES ($1,'Semi Pilot Customer',$2,NOW())
	`, marketplaceCustomerID, email); err != nil {
		t.Fatal(err)
	}
	conversationResult, err := repo.GetOrCreateMarketplaceProviderConversation(
		ctx, marketplaceCustomerID, clientID,
	)
	if err != nil {
		t.Fatal(err)
	}
	conversationID := uuid.MustParse(conversationResult.Detail.Conversation.ID)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM inbox_conversations WHERE id=$1`, conversationID)
		_, _ = pool.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=$1`, marketplaceCustomerID)
		if previousPolicyExists {
			_, _ = pool.Exec(ctx, `
				INSERT INTO inbox_ai_policies (
					client_id, default_mode, enabled_service_ids, paused, max_turns,
					inactivity_timeout_minutes, revision, updated_by, created_at, updated_at
				) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
				ON CONFLICT (client_id) DO UPDATE SET
					default_mode=EXCLUDED.default_mode,
					enabled_service_ids=EXCLUDED.enabled_service_ids,
					paused=EXCLUDED.paused,
					max_turns=EXCLUDED.max_turns,
					inactivity_timeout_minutes=EXCLUDED.inactivity_timeout_minutes,
					revision=EXCLUDED.revision,
					updated_by=EXCLUDED.updated_by,
					created_at=EXCLUDED.created_at,
					updated_at=EXCLUDED.updated_at
			`, clientID, previous.defaultMode, previous.enabledServiceIDs, previous.paused,
				previous.maxTurns, previous.inactivity, previous.revision, previous.updatedBy,
				previous.createdAt, previous.updatedAt)
		} else {
			_, _ = pool.Exec(ctx, `DELETE FROM inbox_ai_policies WHERE client_id=$1`, clientID)
		}
	})

	if _, err := pool.Exec(ctx, `DELETE FROM inbox_ai_policies WHERE client_id=$1`, clientID); err != nil {
		t.Fatal(err)
	}
	startPolicyWrites := make(chan struct{})
	policyWriteErrors := make([]error, 2)
	var policyWriteWait sync.WaitGroup
	policyWriteWait.Add(2)
	for index := range policyWriteErrors {
		go func(index int) {
			defer policyWriteWait.Done()
			<-startPolicyWrites
			mode := InboxAIModeManual
			serviceIDs := []string(nil)
			if index == 1 {
				mode = InboxAIModeSemiPilot
				serviceIDs = []string{serviceID.String()}
			}
			_, policyWriteErrors[index] = repo.UpdateInboxAIPolicy(ctx, clientID, UpdateInboxAIPolicyInput{
				ExpectedRevision: 0, DefaultMode: mode, EnabledServiceIDs: serviceIDs,
				MaxTurns: 8, InactivityTimeoutMinutes: 45,
			})
		}(index)
	}
	close(startPolicyWrites)
	policyWriteWait.Wait()
	successfulPolicyWrites := 0
	conflictingPolicyWrites := 0
	for _, writeErr := range policyWriteErrors {
		switch {
		case writeErr == nil:
			successfulPolicyWrites++
		case errors.Is(writeErr, ErrInboxAIRevisionConflict):
			conflictingPolicyWrites++
		default:
			t.Fatalf("concurrent initial policy write error = %v", writeErr)
		}
	}
	if successfulPolicyWrites != 1 || conflictingPolicyWrites != 1 {
		t.Fatalf("concurrent initial policy writes: success=%d conflict=%d", successfulPolicyWrites, conflictingPolicyWrites)
	}

	policy, err := repo.GetInboxAIPolicy(ctx, clientID)
	if err != nil {
		t.Fatal(err)
	}
	updatedPolicy, err := repo.UpdateInboxAIPolicy(ctx, clientID, UpdateInboxAIPolicyInput{
		ExpectedRevision: policy.Revision, DefaultMode: InboxAIModeSemiPilot,
		EnabledServiceIDs: []string{serviceID.String()}, MaxTurns: 8,
		InactivityTimeoutMinutes: 45,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updatedPolicy.DefaultMode != InboxAIModeSemiPilot || updatedPolicy.Revision != policy.Revision+1 ||
		len(updatedPolicy.EnabledServiceIDs) != 1 || !updatedPolicy.AutomationAvailable {
		t.Fatalf("updated policy = %#v", updatedPolicy)
	}
	if _, err := repo.UpdateInboxAIPolicy(ctx, clientID, UpdateInboxAIPolicyInput{
		ExpectedRevision: policy.Revision, DefaultMode: InboxAIModeManual,
		MaxTurns: 8, InactivityTimeoutMinutes: 45,
	}); !errors.Is(err, ErrInboxAIRevisionConflict) {
		t.Fatalf("stale policy update error = %v", err)
	}
	if servicePrice > 0 {
		paidPolicy, paidErr := repo.UpdateInboxAIPolicy(ctx, clientID, UpdateInboxAIPolicyInput{
			ExpectedRevision: updatedPolicy.Revision, DefaultMode: InboxAIModeAutopilot,
			EnabledServiceIDs: []string{serviceID.String()}, MaxTurns: 8,
			InactivityTimeoutMinutes: 45,
		})
		if paidErr != nil || paidPolicy.DefaultMode != InboxAIModeAutopilot {
			t.Fatalf("paid autopilot policy = %#v, error=%v", paidPolicy, paidErr)
		}
		updatedPolicy, err = repo.UpdateInboxAIPolicy(ctx, clientID, UpdateInboxAIPolicyInput{
			ExpectedRevision: paidPolicy.Revision, DefaultMode: InboxAIModeSemiPilot,
			EnabledServiceIDs: []string{serviceID.String()}, MaxTurns: 8,
			InactivityTimeoutMinutes: 45,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	control, err := repo.GetInboxAIConversationControl(ctx, clientID, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if control.EffectiveMode != InboxAIModeSemiPilot || control.State != "active" || control.Revision != 0 {
		t.Fatalf("initial control = %#v", control)
	}
	manualOverride, err := repo.UpdateInboxAIConversationControl(
		ctx, clientID, conversationID, UpdateInboxAIConversationControlInput{
			ExpectedRevision: control.Revision, ModeOverride: InboxAIModeManual, State: "active",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if manualOverride.ConfiguredMode != InboxAIModeManual ||
		manualOverride.EffectiveMode != InboxAIModeManual {
		t.Fatalf("manual conversation override = %#v", manualOverride)
	}
	if _, err := repo.ExecuteSemiPilotReadAction(ctx, ExecuteSemiPilotReadActionCommand{
		ClientID: clientID.String(), ConversationID: conversationID.String(),
		IdempotencyKey: uuid.NewString(), ActionName: "list_relevant_services",
	}); !errors.Is(err, ErrInboxAIControlBlocked) {
		t.Fatalf("manual override action error = %v", err)
	}
	control, err = repo.UpdateInboxAIConversationControl(
		ctx, clientID, conversationID, UpdateInboxAIConversationControlInput{
			ExpectedRevision: manualOverride.Revision, State: "active",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if control.ConfiguredMode != InboxAIModeSemiPilot ||
		control.EffectiveMode != InboxAIModeSemiPilot {
		t.Fatalf("restored provider default = %#v", control)
	}

	listKey := uuid.New()
	listResult, err := repo.ExecuteSemiPilotReadAction(ctx, ExecuteSemiPilotReadActionCommand{
		ClientID: clientID.String(), ConversationID: conversationID.String(),
		IdempotencyKey: listKey.String(), ActionName: "list_relevant_services",
		Query: serviceTitle,
	})
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Services []InboxAISemiPilotService `json:"services"`
	}
	if err := json.Unmarshal(listResult.Result, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Services) != 1 || listed.Services[0].ID != serviceID.String() {
		t.Fatalf("listed services = %#v", listed.Services)
	}
	replayedList, err := repo.ExecuteSemiPilotReadAction(ctx, ExecuteSemiPilotReadActionCommand{
		ClientID: clientID.String(), ConversationID: conversationID.String(),
		IdempotencyKey: listKey.String(), ActionName: "list_relevant_services",
		Query: serviceTitle,
	})
	if err != nil || !replayedList.Replayed {
		t.Fatalf("replayed action = %#v, error=%v", replayedList, err)
	}
	if _, err := repo.ExecuteSemiPilotReadAction(ctx, ExecuteSemiPilotReadActionCommand{
		ClientID: clientID.String(), ConversationID: conversationID.String(),
		IdempotencyKey: listKey.String(), ActionName: "list_relevant_services",
		Query: "different request",
	}); !errors.Is(err, ErrInboxIdempotencyConflict) {
		t.Fatalf("conflicting action replay error = %v", err)
	}
	detailResult, err := repo.ExecuteSemiPilotReadAction(ctx, ExecuteSemiPilotReadActionCommand{
		ClientID: clientID.String(), ConversationID: conversationID.String(),
		IdempotencyKey: uuid.NewString(), ActionName: "get_service_details",
		ServiceID: serviceID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(detailResult.Result) > 4*1024 {
		t.Fatalf("service detail tool result is unbounded: %d bytes", len(detailResult.Result))
	}
	linkResult, err := repo.ExecuteSemiPilotReadAction(ctx, ExecuteSemiPilotReadActionCommand{
		ClientID: clientID.String(), ConversationID: conversationID.String(),
		IdempotencyKey: uuid.NewString(), ActionName: "get_booking_link",
		ServiceID: serviceID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var link struct {
		Href string `json:"href"`
	}
	if err := json.Unmarshal(linkResult.Result, &link); err != nil {
		t.Fatal(err)
	}
	wantLink := "/booking/checkout?provider=" + url.QueryEscape(handle) + "&service=" + serviceID.String()
	if link.Href != wantLink || strings.Contains(link.Href, marketplaceCustomerID.String()) {
		t.Fatalf("canonical semi-pilot link = %q, want %q", link.Href, wantLink)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE client_profiles SET marketplace_enabled=FALSE WHERE client_id=$1
	`, clientID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `
			UPDATE client_profiles SET marketplace_enabled=TRUE WHERE client_id=$1
		`, clientID)
	})
	failedActionKey := uuid.New()
	failedCommand := ExecuteSemiPilotReadActionCommand{
		ClientID: clientID.String(), ConversationID: conversationID.String(),
		IdempotencyKey: failedActionKey.String(), ActionName: "get_booking_link",
		ServiceID: serviceID.String(),
	}
	if _, err := repo.ExecuteSemiPilotReadAction(ctx, failedCommand); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed canonical-link action error = %v", err)
	}
	var failedStatus, failedErrorCode string
	if err := pool.QueryRow(ctx, `
		SELECT status, error_code FROM inbox_ai_actions WHERE idempotency_key=$1
	`, failedActionKey).Scan(&failedStatus, &failedErrorCode); err != nil {
		t.Fatal(err)
	}
	if failedStatus != "failed" || failedErrorCode != "not_found" {
		t.Fatalf("failed action audit = status %q, error %q", failedStatus, failedErrorCode)
	}
	if _, err := repo.ExecuteSemiPilotReadAction(ctx, failedCommand); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replayed failed canonical-link action error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE client_profiles SET marketplace_enabled=TRUE WHERE client_id=$1
	`, clientID); err != nil {
		t.Fatal(err)
	}
	sessionResponse, err := repo.GetInboxAISession(ctx, clientID, conversationID)
	if err != nil || sessionResponse.Session == nil {
		t.Fatalf("session = %#v, error=%v", sessionResponse, err)
	}
	if sessionResponse.Session.State != "link_ready" ||
		sessionResponse.Session.SelectedServiceID != serviceID.String() ||
		sessionResponse.Session.BookingLinkURL != wantLink {
		t.Fatalf("advanced session = %#v", sessionResponse.Session)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE inbox_ai_sessions SET expires_at=NOW()-INTERVAL '1 minute'
		WHERE conversation_id=$1
	`, conversationID); err != nil {
		t.Fatal(err)
	}
	derivedExpired, err := repo.GetInboxAISession(ctx, clientID, conversationID)
	if err != nil || derivedExpired.Session == nil || derivedExpired.Session.State != "expired" ||
		derivedExpired.Session.HandoffReason != "inactivity_timeout" {
		t.Fatalf("derived expired session = %#v, error=%v", derivedExpired, err)
	}
	if _, err := repo.ExecuteSemiPilotReadAction(ctx, ExecuteSemiPilotReadActionCommand{
		ClientID: clientID.String(), ConversationID: conversationID.String(),
		IdempotencyKey: uuid.NewString(), ActionName: "list_relevant_services",
	}); !errors.Is(err, ErrInboxAIControlBlocked) {
		t.Fatalf("expired session action error = %v", err)
	}
	expired, err := repo.GetInboxAISession(ctx, clientID, conversationID)
	if err != nil || expired.Session == nil || expired.Session.State != "expired" {
		t.Fatalf("expired session = %#v, error=%v", expired, err)
	}
	control, err = repo.UpdateInboxAIConversationControl(
		ctx, clientID, conversationID, UpdateInboxAIConversationControlInput{
			ExpectedRevision: control.Revision, State: "active",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := repo.GetInboxAISession(ctx, clientID, conversationID)
	if err != nil || resumed.Session == nil || resumed.Session.State != "qualifying" {
		t.Fatalf("resumed session = %#v, error=%v", resumed, err)
	}
	paused, err := repo.UpdateInboxAIConversationControl(
		ctx, clientID, conversationID, UpdateInboxAIConversationControlInput{
			ExpectedRevision: control.Revision, State: "paused", Reason: "provider_paused",
		},
	)
	if err != nil || paused.State != "paused" {
		t.Fatalf("paused control = %#v, error=%v", paused, err)
	}
	pausedSession, err := repo.GetInboxAISession(ctx, clientID, conversationID)
	if err != nil || pausedSession.Session == nil || pausedSession.Session.State != "qualifying" {
		t.Fatalf("paused session = %#v, error=%v", pausedSession, err)
	}
	control, err = repo.UpdateInboxAIConversationControl(
		ctx, clientID, conversationID, UpdateInboxAIConversationControlInput{
			ExpectedRevision: paused.Revision, State: "active",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE inbox_ai_sessions SET state='handoff', handoff_reason='unsupported_request'
		WHERE conversation_id=$1
	`, conversationID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ExecuteSemiPilotReadAction(ctx, ExecuteSemiPilotReadActionCommand{
		ClientID: clientID.String(), ConversationID: conversationID.String(),
		IdempotencyKey: uuid.NewString(), ActionName: "list_relevant_services",
	}); !errors.Is(err, ErrInboxAIControlBlocked) {
		t.Fatalf("terminal session action error = %v", err)
	}
	control, err = repo.UpdateInboxAIConversationControl(
		ctx, clientID, conversationID, UpdateInboxAIConversationControlInput{
			ExpectedRevision: control.Revision, State: "active",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	takeover, err := repo.UpdateInboxAIConversationControl(
		ctx, clientID, conversationID, UpdateInboxAIConversationControlInput{
			ExpectedRevision: control.Revision, State: "provider_takeover", Reason: "provider_takeover",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if takeover.EffectiveMode != InboxAIModeManual || takeover.State != "provider_takeover" {
		t.Fatalf("takeover control = %#v", takeover)
	}
	if _, err := repo.ExecuteSemiPilotReadAction(ctx, ExecuteSemiPilotReadActionCommand{
		ClientID: clientID.String(), ConversationID: conversationID.String(),
		IdempotencyKey: uuid.NewString(), ActionName: "list_relevant_services",
	}); !errors.Is(err, ErrInboxAIControlBlocked) {
		t.Fatalf("post-takeover action error = %v", err)
	}
	fenced, err := repo.GetInboxAISession(ctx, clientID, conversationID)
	if err != nil || fenced.Session == nil || fenced.Session.State != "provider_takeover" {
		t.Fatalf("fenced session = %#v, error=%v", fenced, err)
	}
	handoff, err := repo.CustomerInboxAIHandoff(
		ctx, marketplaceCustomerID, conversationID, "talk_to_provider",
	)
	if err != nil || handoff.Status != "handoff" {
		t.Fatalf("customer handoff = %#v, error=%v", handoff, err)
	}
	handoffReplay, err := repo.CustomerInboxAIHandoff(
		ctx, marketplaceCustomerID, conversationID, "talk_to_provider",
	)
	if err != nil || !handoffReplay.EffectiveAt.Equal(handoff.EffectiveAt) {
		t.Fatalf("handoff replay = %#v, error=%v", handoffReplay, err)
	}
	providerDetail, err := repo.GetProviderConversationDetail(ctx, clientID, conversationID, 20)
	if err != nil || !providerDetail.Conversation.ProviderHandoffRequested {
		t.Fatalf("provider handoff visibility = %#v, error=%v", providerDetail.Conversation, err)
	}
}
