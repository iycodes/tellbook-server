package appdata

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestInboxConversationCursorRoundTripsNullableOrderingKey(t *testing.T) {
	createdAt := time.Date(2026, time.August, 25, 12, 0, 0, 123, time.UTC)
	lastMessageAt := createdAt.Add(time.Minute)
	for _, item := range []InboxConversationSummary{
		{ID: uuid.NewString(), CreatedAt: createdAt, LastMessageAt: &lastMessageAt},
		{ID: uuid.NewString(), CreatedAt: createdAt},
	} {
		encoded := encodeInboxConversationCursor(item)
		decoded, err := decodeInboxConversationCursor(encoded)
		if err != nil {
			t.Fatalf("decode cursor: %v", err)
		}
		if decoded.ID.String() != item.ID || !decoded.CreatedAt.Equal(item.CreatedAt) {
			t.Fatalf("decoded cursor = %+v, want item %+v", decoded, item)
		}
		if (decoded.LastMessageAt == nil) != (item.LastMessageAt == nil) {
			t.Fatalf("decoded nullable message time = %v, want %v", decoded.LastMessageAt, item.LastMessageAt)
		}
	}
	if _, err := decodeInboxConversationCursor("not-a-cursor"); !errors.Is(err, ErrInboxInvalidCursor) {
		t.Fatalf("invalid conversation cursor error = %v", err)
	}
	if _, err := decodeInboxSequenceCursor("0"); !errors.Is(err, ErrInboxInvalidCursor) {
		t.Fatalf("zero message cursor error = %v", err)
	}
}

func TestValidateInboxMessagePresentation(t *testing.T) {
	valid := &InboxMessagePresentation{
		Kind: "booking_link", Version: 1, Data: json.RawMessage(`{
			"action_id":"00000000-0000-4000-8000-000000000001",
			"provider_id":"00000000-0000-4000-8000-000000000002",
			"provider_handle":"sample-provider",
			"href":"/providers/sample-provider",
			"label":"View booking page"
		}`),
	}
	if err := validateInboxMessagePresentation(valid); err != nil {
		t.Fatalf("valid presentation: %v", err)
	}
	for _, presentation := range []*InboxMessagePresentation{
		{Kind: "service_choices", Version: 1, Data: json.RawMessage(`{
			"action_id":"00000000-0000-4000-8000-000000000001","session_revision":1,
			"choices":[{"service_id":"00000000-0000-4000-8000-000000000003","title":"Haircut","duration_minutes":45,"price_label":"₦5,000","fulfillment_mode":"provider_location"}]
		}`)},
		{Kind: "availability_choices", Version: 1, Data: json.RawMessage(`{
			"action_id":"00000000-0000-4000-8000-000000000001","session_revision":1,
			"service_id":"00000000-0000-4000-8000-000000000003","timezone":"Africa/Lagos","expires_at":"2026-08-27T12:00:00Z",
			"choices":[{"slot_id":"slot-1","starts_at":"2026-08-27T10:00:00Z","ends_at":"2026-08-27T10:45:00Z","label":"11:00 AM"}]
		}`)},
		{Kind: "booking_proposal", Version: 1, Data: json.RawMessage(`{
			"proposal_id":"00000000-0000-4000-8000-000000000004","proposal_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","revision":1,"expires_at":"2026-08-27T12:00:00Z",
			"service_id":"00000000-0000-4000-8000-000000000003","service_title":"Haircut",
			"starts_at":"2026-08-27T10:00:00Z","ends_at":"2026-08-27T10:45:00Z","timezone":"Africa/Lagos",
			"fulfillment_mode":"provider_location","location_label":"Lagos","price_label":"₦5,000",
			"payment_requirement":"before service","agreement_requirement":"none","action_label":"Confirm reservation"
		}`)},
		{Kind: "reservation_created", Version: 1, Data: json.RawMessage(`{
			"booking_id":"00000000-0000-4000-8000-000000000005","status":"pending_payment","status_label":"Awaiting payment",
			"service_title":"Haircut","starts_at":"2026-08-27T10:00:00Z","ends_at":"2026-08-27T10:45:00Z","timezone":"Africa/Lagos",
			"expires_at":"2026-08-27T09:30:00Z"
		}`)},
		{Kind: "booking_next_step", Version: 1, Data: json.RawMessage(`{
			"booking_id":"00000000-0000-4000-8000-000000000005","next_step":"payment","label":"Pay now","href":"/booking/00000000-0000-4000-8000-000000000005"
		}`)},
		{Kind: "reservation_expired", Version: 1, Data: json.RawMessage(`{
			"booking_id":"00000000-0000-4000-8000-000000000005","reason":"payment_deadline_elapsed",
			"label":"Reservation expired","expired_at":"2026-08-27T09:30:00Z",
			"href":"/booking/00000000-0000-4000-8000-000000000005"
		}`)},
	} {
		if err := validateInboxMessagePresentation(presentation); err != nil {
			t.Fatalf("valid %s presentation: %v", presentation.Kind, err)
		}
	}
	for _, invalid := range []*InboxMessagePresentation{
		nil,
		{Kind: "unknown", Version: 1, Data: json.RawMessage(`{}`)},
		{Kind: "booking_link", Version: 2, Data: json.RawMessage(`{}`)},
		{Kind: "booking_link", Version: 1, Data: json.RawMessage(`[]`)},
		{Kind: "booking_link", Version: 1, Data: json.RawMessage(`{"action_id":"opaque"}`)},
		{Kind: "booking_link", Version: 1, Data: json.RawMessage(`{
			"action_id":"00000000-0000-4000-8000-000000000001",
			"provider_id":"00000000-0000-4000-8000-000000000002",
			"provider_handle":"sample-provider",
			"href":"javascript:alert(1)",
			"label":"Unsafe"
		}`)},
		{Kind: "booking_link", Version: 1, Data: json.RawMessage(`{
			"action_id":"00000000-0000-4000-8000-000000000001",
			"provider_id":"00000000-0000-4000-8000-000000000002",
			"provider_handle":"sample-provider",
			"href":"https://example.com/providers/sample-provider",
			"label":"External"
		}`)},
		{Kind: "service_choices", Version: 1, Data: json.RawMessage(`{
			"action_id":"00000000-0000-4000-8000-000000000001","session_revision":1,
			"choices":[
				{"service_id":"00000000-0000-4000-8000-000000000003","title":"Haircut","duration_minutes":45,"price_label":"₦5,000","fulfillment_mode":"provider_location"},
				{"service_id":"00000000-0000-4000-8000-000000000003","title":"Duplicate","duration_minutes":30,"price_label":"₦4,000","fulfillment_mode":"provider_location"}
			]
		}`)},
		{Kind: "availability_choices", Version: 1, Data: json.RawMessage(`{
			"action_id":"00000000-0000-4000-8000-000000000001","session_revision":1,
			"service_id":"00000000-0000-4000-8000-000000000003","timezone":"Africa/Lagos","expires_at":"2026-08-27T12:00:00Z",
			"choices":[
				{"slot_id":"slot-1","starts_at":"2026-08-27T10:00:00Z","ends_at":"2026-08-27T10:45:00Z","label":"11:00 AM"},
				{"slot_id":"slot-1","starts_at":"2026-08-27T11:00:00Z","ends_at":"2026-08-27T11:45:00Z","label":"12:00 PM"}
			]
		}`)},
		{Kind: "reservation_expired", Version: 1, Data: json.RawMessage(`{
			"booking_id":"00000000-0000-4000-8000-000000000005","reason":"payment_deadline_elapsed",
			"label":"Reservation expired","expired_at":"not-a-time",
			"href":"/booking/00000000-0000-4000-8000-000000000005"
		}`)},
	} {
		if err := validateInboxMessagePresentation(invalid); !errors.Is(err, ErrInboxInvalidPresentation) {
			t.Fatalf("invalid presentation %+v error = %v", invalid, err)
		}
	}
}

