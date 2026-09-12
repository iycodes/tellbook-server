package appdata

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/whatsapp"
)

func TestTessaDeliveryBootstrapPaginationAndRealtimeIntegration(t *testing.T) {
	ctx, repo, clientID, threadID, worker, _ := tessaWhatsAppFixture(t)
	queueTessaWhatsApp(t, ctx, repo, clientID, "Explain bookings")
	if _, err := worker.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	initial, err := repo.GetTessaBootstrap(ctx, clientID, "test-v1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Messages) != 2 || initial.Messages[0].WhatsAppDelivery != nil || initial.Messages[1].WhatsAppDelivery == nil || initial.Messages[1].WhatsAppDelivery.Status != "pending" {
		t.Fatal("bootstrap delivery metadata missing or attached to provider input")
	}
	cursor := initial.RealtimeCursor
	for _, status := range []string{"processing", "dispatching", "retry"} {
		if _, err = repo.db.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET status=$2 WHERE client_id=$1 AND kind='answer'`, clientID, status); err != nil {
			t.Fatal(err)
		}
	}
	events, err := repo.ListTessaEventsAfter(ctx, clientID, cursor, 100)
	if err != nil || len(events.Events) != 0 {
		t.Fatal("internal delivery mechanics emitted UI events", err)
	}
	if _, err = repo.db.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET status='accepted',provider_message_id='private-wamid',last_error_code='private-debug-error' WHERE client_id=$1 AND kind='answer'`, clientID); err != nil {
		t.Fatal(err)
	}
	events, err = repo.ListTessaEventsAfter(ctx, clientID, cursor, 100)
	if err != nil || len(events.Events) != 1 {
		t.Fatal("delivery transition missing from durable SSE", err)
	}
	event := events.Events[0]
	if event.Type != "message.delivery_changed" || event.Message == nil || event.Run != nil || event.Message.ID != initial.Messages[1].ID {
		t.Fatal("wrong delivery event entity")
	}
	delivery := event.Message.WhatsAppDelivery
	if delivery == nil || delivery.Status != "accepted" || delivery.AcceptedAt == nil || delivery.DeliveredAt != nil {
		t.Fatal("acceptance falsely implied delivery")
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "19990001") {
		t.Fatal("transport details leaked into public event")
	}
	// Duplicate writes do not allocate another event or change recorded acceptance.
	if _, err = repo.db.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET status='accepted' WHERE client_id=$1 AND kind='answer'`, clientID); err != nil {
		t.Fatal(err)
	}
	unchanged, err := repo.ListTessaEventsAfter(ctx, clientID, events.Cursor, 100)
	if err != nil || len(unchanged.Events) != 0 {
		t.Fatal("duplicate event", err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	if _, err = repo.db.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET status='delivered',status_at=$2 WHERE client_id=$1 AND kind='answer'`, clientID, at); err != nil {
		t.Fatal(err)
	}
	page, err := repo.ListOlderTessaMessages(ctx, clientID, threadID, 3, 50)
	if err != nil {
		t.Fatal(err)
	}
	got := page.Items[len(page.Items)-1].WhatsAppDelivery
	if got == nil || got.Status != "delivered" || got.DeliveredAt == nil || !got.DeliveredAt.Equal(at) || got.SentAt != nil || !got.AcceptedAt.Equal(*delivery.AcceptedAt) {
		t.Fatal("pagination lost observed timestamps")
	}
}

func TestTessaRevocationPublishesDeliveryCancellationIntegration(t *testing.T) {
	ctx, repo, clientID, _, worker, _ := tessaWhatsAppFixture(t)
	queueTessaWhatsApp(t, ctx, repo, clientID, "Explain bookings")
	if _, err := worker.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	initial, err := repo.GetTessaBootstrap(ctx, clientID, "test-v1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if err = whatsapp.NewTessaLinkRepository(repo.db, "19990001", "+2348000000000", true).Disconnect(ctx, clientID); err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListTessaEventsAfter(ctx, clientID, initial.RealtimeCursor, 100)
	if err != nil || len(events.Events) != 1 || events.Events[0].Message == nil || events.Events[0].Message.WhatsAppDelivery.Status != "cancelled" {
		t.Fatal("revocation did not atomically publish cancellation", err)
	}
}
