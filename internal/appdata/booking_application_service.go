package appdata

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"booking/go-server/internal/bookingdomain"
	"booking/go-server/internal/money"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// BookingApplicationService is the single application boundary shared by the
// public HTTP booking flow and trusted internal workflows such as inbox AI.
// HTTP-specific parsing and authentication remain in handlers; booking rules
// remain behind these typed commands.
type BookingApplicationService struct {
	repo *Repository
}

func NewBookingApplicationService(repo *Repository) *BookingApplicationService {
	return &BookingApplicationService{repo: repo}
}

func (r *Repository) BookingApplication() *BookingApplicationService {
	return NewBookingApplicationService(r)
}

type SearchBookingAvailabilityCommand struct {
	ProviderHandle string
	ServiceID      uuid.UUID
	From           *time.Time
	Days           int
}

type SearchBookingAvailabilityDayCommand struct {
	ProviderHandle string
	ServiceID      uuid.UUID
	Date           time.Time
}

type CreateBookingQuoteCommand struct {
	ProviderHandle        string
	IdempotencyKey        uuid.UUID
	ServiceID             uuid.UUID
	StartsAt              time.Time
	Customer              BookingCustomerDetails
	DiscountCode          string
	CustomerLocationToken string
}

type CreateBookingReservationCommand struct {
	ProviderHandle        string
	QuoteToken            string
	Authority             BookingReservationAuthority
	Source                string
	Customer              BookingCustomerDetails
	Agreement             BookingAgreementEvidenceInput
	NotificationConsent   BookingNotificationConsent
	MarketplaceCustomerID *uuid.UUID
	PrebookingAgreementID *uuid.UUID
	AutopilotAuthority    *InboxAutopilotReservationAuthority
}

// InboxAutopilotReservationAuthority is application-owned proof that the
// authenticated customer confirmed the exact current proposal. The booking
// transaction revalidates it before consuming capacity; generated model text
// can never construct or satisfy this authority.
type InboxAutopilotReservationAuthority struct {
	ConversationID   uuid.UUID
	BookingSessionID uuid.UUID
	ConfirmationID   uuid.UUID
	ProposalID       uuid.UUID
	ProposalRevision int64
	ProposalHash     string
}

// BookingReservationAuthority identifies the trusted workflow asking the
// application service to consume a quote. It is intentionally separate from
// Source, which is customer-supplied booking attribution.
type BookingReservationAuthority string

const (
	BookingReservationAuthorityPublic    BookingReservationAuthority = "public"
	BookingReservationAuthorityAutopilot BookingReservationAuthority = "autopilot"
)

var ErrAutopilotPaymentWindowUnavailable = errors.New("autopilot payment window is unavailable before the appointment")
var ErrAutopilotReservationAuthorityRequired = errors.New("autopilot reservation authority is required")

type BookingCustomerDetails struct {
	FullName string
	Email    string
	Phone    string
	Notes    string
}

type BookingAgreementEvidenceInput struct {
	Accepted         bool
	FullName         string
	SignatureDataURL string
}

type BookingNotificationConsent struct {
	EmailReminder bool
	WhatsApp      bool
	SMS           bool
}

type BookingNextStep string

const (
	BookingNextStepPayment              BookingNextStep = "payment"
	BookingNextStepAgreement            BookingNextStep = "agreement"
	BookingNextStepProviderConfirmation BookingNextStep = "provider_confirmation"
	BookingNextStepComplete             BookingNextStep = "complete"
)

type BookingLifecycleState string

const (
	BookingLifecycleAwaitingPayment              BookingLifecycleState = "awaiting_payment"
	BookingLifecycleAwaitingAgreement            BookingLifecycleState = "awaiting_agreement"
	BookingLifecycleAwaitingProviderConfirmation BookingLifecycleState = "awaiting_provider_confirmation"
	BookingLifecycleComplete                     BookingLifecycleState = "complete"
	BookingLifecycleTerminal                     BookingLifecycleState = "terminal"
)

