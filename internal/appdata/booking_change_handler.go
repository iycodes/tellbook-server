package appdata

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"booking/go-server/internal/auth"
	"booking/go-server/internal/marketplaceauth"

	"github.com/google/uuid"
)

type bookingQuoteRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	StartsAt       string `json:"starts_at"`
}

type bookingCommandRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	QuoteToken     string `json:"quote_token"`
	Reason         string `json:"reason"`
}

func (h *Handler) getMarketplaceRescheduleAvailability(w http.ResponseWriter, r *http.Request) {
	customer, _ := marketplaceauth.CustomerFromContext(r.Context())
	bookingID, ok := marketplaceBookingIDParam(w, r)
	if !ok {
		return
	}
	days := 14
	if rawDays := strings.TrimSpace(r.URL.Query().Get("days")); rawDays != "" {
		parsed, err := strconv.Atoi(rawDays)
		if err != nil || parsed < 1 || parsed > 31 {
			writeError(w, http.StatusBadRequest, "invalid_days", "Days must be between 1 and 31.")
			return
		}
		days = parsed
	}
	var from *time.Time
	if rawFrom := strings.TrimSpace(r.URL.Query().Get("from")); rawFrom != "" {
		parsed, err := time.Parse("2006-01-02", rawFrom)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_from", "From must use YYYY-MM-DD.")
			return
		}
		from = &parsed
	}
	response, err := h.repo.GetMarketplaceRescheduleAvailability(r.Context(), customer.ID, bookingID, from, days)
	if err != nil {
		writeBookingChangeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) createMarketplaceCancellationQuote(w http.ResponseWriter, r *http.Request) {
	h.createMarketplaceBookingChangeQuote(w, r, "cancellation")
}

func (h *Handler) createMarketplaceRescheduleQuote(w http.ResponseWriter, r *http.Request) {
	h.createMarketplaceBookingChangeQuote(w, r, "reschedule")
}

func (h *Handler) createMarketplaceBookingChangeQuote(w http.ResponseWriter, r *http.Request, kind string) {
	customer, _ := marketplaceauth.CustomerFromContext(r.Context())
	bookingID, ok := marketplaceBookingIDParam(w, r)
	if !ok {
		return
	}
	input, err := decodeJSON[bookingQuoteRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_booking_change_quote", "Enter a valid booking change request.")
		return
	}
	idempotencyKey, err := uuid.Parse(strings.TrimSpace(input.IdempotencyKey))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "A valid idempotency key is required.")
		return
	}
	var quote MarketplaceBookingChangeQuote
	if kind == "cancellation" {
		quote, err = h.repo.CreateMarketplaceCancellationQuote(r.Context(), customer.ID, bookingID, idempotencyKey)
	} else {
		startsAt, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(input.StartsAt))
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid_reschedule_time", "Choose a valid available appointment time.")
			return
		}
		quote, err = h.repo.CreateMarketplaceRescheduleQuote(r.Context(), customer.ID, bookingID, idempotencyKey, startsAt.UTC())
	}
	if err != nil {
		writeBookingChangeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, quote)
}

func (h *Handler) cancelMarketplaceBooking(w http.ResponseWriter, r *http.Request) {
	h.applyMarketplaceBookingChange(w, r, "cancel")
}

func (h *Handler) rescheduleMarketplaceBooking(w http.ResponseWriter, r *http.Request) {
	h.applyMarketplaceBookingChange(w, r, "reschedule")
}

func (h *Handler) applyMarketplaceBookingChange(w http.ResponseWriter, r *http.Request, command string) {
	customer, _ := marketplaceauth.CustomerFromContext(r.Context())
	bookingID, ok := marketplaceBookingIDParam(w, r)
	if !ok {
		return
	}
	input, err := decodeJSON[bookingCommandRequest](r)
	if err != nil || strings.TrimSpace(input.QuoteToken) == "" {
		writeError(w, http.StatusBadRequest, "invalid_booking_change", "Review a current quote before confirming this change.")
		return
	}
	if len(strings.TrimSpace(input.Reason)) > 500 {
		writeError(w, http.StatusBadRequest, "invalid_booking_change_reason", "Reason must be 500 characters or fewer.")
		return
	}
	idempotencyKey, err := uuid.Parse(strings.TrimSpace(input.IdempotencyKey))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "A valid idempotency key is required.")
		return
	}
	response, err := h.repo.ApplyMarketplaceBookingChange(
		r.Context(), customer.ID, bookingID, input.QuoteToken, command, input.Reason, idempotencyKey,
	)
	if err != nil {
		writeBookingChangeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) confirmProviderBooking(w http.ResponseWriter, r *http.Request) {
	h.applyProviderBookingCommand(w, r, "confirm")
}

func (h *Handler) declineProviderBooking(w http.ResponseWriter, r *http.Request) {
	h.applyProviderBookingCommand(w, r, "decline")
}

func (h *Handler) completeProviderBooking(w http.ResponseWriter, r *http.Request) {
	h.applyProviderBookingCommand(w, r, "complete")
}

func (h *Handler) markProviderBookingNoShow(w http.ResponseWriter, r *http.Request) {
	h.applyProviderBookingCommand(w, r, "mark_no_show")
}

func (h *Handler) applyProviderBookingCommand(w http.ResponseWriter, r *http.Request, command string) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	bookingID, err := uuidFromURLParam("bookingID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_booking_id", "Booking ID is invalid.")
		return
	}
	input, err := decodeJSON[bookingCommandRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_booking_command", "Enter a valid booking action.")
		return
	}
	if len(strings.TrimSpace(input.Reason)) > 500 {
		writeError(w, http.StatusBadRequest, "invalid_booking_change_reason", "Reason must be 500 characters or fewer.")
		return
	}
	idempotencyKey, err := uuid.Parse(strings.TrimSpace(input.IdempotencyKey))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "A valid idempotency key is required.")
		return
	}
	response, err := h.repo.ApplyProviderBookingCommand(r.Context(), client.ID, bookingID, command, input.Reason, idempotencyKey)
	if err != nil {
		writeBookingChangeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func writeBookingChangeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "booking_not_found", "Booking was not found.")
	case errors.Is(err, ErrSlotUnavailable):
		writeError(w, http.StatusConflict, "slot_unavailable", "That appointment time is no longer available.")
	case errors.Is(err, ErrBookingChangeExpired):
		writeError(w, http.StatusConflict, "booking_change_quote_expired", "This quote expired. Review the change again.")
	case errors.Is(err, ErrBookingChangeStale):
		writeError(w, http.StatusConflict, "booking_change_quote_stale", "The booking changed. Review a new quote before continuing.")
	case errors.Is(err, ErrBookingChangeConsumed):
		writeError(w, http.StatusConflict, "booking_change_quote_used", "This quote has already been used.")
	case errors.Is(err, ErrBookingIdempotency):
		writeError(w, http.StatusConflict, "idempotency_conflict", "This request key was already used for a different booking action.")
	case errors.Is(err, ErrAutomatedReschedule):
		writeError(w, http.StatusConflict, "manual_reschedule_policy", "This booking has a custom policy and must be rescheduled with the provider.")
	case errors.Is(err, ErrBookingActionNotAllowed):
		writeError(w, http.StatusConflict, "booking_action_not_allowed", "That action is not available for the booking's current state.")
	default:
		writeError(w, http.StatusInternalServerError, "booking_change_failed", "Could not update the booking.")
	}
}
