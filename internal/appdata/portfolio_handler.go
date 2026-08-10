package appdata

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"booking/go-server/internal/auth"

	"github.com/google/uuid"
)

func (h *Handler) listPortfolioItems(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	items, err := h.repo.ListPortfolioItems(r.Context(), authedClient.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "portfolio_failed", "Could not load your gallery.")
		return
	}
	for index := range items {
		items[index].ImageURL = h.signedMediaURL(r.Context(), items[index].ImageURL)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createPortfolioItem(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	input, err := decodeJSON[CreatePortfolioItemInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !h.isOwnedPortfolioImage(authedClient.ID, input.ImageURL) {
		writeError(w, http.StatusBadRequest, "invalid_portfolio_image", "Upload the photo before adding it to your gallery.")
		return
	}

	item, err := h.repo.CreatePortfolioItem(r.Context(), authedClient.ID, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "create_portfolio_item_failed", err.Error())
		return
	}
	item.ImageURL = h.signedMediaURL(r.Context(), item.ImageURL)
	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) reorderPortfolioItems(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	input, err := decodeJSON[ReorderPortfolioItemsInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	orderedIDs := make([]uuid.UUID, len(input.OrderedIDs))
	for index, rawID := range input.OrderedIDs {
		orderedIDs[index], err = uuid.Parse(strings.TrimSpace(rawID))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_portfolio_order", "Gallery order contains an invalid photo.")
			return
		}
	}

	if err := h.repo.ReorderPortfolioItems(r.Context(), authedClient.ID, orderedIDs); err != nil {
		if errors.Is(err, ErrInvalidPortfolioOrder) {
			writeError(w, http.StatusConflict, "portfolio_changed", "Your gallery changed. Refresh it before arranging photos again.")
			return
		}
		writeError(w, http.StatusInternalServerError, "reorder_portfolio_failed", "Could not save the gallery order.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deletePortfolioItem(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	itemID, err := uuidFromURLParam("portfolioID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_portfolio_id", "Gallery photo ID is invalid.")
		return
	}
	imageURL, err := h.repo.DeletePortfolioItem(r.Context(), authedClient.ID, itemID)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "portfolio_item_not_found", "Gallery photo was not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete_portfolio_item_failed", "Could not remove the gallery photo.")
		return
	}

	h.deleteOwnedPortfolioImage(r, authedClient.ID, imageURL)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) isOwnedPortfolioImage(clientID uuid.UUID, imageURL string) bool {
	if h.storage == nil {
		return false
	}
	parsed, ok := h.storage.ParseStorageURL(strings.TrimSpace(imageURL))
	return ok && parsed.BucketName == h.storage.PrivateBucketName() && strings.HasPrefix(parsed.ObjectKey, fmt.Sprintf("clients/%s/portfolio/", clientID))
}

func (h *Handler) deleteOwnedPortfolioImage(r *http.Request, clientID uuid.UUID, imageURL string) {
	if !h.isOwnedPortfolioImage(clientID, imageURL) {
		return
	}
	parsed, _ := h.storage.ParseStorageURL(strings.TrimSpace(imageURL))
	if err := h.storage.Delete(r.Context(), parsed.ObjectKey, parsed.BucketName); err != nil {
		slog.Warn("portfolio image cleanup failed", "error", err, "client_id", clientID, "object_key", parsed.ObjectKey)
	}
}