type BookingLifecycle struct {
	BookingStatus      string                `json:"booking_status"`
	PaymentStatus      string                `json:"payment_status"`
	AgreementStatus    string                `json:"agreement_status"`
	PaymentSatisfied   bool                  `json:"payment_satisfied"`
	AgreementSatisfied bool                  `json:"agreement_satisfied"`
	State              BookingLifecycleState `json:"state"`
	NextStep           BookingNextStep       `json:"next_step"`
}

type BookingReservationResult struct {
	Booking   PublicBookingSummaryResponse
	Lifecycle BookingLifecycle
}

type BookingAgreementRequirements struct {
	Required           bool   `json:"required"`
	Timing             string `json:"timing,omitempty"`
	ConfirmationMethod string `json:"confirmation_method,omitempty"`
	Title              string `json:"title,omitempty"`
}

type BookingAutopilotEligibility struct {
	Eligible    bool   `json:"eligible"`
	BlockReason string `json:"block_reason,omitempty"`
}

type BookingRequirements struct {
	ProviderID               uuid.UUID                    `json:"provider_id"`
	ProviderHandle           string                       `json:"provider_handle"`
	ServiceID                uuid.UUID                    `json:"service_id"`
	ServiceTitle             string                       `json:"service_title"`
	FulfillmentMode          string                       `json:"fulfillment_mode"`
	Timezone                 string                       `json:"timezone"`
	CurrencyCode             string                       `json:"currency_code"`
	BaseAmountMinor          money.Minor                  `json:"base_amount_minor"`
	RequiredCustomerFields   []string                     `json:"required_customer_fields"`
	CustomerLocationRequired bool                         `json:"customer_location_required"`
	PaymentRequired          bool                         `json:"payment_required"`
	ProviderConfirmation     bool                         `json:"provider_confirmation_required"`
	Agreement                BookingAgreementRequirements `json:"agreement"`
	Autopilot                BookingAutopilotEligibility  `json:"autopilot"`
}

type BuildCanonicalBookingLinkCommand struct {
	MarketplaceCustomerID uuid.UUID
	ConversationID        uuid.UUID
	ServiceID             *uuid.UUID
}

type CanonicalBookingLink struct {
	ProviderID                uuid.UUID
	ProviderHandle            string
	ServiceID                 *uuid.UUID
	ConversationAttributionID uuid.UUID
	Href                      string
}

func (s *BookingApplicationService) SearchAvailability(
	ctx context.Context,
	command SearchBookingAvailabilityCommand,
) (PublicAvailabilityRangeResponse, error) {
	if s == nil || s.repo == nil {
		return PublicAvailabilityRangeResponse{}, errors.New("booking application service is not configured")
	}
	return s.repo.searchPublicAvailabilityRange(
		ctx,
		strings.TrimSpace(command.ProviderHandle),
		command.ServiceID,
		command.From,
		command.Days,
	)
}

func (s *BookingApplicationService) SearchAvailabilityDay(
	ctx context.Context,
	command SearchBookingAvailabilityDayCommand,
) (PublicAvailabilityResponse, error) {
	if s == nil || s.repo == nil {
		return PublicAvailabilityResponse{}, errors.New("booking application service is not configured")
	}
	return s.repo.searchPublicAvailabilityDay(
		ctx,
		strings.TrimSpace(command.ProviderHandle),
		command.ServiceID,
		command.Date,
	)
}

func (s *BookingApplicationService) CreateQuote(
	ctx context.Context,
	command CreateBookingQuoteCommand,
) (PublicBookingQuoteResponse, error) {
	if s == nil || s.repo == nil {
		return PublicBookingQuoteResponse{}, errors.New("booking application service is not configured")
	}
	return s.repo.createPublicBookingQuote(ctx, strings.TrimSpace(command.ProviderHandle), CreatePublicBookingQuoteInput{
		IdempotencyKey: command.IdempotencyKey.String(),
		ServiceID:      command.ServiceID.String(), StartsAt: command.StartsAt.Format(time.RFC3339),
		CustomerName: command.Customer.FullName, CustomerEmail: command.Customer.Email,
		CustomerPhone: command.Customer.Phone, BookingNotes: command.Customer.Notes,
		DiscountCode: command.DiscountCode, CustomerLocationToken: command.CustomerLocationToken,
	})
}

