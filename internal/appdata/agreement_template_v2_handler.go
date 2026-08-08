package appdata

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"booking/go-server/internal/agreements/domain"
	agreementrepo "booking/go-server/internal/agreements/repository"
	"booking/go-server/internal/auth"
	aiapi "booking/shared/ai_api"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type agreementTemplateVersionView struct {
	ID               uuid.UUID                   `json:"id"`
	VersionNumber    int                         `json:"version_number"`
	State            domain.TemplateVersionState `json:"state"`
	Document         *aiapi.DocumentSchema       `json:"document,omitempty"`
	UsedVariableKeys []string                    `json:"used_variable_keys"`
	ReviewWarnings   []aiapi.Warning             `json:"review_warnings"`
	Revision         int64                       `json:"revision"`
	PublishedAt      *time.Time                  `json:"published_at,omitempty"`
	UpdatedAt        time.Time                   `json:"updated_at"`
}

type agreementTemplateFamilyView struct {
	ID                 uuid.UUID                        `json:"id"`
	Title              string                           `json:"title"`
	Description        string                           `json:"description"`
	Category           string                           `json:"category"`
	Tags               []string                         `json:"tags"`
	ConfirmationMethod domain.ConfirmationMethod        `json:"confirmation_method"`
	Status             domain.TemplateFamilyStatus      `json:"status"`
	Draft              *agreementTemplateVersionView    `json:"draft,omitempty"`
	Current            *agreementTemplateVersionView    `json:"current,omitempty"`
	Previous           []agreementTemplateVersionView   `json:"previous"`
	Usage              agreementTemplateFamilyUsageView `json:"usage"`
	UpdatedAt          time.Time                        `json:"updated_at"`
}

type agreementTemplateFamilyListItemView struct {
	ID                 uuid.UUID                   `json:"id"`
	Title              string                      `json:"title"`
	Description        string                      `json:"description"`
	Category           string                      `json:"category"`
	Tags               []string                    `json:"tags"`
	ConfirmationMethod domain.ConfirmationMethod   `json:"confirmation_method"`
	Status             domain.TemplateFamilyStatus `json:"status"`
	HasDraft           bool                        `json:"has_draft"`
	DraftRevision      *int64                      `json:"draft_revision,omitempty"`
	ServiceUsage       int                         `json:"service_usage"`
	AgreementUsage     int                         `json:"agreement_usage"`
	UpdatedAt          time.Time                   `json:"updated_at"`
}

type agreementTemplateFamilyUsageView struct {
	Services   int `json:"services"`
	Agreements int `json:"agreements"`
}

type agreementTemplateLibraryItemView struct {
	ID                 uuid.UUID                 `json:"id"`
	Title              string                    `json:"title"`
	Description        string                    `json:"description"`
	Category           string                    `json:"category"`
	Tags               []string                  `json:"tags"`
	ConfirmationMethod domain.ConfirmationMethod `json:"confirmation_method"`
}

type updateAgreementTemplateDraftRequest struct {
	Revision           int64                     `json:"revision"`
	Title              string                    `json:"title"`
	Description        string                    `json:"description"`
	Category           string                    `json:"category"`
	Tags               []string                  `json:"tags"`
	ConfirmationMethod domain.ConfirmationMethod `json:"confirmation_method"`
	Document           aiapi.DocumentSchema      `json:"document"`
}

type startAgreementGenerationRequest struct {
	Title              string                    `json:"title"`
	Description        string                    `json:"description"`
	Category           string                    `json:"category"`
	Tags               []string                  `json:"tags"`
	ConfirmationMethod domain.ConfirmationMethod `json:"confirmation_method"`
	Fields             *agreementFieldsInput     `json:"fields,omitempty"`
	Upload             *agreementUploadInput     `json:"upload,omitempty"`
}

type agreementFieldsInput struct {
	BusinessCategory          string `json:"business_category"`
	ServiceName               string `json:"service_name"`
	CustomInstructions        string `json:"custom_instructions"`
	AgreementStyle            string `json:"agreement_style"`
	TypicalServiceLocation    string `json:"typical_service_location"`
	Tone                      string `json:"tone"`
	IncludeCancellationPolicy bool   `json:"include_cancellation_policy"`
	IncludeLatenessPolicy     bool   `json:"include_lateness_policy"`
	IncludePaymentTerms       bool   `json:"include_payment_terms"`
}

