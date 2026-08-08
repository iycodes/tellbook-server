package aiapi

type GenerateMessageRequest struct {
	MessageType     string         `json:"message_type"`
	Goal            string         `json:"goal,omitempty"`
	Channel         MessageChannel `json:"channel,omitempty"`
	Tone            MessageTone    `json:"tone,omitempty"`
	BusinessName    string         `json:"business_name,omitempty"`
	CustomerName    string         `json:"customer_name,omitempty"`
	ServiceName     string         `json:"service_name,omitempty"`
	BookingDate     string         `json:"booking_date,omitempty"`
	BookingTime     string         `json:"booking_time,omitempty"`
	Location        string         `json:"location,omitempty"`
	PaymentAmount   string         `json:"payment_amount,omitempty"`
	BookingURL      string         `json:"booking_url,omitempty"`
	DiscountCode    string         `json:"discount_code,omitempty"`
	AdditionalNotes string         `json:"additional_notes,omitempty"`
	Context         []NamedValue   `json:"context,omitempty"`
	MaxCharacters   int            `json:"max_characters,omitempty"`
}

type GenerateMessageResponse struct {
	Subject      string    `json:"subject,omitempty"`
	Message      string    `json:"message"`
	ShortMessage string    `json:"short_message,omitempty"`
	CallToAction string    `json:"call_to_action,omitempty"`
	Warnings     []Warning `json:"warnings,omitempty"`
}
