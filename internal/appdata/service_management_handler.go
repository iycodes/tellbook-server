package appdata

import (
	"errors"
	"net/http"

	"booking/go-server/internal/auth"

	"github.com/google/uuid"
)

func (h *Handler) listServiceSections(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	items, err := h.repo.ListServiceSections(r.Context(), authedClient.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "service_sections_failed", "Could not load sections.")
		return
	}

	items = h.signServiceSectionItems(r.Context(), items)
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createServiceSection(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	input, err := decodeJSON[CreateServiceSectionInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	item, err := h.repo.CreateServiceSection(r.Context(), authedClient.ID, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "create_service_section_failed", err.Error())
		return
	}

	item.CoverImageURL = h.signedMediaURL(r.Context(), item.CoverImageURL)
	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) updateServiceSection(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	sectionID, err := uuidFromURLParam("sectionID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_section_id", "Section ID is invalid.")
		return
	}

	input, err := decodeJSON[UpdateServiceSectionInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	item, err := h.repo.UpdateServiceSection(r.Context(), authedClient.ID, sectionID, input)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "service_section_not_found", "Section was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "update_service_section_failed", err.Error())
		return
	}

	item.CoverImageURL = h.signedMediaURL(r.Context(), item.CoverImageURL)
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) getServiceSectionDetails(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	sectionID, err := uuidFromURLParam("sectionID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_section_id", "Section ID is invalid.")
		return
	}

	response, err := h.repo.GetServiceSectionDetails(r.Context(), authedClient.ID, sectionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "service_section_not_found", "Section was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "service_section_details_failed", "Could not load section details.")
		return
	}

	response = h.signServiceSectionDetailsResponse(r.Context(), response)
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) deleteServiceSection(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	sectionID, err := uuidFromURLParam("sectionID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_section_id", "Section ID is invalid.")
		return
	}

	input := DeleteServiceSectionInput{
		Mode:            r.URL.Query().Get("mode"),
		TargetSectionID: r.URL.Query().Get("target_section_id"),
	}

	if err := h.repo.DeleteServiceSection(r.Context(), authedClient.ID, sectionID, input); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "service_section_not_found", "Section was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "delete_service_section_failed", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) reorderServiceSections(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	input, err := decodeJSON[ReorderItemsInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	orderedIDs, err := parseOrderedUUIDs(input.OrderedIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_ordered_ids", err.Error())
		return
	}

	if err := h.repo.ReorderServiceSections(r.Context(), authedClient.ID, orderedIDs); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "service_section_not_found", "A section was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "reorder_service_sections_failed", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listManagedServices(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	items, err := h.repo.ListManagedServices(r.Context(), authedClient.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "managed_services_failed", "Could not load services.")
		return
	}

	items = h.signManagedServiceItems(r.Context(), items)
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createManagedService(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if !h.requireClientMarket(w, r, authedClient.ID) {
		return
	}

	input, err := decodeJSON[CreateManagedServiceInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	item, err := h.repo.CreateManagedService(r.Context(), authedClient.ID, input)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "service_section_not_found", "Selected section was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "create_service_failed", err.Error())
		return
	}

	item.ImageURL = h.signedMediaURL(r.Context(), item.ImageURL)
	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) updateManagedService(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if !h.requireClientMarket(w, r, authedClient.ID) {
		return
	}

	serviceID, err := uuidFromURLParam("serviceID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_service_id", "Service ID is invalid.")
		return
	}

	input, err := decodeJSON[CreateManagedServiceInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	item, err := h.repo.UpdateManagedService(r.Context(), authedClient.ID, serviceID, input)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "service_not_found", "Service or section was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "update_service_failed", err.Error())
		return
	}

	item.ImageURL = h.signedMediaURL(r.Context(), item.ImageURL)
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) updateManagedServiceVisibility(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if !h.requireClientMarket(w, r, authedClient.ID) {
		return
	}

	serviceID, err := uuidFromURLParam("serviceID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_service_id", "Service ID is invalid.")
		return
	}

	input, err := decodeJSON[UpdateManagedServiceVisibilityInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	item, err := h.repo.UpdateManagedServiceVisibility(r.Context(), authedClient.ID, serviceID, input.IsHidden)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "service_not_found", "Service was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "update_service_visibility_failed", err.Error())
		return
	}

	item.ImageURL = h.signedMediaURL(r.Context(), item.ImageURL)
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) getManagedServiceDetails(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	serviceID, err := uuidFromURLParam("serviceID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_service_id", "Service ID is invalid.")
		return
	}

	item, err := h.repo.GetManagedServiceDetails(r.Context(), authedClient.ID, serviceID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "service_not_found", "Service was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "service_details_failed", "Could not load service details.")
		return
	}

	item.ImageURL = h.signedMediaURL(r.Context(), item.ImageURL)
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) deleteManagedService(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	serviceID, err := uuidFromURLParam("serviceID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_service_id", "Service ID is invalid.")
		return
	}

	if err := h.repo.DeleteManagedService(r.Context(), authedClient.ID, serviceID); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "service_not_found", "Service was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "delete_service_failed", "Could not delete service.")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) reorderSectionServices(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	sectionID, err := uuidFromURLParam("sectionID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_section_id", "Section ID is invalid.")
		return
	}

	input, err := decodeJSON[ReorderItemsInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	orderedIDs, err := parseOrderedUUIDs(input.OrderedIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_ordered_ids", err.Error())
		return
	}

	if err := h.repo.ReorderSectionServices(r.Context(), authedClient.ID, sectionID, orderedIDs); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "service_not_found", "A service was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "reorder_services_failed", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) reorderUncategorizedServices(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	input, err := decodeJSON[ReorderItemsInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	orderedIDs, err := parseOrderedUUIDs(input.OrderedIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_ordered_ids", err.Error())
		return
	}

	if err := h.repo.ReorderUncategorizedServices(r.Context(), authedClient.ID, orderedIDs); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "service_not_found", "A service was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "reorder_uncategorized_services_failed", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func parseOrderedUUIDs(values []string) ([]uuid.UUID, error) {
	if len(values) == 0 {
		return nil, errors.New("ordered_ids is required")
	}
	ordered := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		parsed, err := uuid.Parse(value)
		if err != nil {
			return nil, errors.New("ordered_ids contains an invalid UUID")
		}
		ordered = append(ordered, parsed)
	}
	return ordered, nil
}

func (h *Handler) duplicateManagedService(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if !h.requireClientMarket(w, r, authedClient.ID) {
		return
	}

	serviceID, err := uuidFromURLParam("serviceID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_service_id", "Service ID is invalid.")
		return
	}

	item, err := h.repo.DuplicateManagedService(r.Context(), authedClient.ID, serviceID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "service_not_found", "Service was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "duplicate_service_failed", "Could not duplicate service.")
		return
	}

	item.ImageURL = h.signedMediaURL(r.Context(), item.ImageURL)
	writeJSON(w, http.StatusCreated, item)
}
