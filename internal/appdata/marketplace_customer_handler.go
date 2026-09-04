package appdata

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"booking/go-server/internal/marketplaceauth"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func marketplaceCustomerID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	customer, ok := marketplaceauth.CustomerFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return uuid.Nil, false
	}
	return customer.ID, true
}

func marketplaceProviderID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "providerID")))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_provider_id", "Provider ID is invalid.")
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) listMarketplaceSavedProviders(w http.ResponseWriter, r *http.Request) {
	customerID, ok := marketplaceCustomerID(w, r)
	if !ok {
		return
	}
	limit, ok := marketplaceIntQuery(w, r.URL.Query().Get("limit"), "limit", 24, 1, 50)
	if !ok {
		return
	}
	response, err := h.repo.ListMarketplaceSavedProviders(
		r.Context(), customerID, limit, strings.TrimSpace(r.URL.Query().Get("cursor")),
	)
	if err != nil {
		if errors.Is(err, ErrMarketplaceCustomerCursor) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "Saved provider cursor is invalid.")
			return
		}
		writeError(w, http.StatusInternalServerError, "saved_providers_failed", "Could not load saved providers.")
		return
	}
	for index := range response.Items {
		response.Items[index].PublicBookingURL = h.publicBaseURL + "/p/" + url.PathEscape(response.Items[index].HandleSlug)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) saveMarketplaceProvider(w http.ResponseWriter, r *http.Request) {
	customerID, ok := marketplaceCustomerID(w, r)
	if !ok {
		return
	}
	providerID, ok := marketplaceProviderID(w, r)
	if !ok {
		return
	}
	if err := h.repo.SaveMarketplaceProvider(r.Context(), customerID, providerID); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "provider_not_found", "This marketplace provider is unavailable.")
			return
		}
		writeError(w, http.StatusInternalServerError, "save_provider_failed", "Could not save this provider.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getMarketplaceProviderSaved(w http.ResponseWriter, r *http.Request) {
	customerID, ok := marketplaceCustomerID(w, r)
	if !ok {
		return
	}
	providerID, ok := marketplaceProviderID(w, r)
	if !ok {
		return
	}
	saved, err := h.repo.IsMarketplaceProviderSaved(r.Context(), customerID, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "saved_provider_status_failed", "Could not load saved status.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"saved": saved})
}

func (h *Handler) removeMarketplaceSavedProvider(w http.ResponseWriter, r *http.Request) {
	customerID, ok := marketplaceCustomerID(w, r)
	if !ok {
		return
	}
	providerID, ok := marketplaceProviderID(w, r)
	if !ok {
		return
	}
	if err := h.repo.RemoveMarketplaceSavedProvider(r.Context(), customerID, providerID); err != nil {
		writeError(w, http.StatusInternalServerError, "remove_saved_provider_failed", "Could not remove this provider.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listMarketplaceNotifications(w http.ResponseWriter, r *http.Request) {
	customerID, ok := marketplaceCustomerID(w, r)
	if !ok {
		return
	}
	limit, ok := marketplaceIntQuery(w, r.URL.Query().Get("limit"), "limit", 30, 1, 50)
	if !ok {
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind != "" && kind != "booking" && kind != "message" && kind != "offer" && kind != "system" {
		writeError(w, http.StatusBadRequest, "invalid_notification_kind", "Notification kind is invalid.")
		return
	}
	unreadOnly := r.URL.Query().Get("unread_only") == "true"
	response, err := h.repo.ListMarketplaceNotifications(
		r.Context(), customerID, kind, unreadOnly, limit, strings.TrimSpace(r.URL.Query().Get("cursor")),
	)
	if err != nil {
		if errors.Is(err, ErrMarketplaceCustomerCursor) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "Notification cursor is invalid.")
			return
		}
		writeError(w, http.StatusInternalServerError, "notifications_failed", "Could not load notifications.")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) markMarketplaceNotificationRead(w http.ResponseWriter, r *http.Request) {
	customerID, ok := marketplaceCustomerID(w, r)
	if !ok {
		return
	}
	notificationID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "notificationID")))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_notification_id", "Notification ID is invalid.")
		return
	}
	if err := h.repo.MarkMarketplaceNotificationRead(r.Context(), customerID, notificationID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "notification_not_found", "Notification was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "notification_update_failed", "Could not update notification.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) markAllMarketplaceNotificationsRead(w http.ResponseWriter, r *http.Request) {
	customerID, ok := marketplaceCustomerID(w, r)
	if !ok {
		return
	}
	if err := h.repo.MarkAllMarketplaceNotificationsRead(r.Context(), customerID); err != nil {
		writeError(w, http.StatusInternalServerError, "notification_update_failed", "Could not update notifications.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
