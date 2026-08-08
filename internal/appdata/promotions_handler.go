package appdata

import (
	"errors"
	"net/http"

	"booking/go-server/internal/auth"
)

func (h *Handler) listPromotions(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	items, err := h.repo.ListPromotions(r.Context(), authedClient.ID, r.URL.Query().Get("type"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "promotions_failed", "Could not load discounts.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createPromotion(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if !h.requireClientMarket(w, r, authedClient.ID) {
		return
	}

	input, err := decodeJSON[CreatePromotionInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	item, err := h.repo.CreatePromotion(r.Context(), authedClient.ID, input)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "promotion_target_not_found", "A selected service or section was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "promotion_create_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) getPromotionDetails(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	promotionID, err := uuidFromURLParam("promotionID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_promotion_id", "Promotion ID is invalid.")
		return
	}

	item, err := h.repo.GetPromotionDetails(r.Context(), authedClient.ID, promotionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "promotion_not_found", "Discount was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "promotion_failed", "Could not load discount details.")
		return
	}

	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) updatePromotion(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if !h.requireClientMarket(w, r, authedClient.ID) {
		return
	}

	promotionID, err := uuidFromURLParam("promotionID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_promotion_id", "Promotion ID is invalid.")
		return
	}

	input, err := decodeJSON[CreatePromotionInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	item, err := h.repo.UpdatePromotion(r.Context(), authedClient.ID, promotionID, input)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "promotion_not_found", "Discount was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "promotion_update_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) updatePromotionStatus(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if !h.requireClientMarket(w, r, authedClient.ID) {
		return
	}

	promotionID, err := uuidFromURLParam("promotionID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_promotion_id", "Promotion ID is invalid.")
		return
	}

	input, err := decodeJSON[UpdatePromotionStatusInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if err := h.repo.UpdatePromotionStatus(r.Context(), authedClient.ID, promotionID, input.IsActive); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "promotion_not_found", "Discount was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "promotion_status_failed", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deletePromotion(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	promotionID, err := uuidFromURLParam("promotionID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_promotion_id", "Promotion ID is invalid.")
		return
	}

	if err := h.repo.DeletePromotion(r.Context(), authedClient.ID, promotionID); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "promotion_not_found", "Discount was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "promotion_delete_failed", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listPromotionRedemptions(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	promotionID, err := uuidFromURLParam("promotionID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_promotion_id", "Promotion ID is invalid.")
		return
	}

	items, err := h.repo.ListPromotionRedemptions(r.Context(), authedClient.ID, promotionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "promotion_redemptions_failed", "Could not load discount usage.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
