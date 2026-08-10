package appdata

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"booking/go-server/internal/auth"
	"booking/go-server/internal/money"
	"booking/go-server/internal/payments"
	"booking/go-server/internal/payments/capabilities"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (h *Handler) getPayoutSetup(w http.ResponseWriter, r *http.Request) {
	clientID, profile, ok := h.payoutProfile(w, r)
	if !ok {
		return
	}
	destinations, err := h.destinations.List(r.Context(), clientID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "payout_destinations_failed", "Could not load payout destinations.")
		return
	}
	rails := h.destinations.AvailableRails(profile.CountryCode, profile.CurrencyCode)
	response := PayoutSetupResponse{
		Available: len(rails) > 0, CountryCode: profile.CountryCode, CurrencyCode: profile.CurrencyCode,
		Rails: rails, Institutions: []PayoutInstitutionItem{}, Destinations: payoutDestinationItems(destinations),
	}
	if len(rails) > 0 {
		response.SelectedRail = rails[0]
		currentDestinationIndex := -1
		if len(destinations) > 0 {
			currentDestinationIndex = 0
		}
		for index := range destinations {
			if destinations[index].IsDefault {
				currentDestinationIndex = index
				break
			}
		}
		if currentDestinationIndex >= 0 {
			destination := destinations[currentDestinationIndex]
			for _, rail := range rails {
				if destination.Rail == rail {
					response.SelectedRail = rail
					break
				}
			}
		}
		options, err := h.destinations.Options(r.Context(), profile.CountryCode, profile.CurrencyCode, response.SelectedRail)
		if err != nil {
			writePayoutProviderError(w, err, "Could not load payout options.")
			return
		}
		response.Input = payoutInputMetadata(options.Input)
		response.Institutions = payoutInstitutionItems(options.Items)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) getPayoutDestinationOptions(w http.ResponseWriter, r *http.Request) {
	_, profile, ok := h.payoutProfile(w, r)
	if !ok {
		return
	}
	rail := strings.TrimSpace(r.URL.Query().Get("rail"))
	if rail == "" {
		writeError(w, http.StatusBadRequest, "invalid_payout_rail", "Payout rail is required.")
		return
	}
	options, err := h.destinations.Options(r.Context(), profile.CountryCode, profile.CurrencyCode, rail)
	if err != nil {
		writePayoutProviderError(w, err, "Could not load payout options.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"country_code": profile.CountryCode, "currency_code": profile.CurrencyCode, "rail": rail,
		"input": payoutInputMetadata(options.Input), "institutions": payoutInstitutionItems(options.Items),
	})
}

func (h *Handler) listPayoutDestinations(w http.ResponseWriter, r *http.Request) {
	clientID, _, ok := h.payoutProfile(w, r)
	if !ok {
		return
	}
	items, err := h.destinations.List(r.Context(), clientID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "payout_destinations_failed", "Could not load payout destinations.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": payoutDestinationItems(items)})
}

