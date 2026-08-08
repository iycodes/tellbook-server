package appdata

import (
	"errors"
	"net/http"
	"time"

	"booking/go-server/internal/auth"

	"github.com/google/uuid"
)

func (h *Handler) createManagedAgreement(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	input, err := decodeJSON[CreateManagedAgreementInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_agreement", "Agreement details are invalid.")
		return
	}
	agreement, err := h.repo.CreateManagedAgreement(r.Context(), client.ID, input)
	if err != nil {
		var missing *MissingAgreementVariablesError
		if errors.As(err, &missing) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": map[string]any{
					"code":           "agreement_details_required",
					"message":        "Add the remaining details before previewing this agreement.",
					"missing_fields": missing.Keys,
				},
			})
			return
		}
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "agreement_source_not_found", "The selected customer, booking, or template was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "agreement_create_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, agreement)
}

func (h *Handler) listManagedAgreements(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	items, err := h.repo.ListManagedAgreements(
		r.Context(), client.ID, r.URL.Query().Get("status"), r.URL.Query().Get("search"),
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, "agreements_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) getManagedAgreementDeliveryLink(w http.ResponseWriter, r *http.Request) {
	client, agreementID, ok := managedAgreementIdentity(w, r)
	if !ok {
		return
	}
	token, err := h.repo.ActivateManagedAgreementLink(r.Context(), client.ID, agreementID)
	if err != nil {
		writeManagedAgreementActionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": h.publicBaseURL + "/agreement/" + token})
}

func (h *Handler) sendManagedAgreement(w http.ResponseWriter, r *http.Request) {
	client, agreementID, ok := managedAgreementIdentity(w, r)
	if !ok {
		return
	}
	if err := h.repo.SendManagedAgreement(r.Context(), client.ID, agreementID); err != nil {
		writeManagedAgreementActionError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) resendManagedAgreement(w http.ResponseWriter, r *http.Request) {
	client, agreementID, ok := managedAgreementIdentity(w, r)
	if !ok {
		return
	}
	if err := h.repo.ResendManagedAgreement(r.Context(), client.ID, agreementID); err != nil {
		writeManagedAgreementActionError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) cancelManagedAgreement(w http.ResponseWriter, r *http.Request) {
	h.changeManagedAgreementStatus(w, r, "cancelled")
}

func (h *Handler) expireManagedAgreement(w http.ResponseWriter, r *http.Request) {
	h.changeManagedAgreementStatus(w, r, "expired")
}

func (h *Handler) changeManagedAgreementStatus(w http.ResponseWriter, r *http.Request, status string) {
	client, agreementID, ok := managedAgreementIdentity(w, r)
	if !ok {
		return
	}
	if err := h.repo.ChangeManagedAgreementStatus(r.Context(), client.ID, agreementID, status); err != nil {
		writeManagedAgreementActionError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) retryManagedAgreementProcessing(w http.ResponseWriter, r *http.Request) {
	client, agreementID, ok := managedAgreementIdentity(w, r)
	if !ok {
		return
	}
	if err := h.repo.RetryManagedAgreementProcessing(r.Context(), client.ID, agreementID); err != nil {
		writeManagedAgreementActionError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getManagedAgreementPDF(w http.ResponseWriter, r *http.Request) {
	client, agreementID, ok := managedAgreementIdentity(w, r)
	if !ok {
		return
	}
	status, key, err := h.repo.GetManagedAgreementArtifact(r.Context(), client.ID, agreementID)
	if err != nil {
		writeManagedAgreementActionError(w, err)
		return
	}
	if status != "ready" || key == "" || h.storage == nil {
		writeError(w, http.StatusConflict, "agreement_pdf_not_ready", "The completed PDF is not ready yet.")
		return
	}
	storageURL, err := h.storage.GetStorageObjectURL(key, h.storage.PrivateBucketName())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "agreement_pdf_failed", "Could not open the completed PDF.")
		return
	}
	signedURL, err := h.storage.ResolveBrowserURL(r.Context(), storageURL, 5*time.Minute)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "agreement_pdf_failed", "Could not open the completed PDF.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": signedURL})
}

func (h *Handler) getManagedAgreementSignature(w http.ResponseWriter, r *http.Request) {
	client, agreementID, ok := managedAgreementIdentity(w, r)
	if !ok {
		return
	}
	content, err := h.repo.GetManagedAgreementSignature(r.Context(), client.ID, agreementID)
	if err != nil {
		writeManagedAgreementActionError(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func managedAgreementIdentity(w http.ResponseWriter, r *http.Request) (auth.User, uuid.UUID, bool) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return auth.User{}, uuid.Nil, false
	}
	agreementID, err := uuidFromURLParam("agreementID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_agreement_id", "Agreement ID is invalid.")
		return auth.User{}, uuid.Nil, false
	}
	return client, agreementID, true
}

func writeManagedAgreementActionError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusConflict, "agreement_action_unavailable", "This action is not available for the agreement in its current state.")
		return
	}
	writeError(w, http.StatusInternalServerError, "agreement_action_failed", err.Error())
}

func (h *Handler) getManagedAgreement(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	agreementID, err := uuidFromURLParam("agreementID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_agreement_id", "Agreement ID is invalid.")
		return
	}
	response, err := h.repo.GetManagedAgreement(r.Context(), client.ID, agreementID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "agreement_not_found", "Agreement was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "agreement_failed", "Could not load agreement.")
		return
	}
	writeJSON(w, http.StatusOK, response)
}
