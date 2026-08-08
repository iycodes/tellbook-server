package appdata

import (
	"net/http"
	"strings"

	aiapi "booking/shared/ai_api"

	"booking/go-server/internal/auth"
)

func (h *Handler) generateServiceDescription(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if h.ai == nil || !h.ai.Available() {
		writeError(w, http.StatusBadGateway, "ai_unavailable", "AI generation is not available right now.")
		return
	}

	input, err := decodeJSON[aiapi.GenerateServiceDescriptionRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(input.ServiceTitle) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Service title is required.")
		return
	}
	profile, err := h.repo.GetClientProfile(r.Context(), authedClient.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "profile_failed", "Could not load business currency.")
		return
	}
	input.CurrencyCode = profile.CurrencyCode

	response, err := h.ai.GenerateServiceDescription(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ai_generation_failed", "Could not generate service description.")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) generatePrepAftercareInstructions(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if h.ai == nil || !h.ai.Available() {
		writeError(w, http.StatusBadGateway, "ai_unavailable", "AI generation is not available right now.")
		return
	}

	input, err := decodeJSON[aiapi.GeneratePrepAftercareInstructionsRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(input.ServiceTitle) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Service title is required.")
		return
	}
	if !input.IncludePrep && !input.IncludeAftercare {
		writeError(w, http.StatusBadRequest, "invalid_request", "Choose at least one instruction type.")
		return
	}

	response, err := h.ai.GeneratePrepAftercareInstructions(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ai_generation_failed", "Could not generate prep and aftercare instructions.")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) generateSectionDescription(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if h.ai == nil || !h.ai.Available() {
		writeError(w, http.StatusBadGateway, "ai_unavailable", "AI generation is not available right now.")
		return
	}

	input, err := decodeJSON[aiapi.GenerateSectionDescriptionRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(input.SectionTitle) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Section title is required.")
		return
	}

	response, err := h.ai.GenerateSectionDescription(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ai_generation_failed", "Could not generate section description.")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) generatePublicPageContent(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if h.ai == nil || !h.ai.Available() {
		writeError(w, http.StatusBadGateway, "ai_unavailable", "AI generation is not available right now.")
		return
	}

	input, err := decodeJSON[aiapi.GeneratePublicPageContentRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(input.BusinessName) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Business name is required.")
		return
	}
	if input.ClientID == "" {
		input.ClientID = authedClient.ID.String()
	}

	response, err := h.ai.GeneratePublicPageContent(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ai_generation_failed", "Could not generate profile content.")
		return
	}

	writeJSON(w, http.StatusOK, response)
}
