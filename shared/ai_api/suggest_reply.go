package aiapi

type SuggestReplyRequest struct {
	ThreadID              string        `json:"thread_id,omitempty"`
	Tone                  MessageTone   `json:"tone,omitempty"`
	BusinessName          string        `json:"business_name,omitempty"`
	CustomerName          string        `json:"customer_name,omitempty"`
	LatestCustomerMessage string        `json:"latest_customer_message"`
	Goal                  string        `json:"goal,omitempty"`
	Conversation          []MessageTurn `json:"conversation,omitempty"`
	Context               []NamedValue  `json:"context,omitempty"`
}

type SuggestReplyResponse struct {
	Intent           string    `json:"intent,omitempty"`
	Reply            string    `json:"reply"`
	Confidence       float64   `json:"confidence,omitempty"`
	SafeToSend       bool      `json:"safe_to_send"`
	NeedsHumanReview bool      `json:"needs_human_review"`
	EscalationReason string    `json:"escalation_reason,omitempty"`
	Warnings         []Warning `json:"warnings,omitempty"`
}