type agreementUploadInput struct {
	SourcePDFR2Key     string `json:"source_pdf_r2_key"`
	SourcePDFFileName  string `json:"source_pdf_file_name"`
	SourceTitle        string `json:"source_title"`
	BusinessCategory   string `json:"business_category"`
	ServiceName        string `json:"service_name"`
	CustomInstructions string `json:"custom_instructions"`
}

type agreementGenerationJobView struct {
	ID           uuid.UUID        `json:"id"`
	FamilyID     uuid.UUID        `json:"family_id"`
	VersionID    uuid.UUID        `json:"version_id"`
	Status       domain.JobStatus `json:"status"`
	AttemptCount int              `json:"attempt_count"`
	ErrorCode    string           `json:"error_code,omitempty"`
	ErrorMessage string           `json:"error_message,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	CompletedAt  *time.Time       `json:"completed_at,omitempty"`
	UpdatedAt    time.Time        `json:"updated_at"`
}

func (h *Handler) listAgreementTemplateFamilies(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	filter := agreementrepo.TemplateFamilyListFilter{
		Search:   r.URL.Query().Get("search"),
		Category: r.URL.Query().Get("category"),
		Limit:    parseTemplateListLimit(r.URL.Query().Get("limit")),
	}
	if value := strings.TrimSpace(r.URL.Query().Get("status")); value != "" {
		status, err := domain.ParseTemplateFamilyStatus(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_template_status", "Template status is invalid.")
			return
		}
		filter.Status = &status
	}
	if err := parseTemplateListCursor(r, &filter); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "Template list cursor is invalid.")
		return
	}
	items, err := h.agreements.ListClientTemplateFamilies(r.Context(), client.ID, filter)
	if err != nil {
		writeAgreementStoreError(w, err)
		return
	}
	views := make([]agreementTemplateFamilyListItemView, len(items))
	for index, item := range items {
		views[index] = agreementTemplateFamilyListItemView{
			ID: item.ID, Title: item.Title, Description: item.Description,
			Category: item.Category, Tags: item.Tags,
			ConfirmationMethod: item.ConfirmationMethod, Status: item.Status,
			HasDraft: item.DraftVersionID != nil, DraftRevision: item.DraftRevision,
			ServiceUsage: item.ServiceUsage, AgreementUsage: item.AgreementUsage,
			UpdatedAt: item.UpdatedAt,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": views})
}

func (h *Handler) listAgreementTemplateLibrary(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	items, err := h.agreements.ListSystemTemplateFamilies(
		r.Context(), r.URL.Query().Get("search"), r.URL.Query().Get("category"),
	)
	if err != nil {
		writeAgreementStoreError(w, err)
		return
	}
	views := make([]agreementTemplateLibraryItemView, len(items))
	for index, item := range items {
		views[index] = agreementTemplateLibraryItemView{
			ID: item.ID, Title: item.Title, Description: item.Description,
			Category: item.Category, Tags: item.Tags, ConfirmationMethod: item.ConfirmationMethod,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": views})
}

func (h *Handler) copyAgreementTemplateLibraryFamily(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	familyID, err := uuid.Parse(chi.URLParam(r, "familyID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_template_id", "Template ID is invalid.")
		return
	}
	createdID, err := h.agreements.CopySystemTemplate(r.Context(), client.ID, familyID)
	if err != nil {
		writeAgreementStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]uuid.UUID{"family_id": createdID})
}

func (h *Handler) getAgreementTemplateFamily(w http.ResponseWriter, r *http.Request) {
	client, familyID, ok := h.agreementTemplateRequestIdentity(w, r)
	if !ok {
		return
	}
	details, err := h.agreements.GetClientTemplateFamily(r.Context(), client.ID, familyID)
	if err != nil {
		writeAgreementStoreError(w, err)
		return
	}
	view, err := agreementTemplateFamilyDetailsView(details)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "agreement_template_invalid", "The stored agreement template is invalid.")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) updateAgreementTemplateDraft(w http.ResponseWriter, r *http.Request) {
	client, familyID, ok := h.agreementTemplateRequestIdentity(w, r)
	if !ok {
		return
	}
	input, err := decodeJSON[updateAgreementTemplateDraftRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	version, err := h.agreements.UpdateClientTemplateDraft(r.Context(), agreementrepo.UpdateTemplateDraftParams{
		ClientID: client.ID, FamilyID: familyID, ExpectedRevision: input.Revision,
		Title: input.Title, Description: input.Description, Category: input.Category,
		Tags: input.Tags, ConfirmationMethod: input.ConfirmationMethod, Document: input.Document,
	})
	if err != nil {
		writeAgreementStoreError(w, err)
		return
	}
	view, err := agreementTemplateVersionResponse(version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "agreement_template_invalid", "The saved agreement template is invalid.")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) publishAgreementTemplateDraft(w http.ResponseWriter, r *http.Request) {
	client, familyID, ok := h.agreementTemplateRequestIdentity(w, r)
	if !ok {
		return
	}
	version, err := h.agreements.PublishClientTemplateDraft(r.Context(), client.ID, familyID)
	if err != nil {
		writeAgreementStoreError(w, err)
		return
	}
	view, err := agreementTemplateVersionResponse(version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "agreement_template_invalid", "The published agreement template is invalid.")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) duplicateAgreementTemplateFamily(w http.ResponseWriter, r *http.Request) {
	client, familyID, ok := h.agreementTemplateRequestIdentity(w, r)
	if !ok {
		return
	}
	createdID, err := h.agreements.DuplicateClientTemplateFamily(r.Context(), client.ID, familyID)
	if err != nil {
		writeAgreementStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]uuid.UUID{"family_id": createdID})
}

func (h *Handler) archiveAgreementTemplateFamily(w http.ResponseWriter, r *http.Request) {
	client, familyID, ok := h.agreementTemplateRequestIdentity(w, r)
	if !ok {
		return
	}
	if err := h.agreements.ArchiveClientTemplateFamily(r.Context(), client.ID, familyID); err != nil {
		writeAgreementStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) restoreAgreementTemplateFamily(w http.ResponseWriter, r *http.Request) {
	client, familyID, ok := h.agreementTemplateRequestIdentity(w, r)
	if !ok {
		return
	}
	if err := h.agreements.RestoreClientTemplateFamily(r.Context(), client.ID, familyID); err != nil {
		writeAgreementStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteAgreementTemplateFamily(w http.ResponseWriter, r *http.Request) {
	client, familyID, ok := h.agreementTemplateRequestIdentity(w, r)
	if !ok {
		return
	}
	keys, err := h.agreements.DeleteClientTemplateDraftFamily(r.Context(), client.ID, familyID)
	if err != nil {
		writeAgreementStoreError(w, err)
		return
	}
	if h.storage != nil {
		for _, key := range keys {
			if err := h.storage.Delete(r.Context(), key, h.storage.PrivateBucketName()); err != nil {
				slog.Warn("delete agreement template source PDF failed", "client_id", client.ID, "family_id", familyID, "object_key", key, "error", err)
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getAgreementTemplateFamilyUsage(w http.ResponseWriter, r *http.Request) {
	client, familyID, ok := h.agreementTemplateRequestIdentity(w, r)
	if !ok {
		return
	}
	details, err := h.agreements.GetClientTemplateFamily(r.Context(), client.ID, familyID)
	if err != nil {
		writeAgreementStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agreementTemplateFamilyUsageView{
		Services: details.ServiceUsage, Agreements: details.AgreementUsage,
	})
}

func (h *Handler) startAgreementTemplateGeneration(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if h.ai == nil || !h.ai.Available() {
		writeError(w, http.StatusServiceUnavailable, "ai_unavailable", "Agreement generation is temporarily unavailable.")
		return
	}
	input, err := decodeJSON[startAgreementGenerationRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if (input.Fields == nil) == (input.Upload == nil) {
		writeError(w, http.StatusBadRequest, "invalid_generation_input", "Choose either AI fields or one uploaded agreement.")
		return
	}
	kind := domain.GenerationInputFields
	var generationInput any
	if input.Fields != nil {
		generationInput = domain.FieldsGenerationInput{
			BusinessCategory: input.Fields.BusinessCategory, ServiceName: input.Fields.ServiceName,
			CustomInstructions: input.Fields.CustomInstructions, AgreementStyle: input.Fields.AgreementStyle,
			TypicalServiceLocation: input.Fields.TypicalServiceLocation, Tone: input.Fields.Tone,
			IncludeCancellationPolicy: input.Fields.IncludeCancellationPolicy,
			IncludeLatenessPolicy:     input.Fields.IncludeLatenessPolicy,
			IncludePaymentTerms:       input.Fields.IncludePaymentTerms,
		}
	} else {
		profile, err := h.repo.GetClientProfile(r.Context(), client.ID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "profile_required", "Complete your business profile before uploading an agreement.")
			return
		}
		kind = domain.GenerationInputUpload
		generationInput = domain.UploadGenerationInput{
			SourcePDFR2Key: input.Upload.SourcePDFR2Key, SourcePDFFileName: input.Upload.SourcePDFFileName,
			SourceTitle: input.Upload.SourceTitle, BusinessCategory: input.Upload.BusinessCategory,
			ServiceName: input.Upload.ServiceName, CustomInstructions: input.Upload.CustomInstructions,
			Context: agreementGenerationContext(profile),
		}
	}
	created, err := h.agreements.CreateGenerationDraft(r.Context(), agreementrepo.CreateGenerationDraftParams{
		ClientID: client.ID, Title: input.Title, Description: input.Description,
		Category: input.Category, Tags: input.Tags, ConfirmationMethod: input.ConfirmationMethod,
		InputKind: kind, Input: generationInput,
	})
	if err != nil {
		writeAgreementStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"family_id": created.FamilyID, "version_id": created.VersionID, "job_id": created.JobID,
	})
}

func (h *Handler) getAgreementTemplateGeneration(w http.ResponseWriter, r *http.Request) {
	client, jobID, ok := h.agreementGenerationRequestIdentity(w, r)
	if !ok {
		return
	}
	job, err := h.agreements.GetGenerationJob(r.Context(), client.ID, jobID)
	if err != nil {
		writeAgreementStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agreementGenerationJobResponse(job))
}

func (h *Handler) retryAgreementTemplateGeneration(w http.ResponseWriter, r *http.Request) {
	client, jobID, ok := h.agreementGenerationRequestIdentity(w, r)
	if !ok {
		return
	}
	if err := h.agreements.RetryGenerationJob(r.Context(), client.ID, jobID); err != nil {
		writeAgreementStoreError(w, err)
		return
	}
	job, err := h.agreements.GetGenerationJob(r.Context(), client.ID, jobID)
	if err != nil {
		writeAgreementStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agreementGenerationJobResponse(job))
}

func (h *Handler) agreementTemplateRequestIdentity(w http.ResponseWriter, r *http.Request) (auth.User, uuid.UUID, bool) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return auth.User{}, uuid.Nil, false
	}
	familyID, err := uuid.Parse(chi.URLParam(r, "familyID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_template_id", "Template ID is invalid.")
		return auth.User{}, uuid.Nil, false
	}
	return client, familyID, true
}

func (h *Handler) agreementGenerationRequestIdentity(w http.ResponseWriter, r *http.Request) (auth.User, uuid.UUID, bool) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return auth.User{}, uuid.Nil, false
	}
	jobID, err := uuid.Parse(chi.URLParam(r, "jobID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_generation_job_id", "Generation job ID is invalid.")
		return auth.User{}, uuid.Nil, false
	}
	return client, jobID, true
}

func agreementTemplateFamilyDetailsView(details agreementrepo.TemplateFamilyDetails) (agreementTemplateFamilyView, error) {
	view := agreementTemplateFamilyView{
		ID: details.Family.ID, Title: details.Family.Title, Description: details.Family.Description,
		Category: details.Family.Category, Tags: details.Family.Tags,
		ConfirmationMethod: details.Family.ConfirmationMethod, Status: details.Family.Status,
		Usage:     agreementTemplateFamilyUsageView{Services: details.ServiceUsage, Agreements: details.AgreementUsage},
		Previous:  make([]agreementTemplateVersionView, 0, len(details.PreviousVersions)),
		UpdatedAt: details.Family.UpdatedAt,
	}
	if details.Draft != nil {
		item, err := agreementTemplateVersionResponse(*details.Draft)
		if err != nil {
			return agreementTemplateFamilyView{}, err
		}
		view.Draft = &item
	}
	if details.CurrentPublished != nil {
		item, err := agreementTemplateVersionResponse(*details.CurrentPublished)
		if err != nil {
			return agreementTemplateFamilyView{}, err
		}
		view.Current = &item
	}
	for _, version := range details.PreviousVersions {
		item, err := agreementTemplateVersionResponse(version)
		if err != nil {
			return agreementTemplateFamilyView{}, err
		}
		view.Previous = append(view.Previous, item)
	}
	return view, nil
}

func agreementTemplateVersionResponse(version domain.TemplateVersion) (agreementTemplateVersionView, error) {
	warnings := make([]aiapi.Warning, 0)
	if len(version.ReviewWarnings) > 0 {
		if err := json.Unmarshal(version.ReviewWarnings, &warnings); err != nil {
			return agreementTemplateVersionView{}, err
		}
	}
	return agreementTemplateVersionView{
		ID: version.ID, VersionNumber: version.VersionNumber, State: version.State,
		Document: version.Document, UsedVariableKeys: version.UsedVariableKeys,
		ReviewWarnings: warnings, Revision: version.Revision,
		PublishedAt: version.PublishedAt, UpdatedAt: version.UpdatedAt,
	}, nil
}

func agreementGenerationJobResponse(job domain.TemplateGenerationJob) agreementGenerationJobView {
	return agreementGenerationJobView{
		ID: job.ID, FamilyID: job.FamilyID, VersionID: job.VersionID, Status: job.Status,
		AttemptCount: job.AttemptCount, ErrorCode: job.ErrorCode, ErrorMessage: job.ErrorMessage,
		CreatedAt: job.CreatedAt, CompletedAt: job.CompletedAt, UpdatedAt: job.UpdatedAt,
	}
}

func agreementGenerationContext(profile ClientProfileResponse) []aiapi.NamedValue {
	values := []aiapi.NamedValue{
		{Key: "business_name", Value: profile.BusinessName},
		{Key: "business_email", Value: profile.Email},
		{Key: "business_location", Value: profile.Location},
	}
	result := values[:0]
	for _, value := range values {
		value.Value = strings.TrimSpace(value.Value)
		if value.Value != "" {
			result = append(result, value)
		}
	}
	return result
}

func parseTemplateListLimit(value string) int {
	limit, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 20
	}
	return limit
}

func parseTemplateListCursor(r *http.Request, filter *agreementrepo.TemplateFamilyListFilter) error {
	updatedAt := strings.TrimSpace(r.URL.Query().Get("before_updated_at"))
	id := strings.TrimSpace(r.URL.Query().Get("before_id"))
	if updatedAt == "" && id == "" {
		return nil
	}
	if updatedAt == "" || id == "" {
		return errors.New("incomplete cursor")
	}
	parsedTime, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return err
	}
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return err
	}
	filter.BeforeUpdatedAt = &parsedTime
	filter.BeforeID = &parsedID
	return nil
}

func writeAgreementStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agreementrepo.ErrNotFound):
		writeError(w, http.StatusNotFound, "agreement_template_not_found", "Agreement template was not found.")
	case errors.Is(err, agreementrepo.ErrConflict):
		writeError(w, http.StatusConflict, "agreement_template_conflict", "This template changed elsewhere. Reload it before saving again.")
	case errors.Is(err, agreementrepo.ErrInvalidTransition):
		writeError(w, http.StatusConflict, "agreement_template_state", "This action is not available for the template in its current state.")
	default:
		writeError(w, http.StatusBadRequest, "agreement_template_failed", err.Error())
	}
}
