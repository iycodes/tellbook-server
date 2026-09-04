package appdata

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMarketplaceBookingConversationGetOrCreateIsOwnedAndConcurrent(t *testing.T) {
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

	var bookingID uuid.UUID
	var previousOwner uuid.NullUUID
	if err := pool.QueryRow(ctx, `
		SELECT booking.id, booking.marketplace_customer_id
		FROM bookings booking
		INNER JOIN client_profiles profile ON profile.client_id=booking.client_id
		INNER JOIN client_profile_handles handle ON handle.client_id=booking.client_id
		INNER JOIN customers customer ON customer.id=booking.customer_id
		ORDER BY booking.created_at DESC
		LIMIT 1
	`).Scan(&bookingID, &previousOwner); errors.Is(err, pgx.ErrNoRows) {
		t.Skip("no booking is available for the inbox integration test")
	} else if err != nil {
		t.Fatal(err)
	}

	ownerID, otherCustomerID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO marketplace_customers (id,full_name,email,email_verified_at)
		VALUES ($1,'Inbox Owner',$2,NOW()),($3,'Inbox Other',$4,NOW())
	`, ownerID, "inbox-owner-"+ownerID.String()+"@example.com",
		otherCustomerID, "inbox-other-"+otherCustomerID.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE bookings SET marketplace_customer_id=$1 WHERE id=$2`, ownerID, bookingID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `
			DELETE FROM inbox_conversations WHERE marketplace_customer_id IN ($1,$2)
		`, ownerID, otherCustomerID)
		if previousOwner.Valid {
			_, _ = pool.Exec(ctx, `UPDATE bookings SET marketplace_customer_id=$1 WHERE id=$2`, previousOwner.UUID, bookingID)
		} else {
			_, _ = pool.Exec(ctx, `UPDATE bookings SET marketplace_customer_id=NULL WHERE id=$1`, bookingID)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM marketplace_customers WHERE id IN ($1,$2)`, ownerID, otherCustomerID)
	})

	type result struct {
		value GetOrCreateInboxConversationResult
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	repo := NewRepository(pool)
	for range 2 {
		go func() {
			<-start
			value, resultErr := repo.GetOrCreateMarketplaceBookingConversation(ctx, ownerID, bookingID)
			results <- result{value: value, err: resultErr}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent get-or-create errors: first=%v second=%v", first.err, second.err)
	}
	if first.value.Detail.Conversation.ID != second.value.Detail.Conversation.ID {
		t.Fatalf("conversation IDs differ: %s and %s", first.value.Detail.Conversation.ID, second.value.Detail.Conversation.ID)
	}
	if first.value.Created == second.value.Created {
		t.Fatalf("created flags = %v and %v, want exactly one creator", first.value.Created, second.value.Created)
	}
	conversationID := uuid.MustParse(first.value.Detail.Conversation.ID)

	var conversations, links, states int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM inbox_conversations WHERE client_id=booking.client_id AND marketplace_customer_id=$2),
			(SELECT COUNT(*) FROM inbox_conversation_bookings WHERE conversation_id=$3 AND booking_id=$1),
			(SELECT COUNT(*) FROM inbox_participant_states WHERE conversation_id=$3)
		FROM bookings booking WHERE booking.id=$1
	`, bookingID, ownerID, conversationID).Scan(&conversations, &links, &states); err != nil {
		t.Fatal(err)
	}
	if conversations != 1 || links != 1 || states != 2 {
		t.Fatalf("conversation/link/state counts = %d/%d/%d, want 1/1/2", conversations, links, states)
	}

	detail, err := repo.GetMarketplaceConversationDetail(ctx, ownerID, conversationID, 20)
	if err != nil || detail.Conversation.Counterparty.Kind != "provider" || len(detail.Conversation.BookingContexts) != 1 {
		t.Fatalf("owned conversation detail = %+v, error = %v", detail, err)
	}
	if _, err := repo.GetMarketplaceConversationDetail(ctx, otherCustomerID, conversationID, 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-customer conversation error = %v, want not found", err)
	}
	if _, err := repo.GetOrCreateMarketplaceBookingConversation(ctx, otherCustomerID, bookingID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-customer booking conversation error = %v, want not found", err)
	}

	var providerID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT client_id FROM inbox_conversations WHERE id=$1`, conversationID).Scan(&providerID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SendMarketplaceMessage(
		ctx, ownerID, conversationID, uuid.New(), "Invalid booking context", uuid.NullUUID{UUID: uuid.New(), Valid: true},
	); !errors.Is(err, ErrInboxBookingContext) {
		t.Fatalf("foreign booking context error = %v, want booking context error", err)
	}
	customerMessageID, providerMessageID := uuid.New(), uuid.New()
	customerSend, err := repo.SendMarketplaceMessage(
		ctx, ownerID, conversationID, customerMessageID, "Hello from the customer", uuid.NullUUID{},
	)
	if err != nil {
		t.Fatalf("send marketplace message: %v", err)
	}
	providerSend, err := repo.SendProviderMessage(
		ctx, providerID, conversationID, providerMessageID, "Hello from the provider", uuid.NullUUID{},
	)
	if err != nil {
		t.Fatalf("send provider message: %v", err)
	}
	replay, err := repo.SendMarketplaceMessage(
		ctx, ownerID, conversationID, customerMessageID, "Hello from the customer", uuid.NullUUID{},
	)
	if err != nil || replay.Message.ID != customerSend.Message.ID || !replay.Replayed {
		t.Fatalf("idempotent replay = %+v, error = %v, want message %s", replay, err, customerSend.Message.ID)
	}
	if customerSend.Replayed || providerSend.Replayed {
		t.Fatalf("new send replay flags = customer %v/provider %v, want false/false", customerSend.Replayed, providerSend.Replayed)
	}
	if replay.Conversation.Preview != providerSend.Message.Content {
		t.Fatalf("replayed old command regressed preview to %q, want %q", replay.Conversation.Preview, providerSend.Message.Content)
	}
	if _, err := repo.SendMarketplaceMessage(
		ctx, ownerID, conversationID, customerMessageID, "Changed retry content", uuid.NullUUID{},
	); !errors.Is(err, ErrInboxIdempotencyConflict) {
		t.Fatalf("changed idempotency replay error = %v, want conflict", err)
	}

	marketplaceDetail, err := repo.GetMarketplaceConversationDetail(ctx, ownerID, conversationID, 20)
	if err != nil || len(marketplaceDetail.Messages) != 2 {
		t.Fatalf("marketplace round-trip detail has %d messages, error = %v, want 2", len(marketplaceDetail.Messages), err)
	}
	providerDetail, err := repo.GetProviderConversationDetail(ctx, providerID, conversationID, 20)
	if err != nil || len(providerDetail.Messages) != 2 {
		t.Fatalf("provider round-trip detail has %d messages, error = %v, want 2", len(providerDetail.Messages), err)
	}
	if marketplaceDetail.Messages[0].ID != customerSend.Message.ID || marketplaceDetail.Messages[1].ID != providerSend.Message.ID {
		t.Fatalf("persisted message order = %s/%s, want %s/%s",
			marketplaceDetail.Messages[0].ID, marketplaceDetail.Messages[1].ID,
			customerSend.Message.ID, providerSend.Message.ID)
	}
	var customerMessageSequence, providerMessageSequence int64
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT sequence FROM inbox_messages WHERE id=$1),
			(SELECT sequence FROM inbox_messages WHERE id=$2)
	`, uuid.MustParse(customerSend.Message.ID), uuid.MustParse(providerSend.Message.ID)).Scan(
		&customerMessageSequence, &providerMessageSequence,
	); err != nil {
		t.Fatal(err)
	}
	var customerReadSequence, providerReadSequence int64
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT last_read_sequence FROM inbox_participant_states
			 WHERE conversation_id=$1 AND participant_type='marketplace_customer' AND participant_id=$2),
			(SELECT last_read_sequence FROM inbox_participant_states
			 WHERE conversation_id=$1 AND participant_type='provider' AND participant_id=$3)
	`, conversationID, ownerID, providerID).Scan(&customerReadSequence, &providerReadSequence); err != nil {
		t.Fatal(err)
	}
	if customerReadSequence != customerMessageSequence || providerReadSequence != providerMessageSequence {
		t.Fatalf(
			"sender read sequences = customer %d/provider %d, want %d/%d",
			customerReadSequence, providerReadSequence, customerMessageSequence, providerMessageSequence,
		)
	}
	newestPage, err := repo.GetMarketplaceConversationDetail(ctx, ownerID, conversationID, 1)
	if err != nil || len(newestPage.Messages) != 1 || newestPage.NextBeforeCursor == "" {
		t.Fatalf("bounded newest page = %+v, error = %v", newestPage, err)
	}
	olderPage, err := repo.ListMarketplaceConversationMessages(
		ctx, ownerID, conversationID, newestPage.NextBeforeCursor, 1,
	)
	if err != nil || len(olderPage.Items) != 1 || olderPage.Items[0].ID != customerSend.Message.ID || olderPage.SyncCursor == "" {
		t.Fatalf("older marketplace page = %+v, error = %v", olderPage, err)
	}
	if _, err := repo.ListMarketplaceConversationMessages(
		ctx, otherCustomerID, conversationID, newestPage.NextBeforeCursor, 1,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign message page error = %v, want not found", err)
	}
	unreadBeforeRead, err := repo.GetMarketplaceInboxUnreadCount(ctx, ownerID)
	if err != nil || unreadBeforeRead.UnreadTotal != 1 {
		t.Fatalf("marketplace unread before read = %+v, error = %v, want 1", unreadBeforeRead, err)
	}
	readState, err := repo.MarkMarketplaceConversationRead(
		ctx, ownerID, conversationID, uuid.MustParse(providerSend.Message.ID),
	)
	if err != nil || readState.ConversationUnreadCount != 0 || readState.UnreadTotal != 0 {
		t.Fatalf("marketplace read state = %+v, error = %v", readState, err)
	}
	var readEventsBeforeReplay int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM inbox_events WHERE conversation_id=$1 AND event_type='read.updated'
	`, conversationID).Scan(&readEventsBeforeReplay); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.MarkMarketplaceConversationRead(
		ctx, ownerID, conversationID, uuid.MustParse(customerSend.Message.ID),
	); err != nil {
		t.Fatalf("monotonic older read: %v", err)
	}
	var readEventsAfterReplay int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM inbox_events WHERE conversation_id=$1 AND event_type='read.updated'
	`, conversationID).Scan(&readEventsAfterReplay); err != nil {
		t.Fatal(err)
	}
	if readEventsAfterReplay != readEventsBeforeReplay {
		t.Fatalf("older read emitted event count %d, want %d", readEventsAfterReplay, readEventsBeforeReplay)
	}
	var messageEventCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM inbox_events WHERE conversation_id=$1 AND event_type='message.created'
	`, conversationID).Scan(&messageEventCount); err != nil {
		t.Fatal(err)
	}
	if messageEventCount != 2 {
		t.Fatalf("message.created event count = %d, want 2 after replay", messageEventCount)
	}

	marketplaceList, err := repo.ListMarketplaceConversations(ctx, ownerID, 20, "", "")
	if err != nil || len(marketplaceList.Items) != 1 || marketplaceList.Items[0].ID != conversationID.String() {
		t.Fatalf("marketplace list = %+v, error = %v", marketplaceList, err)
	}
	providerList, err := repo.ListProviderConversations(ctx, providerID, 20, "inbox owner", "all", "")
	if err != nil || len(providerList.Items) == 0 || providerList.Items[0].ID != conversationID.String() {
		t.Fatalf("provider list = %+v, error = %v", providerList, err)
	}
	providerNoMatch, err := repo.ListProviderConversations(ctx, providerID, 20, "does-not-exist", "all", "")
	if err != nil || len(providerNoMatch.Items) != 0 {
		t.Fatalf("provider no-match search = %+v, error = %v", providerNoMatch, err)
	}
	archiveState, err := repo.SetProviderConversationArchived(ctx, providerID, conversationID, true)
	if err != nil || !archiveState.Archived {
		t.Fatalf("archive provider conversation = %+v, error = %v", archiveState, err)
	}
	providerActive, err := repo.ListProviderConversations(ctx, providerID, 20, "inbox owner", "all", "")
	if err != nil || len(providerActive.Items) != 0 {
		t.Fatalf("provider active list after archive = %+v, error = %v", providerActive, err)
	}
	providerArchived, err := repo.ListProviderConversations(ctx, providerID, 20, "inbox owner", "archived", "")
	if err != nil || len(providerArchived.Items) != 1 || !providerArchived.Items[0].Archived {
		t.Fatalf("provider archived list = %+v, error = %v", providerArchived, err)
	}
	restoredState, err := repo.SetProviderConversationArchived(ctx, providerID, conversationID, false)
	if err != nil || restoredState.Archived {
		t.Fatalf("restore provider conversation = %+v, error = %v", restoredState, err)
	}
	if _, err := repo.SetProviderConversationArchived(ctx, uuid.New(), conversationID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign archive error = %v, want not found", err)
	}
	if _, err := repo.SetProviderConversationArchived(ctx, providerID, conversationID, true); err != nil {
		t.Fatalf("archive before inbound message: %v", err)
	}
	var beforeInboundEventSequence int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),0) FROM inbox_events`).Scan(
		&beforeInboundEventSequence,
	); err != nil {
		t.Fatal(err)
	}
	providerUnreadBeforeInbound, err := repo.GetProviderInboxUnreadCount(ctx, providerID)
	if err != nil {
		t.Fatal(err)
	}
	inboundAfterArchive, err := repo.SendMarketplaceMessage(
		ctx, ownerID, conversationID, uuid.New(), "A new customer message", uuid.NullUUID{},
	)
	if err != nil {
		t.Fatalf("send inbound message to archived provider conversation: %v", err)
	}
	providerAfterInbound, err := repo.GetProviderConversationDetail(ctx, providerID, conversationID, 20)
	if err != nil || providerAfterInbound.Conversation.Archived || providerAfterInbound.Conversation.UnreadCount != 1 {
		t.Fatalf(
			"provider state after inbound message = %+v, error = %v, want active with one unread",
			providerAfterInbound.Conversation, err,
		)
	}
	if providerAfterInbound.Conversation.Preview != inboundAfterArchive.Message.Content {
		t.Fatalf(
			"provider preview after inbound message = %q, want %q",
			providerAfterInbound.Conversation.Preview, inboundAfterArchive.Message.Content,
		)
	}
	providerEvents, err := repo.ListInboxEventsAfter(
		ctx, "provider", providerID, encodeInboxSequenceCursor(beforeInboundEventSequence), 100,
	)
	if err != nil || providerEvents.Reset || len(providerEvents.Events) == 0 {
		t.Fatalf("provider event drain = %+v, error = %v", providerEvents, err)
	}
	var foundInbound bool
	for _, event := range providerEvents.Events {
		if event.Type == "message.created" && event.Message != nil && event.Message.ID == inboundAfterArchive.Message.ID {
			foundInbound = true
			if event.Conversation.ID != conversationID.String() ||
				event.UnreadTotal != providerUnreadBeforeInbound.UnreadTotal+1 {
				t.Fatalf("hydrated inbound event = %+v", event)
			}
		}
	}
	if !foundInbound {
		t.Fatalf("provider event drain omitted message %s", inboundAfterArchive.Message.ID)
	}
	for _, event := range providerEvents.Events {
		if event.Type == "read.updated" && event.Message != nil {
			t.Fatalf("read event hydrated a historical message as new: %+v", event)
		}
	}
	foreignEvents, err := repo.ListInboxEventsAfter(ctx, "marketplace_customer", otherCustomerID, "0", 100)
	if err != nil || len(foreignEvents.Events) != 0 {
		t.Fatalf("foreign customer event drain = %+v, error = %v", foreignEvents, err)
	}
	futureEvents, err := repo.ListInboxEventsAfter(
		ctx, "provider", providerID, encodeInboxSequenceCursor(1<<60), 100,
	)
	if err != nil || !futureEvents.Reset {
		t.Fatalf("future event cursor = %+v, error = %v, want reset", futureEvents, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE inbox_events SET created_at=NOW()-INTERVAL '60 days'
		WHERE sequence=(SELECT MIN(sequence) FROM inbox_events WHERE conversation_id=$1)
	`, conversationID); err != nil {
		t.Fatal(err)
	}
	deletedEvents, err := pruneExpiredInboxEvents(
		ctx, pool, time.Now().UTC().Add(-inboxEventRetentionAge), 1,
	)
	if err != nil || deletedEvents != 1 {
		t.Fatalf("prune expired inbox events deleted %d, error = %v, want 1", deletedEvents, err)
	}
	moderation, err := repo.SetInboxConversationDisabled(
		ctx, conversationID, true, "integration abuse report", "integration-test",
	)
	if err != nil || !moderation.Changed || !moderation.Disabled || moderation.AuditID == "" {
		t.Fatalf("disable moderation = %+v, error = %v", moderation, err)
	}
	replayedModeration, err := repo.SetInboxConversationDisabled(
		ctx, conversationID, true, "integration abuse report", "integration-test",
	)
	if err != nil || replayedModeration.Changed {
		t.Fatalf("idempotent moderation = %+v, error = %v", replayedModeration, err)
	}
	var moderationAuditCount, disabledEventCount int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM inbox_conversation_moderation_events WHERE conversation_id=$1),
			(SELECT COUNT(*) FROM inbox_events
			 WHERE conversation_id=$1 AND event_type='conversation.disabled')
	`, conversationID).Scan(&moderationAuditCount, &disabledEventCount); err != nil {
		t.Fatal(err)
	}
	if moderationAuditCount != 1 || disabledEventCount != 1 {
		t.Fatalf("moderation audit/events = %d/%d, want 1/1", moderationAuditCount, disabledEventCount)
	}
	if _, err := repo.SendMarketplaceMessage(
		ctx, ownerID, conversationID, uuid.New(), "Should be blocked", uuid.NullUUID{},
	); !errors.Is(err, ErrInboxConversationDisabled) {
		t.Fatalf("disabled marketplace send error = %v, want disabled", err)
	}
	if _, err := repo.SendProviderMessage(
		ctx, providerID, conversationID, uuid.New(), "Should also be blocked", uuid.NullUUID{},
	); !errors.Is(err, ErrInboxConversationDisabled) {
		t.Fatalf("disabled provider send error = %v, want disabled", err)
	}
	enabled, err := repo.SetInboxConversationDisabled(
		ctx, conversationID, false, "investigation completed", "integration-test",
	)
	if err != nil || !enabled.Changed || enabled.Disabled {
		t.Fatalf("enable moderation = %+v, error = %v", enabled, err)
	}
	if _, err := repo.SendProviderMessage(
		ctx, providerID, conversationID, uuid.New(), "Messaging restored", uuid.NullUUID{},
	); err != nil {
		t.Fatalf("send after moderation enable: %v", err)
	}
}