func TestNormalizeInboxMessageContentAndFingerprint(t *testing.T) {
	bookingID := uuid.NullUUID{UUID: uuid.MustParse("00000000-0000-4000-8000-000000000001"), Valid: true}
	normalized := normalizeInboxMessageContent("  hello\r\nworld  ")
	if normalized != "hello\nworld" {
		t.Fatalf("normalized content = %q, want %q", normalized, "hello\nworld")
	}
	if inboxMessageFingerprint(normalized, bookingID) != inboxMessageFingerprint("hello\nworld", bookingID) {
		t.Fatal("equivalent normalized sends produced different fingerprints")
	}
	if inboxMessageFingerprint(normalized, bookingID) == inboxMessageFingerprint(normalized, uuid.NullUUID{}) {
		t.Fatal("booking context did not participate in the request fingerprint")
	}
}

func TestParseInboxSearchQueryRejectsUnboundedInput(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/?query="+strings.Repeat("a", 161), nil)
	response := httptest.NewRecorder()
	if _, ok := parseInboxSearchQuery(response, request); ok {
		t.Fatal("oversized inbox query was accepted")
	}
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized inbox query status = %d, want 400", response.Code)
	}
}

func TestInboxMessagePreviewIsWhitespaceNormalizedAndBounded(t *testing.T) {
	preview := inboxMessagePreview(strings.Repeat("a ", 300))
	if len([]rune(preview)) != 240 {
		t.Fatalf("preview rune length = %d, want 240", len([]rune(preview)))
	}
	if strings.Contains(preview, "  ") || strings.Contains(preview, "\n") {
		t.Fatalf("preview retained repeated whitespace: %q", preview)
	}
}
