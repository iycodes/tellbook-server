package appdata

import (
	"errors"
	"net/http"

	"booking/go-server/internal/marketplaceauth"

	"github.com/google/uuid"
)

func (h *Handler) getMarketplaceInboxAIBookingWorkflow(w http.ResponseWriter, r *http.Request) {
	customer, conversationID, ok := marketplaceInboxAIBookingActor(w, r)
	if !ok {
		return
	}
	workflow, err := h.repo.GetMarketplaceInboxAIBookingWorkflow(
		r.Context(), customer.ID, conversationID,
	)
	if !writeInboxAIBookingError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, workflow)
}

func (h *Handler) startMarketplaceInboxAIBooking(w http.ResponseWriter, r *http.Request) {
	customer, conversationID, ok := marketplaceInboxAIBookingActor(w, r)
	if !ok {
		return
	}
	input, err := decodeJSON[StartInboxAIBookingInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Booking action is invalid.")
		return
	}
	if !h.enforceInboxCommandLimit(r.Context(), w, inboxCommandAI, "marketplace_customer", customer.ID, conversationID) {
		return
	}
	result, err := h.repo.StartMarketplaceInboxAIBooking(
		r.Context(), customer.ID, conversationID, input,
	)
	if !writeInboxAIBookingError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) offerMarketplaceInboxAIAvailability(w http.ResponseWriter, r *http.Request) {
	customer, conversationID, ok := marketplaceInboxAIBookingActor(w, r)
	if !ok {
		return
	}
	input, err := decodeJSON[OfferInboxAIAvailabilityInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Availability action is invalid.")
		return
	}
	if !h.enforceInboxCommandLimit(r.Context(), w, inboxCommandAI, "marketplace_customer", customer.ID, conversationID) {
		return
	}
	result, err := h.repo.OfferMarketplaceInboxAIAvailability(
		r.Context(), customer.ID, conversationID, input,
	)
	if !writeInboxAIBookingError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) prepareMarketplaceInboxAIBookingProposal(w http.ResponseWriter, r *http.Request) {
	customer, conversationID, ok := marketplaceInboxAIBookingActor(w, r)
	if !ok {
		return
	}
	input, err := decodeJSON[PrepareInboxAIBookingProposalInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Proposal action is invalid.")
		return
	}
	if !h.enforceInboxCommandLimit(r.Context(), w, inboxCommandAI, "marketplace_customer", customer.ID, conversationID) {
		return
	}
	result, err := h.repo.PrepareMarketplaceInboxAIBookingProposal(
		r.Context(), customer.ID, conversationID, input,
	)
	if !writeInboxAIBookingError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) acceptMarketplaceInboxAIBookingAgreement(w http.ResponseWriter, r *http.Request) {
	customer, conversationID, ok := marketplaceInboxAIBookingActor(w, r)
	if !ok {
		return
	}
	proposalID, ok := inboxAIProposalIDParam(w, r)
	if !ok {
		return
	}
	input, err := decodeJSON[AcceptInboxAIBookingAgreementInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Agreement evidence is invalid.")
		return
	}
	if !h.enforceInboxCommandLimit(r.Context(), w, inboxCommandAI, "marketplace_customer", customer.ID, conversationID) {
		return
	}
	result, err := h.repo.AcceptMarketplaceInboxAIBookingAgreement(
		r.Context(), customer.ID, conversationID, proposalID, input,
	)
	if !writeInboxAIBookingError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) confirmMarketplaceInboxAIBookingProposal(w http.ResponseWriter, r *http.Request) {
	customer, conversationID, ok := marketplaceInboxAIBookingActor(w, r)
	if !ok {
		return
	}
	proposalID, ok := inboxAIProposalIDParam(w, r)
	if !ok {
		return
	}
	input, err := decodeJSON[ConfirmInboxAIBookingProposalInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Proposal confirmation is invalid.")
		return
	}
	if !h.enforceInboxCommandLimit(r.Context(), w, inboxCommandAI, "marketplace_customer", customer.ID, conversationID) {
		return
	}
	result, err := h.repo.ConfirmMarketplaceInboxAIBookingProposal(
		r.Context(), customer.ID, conversationID, proposalID, input,
	)
	if !writeInboxAIBookingError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func marketplaceInboxAIBookingActor(
	w http.ResponseWriter,
	r *http.Request,
) (marketplaceauth.Customer, uuid.UUID, bool) {
	customer, ok := marketplaceauth.CustomerFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
		return marketplaceauth.Customer{}, uuid.Nil, false
	}
	conversationID, err := uuidFromURLParam("conversationID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Conversation ID is invalid.")
		return marketplaceauth.Customer{}, uuid.Nil, false
	}
	return customer, conversationID, true
}

func inboxAIProposalIDParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	proposalID, err := uuidFromURLParam("proposalID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Proposal ID is invalid.")
		return uuid.Nil, false
	}
	return proposalID, true
}

func writeInboxAIBookingError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Conversation or proposal was not found.")
	case errors.Is(err, ErrBusinessRestricted):
		writeError(w, http.StatusConflict, "business_restricted", "This business is not accepting new bookings.")
	case errors.Is(err, ErrInboxConversationDisabled):
		writeError(w, http.StatusConflict, "conversation_disabled", "This conversation can no longer accept booking actions.")
	case errors.Is(err, ErrInboxAIProposalStale), errors.Is(err, ErrQuoteExpired),
		errors.Is(err, ErrSlotUnavailable), errors.Is(err, ErrAutopilotPaymentWindowUnavailable):
		writeError(w, http.StatusConflict, "stale_proposal", "This option changed or expired. Refresh the booking choices.")
	case errors.Is(err, ErrInboxAIBookingDetailsRequired):
		writeError(w, http.StatusUnprocessableEntity, "customer_details_required", "Add your name, email, and phone or WhatsApp number in your account before continuing.")
	case errors.Is(err, ErrLocationRequired), errors.Is(err, ErrLocationNotAllowed), errors.Is(err, ErrOutsideServiceArea):
		writeError(w, http.StatusUnprocessableEntity, "customer_location_required", "Choose a saved service address before continuing.")
	case errors.Is(err, ErrInboxAIAgreementRequired):
		writeError(w, http.StatusUnprocessableEntity, "agreement_required", "Review and complete the agreement before confirming.")
	case errors.Is(err, ErrInboxIdempotencyConflict), errors.Is(err, ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict", "This action key was already used for different details.")
	case errors.Is(err, ErrInboxAIControlBlocked), errors.Is(err, ErrInboxAIAutomationUnavailable):
		writeError(w, http.StatusConflict, "automation_blocked", "In-chat booking is not active for this conversation.")
	case errors.Is(err, ErrInboxAIServiceUnavailable):
		writeError(w, http.StatusUnprocessableEntity, "service_unavailable", "This service is not available for in-chat reservation yet.")
	case errors.Is(err, ErrInvalidQuoteRequest), errors.Is(err, ErrInvalidContact):
		writeError(w, http.StatusUnprocessableEntity, "invalid_booking_details", "Check the selected service, time, and account details.")
	case errors.Is(err, ErrInboxAIInvalidState):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not update the in-chat booking.")
	}
	return false
}
