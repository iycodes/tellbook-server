package appdata

import (
	"errors"
	"testing"

	"booking/go-server/internal/bookingdomain"
)

func TestResolveBookingLifecycleUsesCanonicalObligationOrder(t *testing.T) {
	tests := []struct {
		name    string
		booking PublicBookingSummaryResponse
		want    BookingNextStep
		state   BookingLifecycleState
	}{
		{
			name: "payment precedes after-payment agreement",
			booking: PublicBookingSummaryResponse{
				Status: "booked", PaymentStatus: "full_payment_pending",
				AgreementStatus: "pending", AgreementTemplateTitle: "Service agreement",
			},
			want:  BookingNextStepPayment,
			state: BookingLifecycleAwaitingPayment,
		},
		{
			name: "agreement follows satisfied payment",
			booking: PublicBookingSummaryResponse{
				Status: "booked", PaymentStatus: "paid_in_full",
				AgreementStatus: "pending", AgreementTemplateTitle: "Service agreement",
			},
			want:  BookingNextStepAgreement,
			state: BookingLifecycleAwaitingAgreement,
		},
		{
			name: "provider confirmation follows customer obligations",
			booking: PublicBookingSummaryResponse{
				Status: "booked", PaymentStatus: "paid_in_full", AgreementStatus: "not_required",
			},
			want:  BookingNextStepProviderConfirmation,
			state: BookingLifecycleAwaitingProviderConfirmation,
		},
		{
			name: "confirmed booking has no remaining creation step",
			booking: PublicBookingSummaryResponse{
				Status: "confirmed", PaymentStatus: "paid_in_full", AgreementStatus: "not_required",
			},
			want:  BookingNextStepComplete,
			state: BookingLifecycleComplete,
		},
		{
			name: "declined unpaid booking is terminal",
			booking: PublicBookingSummaryResponse{
				Status: "declined", PaymentStatus: "full_payment_pending",
				AgreementStatus: "not_required",
			},
			want:  BookingNextStepComplete,
			state: BookingLifecycleTerminal,
		},
		{
			name: "cancelled booking with pending agreement is terminal",
			booking: PublicBookingSummaryResponse{
				Status: "cancelled", PaymentStatus: "paid_in_full",
				AgreementStatus: "pending", AgreementTemplateTitle: "Service agreement",
			},
			want:  BookingNextStepComplete,
			state: BookingLifecycleTerminal,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := ResolveBookingLifecycle(test.booking)
			if lifecycle.NextStep != test.want {
				t.Fatalf("next step = %q, want %q", lifecycle.NextStep, test.want)
			}
			if lifecycle.State != test.state {
				t.Fatalf("state = %q, want %q", lifecycle.State, test.state)
			}
		})
	}
}

func TestBookingPaymentMayBeRequiredIncludesQuoteSurcharges(t *testing.T) {
	tests := []struct {
		name    string
		service publicBookingServiceInfo
		rules   []bookingdomain.ShortNoticeRule
		want    bool
	}{
		{
			name: "free provider-location service",
			service: publicBookingServiceInfo{
				FulfillmentMode: string(bookingdomain.FulfillmentProviderLocation),
			},
			want: false,
		},
		{
			name: "customer-location travel fee",
			service: publicBookingServiceInfo{
				FulfillmentMode: string(bookingdomain.FulfillmentCustomerLocation),
				TravelFeeMinor:  1500,
			},
			want: true,
		},
		{
			name: "fixed short-notice fee",
			service: publicBookingServiceInfo{
				FulfillmentMode: string(bookingdomain.FulfillmentProviderLocation),
			},
			rules: []bookingdomain.ShortNoticeRule{{
				Type: bookingdomain.SurchargeFixedAmount, AmountMinor: 2500,
			}},
			want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := bookingPaymentMayBeRequired(test.service, test.rules); got != test.want {
				t.Fatalf("bookingPaymentMayBeRequired() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestStandaloneSignatureRequirementsUseBeforePaymentTiming(t *testing.T) {
	requirements := bookingAgreementRequirements(publicBookingServiceInfo{
		StandaloneSignatureRequired: true,
	})
	if !requirements.Required || requirements.Timing != "before_payment" ||
		requirements.ConfirmationMethod != "signature" {
		t.Fatalf("standalone signature requirements = %#v", requirements)
	}
}

func TestBookingApplicationRejectsMissingRepository(t *testing.T) {
	service := NewBookingApplicationService(nil)
	if _, err := service.CreateQuote(t.Context(), CreateBookingQuoteCommand{}); err == nil {
		t.Fatal("CreateQuote with no repository returned nil error")
	}
	if _, err := service.CreateReservation(t.Context(), CreateBookingReservationCommand{}); err == nil {
		t.Fatal("CreateReservation with no repository returned nil error")
	}
}

func TestBookingApplicationRequiresReservationAuthorityBeforeDatabaseWork(t *testing.T) {
	service := NewBookingApplicationService(&Repository{})
	if _, err := service.CreateReservation(t.Context(), CreateBookingReservationCommand{}); err == nil {
		t.Fatal("CreateReservation with no authority returned nil error")
	}
}

func TestPublicQuoteAdapterValidatesTypedCommandFieldsBeforeDatabaseWork(t *testing.T) {
	repo := &Repository{}
	tests := []CreatePublicBookingQuoteInput{
		{IdempotencyKey: "not-a-uuid", ServiceID: "00000000-0000-4000-8000-000000000001", StartsAt: "2026-08-27T10:00:00Z"},
		{IdempotencyKey: "00000000-0000-4000-8000-000000000001", ServiceID: "not-a-uuid", StartsAt: "2026-08-27T10:00:00Z"},
		{IdempotencyKey: "00000000-0000-4000-8000-000000000001", ServiceID: "00000000-0000-4000-8000-000000000002", StartsAt: "tomorrow"},
	}
	for _, input := range tests {
		if _, err := repo.CreatePublicBookingQuote(t.Context(), "provider", input); !errors.Is(err, ErrInvalidQuoteRequest) {
			t.Fatalf("invalid public quote input returned %v, want ErrInvalidQuoteRequest", err)
		}
	}
}
