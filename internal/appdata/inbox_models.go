package appdata

import (
	"encoding/json"
	"time"

	aiapi "booking/go-server/shared/ai_api"
)

type InboxCounterparty struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Handle    string `json:"handle,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

type InboxBookingContext struct {
	ID           string    `json:"id"`
	ServiceTitle string    `json:"service_title"`
	Status       string    `json:"status"`
	StartsAt     time.Time `json:"starts_at"`
	Timezone     string    `json:"timezone"`
}

type InboxConversationSummary struct {
	ID                       string                `json:"id"`
	Channel                  string                `json:"channel"`
	Counterparty             InboxCounterparty     `json:"counterparty"`
	Preview                  string                `json:"preview"`
	UnreadCount              int                   `json:"unread_count"`
	ProviderHandoffRequested bool                  `json:"provider_handoff_requested"`
	LastMessageAt            *time.Time            `json:"last_message_at"`
	LastMessageSenderType    string                `json:"last_message_sender_type,omitempty"`
	BookingContexts          []InboxBookingContext `json:"booking_contexts"`
	Archived                 bool                  `json:"archived"`
	Disabled                 bool                  `json:"disabled"`
	CreatedAt                time.Time             `json:"created_at"`
	UpdatedAt                time.Time             `json:"updated_at"`
}

type InboxMessage struct {
	ID              string                    `json:"id"`
	ConversationID  string                    `json:"conversation_id"`
	SenderType      string                    `json:"sender_type"`
	SenderID        string                    `json:"sender_id,omitempty"`
	ClientMessageID string                    `json:"client_message_id,omitempty"`
	BookingID       string                    `json:"booking_id,omitempty"`
	Content         string                    `json:"content"`
	MessageType     string                    `json:"message_type"`
	Presentation    *InboxMessagePresentation `json:"presentation,omitempty"`
	SentAt          time.Time                 `json:"sent_at"`
}

type InboxMessagePresentation struct {
	Kind    string          `json:"kind"`
	Version int             `json:"version"`
	Data    json.RawMessage `json:"data"`
}

type InboxConversationDetailResponse struct {
	Conversation      InboxConversationSummary `json:"conversation"`
	Messages          []InboxMessage           `json:"messages"`
	NextBeforeCursor  string                   `json:"next_before_cursor,omitempty"`
	SyncCursor        string                   `json:"sync_cursor"`
	AIDraftsAvailable bool                     `json:"ai_drafts_available"`
}

type InboxAIDraftResponse struct {
	RunID              string          `json:"run_id"`
	Status             string          `json:"status"`
	Draft              string          `json:"draft"`
	NeedsProviderInput bool            `json:"needs_provider_input"`
	Warnings           []aiapi.Warning `json:"warnings"`
	SourceMessageID    string          `json:"source_message_id,omitempty"`
	Replayed           bool            `json:"replayed"`
	CreatedAt          time.Time       `json:"created_at"`
	ErrorCode          string          `json:"error_code,omitempty"`
}

type GenerateInboxAIDraftInput struct {
	ClientRequestID string `json:"client_request_id"`
}

type DiscardInboxAIDraftInput struct {
	Reason string `json:"reason"`
}

type InboxConversationListResponse struct {
	Items       []InboxConversationSummary `json:"items"`
	UnreadTotal int                        `json:"unread_total"`
	NextCursor  string                     `json:"next_cursor,omitempty"`
	SyncCursor  string                     `json:"sync_cursor"`
}

type InboxMessagePageResponse struct {
	Items            []InboxMessage `json:"items"`
	NextBeforeCursor string         `json:"next_before_cursor,omitempty"`
	SyncCursor       string         `json:"sync_cursor"`
}

type MarkInboxReadInput struct {
	MessageID string `json:"message_id"`
}

type InboxParticipantState struct {
	ConversationID          string     `json:"conversation_id"`
	ParticipantType         string     `json:"participant_type"`
	ParticipantID           string     `json:"participant_id"`
	LastReadCursor          string     `json:"last_read_cursor"`
	LastReadAt              *time.Time `json:"last_read_at"`
	Archived                bool       `json:"archived"`
	ConversationUnreadCount int        `json:"conversation_unread_count"`
	UnreadTotal             int        `json:"unread_total"`
	SyncCursor              string     `json:"sync_cursor"`
}

type InboxUnreadCountResponse struct {
	UnreadTotal int    `json:"unread_total"`
	SyncCursor  string `json:"sync_cursor"`
}

type InboxRealtimeEvent struct {
	Cursor          string                   `json:"cursor"`
	Type            string                   `json:"type"`
	ConversationID  string                   `json:"conversation_id"`
	Conversation    InboxConversationSummary `json:"conversation"`
	Message         *InboxMessage            `json:"message,omitempty"`
	ParticipantType string                   `json:"participant_type,omitempty"`
	UnreadTotal     int                      `json:"unread_total"`
	CreatedAt       time.Time                `json:"created_at"`
}

type InboxRealtimeReset struct {
	Cursor string `json:"cursor"`
	Reason string `json:"reason"`
}

type InboxEventDrain struct {
	Events       []InboxRealtimeEvent
	Cursor       string
	LatestCursor string
	HasMore      bool
	Reset        bool
}

type SendInboxMessageInput struct {
	ClientMessageID string `json:"client_message_id"`
	Content         string `json:"content"`
	BookingID       string `json:"booking_id,omitempty"`
	AIRunID         string `json:"ai_run_id,omitempty"`
}

type SendInboxMessageResponse struct {
	Message      InboxMessage             `json:"message"`
	Conversation InboxConversationSummary `json:"conversation"`
	Replayed     bool                     `json:"replayed"`
}

type GetOrCreateInboxConversationResult struct {
	Detail  InboxConversationDetailResponse
	Created bool
}