func (s *BookingApplicationService) CreateReservation(
	ctx context.Context,
	command CreateBookingReservationCommand,
) (BookingReservationResult, error) {
	if s == nil || s.repo == nil {
		return BookingReservationResult{}, errors.New("booking application service is not configured")
	}
	if command.Authority != BookingReservationAuthorityPublic &&
		command.Authority != BookingReservationAuthorityAutopilot {
		return BookingReservationResult{}, errors.New("booking reservation authority is required")
	}
	booking, err := s.repo.createPublicBooking(
		ctx,
		strings.TrimSpace(command.ProviderHandle),
		CreatePublicBookingInput{
			QuoteToken: command.QuoteToken, Source: command.Source,
			FullName: command.Customer.FullName, Email: command.Customer.Email,
			Phone: command.Customer.Phone, Notes: command.Customer.Notes,
			AgreementAccepted:         command.Agreement.Accepted,
			AgreementFullName:         command.Agreement.FullName,
			AgreementSignatureDataURL: command.Agreement.SignatureDataURL,
			EmailReminderConsent:      command.NotificationConsent.EmailReminder,
			WhatsAppConsent:           command.NotificationConsent.WhatsApp,
			SMSConsent:                command.NotificationConsent.SMS,
			MarketplaceCustomerID:     command.MarketplaceCustomerID,
			PrebookingAgreementID:     command.PrebookingAgreementID,
			AutopilotAuthority:        command.AutopilotAuthority,
		},
		command.Authority,
	)
	if err != nil {
		return BookingReservationResult{}, err
	}
	return BookingReservationResult{
		Booking:   booking,
		Lifecycle: ResolveBookingLifecycle(booking),
	}, nil
}

func ResolveBookingLifecycle(booking PublicBookingSummaryResponse) BookingLifecycle {
	paymentSatisfied := initialBookingObligationSatisfied(booking.PaymentStatus)
	agreementSatisfied := publicBookingAgreementSatisfied(booking)
	nextStep := BookingNextStepComplete
	state := BookingLifecycleComplete
	switch {
	case isTerminalBookingStatus(booking.Status):
		state = BookingLifecycleTerminal
	case !paymentSatisfied:
		nextStep = BookingNextStepPayment
		state = BookingLifecycleAwaitingPayment
	case !agreementSatisfied:
		nextStep = BookingNextStepAgreement
		state = BookingLifecycleAwaitingAgreement
	case strings.EqualFold(strings.TrimSpace(booking.Status), "booked") ||
		strings.EqualFold(strings.TrimSpace(booking.Status), "pending"):
		nextStep = BookingNextStepProviderConfirmation
		state = BookingLifecycleAwaitingProviderConfirmation
	}
	return BookingLifecycle{
		BookingStatus: booking.Status, PaymentStatus: booking.PaymentStatus,
		AgreementStatus: booking.AgreementStatus, PaymentSatisfied: paymentSatisfied,
		AgreementSatisfied: agreementSatisfied, State: state, NextStep: nextStep,
	}
}

func isTerminalBookingStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "cancelled", "canceled", "declined", "no_show", "expired":
		return true
	default:
		return false
	}
}

