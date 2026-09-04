package appdata

import (
	"encoding/json"
	"time"

	"booking/go-server/internal/money"
)

const (
	InboxAIModeManual    = "manual"
	InboxAIModeSemiPilot = "semi_pilot"
	InboxAIModeAutopilot = "autopilot"
)

type InboxAIServiceOption struct {
	ID                   string   `json:"id"`
	Title                string   `json:"title"`
	FulfillmentMode      string   `json:"fulfillment_mode"`
	CurrencyCode         string   `json:"currency_code"`
	BaseAmountMinor      int64    `json:"base_amount_minor"`
	Enabled              bool     `json:"enabled"`
	EligibleModes        []string `json:"eligible_modes"`
	AutopilotBlockReason string   `json:"autopilot_block_reason,omitempty"`
}

type InboxAIPolicy struct {
	DefaultMode              string                 `json:"default_mode"`
	EnabledServiceIDs        []string               `json:"enabled_service_ids"`
	Paused                   bool                   `json:"paused"`
	MaxTurns                 int                    `json:"max_turns"`
	InactivityTimeoutMinutes int                    `json:"inactivity_timeout_minutes"`
	Revision                 int64                  `json:"revision"`
	AutomationAvailable      bool                   `json:"automation_available"`
	Services                 []InboxAIServiceOption `json:"services"`
	UpdatedAt                *time.Time             `json:"updated_at,omitempty"`
}

type UpdateInboxAIPolicyInput struct {
	ExpectedRevision         int64    `json:"expected_revision"`
	DefaultMode              string   `json:"default_mode"`
	EnabledServiceIDs        []string `json:"enabled_service_ids"`
	Paused                   bool     `json:"paused"`
	MaxTurns                 int      `json:"max_turns"`
	InactivityTimeoutMinutes int      `json:"inactivity_timeout_minutes"`
}

type InboxAIConversationControl struct {
	ConversationID string     `json:"conversation_id"`
	ModeOverride   string     `json:"mode_override,omitempty"`
	ConfiguredMode string     `json:"configured_mode"`
	EffectiveMode  string     `json:"effective_mode"`
	State          string     `json:"state"`
	Reason         string     `json:"reason,omitempty"`
	PolicyRevision int64      `json:"policy_revision"`
	Revision       int64      `json:"revision"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"`
}

type UpdateInboxAIConversationControlInput struct {
	ExpectedRevision int64  `json:"expected_revision"`
	ModeOverride     string `json:"mode_override"`
	State            string `json:"state"`
	Reason           string `json:"reason"`
}

type InboxAISession struct {
	ID                           string    `json:"id"`
	ConversationID               string    `json:"conversation_id"`
	Mode                         string    `json:"mode"`
	State                        string    `json:"state"`
	SelectedServiceID            string    `json:"selected_service_id,omitempty"`
	LastProcessedMessageSequence int64     `json:"last_processed_message_sequence"`
	BookingLinkURL               string    `json:"booking_link_url,omitempty"`
	BookingLinkRevision          int64     `json:"booking_link_revision"`
	PolicyRevision               int64     `json:"policy_revision"`
	ControlRevision              int64     `json:"control_revision"`
	TurnCount                    int       `json:"turn_count"`
	MaxTurns                     int       `json:"max_turns"`
	HandoffReason                string    `json:"handoff_reason,omitempty"`
	Revision                     int64     `json:"revision"`
	ExpiresAt                    time.Time `json:"expires_at"`
	UpdatedAt                    time.Time `json:"updated_at"`
}

type InboxAISessionResponse struct {
	Session *InboxAISession `json:"session"`
}

type CustomerInboxAIHandoffInput struct {
	Reason string `json:"reason"`
}

type CustomerInboxAIHandoffResult struct {
	ConversationID string    `json:"conversation_id"`
	Status         string    `json:"status"`
	EffectiveAt    time.Time `json:"effective_at"`
}

type ExecuteSemiPilotReadActionCommand struct {
	ClientID       string
	ConversationID string
	IdempotencyKey string
	ActionName     string
	Query          string
	ServiceID      string
	// ExpectedMessageSequence fences worker-owned actions when a newer customer
	// or provider message arrives. Zero preserves the internal test/admin use.
	ExpectedMessageSequence int64
	TurnJobID               string
}

type InboxAISemiPilotService struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	Description        string `json:"description,omitempty"`
	FulfillmentMode    string `json:"fulfillment_mode"`
	DurationMinutes    int    `json:"duration_minutes"`
	AmountMinor        int64  `json:"amount_minor"`
	CurrencyCode       string `json:"currency_code"`
	LocationLabel      string `json:"location_label,omitempty"`
	CancellationPolicy string `json:"cancellation_policy,omitempty"`
	LatenessPolicy     string `json:"lateness_policy,omitempty"`
}

type InboxAISemiPilotReadActionResult struct {
	ActionID   string          `json:"action_id"`
	ActionName string          `json:"action_name"`
	Replayed   bool            `json:"replayed"`
	State      string          `json:"state"`
	Result     json.RawMessage `json:"result"`
}

type InboxAIBookingRequirements struct {
	MissingCustomerFields     []string `json:"missing_customer_fields"`
	CustomerLocationRequired  bool     `json:"customer_location_required"`
	AgreementRequired         bool     `json:"agreement_required"`
	EmailReminderDefault      bool     `json:"email_reminder_default"`
	WhatsAppReminderDefault   bool     `json:"whatsapp_reminder_default"`
	AgreementTitle            string   `json:"agreement_title,omitempty"`
	AgreementConfirmationMode string   `json:"agreement_confirmation_method,omitempty"`
}