func (h *Handler) resolvePayoutDestination(w http.ResponseWriter, r *http.Request) {
	_, profile, ok := h.payoutProfile(w, r)
	if !ok {
		return
	}
	input, err := decodeJSON[ResolvePayoutDestinationInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	resolved, _, err := h.destinations.Resolve(r.Context(), payments.DestinationSelection{
		CountryCode: profile.CountryCode, CurrencyCode: profile.CurrencyCode, Rail: input.Rail,
		Institution: input.InstitutionCode, Identifier: input.Identifier,
	})
	if err != nil {
		writePayoutProviderError(w, err, "Could not verify those payout details.")
		return
	}
	writeJSON(w, http.StatusOK, ResolvedPayoutDestinationResponse{
		CountryCode: resolved.CountryCode, CurrencyCode: resolved.CurrencyCode, Rail: resolved.Rail,
		InstitutionCode: resolved.InstitutionCode, InstitutionName: resolved.InstitutionName,
		AccountName: resolved.AccountName,
	})
}

func (h *Handler) savePayoutDestination(w http.ResponseWriter, r *http.Request) {
	clientID, profile, ok := h.payoutProfile(w, r)
	if !ok {
		return
	}
	input, err := decodeJSON[SavePayoutDestinationInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	destination, err := h.destinations.Save(r.Context(), payments.DestinationSelection{
		ClientID: clientID, CountryCode: profile.CountryCode, CurrencyCode: profile.CurrencyCode,
		Rail: input.Rail, Institution: input.InstitutionCode, Identifier: input.Identifier,
		ConfirmedName: input.ConfirmedAccountName, MakeDefault: input.MakeDefault,
	})
	if err != nil {
		writePayoutProviderError(w, err, "Could not save this payout destination.")
		return
	}
	writeJSON(w, http.StatusCreated, payoutDestinationItem(destination))
}

func (h *Handler) revokePayoutDestination(w http.ResponseWriter, r *http.Request) {
	clientID, _, ok := h.payoutProfile(w, r)
	if !ok {
		return
	}
	destinationID, err := uuid.Parse(chi.URLParam(r, "destinationID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_payout_destination", "Payout destination is invalid.")
		return
	}
	if err := h.destinations.Revoke(r.Context(), clientID, destinationID); err != nil {
		if errors.Is(err, payments.ErrLedgerRecordNotFound) {
			writeError(w, http.StatusNotFound, "payout_destination_not_found", "Payout destination was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "payout_destination_failed", "Could not remove payout destination.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getPayoutOverview(w http.ResponseWriter, r *http.Request) {
	clientID, profile, ok := h.payoutProfile(w, r)
	if !ok {
		return
	}
	if h.payoutService == nil {
		writeError(w, http.StatusServiceUnavailable, "payouts_unavailable", "Payout processing is not configured.")
		return
	}
	overview, err := h.payoutService.Overview(r.Context(), clientID, profile.CurrencyCode)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "payouts_failed", "Could not load payout activity.")
		return
	}
	response := PayoutOverviewResponse{
		CurrencyCode: overview.CurrencyCode, AvailableAmountMinor: overview.AvailableAmountMinor,
		PendingSettlementAmountMinor: overview.PendingSettlementAmountMinor,
		PayoutInProgressAmountMinor:  overview.PayoutInProgressAmountMinor,
		PaidOutAmountMinor:           overview.PaidOutAmountMinor,
		EligibleAllocations:          make([]EligiblePayoutAllocationItem, 0, len(overview.EligibleAllocations)),
		RecentPayouts:                make([]PayoutItem, 0, len(overview.RecentPayouts)),
	}
	for _, allocation := range overview.EligibleAllocations {
		response.EligibleAllocations = append(response.EligibleAllocations, EligiblePayoutAllocationItem{
			ID: allocation.ID.String(), AmountMinor: allocation.AmountMinor, AvailableAt: allocation.AvailableAt,
		})
	}
	for _, payout := range overview.RecentPayouts {
		response.RecentPayouts = append(response.RecentPayouts, payoutItem(payout))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) createPayout(w http.ResponseWriter, r *http.Request) {
	clientID, _, ok := h.payoutProfile(w, r)
	if !ok {
		return
	}
	if h.payoutService == nil {
		writeError(w, http.StatusServiceUnavailable, "payouts_unavailable", "Payout processing is not configured.")
		return
	}
	input, err := decodeJSON[CreatePayoutInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	allocationID, allocationErr := uuid.Parse(input.PaymentAllocationID)
	destinationID, destinationErr := uuid.Parse(input.PayoutDestinationID)
	if allocationErr != nil || destinationErr != nil {
		writeError(w, http.StatusBadRequest, "invalid_payout", "Payout allocation or destination is invalid.")
		return
	}
	payout, err := h.payoutService.Initiate(r.Context(), payments.InitiatePayoutInput{
		ClientID: clientID, PaymentAllocationID: allocationID, PayoutDestinationID: destinationID,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		var initializationError *payments.PayoutInitializationError
		var activeError *payments.ActivePayoutError
		switch {
		case errors.As(err, &initializationError) && initializationError.Ambiguous:
			writeJSON(w, http.StatusAccepted, payoutItem(initializationError.Payout))
		case errors.As(err, &activeError):
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": map[string]any{"code": "active_payout_exists", "message": "This allocation already has an active payout.", "payout_id": activeError.Payout.ID.String()},
			})
		case errors.Is(err, payments.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "That idempotency key was already used for different payout details.")
		case errors.Is(err, payments.ErrLedgerRecordNotFound):
			writeError(w, http.StatusNotFound, "payout_source_not_found", "The payout allocation or destination was not found.")
		case errors.Is(err, capabilities.ErrCapabilityNotReady), errors.Is(err, capabilities.ErrUnsupportedCapability):
			writeError(w, http.StatusServiceUnavailable, "payouts_unavailable", "This payout route is not currently available.")
		default:
			writeError(w, http.StatusUnprocessableEntity, "payout_failed", "Could not initiate this payout.")
		}
		return
	}
	writeJSON(w, http.StatusAccepted, payoutItem(payout))
}

func (h *Handler) getPayout(w http.ResponseWriter, r *http.Request) {
	clientID, _, ok := h.payoutProfile(w, r)
	if !ok {
		return
	}
	payoutID, err := uuid.Parse(chi.URLParam(r, "payoutID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_payout", "Payout is invalid.")
		return
	}
	payout, err := h.payoutService.Get(r.Context(), clientID, payoutID)
	if errors.Is(err, payments.ErrLedgerRecordNotFound) {
		writeError(w, http.StatusNotFound, "payout_not_found", "Payout was not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "payout_failed", "Could not load this payout.")
		return
	}
	writeJSON(w, http.StatusOK, payoutItem(payout))
}

func (h *Handler) payoutProfile(w http.ResponseWriter, r *http.Request) (uuid.UUID, ClientProfileResponse, bool) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return uuid.Nil, ClientProfileResponse{}, false
	}
	if h.destinations == nil {
		writeError(w, http.StatusServiceUnavailable, "payouts_unavailable", "Payout setup is not configured.")
		return uuid.Nil, ClientProfileResponse{}, false
	}
	profile, err := h.repo.GetClientProfile(r.Context(), authedClient.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "profile_failed", "Could not load client profile.")
		return uuid.Nil, ClientProfileResponse{}, false
	}
	if !profile.MarketConfigured {
		writeError(w, http.StatusConflict, "market_not_configured", "Choose your country and currency before setting up payouts.")
		return uuid.Nil, ClientProfileResponse{}, false
	}
	return authedClient.ID, profile, true
}

func writePayoutProviderError(w http.ResponseWriter, err error, message string) {
	if errors.Is(err, capabilities.ErrUnsupportedCapability) {
		writeError(w, http.StatusUnprocessableEntity, "payout_option_unsupported", "That payout option is not supported for this market.")
		return
	}
	if errors.Is(err, capabilities.ErrCapabilityNotReady) || errors.Is(err, capabilities.ErrAmbiguousCapability) {
		writeError(w, http.StatusServiceUnavailable, "payouts_unavailable", "Payout setup is temporarily unavailable.")
		return
	}
	writeError(w, http.StatusUnprocessableEntity, "payout_destination_unresolved", message)
}

func payoutInputMetadata(input capabilities.InputMetadata) PayoutInputMetadata {
	return PayoutInputMetadata{
		Label: input.Label, MinimumLength: input.MinimumLength, MaximumLength: input.MaximumLength,
		AllowedCharacters: input.AllowedCharacters, ResolutionEnabled: input.ResolutionEnabled,
	}
}

func payoutInstitutionItems(items []payments.DestinationOption) []PayoutInstitutionItem {
	result := make([]PayoutInstitutionItem, 0, len(items))
	for _, item := range items {
		result = append(result, PayoutInstitutionItem{Code: item.Code, Name: item.Name})
	}
	return result
}

func payoutDestinationItems(items []payments.PayoutDestination) []PayoutDestinationItem {
	result := make([]PayoutDestinationItem, 0, len(items))
	for _, item := range items {
		result = append(result, payoutDestinationItem(item))
	}
	return result
}

func payoutDestinationItem(item payments.PayoutDestination) PayoutDestinationItem {
	return PayoutDestinationItem{
		ID: item.ID.String(), CountryCode: item.CountryCode, CurrencyCode: item.CurrencyCode,
		Rail: item.Rail, InstitutionCode: item.InstitutionCode, InstitutionName: item.InstitutionName,
		MaskedIdentifier: item.MaskedIdentifier, ResolvedAccountName: item.ResolvedAccountName,
		VerifiedAt: item.VerifiedAt, IsDefault: item.IsDefault, Status: item.Status,
	}
}

func payoutItem(item payments.FinancialPayout) PayoutItem {
	var destination struct {
		InstitutionName  string `json:"institution_name"`
		MaskedIdentifier string `json:"masked_identifier"`
		AccountName      string `json:"account_name"`
	}
	_ = json.Unmarshal(item.DestinationSnapshot, &destination)
	return PayoutItem{
		ID: item.ID.String(), AmountMinor: money.Minor(item.AmountMinor), FeeMinor: money.Minor(item.FeeMinor),
		CurrencyCode: item.CurrencyCode, Status: string(item.Status),
		InstitutionName: destination.InstitutionName, MaskedIdentifier: destination.MaskedIdentifier,
		AccountName: destination.AccountName, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}
