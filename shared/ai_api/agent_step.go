package aiapi

type AgentConversationMode string

const (
	AgentConversationModeManual    AgentConversationMode = "manual"
	AgentConversationModeSemiPilot AgentConversationMode = "semi_pilot"
	AgentConversationModeAutoPilot AgentConversationMode = "auto_pilot"
)

type AgentAction string

const (
	AgentActionReplyOnly       AgentAction = "reply_only"
	AgentActionAskFollowUp     AgentAction = "ask_follow_up"
	AgentActionSendBookingLink AgentAction = "send_booking_link"
	AgentActionBookingReady    AgentAction = "booking_ready"
	AgentActionHandoffToHuman  AgentAction = "handoff_to_human"
)

type ConversationAgentStepRequest struct {
	ThreadID              string                `json:"thread_id,omitempty"`
	Mode                  AgentConversationMode `json:"mode"`
	BusinessName          string                `json:"business_name,omitempty"`
	BusinessCategory      string                `json:"business_category,omitempty"`
	BookingURL            string                `json:"booking_url,omitempty"`
	CustomerName          string                `json:"customer_name,omitempty"`
	LeadContact           string                `json:"lead_contact,omitempty"`
	LatestCustomerMessage string                `json:"latest_customer_message"`
	Goal                  string                `json:"goal,omitempty"`
	Conversation          []MessageTurn         `json:"conversation,omitempty"`
	Context               []NamedValue          `json:"context,omitempty"`
}

type ConversationAgentStepResponse struct {
	Action                AgentAction `json:"action"`
	Reply                 string      `json:"reply,omitempty"`
	Confidence            float64     `json:"confidence,omitempty"`
	SafeToSend            bool        `json:"safe_to_send"`
	NeedsHumanReview      bool        `json:"needs_human_review"`
	EscalationReason      string      `json:"escalation_reason,omitempty"`
	NextState             string      `json:"next_state,omitempty"`
	BookingIntent         string      `json:"booking_intent,omitempty"`
	MissingFields         []string    `json:"missing_fields,omitempty"`
	ShouldSendBookingLink bool        `json:"should_send_booking_link"`
	Warnings              []Warning   `json:"warnings,omitempty"`
}