type InboxAIBookingAgreement struct {
	Required           bool   `json:"required"`
	Completed          bool   `json:"completed"`
	Title              string `json:"title,omitempty"`
	ConfirmationMethod string `json:"confirmation_method,omitempty"`
	RenderedHTML       string `json:"rendered_html,omitempty"`
}

type InboxAIBookingProposal struct {
	ID                      string                  `json:"id"`
	Revision                int64                   `json:"revision"`
	Hash                    string                  `json:"hash"`
	ExpiresAt               time.Time               `json:"expires_at"`
	ServiceID               string                  `json:"service_id"`
	ServiceTitle            string                  `json:"service_title"`
	StartsAt                time.Time               `json:"starts_at"`
	EndsAt                  time.Time               `json:"ends_at"`
	Timezone                string                  `json:"timezone"`
	FulfillmentMode         string                  `json:"fulfillment_mode"`
	LocationLabel           string                  `json:"location_label"`
	CurrencyCode            string                  `json:"currency_code"`
	TotalAmountMinor        money.Minor             `json:"total_amount_minor"`
	PriceLabel              string                  `json:"price_label"`
	PaymentRequirement      string                  `json:"payment_requirement"`
	AgreementRequirement    string                  `json:"agreement_requirement"`
	Agreement               InboxAIBookingAgreement `json:"agreement"`
	Confirmed               bool                    `json:"confirmed"`
	ConfirmationID          string                  `json:"confirmation_id,omitempty"`
	ConfirmedAt             *time.Time              `json:"confirmed_at,omitempty"`
	ContactDetailsConfirmed bool                    `json:"contact_details_confirmed,omitempty"`
	EmailReminderConsent    bool                    `json:"email_reminder_consent,omitempty"`
	WhatsAppConsent         bool                    `json:"whatsapp_consent,omitempty"`
	SMSConsent              bool                    `json:"sms_consent,omitempty"`
}

type InboxAIBookingWorkflow struct {
	Available    bool                       `json:"available"`
	Mode         string                     `json:"mode"`
	State        string                     `json:"state"`
	Revision     int64                      `json:"revision"`
	ActionID     string                     `json:"action_id,omitempty"`
	Services     []InboxAIServiceOption     `json:"services"`
	Requirements InboxAIBookingRequirements `json:"requirements"`
	Proposal     *InboxAIBookingProposal    `json:"proposal,omitempty"`
	Reservation  *InboxAIBookingReservation `json:"reservation,omitempty"`
}

type InboxAIBookingReservation struct {
	BookingID     string          `json:"booking_id"`
	BookingStatus string          `json:"booking_status"`
	NextStep      BookingNextStep `json:"next_step"`
	Href          string          `json:"href"`
	ExpiresAt     *time.Time      `json:"expires_at,omitempty"`
}

type StartInboxAIBookingInput struct {
	IdempotencyKey string `json:"idempotency_key"`
}

type OfferInboxAIAvailabilityInput struct {
	IdempotencyKey   string `json:"idempotency_key"`
	ActionID         string `json:"action_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	ServiceID        string `json:"service_id"`
	From             string `json:"from"`
	Days             int    `json:"days"`
}

type PrepareInboxAIBookingProposalInput struct {
	IdempotencyKey        string `json:"idempotency_key"`
	ActionID              string `json:"action_id"`
	ExpectedRevision      int64  `json:"expected_revision"`
	ServiceID             string `json:"service_id"`
	StartsAt              string `json:"starts_at"`
	CustomerLocationToken string `json:"customer_location_token"`
}

type AcceptInboxAIBookingAgreementInput struct {
	IdempotencyKey   string `json:"idempotency_key"`
	ProposalRevision int64  `json:"proposal_revision"`
	ProposalHash     string `json:"proposal_hash"`
	Accepted         bool   `json:"accepted"`
	FullName         string `json:"full_name"`
	SignatureDataURL string `json:"signature_data_url"`
}

type ConfirmInboxAIBookingProposalInput struct {
	IdempotencyKey          string `json:"idempotency_key"`
	ProposalRevision        int64  `json:"proposal_revision"`
	ProposalHash            string `json:"proposal_hash"`
	ContactDetailsConfirmed bool   `json:"contact_details_confirmed"`
	EmailReminderConsent    bool   `json:"email_reminder_consent"`
	WhatsAppConsent         bool   `json:"whatsapp_consent"`
	SMSConsent              bool   `json:"sms_consent"`
}

type InboxAIBookingActionResult struct {
	Workflow     InboxAIBookingWorkflow    `json:"workflow"`
	Message      *InboxMessage             `json:"message,omitempty"`
	Conversation *InboxConversationSummary `json:"conversation,omitempty"`
	Replayed     bool                      `json:"replayed"`
}

type InboxAIBookingConfirmationResult struct {
	Workflow           InboxAIBookingWorkflow    `json:"workflow"`
	Messages           []InboxMessage            `json:"messages"`
	Conversation       *InboxConversationSummary `json:"conversation,omitempty"`
	ConfirmationID     string                    `json:"confirmation_id"`
	ConfirmedAt        time.Time                 `json:"confirmed_at"`
	ReservationCreated bool                      `json:"reservation_created"`
	BookingID          string                    `json:"booking_id"`
	BookingStatus      string                    `json:"booking_status"`
	NextStep           BookingNextStep           `json:"next_step"`
	Replayed           bool                      `json:"replayed"`
}
