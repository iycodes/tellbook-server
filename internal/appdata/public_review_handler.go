package appdata

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (h *Handler) listPublicReviews(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	var serviceID *uuid.UUID
	if raw := strings.TrimSpace(query.Get("service_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_service_id", "service_id is invalid.")
			return
		}
		serviceID = &parsed
	}
	rating := 0
	if raw := strings.TrimSpace(query.Get("rating")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 5 {
			writeError(w, http.StatusBadRequest, "invalid_rating", "rating must be between 1 and 5.")
			return
		}
		rating = parsed
	}
	var hasPhotos *bool
	if raw := strings.TrimSpace(query.Get("has_photos")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_has_photos", "has_photos must be true or false.")
			return
		}
		hasPhotos = &parsed
	}
	sortMode := strings.TrimSpace(query.Get("sort"))
	if sortMode == "" {
		sortMode = "newest"
	}
	if sortMode != "newest" && sortMode != "highest" {
		writeError(w, http.StatusBadRequest, "invalid_sort", "sort must be newest or highest.")
		return
	}
	limit, ok := marketplaceIntQuery(w, query.Get("limit"), "limit", 10, 1, 50)
	if !ok {
		return
	}
	cursor := strings.TrimSpace(query.Get("cursor"))
	slug := chi.URLParam(r, "slug")
	revision := int64(0)
	if anonymousPublicCacheRequest(r) {
		var err error
		revision, err = h.repo.PublicProviderResourceRevisionBySlug(r.Context(), slug)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				writeError(w, http.StatusNotFound, "public_profile_not_found", "Client profile was not found.")
				return
			}
			writeError(w, http.StatusInternalServerError, "public_reviews_failed", "Could not load reviews.")
			return
		}
		if writeKnownRevisionNotModified(w, r, "public-reviews", publicProviderRevisionPart(revision), publicProviderCacheControl) {
			return
		}
	}

	response, err := h.repo.ListPublicReviewsBySlug(r.Context(), slug, PublicReviewsInput{
		ServiceID: serviceID,
		Rating:    rating,
		HasPhotos: hasPhotos,
		Sort:      sortMode,
		Cursor:    cursor,
		Limit:     limit,
	})
	if errors.Is(err, ErrInvalidKeysetCursor) {
		writeError(w, http.StatusBadRequest, "invalid_review_cursor", "Review cursor does not match these filters.")
		return
	}
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "public_profile_not_found", "Client profile was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "public_reviews_failed", "Could not load reviews.")
		return
	}
	if err := writeKnownRevisionPublicJSON(w, r, response, "public-reviews", publicProviderRevisionPart(revision), publicProviderCacheControl); err != nil {
		writeError(w, http.StatusInternalServerError, "public_reviews_failed", "Could not load reviews.")
	}
}