func (s *BookingApplicationService) GetRequirements(
	ctx context.Context,
	providerHandle string,
	serviceID uuid.UUID,
) (BookingRequirements, error) {
	if s == nil || s.repo == nil {
		return BookingRequirements{}, errors.New("booking application service is not configured")
	}
	providerHandle = strings.TrimSpace(providerHandle)
	service, err := s.repo.getPublicServiceForBooking(ctx, providerHandle, serviceID)
	if err != nil {
		return BookingRequirements{}, err
	}
	if err := s.repo.EnsureClientMarketConfigured(ctx, service.ClientID); err != nil {
		return BookingRequirements{}, err
	}

	agreement := bookingAgreementRequirements(service)
	shortNoticeRules, err := loadPublicShortNoticeRules(ctx, s.repo.db, service.ID)
	if err != nil {
		return BookingRequirements{}, err
	}
	paymentRequired := bookingPaymentMayBeRequired(service, shortNoticeRules)
	autopilot := BookingAutopilotEligibility{Eligible: true}
	if service.StandaloneSignatureRequired {
		autopilot = BookingAutopilotEligibility{
			Eligible: false, BlockReason: "standalone_signature_not_enabled",
		}
	}

	return BookingRequirements{
		ProviderID: service.ClientID, ProviderHandle: providerHandle,
		ServiceID: service.ID, ServiceTitle: service.Title,
		FulfillmentMode: service.FulfillmentMode, Timezone: service.Timezone,
		CurrencyCode: service.CurrencyCode, BaseAmountMinor: money.Minor(service.PriceAmountMinor),
		RequiredCustomerFields:   []string{"full_name", "email", "phone"},
		CustomerLocationRequired: service.FulfillmentMode == string(bookingdomain.FulfillmentCustomerLocation),
		PaymentRequired:          paymentRequired, ProviderConfirmation: true,
		Agreement: agreement,
		Autopilot: autopilot,
	}, nil
}

func bookingAgreementRequirements(service publicBookingServiceInfo) BookingAgreementRequirements {
	requirements := BookingAgreementRequirements{
		Required: service.AgreementTemplateFamilyID != uuid.Nil || service.StandaloneSignatureRequired,
		Timing:   service.AgreementTiming, ConfirmationMethod: string(service.AgreementConfirmationMethod),
		Title: service.AgreementTemplateTitle,
	}
	if service.StandaloneSignatureRequired {
		requirements.Timing = "before_payment"
		requirements.ConfirmationMethod = "signature"
	}
	return requirements
}

func bookingPaymentMayBeRequired(
	service publicBookingServiceInfo,
	rules []bookingdomain.ShortNoticeRule,
) bool {
	if service.PriceAmountMinor > 0 {
		return true
	}
	if service.FulfillmentMode == string(bookingdomain.FulfillmentCustomerLocation) &&
		service.TravelFeeMinor > 0 {
		return true
	}
	for _, rule := range rules {
		if rule.Type == bookingdomain.SurchargeFixedAmount && rule.AmountMinor > 0 {
			return true
		}
	}
	return false
}

func (s *BookingApplicationService) BuildCanonicalBookingLink(
	ctx context.Context,
	command BuildCanonicalBookingLinkCommand,
) (CanonicalBookingLink, error) {
	if s == nil || s.repo == nil {
		return CanonicalBookingLink{}, errors.New("booking application service is not configured")
	}
	return buildCanonicalBookingLink(ctx, s.repo.db, command)
}

func buildCanonicalBookingLink(
	ctx context.Context,
	queryer inboxQueryer,
	command BuildCanonicalBookingLinkCommand,
) (CanonicalBookingLink, error) {
	if command.MarketplaceCustomerID == uuid.Nil || command.ConversationID == uuid.Nil {
		return CanonicalBookingLink{}, ErrNotFound
	}

	var providerID uuid.UUID
	var providerHandle string
	if err := queryer.QueryRow(ctx, `
		SELECT conversation.client_id, profile.handle_slug
		FROM inbox_conversations conversation
		INNER JOIN client_profiles profile ON profile.client_id=conversation.client_id
		WHERE conversation.id=$1
		  AND conversation.marketplace_customer_id=$2
		  AND conversation.disabled_at IS NULL
		  AND profile.marketplace_enabled
		  AND profile.market_configured_at IS NOT NULL
	`, command.ConversationID, command.MarketplaceCustomerID).Scan(
		&providerID, &providerHandle,
	); errors.Is(err, pgx.ErrNoRows) {
		return CanonicalBookingLink{}, ErrNotFound
	} else if err != nil {
		return CanonicalBookingLink{}, fmt.Errorf("authorize canonical booking link: %w", err)
	}

	result := CanonicalBookingLink{
		ProviderID: providerID, ProviderHandle: providerHandle,
		ConversationAttributionID: command.ConversationID,
		Href:                      canonicalMarketplaceBookingHref(providerHandle, nil),
	}
	if command.ServiceID == nil {
		return result, nil
	}
	service, err := getPublicServiceForBooking(ctx, queryer, providerHandle, *command.ServiceID)
	if err != nil {
		return CanonicalBookingLink{}, err
	}
	if service.ClientID != providerID {
		return CanonicalBookingLink{}, ErrNotFound
	}
	result.ServiceID = &service.ID
	result.Href = canonicalMarketplaceBookingHref(providerHandle, &service.ID)
	return result, nil
}

