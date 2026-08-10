package appdata

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	aiapi "booking/go-server/shared/ai_api"

	"booking/go-server/internal/auth"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

func (h *Handler) generateServiceDescription(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFromContext(r.Context()); !ok {
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
	startedAt := time.Now()
	response, err := h.ai.GenerateServiceDescription(r.Context(), input)
	if err != nil {
		logAIGenerationFailure(r, "service_description", startedAt, err)
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

	startedAt := time.Now()
	response, err := h.ai.GeneratePrepAftercareInstructions(r.Context(), input)
	if err != nil {
		logAIGenerationFailure(r, "prep_aftercare", startedAt, err)
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

	startedAt := time.Now()
	response, err := h.ai.GenerateSectionDescription(r.Context(), input)
	if err != nil {
		logAIGenerationFailure(r, "section_description", startedAt, err)
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
	input.ClientID = authedClient.ID.String()
	input.ServiceTitles = nil
	input.ServiceCategories = nil
	services, err := h.repo.ListManagedServices(r.Context(), authedClient.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "services_failed", "Could not load services for profile generation.")
		return
	}
	for _, service := range services {
		if service.IsHidden || strings.TrimSpace(service.Name) == "" {
			continue
		}
		input.ServiceTitles = appendUniqueAIContextValue(input.ServiceTitles, service.Name)
		input.ServiceCategories = appendUniqueAIContextValue(input.ServiceCategories, service.SectionName)
	}

	startedAt := time.Now()
	response, err := h.ai.GeneratePublicPageContent(r.Context(), input)
	if err != nil {
		logAIGenerationFailure(r, "public_page_content", startedAt, err)
		writeError(w, http.StatusBadGateway, "ai_generation_failed", "Could not generate profile content.")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func appendUniqueAIContextValue(values []string, candidate string) []string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return values
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), candidate) {
			return values
		}
	}
	return append(values, candidate)
}

func logAIGenerationFailure(r *http.Request, task string, startedAt time.Time, err error) {
	slog.Error(
		"ai generation failed",
		"task", task,
		"duration", time.Since(startedAt),
		"request_id", chimiddleware.GetReqID(r.Context()),
		"error", err,
	)
}
