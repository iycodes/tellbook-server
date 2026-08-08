package appdata

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) preparePublicBookingAgreement(w http.ResponseWriter, r *http.Request) {
	response, err := h.repo.PreparePublicBookingAgreement(r.Context(), chi.URLParam(r, "bookingToken"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "agreement_not_found", "Agreement was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "agreement_not_ready", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) getPublicAgreementPDF(w http.ResponseWriter, r *http.Request) {
	status, key, err := h.repo.GetPublicAgreementPDFArtifact(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "agreement_pdf_not_found", "The completed agreement was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "agreement_pdf_failed", "Could not check the completed PDF.")
		return
	}
	response := map[string]string{"status": status}
	if status == "ready" {
		if key == "" || h.storage == nil {
			writeError(w, http.StatusInternalServerError, "agreement_pdf_failed", "The completed PDF is unavailable.")
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
		response["url"] = signedURL
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) getPublicAgreement(w http.ResponseWriter, r *http.Request) {
	response, err := h.repo.GetPublicAgreementByToken(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "agreement_not_found", "Agreement was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "agreement_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) acceptPublicAgreement(w http.ResponseWriter, r *http.Request) {
	input, err := decodeJSON[PublicAgreementAcceptInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	response, err := h.repo.AcceptPublicAgreementByToken(r.Context(), chi.URLParam(r, "token"), input)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "agreement_not_found", "Agreement was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "agreement_completion_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}
