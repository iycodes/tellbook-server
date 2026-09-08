package whatsapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestTessaAnswerLinksAndLimits(t *testing.T) {
	for _, origin := range []string{"http://provider.example.invalid", "https://user:pass@provider.example.invalid", "https://provider.example.invalid/?token=secret", "https://provider.example.invalid/path", "javascript:alert(1)"} {
		if (&TessaLinkRepository{}).WithAssistantReplies(origin, "test-v1") == nil {
			t.Fatal("invalid origin accepted")
		}
	}
	body, err := renderTessaAnswer("Your bookings are available.", []byte(`{"actions":[{"route_id":"bookings","label":"https://evil.invalid"},{"route_id":"booking_details","entity_id":"//evil.invalid"},{"route_id":"evil"}]}`), "https://provider.example.invalid")
	if err != nil || strings.Contains(body.Body, "http") || body.Button == nil || body.Button.URL != "https://provider.example.invalid/bookings" || body.Button.Label != "View bookings" {
		t.Fatal(body, err)
	}
	body, err = renderTessaAnswer(strings.Repeat("😀", 4000), []byte(`{"actions":[{"route_id":"bookings"},{"route_id":"inbox"},{"route_id":"customers"}]}`), "https://provider.example.invalid")
	if err == nil {
		t.Fatal("oversized booking button body accepted")
	}
	body, err = renderTessaAnswer(strings.Repeat("😀", 4000), []byte(`{"actions":[]}`), "https://provider.example.invalid")
	if err != nil || !utf8.ValidString(body.Body) || utf8.RuneCountInString(body.Body) != 4000 || body.Button != nil {
		t.Fatal("plain text limit changed", err)
	}
}

