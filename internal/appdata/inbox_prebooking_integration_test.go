package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMarketplaceProviderConversationIsPreBookingIdempotentAndAIReady(t *testing.T) {
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

	var providerID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT client_id FROM client_profiles
		WHERE marketplace_enabled AND market_configured_at IS NOT NULL
		ORDER BY client_id LIMIT 1
	`).Scan(&providerID); errors.Is(err, pgx.ErrNoRows) {
		t.Skip("no marketplace-enabled provider is available")
	} else if err != nil {
		t.Fatal(err)
	}

	marketplaceCustomerID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO marketplace_customers (id,full_name,email,email_verified_at)
		VALUES ($1,'Pre-booking Customer',$2,NOW())
	`, marketplaceCustomerID, "prebooking-"+marketplaceCustomerID.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM inbox_conversations WHERE marketplace_customer_id=$1`, marketplaceCustomerID)
		_, _ = pool.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=$1`, marketplaceCustomerID)
	})

	type result struct {
		value GetOrCreateInboxConversationResult
		err   error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	repo := NewRepository(pool)
	for range 2 {
		go func() {
			<-start
			value, resultErr := repo.GetOrCreateMarketplaceProviderConversation(
				ctx, marketplaceCustomerID, providerID,
			)
			results <- result{value: value, err: resultErr}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent provider conversation errors: first=%v second=%v", first.err, second.err)
	}
	if first.value.Detail.Conversation.ID != second.value.Detail.Conversation.ID {
		t.Fatalf("conversation IDs differ: %s and %s", first.value.Detail.Conversation.ID, second.value.Detail.Conversation.ID)
	}
	if first.value.Created == second.value.Created {
		t.Fatalf("created flags = %v and %v, want exactly one creator", first.value.Created, second.value.Created)
	}
	conversationID := uuid.MustParse(first.value.Detail.Conversation.ID)

	var conversationCount, participantCount int
	var providerCustomerID uuid.NullUUID
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM inbox_conversations WHERE client_id=$1 AND marketplace_customer_id=$2),
			(SELECT COUNT(*) FROM inbox_participant_states WHERE conversation_id=$3),
			(SELECT customer_id FROM inbox_conversations WHERE id=$3)
	`, providerID, marketplaceCustomerID, conversationID).Scan(
		&conversationCount, &participantCount, &providerCustomerID,
	); err != nil {
		t.Fatal(err)
	}
	if conversationCount != 1 || participantCount != 2 || providerCustomerID.Valid {
		t.Fatalf(
			"pre-booking relationship = conversations %d/participants %d/customer %v, want 1/2/null",
			conversationCount, participantCount, providerCustomerID,
		)
	}

	presentation := InboxMessagePresentation{
		Kind:    "booking_link",
		Version: 1,
		Data: json.RawMessage(`{
			"action_id":"10000000-0000-4000-8000-000000000001",
			"provider_id":"` + providerID.String() + `",
			"provider_handle":"integration-provider",
			"href":"/providers/integration-provider",
			"label":"Book now"
		}`),
	}
	aiMessage, err := repo.SendAIInboxMessage(
		ctx, providerID, conversationID, uuid.New(), "Choose a service when you are ready.",
		uuid.NullUUID{}, presentation,
	)
	if err != nil {
		t.Fatalf("send internal AI message: %v", err)
	}
	if aiMessage.Message.SenderType != "ai" || aiMessage.Message.Presentation == nil ||
		aiMessage.Message.Presentation.Kind != presentation.Kind {
		t.Fatalf("AI message = %+v, want typed AI presentation", aiMessage.Message)
	}

	providerDetail, err := repo.GetProviderConversationDetail(ctx, providerID, conversationID, 20)
	if err != nil || providerDetail.Conversation.UnreadCount != 0 {
		t.Fatalf("provider detail after provider-side AI message = %+v, error=%v", providerDetail, err)
	}
	marketplaceDetail, err := repo.GetMarketplaceConversationDetail(
		ctx, marketplaceCustomerID, conversationID, 20,
	)
	if err != nil || marketplaceDetail.Conversation.UnreadCount != 1 ||
		len(marketplaceDetail.Messages) != 1 || marketplaceDetail.Messages[0].Presentation == nil {
		t.Fatalf("marketplace AI message detail = %+v, error=%v", marketplaceDetail, err)
	}

	replay, err := repo.SendAIInboxMessage(
		ctx, providerID, conversationID, uuid.MustParse(aiMessage.Message.ClientMessageID),
		"Choose a service when you are ready.", uuid.NullUUID{}, presentation,
	)
	if err != nil || !replay.Replayed || replay.Message.ID != aiMessage.Message.ID {
		t.Fatalf("AI message replay = %+v, error=%v", replay, err)
	}

	if _, err := repo.GetOrCreateMarketplaceProviderConversation(
		ctx, marketplaceCustomerID, uuid.New(),
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unavailable provider error = %v, want not found", err)
	}
}