func canonicalMarketplaceBookingHref(providerHandle string, serviceID *uuid.UUID) string {
	if serviceID == nil {
		return "/providers/" + url.PathEscape(providerHandle)
	}
	values := url.Values{}
	values.Set("provider", providerHandle)
	values.Set("service", serviceID.String())
	return "/booking/checkout?" + values.Encode()
}

func (r *Repository) GetPublicAvailabilityRange(
	ctx context.Context,
	slug string,
	serviceID uuid.UUID,
	from *time.Time,
	days int,
) (PublicAvailabilityRangeResponse, error) {
	return r.BookingApplication().SearchAvailability(ctx, SearchBookingAvailabilityCommand{
		ProviderHandle: slug, ServiceID: serviceID, From: from, Days: days,
	})
}

func (r *Repository) GetPublicAvailability(
	ctx context.Context,
	slug string,
	serviceID uuid.UUID,
	date time.Time,
) (PublicAvailabilityResponse, error) {
	return r.BookingApplication().SearchAvailabilityDay(ctx, SearchBookingAvailabilityDayCommand{
		ProviderHandle: slug, ServiceID: serviceID, Date: date,
	})
}

func (r *Repository) CreatePublicBookingQuote(
	ctx context.Context,
	slug string,
	input CreatePublicBookingQuoteInput,
) (PublicBookingQuoteResponse, error) {
	idempotencyKey, err := uuid.Parse(strings.TrimSpace(input.IdempotencyKey))
	if err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("%w: idempotency_key must be a UUID", ErrInvalidQuoteRequest)
	}
	serviceID, err := uuid.Parse(strings.TrimSpace(input.ServiceID))
	if err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("%w: invalid service_id", ErrInvalidQuoteRequest)
	}
	startsAt, err := time.Parse(time.RFC3339, strings.TrimSpace(input.StartsAt))
	if err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("%w: invalid starts_at", ErrInvalidQuoteRequest)
	}
	return r.BookingApplication().CreateQuote(ctx, CreateBookingQuoteCommand{
		ProviderHandle: slug, IdempotencyKey: idempotencyKey, ServiceID: serviceID, StartsAt: startsAt,
		Customer: BookingCustomerDetails{
			FullName: input.CustomerName, Email: input.CustomerEmail,
			Phone: input.CustomerPhone, Notes: input.BookingNotes,
		},
		DiscountCode: input.DiscountCode, CustomerLocationToken: input.CustomerLocationToken,
	})
}

func (r *Repository) CreatePublicBooking(
	ctx context.Context,
	slug string,
	input CreatePublicBookingInput,
) (PublicBookingSummaryResponse, error) {
	result, err := r.BookingApplication().CreateReservation(ctx, CreateBookingReservationCommand{
		ProviderHandle: slug, QuoteToken: input.QuoteToken,
		Authority: BookingReservationAuthorityPublic, Source: input.Source,
		Customer: BookingCustomerDetails{
			FullName: input.FullName, Email: input.Email, Phone: input.Phone, Notes: input.Notes,
		},
		Agreement: BookingAgreementEvidenceInput{
			Accepted: input.AgreementAccepted, FullName: input.AgreementFullName,
			SignatureDataURL: input.AgreementSignatureDataURL,
		},
		NotificationConsent: BookingNotificationConsent{
			EmailReminder: input.EmailReminderConsent,
			WhatsApp:      input.WhatsAppConsent,
			SMS:           input.SMSConsent,
		},
		MarketplaceCustomerID: input.MarketplaceCustomerID,
	})
	if err != nil {
		return PublicBookingSummaryResponse{}, err
	}
	return result.Booking, nil
}