func TestTessaBookingButtonSelection(t *testing.T) {
	const first = "10000000-0000-4000-8000-000000000001"
	const second = "10000000-0000-4000-8000-000000000002"
	for _, test := range []struct{ name, actions, path, label string }{
		{"single", `[{"route_id":"booking_details","entity_id":"` + first + `"}]`, "/bookings?booking=" + first, "View booking"},
		{"multiple", `[{"route_id":"booking_details","entity_id":"` + first + `"},{"route_id":"booking_details","entity_id":"` + second + `"},{"route_id":"bookings"}]`, "/bookings", "View bookings"},
		{"duplicate", `[{"route_id":"booking_details","entity_id":"` + first + `"},{"route_id":"booking_details","entity_id":"` + first + `"}]`, "/bookings?booking=" + first, "View booking"},
		{"empty list", `[{"route_id":"bookings"}]`, "/bookings", "View bookings"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := renderTessaAnswer("7 September, 9:10 AM (Africa/Lagos)", []byte(`{"actions":`+test.actions+`}`), "https://provider.example.invalid")
			if err != nil || result.Button == nil || result.Button.URL != "https://provider.example.invalid"+test.path || result.Button.Label != test.label || strings.Contains(result.Body, "http") {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func TestTessaConversationCapabilityRequiresCompleteWiringIntegration(t *testing.T) {
	ctx, r, clientID, _ := tessaLinkFixture(t)
	state, err := r.State(ctx, clientID)
	if err != nil || state.ConversationAvailable {
		t.Fatal("linking alone enabled chat", err)
	}
	if err = r.WithAssistantReplies("https://provider.example.invalid", "test-v1"); err != nil {
		t.Fatal(err)
	}
	state, err = r.State(ctx, clientID)
	if err != nil || state.ConversationAvailable {
		t.Fatal("sender without ingress enabled chat", err)
	}
	r.WithConversationIngress(&captureTessaIngress{})
	state, err = r.State(ctx, clientID)
	if err != nil || !state.ConversationAvailable {
		t.Fatal("complete wiring not reflected in capability", err)
	}
	r.enabled = false
	state, err = r.State(ctx, clientID)
	if err != nil || state.ConversationAvailable {
		t.Fatal("disabled chat reported available", err)
	}
}

func TestTessaAnswerStatusTimestampsAndPublicationIntegration(t *testing.T) {
	ctx, r, clientID, id, store := tessaAnswerFixture(t)
	w := NewTessaControlWorker(r, &answerCaptureSender{}, nil)
	if _, err := w.processOne(ctx); err != nil {
		t.Fatal(err)
	}
	var accepted *time.Time
	if err := r.db.QueryRow(ctx, `SELECT accepted_at FROM tessa_whatsapp_outbox WHERE id=$1`, id).Scan(&accepted); err != nil || accepted == nil {
		t.Fatal("Graph acceptance timestamp missing", err)
	}
	base := time.Now().UTC().Truncate(time.Microsecond)
	for _, update := range []struct {
		status string
		at     time.Time
	}{{"delivered", base}, {"sent", base.Add(-time.Minute)}, {"read", base.Add(time.Second)}, {"read", base.Add(time.Second)}} {
		receipt := WebhookReceipt{DedupeKey: receiptDedupeKey("delivery-state", uuid.NewString()), BusinessID: "100", PhoneNumberID: r.phoneID, EventKind: "status", MessageID: "wamid.answer." + id.String(), MessageStatus: update.status, CorrelationID: id.String(), ProviderTimestamp: &update.at, ProcessingStatus: "pending"}
		if err := store.StoreWebhookReceipts(ctx, []WebhookReceipt{receipt}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			r.db.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, receipt.DedupeKey)
		})
		if err := w.processStatuses(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var finalAccepted, delivered, read *time.Time
	var sent *time.Time
	var status string
	var events int
	if err := r.db.QueryRow(ctx, `SELECT status,accepted_at,sent_at,delivered_at,read_at FROM tessa_whatsapp_outbox WHERE id=$1`, id).Scan(&status, &finalAccepted, &sent, &delivered, &read); err != nil {
		t.Fatal(err)
	}
	if status != "read" || sent != nil || delivered == nil || read == nil || finalAccepted == nil || !finalAccepted.Equal(*accepted) || !delivered.Equal(base) || !read.Equal(base.Add(time.Second)) {
		t.Fatal("callback timestamps regressed or were fabricated")
	}
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_events WHERE client_id=$1 AND event_type='message.delivery_changed'`, clientID).Scan(&events); err != nil || events != 3 {
		t.Fatal("duplicate/out-of-order callback emitted a delivery event", events, err)
	}
}

func tessaAnswerFixture(t *testing.T) (context.Context, *TessaLinkRepository, uuid.UUID, uuid.UUID, *WebhookRepository) {
	t.Helper()
	ctx, r, clientID, store := tessaLinkFixture(t)
	if err := r.WithAssistantReplies("https://provider.example.invalid", "test-v1"); err != nil {
		t.Fatal(err)
	}
	receipt := tessaInbound("", "", "2348142751683")
	if err := store.StoreWebhookReceipts(ctx, []WebhookReceipt{receipt}); err != nil {
		t.Fatal(err)
	}
	thread, question, run, answer := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	err := pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		statements := []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO tessa_whatsapp_connections(client_id,phone_number_id,destination,status,security_revision,notice_revision,expires_at) SELECT id,'19990001','+2348142751683','active',security_revision,$2,NOW()+INTERVAL '30 days' FROM clients WHERE id=$1`, []any{clientID, TessaWhatsAppNotice}},
			{`INSERT INTO tessa_preferences(client_id,introduction_completed_at,acknowledged_notice_revision,notice_acknowledged_at) VALUES($1,NOW(),'test-v1',NOW())`, []any{clientID}},
			{`INSERT INTO tessa_threads(id,client_id,creation_request_id,last_message_sequence) VALUES($1,$2,$1,2)`, []any{thread, clientID}},
			{`INSERT INTO tessa_messages(id,thread_id,client_id,sequence,sender_type,source_channel,client_message_id,request_fingerprint,content) VALUES($1,$2,$3,1,'provider','whatsapp',$1,repeat('a',64),'Explain bookings')`, []any{question, thread, clientID}},
			{`INSERT INTO tessa_runs(id,thread_id,client_id,trigger_message_id,status,stage,input_hash,schema_revision,config_hash,primary_provider,primary_model,completed_at) VALUES($1,$2,$3,$4,'completed','completed',repeat('a',64),'test-v1',repeat('a',64),'self_hosted','test',NOW())`, []any{run, thread, clientID, question}},
			{`INSERT INTO tessa_messages(id,thread_id,client_id,sequence,sender_type,source_channel,run_id,content,presentation) VALUES($1,$2,$3,2,'tessa','whatsapp',$4,'Your committed booking answer.','{"actions":[{"route_id":"bookings"}]}')`, []any{answer, thread, clientID, run}},
			{`INSERT INTO tessa_whatsapp_ingress(id,source_receipt_id,client_id,thread_id,phone_number_id,destination,connection_revision,security_revision,source_message_id,source_timestamp,status,message_id,run_id,completed_at)
              SELECT $1,q.id,c.client_id,$2,c.phone_number_id,c.destination,c.revision,c.security_revision,q.wamid,q.provider_timestamp,'admitted',$3,$4,NOW()
              FROM tessa_whatsapp_connections c,meta_whatsapp_webhook_receipts q WHERE c.client_id=$5 AND q.dedupe_key=$6`, []any{uuid.New(), thread, question, run, clientID, receipt.DedupeKey}},
		}
		for _, s := range statements {
			if _, err := tx.Exec(ctx, s.sql, s.args...); err != nil {
				return err
			}
		}
		return EnqueueTessaAnswerTx(ctx, tx, clientID, thread, run, answer, "test-v1")
	})
	if err != nil {
		t.Fatal(err)
	}
	var delivery uuid.UUID
	if err = r.db.QueryRow(ctx, `SELECT id FROM tessa_whatsapp_outbox WHERE assistant_message_id=$1`, answer).Scan(&delivery); err != nil {
		t.Fatal(err)
	}
	return ctx, r, clientID, delivery, store
}

