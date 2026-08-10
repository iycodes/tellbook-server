package appdata

import (
	"time"

	aiapi "booking/go-server/shared/ai_api"
)

type InboxConversationItem struct {
	ID             string     `json:"id"`
	CustomerID     string     `json:"customer_id,omitempty"`
	CustomerName   string     `json:"customer_name,omitempty"`
	LeadName       string     `json:"lead_name,omitempty"`
	LeadContact    string     `json:"lead_contact,omitempty"`
	ExternalLeadID string     `json:"external_lead_id,omitempty"`
	Source         string     `json:"source"`
	Status         string     `json:"status"`
	Subject        string     `json:"subject"`
	Preview        string     `json:"preview"`
	AvatarURL      string     `json:"avatar_url,omitempty"`
	AutopilotMode  string     `json:"autopilot_mode"`
	AgentState     string     `json:"agent_state"`
	HumanTakeover  bool       `json:"human_takeover"`
	HumanComposing bool       `json:"human_composing"`
	LastMessageAt  time.Time  `json:"last_message_at"`
	LastAIReplyAt  *time.Time `json:"last_ai_reply_at,omitempty"`
}

type InboxMessageItem struct {
	ID          string    `json:"id"`
	SenderRole  string    `json:"sender_role"`
	Content     string    `json:"content"`
	MessageType string    `json:"message_type"`
	ActionType  string    `json:"action_type,omitempty"`
	SentAt      time.Time `json:"sent_at"`
}

type InboxConversationDetailsResponse struct {
	Conversation InboxConversationItem `json:"conversation"`
	Messages     []InboxMessageItem    `json:"messages"`
}

type UpdateInboxConversationControlsInput struct {
	AutopilotMode string `json:"autopilot_mode"`
	HumanTakeover bool   `json:"human_takeover"`
}

type SendInboxMessageInput struct {
	Content string `json:"content"`
}

type UpdateInboxComposeStateInput struct {
	IsComposing bool `json:"is_composing"`
}

type SuggestInboxReplyResponse struct {
	Reply            string          `json:"reply"`
	SafeToSend       bool            `json:"safe_to_send"`
	NeedsHumanReview bool            `json:"needs_human_review"`
	Warnings         []aiapi.Warning `json:"warnings,omitempty"`
}

type InboxConversationStreamEvent struct {
	Type    string                            `json:"type"`
	Details *InboxConversationDetailsResponse `json:"details,omitempty"`
	Error   *APIError                         `json:"error,omitempty"`
}

type InboxConversationListStreamEvent struct {
	Type  string                  `json:"type"`
	Items []InboxConversationItem `json:"items,omitempty"`
	Error *APIError               `json:"error,omitempty"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
