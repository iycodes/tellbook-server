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
	h.catalogREST(w, r, "create_service_section")
}

func (h *Handler) updateServiceSection(w http.ResponseWriter, r *http.Request) {
	h.catalogREST(w, r, "replace_service_section")
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

	response, err := h.readSectionForREST(r.Context(), authedClient.ID, sectionID)
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
	h.catalogREST(w, r, "delete_service_section")
}

func (h *Handler) reorderServiceSections(w http.ResponseWriter, r *http.Request) {
	h.catalogREST(w, r, "reorder_service_sections")
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
	h.catalogREST(w, r, "create_service")
}

func (h *Handler) updateManagedService(w http.ResponseWriter, r *http.Request) {
	h.catalogREST(w, r, "replace_service")
}

func (h *Handler) updateManagedServiceVisibility(w http.ResponseWriter, r *http.Request) {
	h.catalogREST(w, r, "set_service_visibility")
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

	item, err := h.readManagedServiceForREST(r.Context(), authedClient.ID, serviceID)
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
	h.catalogREST(w, r, "delete_service")
}

func (h *Handler) reorderSectionServices(w http.ResponseWriter, r *http.Request) {
	h.catalogREST(w, r, "reorder_section_services")
}

func (h *Handler) reorderUncategorizedServices(w http.ResponseWriter, r *http.Request) {
	h.catalogREST(w, r, "reorder_uncategorized_services")
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
	h.catalogREST(w, r, "duplicate_service")
}