type answerCaptureSender struct {
	calls   int
	bodies  []string
	buttons []URLButton
	err     error
	before  func(string)
}

func (s *answerCaptureSender) SendURLButton(ctx context.Context, to, body string, button URLButton, correlation string) (SendResult, error) {
	s.buttons = append(s.buttons, button)
	return s.SendText(ctx, to, body, correlation)
}

func TestTessaAnswerReplacementCancelsOldDestinationIntegration(t *testing.T) {
	ctx, r, clientID, answerID, store := tessaAnswerFixture(t)
	challenge, err := r.Start(ctx, clientID, "+2348000000001", TessaWhatsAppNotice)
	if err != nil {
		t.Fatal(err)
	}
	receipt := tessaInbound("tessa_link", tessaChallengeToken(t, challenge), "2348000000001")
	if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{receipt}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		r.db.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, receipt.DedupeKey)
	})
	var status, destination string
	if err = r.db.QueryRow(ctx, `SELECT status,destination FROM tessa_whatsapp_outbox WHERE id=$1`, answerID).Scan(&status, &destination); err != nil || status != "cancelled" || destination != "+2348142751683" {
		t.Fatal("replacement redirected or left an old answer runnable", status, destination, err)
	}
	var revision int64
	if err = r.db.QueryRow(ctx, `SELECT revision,destination FROM tessa_whatsapp_connections WHERE client_id=$1`, clientID).Scan(&revision, &destination); err != nil || revision <= 1 || destination != "+2348000000001" {
		t.Fatal("replacement did not advance connection authority", revision, destination, err)
	}
	sender := &answerCaptureSender{}
	w := NewTessaControlWorker(r, sender, nil)
	for range 3 {
		if _, err = w.processOne(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range sender.bodies {
		if strings.Contains(body, "Your committed booking answer") {
			t.Fatal("replacement replayed the previous destination's private answer")
		}
	}
}

func (s *answerCaptureSender) SendText(_ context.Context, _, body, correlation string) (SendResult, error) {
	s.calls++
	s.bodies = append(s.bodies, body)
	if s.before != nil {
		s.before(correlation)
	}
	return SendResult{MessageID: "wamid.answer." + correlation}, s.err
}

func TestTessaAnswerDispatchFencesAndRestartIntegration(t *testing.T) {
	for _, scenario := range []string{"send", "window", "grant_expired", "revoked", "security", "notice", "archived", "namespace", "disabled", "retry", "unknown", "early_callback"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, r, clientID, id, store := tessaAnswerFixture(t)
			sender := &answerCaptureSender{}
			w := NewTessaControlWorker(r, sender, nil)
			var err error
			switch scenario {
			case "window":
				_, err = r.db.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET window_expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, id)
			case "grant_expired":
				_, err = r.db.Exec(ctx, `UPDATE tessa_whatsapp_connections SET expires_at=NOW()-INTERVAL '1 second' WHERE client_id=$1`, clientID)
			case "revoked":
				err = r.Disconnect(ctx, clientID)
			case "security":
				_, err = r.db.Exec(ctx, `UPDATE clients SET security_revision=security_revision+1 WHERE id=$1`, clientID)
			case "notice":
				r.answerNotice = "new-notice"
			case "archived":
				_, err = r.db.Exec(ctx, `UPDATE tessa_threads SET status='archived',archived_at=NOW() WHERE client_id=$1`, clientID)
			case "namespace":
				_, err = r.db.Exec(ctx, `UPDATE tessa_whatsapp_connections SET phone_number_id='19990002' WHERE client_id=$1`, clientID)
			case "disabled":
				r.enabled = false
			case "retry":
				sender.err = &TransportError{Cause: errors.New("not dispatched"), Ambiguous: false}
			case "unknown":
				sender.err = &TransportError{Cause: errors.New("response lost"), Ambiguous: true}
			case "early_callback":
				sender.before = func(correlation string) {
					at := time.Now()
					receipt := WebhookReceipt{DedupeKey: receiptDedupeKey("b4-status", uuid.NewString()), BusinessID: "100", PhoneNumberID: r.phoneID, EventKind: "status", MessageID: "wamid.answer." + correlation, MessageStatus: "delivered", CorrelationID: correlation, ProviderTimestamp: &at, ProcessingStatus: "pending"}
					if err := store.StoreWebhookReceipts(ctx, []WebhookReceipt{receipt}); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						r.db.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, receipt.DedupeKey)
					})
					if err := w.processStatuses(ctx); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.processOne(ctx); err != nil {
				t.Fatal(err)
			}
			want, status := 0, "cancelled"
			switch scenario {
			case "send":
				want, status = 1, "accepted"
			case "window":
				status = "expired"
			case "retry":
				want, status = 1, "retry"
			case "unknown":
				want, status = 1, "unknown"
			case "early_callback":
				want, status = 1, "delivered"
			}
			var actual string
			if err = r.db.QueryRow(ctx, `SELECT status FROM tessa_whatsapp_outbox WHERE id=$1`, id).Scan(&actual); err != nil || actual != status || sender.calls != want {
				t.Fatalf("status=%s calls=%d err=%v", actual, sender.calls, err)
			}
			if sender.calls > 0 && (sender.bodies[0] != "Your committed booking answer." || len(sender.buttons) != sender.calls || sender.buttons[0].URL != "https://provider.example.invalid/bookings" || sender.buttons[0].Label != "View bookings") {
				t.Fatal("not the committed answer with server route")
			}
			// Restart cannot resend an accepted/ambiguous/terminal answer. Only a
			// proven pre-dispatch failure gets the existing bounded safe retry.
			if scenario == "window" {
				fresh := tessaInbound("", "", "2348142751683")
				if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{fresh}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					r.db.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, fresh.DedupeKey)
				})
			}
			if _, err = r.db.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET available_at=NOW() WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
			sender.err = nil
			restarted := NewTessaControlWorker(r, sender, nil)
			if _, err = restarted.processOne(ctx); err != nil {
				t.Fatal(err)
			}
			if scenario == "retry" {
				want = 2
				if sender.bodies[0] != sender.bodies[1] || sender.buttons[0] != sender.buttons[1] {
					t.Fatal("retry changed answer")
				}
			}
			if sender.calls != want {
				t.Fatal("unexpected restart dispatch")
			}
		})
	}
}
