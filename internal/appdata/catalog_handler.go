package appdata

import (
	"booking/go-server/internal/auth"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func (h *Handler) patchManagedService(w http.ResponseWriter, r *http.Request) {
	h.catalogREST(w, r, "update_service")
}
func (h *Handler) patchManagedServiceStatus(w http.ResponseWriter, r *http.Request) {
	h.catalogREST(w, r, "set_service_status")
}
func (h *Handler) patchServiceSection(w http.ResponseWriter, r *http.Request) {
	h.catalogREST(w, r, "update_service_section")
}
func (h *Handler) catalogREST(w http.ResponseWriter, r *http.Request, operation string) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, 401, "unauthorized", "Sign in to Tellbook.")
		return
	}
	rawID := chi.URLParam(r, "serviceID")
	if rawID == "" {
		rawID = chi.URLParam(r, "sectionID")
	}
	id := uuid.Nil
	var err error
	if rawID != "" {
		id, err = uuid.Parse(rawID)
		if err != nil {
			writeError(w, 400, "invalid_id", "Invalid resource ID.")
			return
		}
	}
	revision, _ := strconv.ParseInt(strings.Trim(r.Header.Get("If-Match"), "\""), 10, 64)
	input := []byte(`{}`)
	if operation == "delete_service_section" {
		input, _ = json.Marshal(DeleteServiceSectionInput{Mode: r.URL.Query().Get("mode"), TargetSectionID: r.URL.Query().Get("target_section_id")})
	} else if r.Body != nil && r.ContentLength != 0 {
		input, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 128*1024))
		if err != nil {
			writeError(w, 400, "invalid_request", "Invalid request body.")
			return
		}
	}
	receipt, err := h.MutateCatalog(r.Context(), CatalogActor{ProviderID: user.ID, SecurityRevision: user.SecurityRevision}, operation, id, input, revision, r.Header.Get("Idempotency-Key"))
	if err != nil {
		var public *CatalogError
		if !errors.As(err, &public) {
			public = &CatalogError{"operation_failed", "Could not complete this operation."}
		}
		status := 400
		switch public.Code {
		case "not_found":
			status = 404
		case "unauthorized":
			status = 401
		case "insufficient_scope":
			status = 403
		case "revision_conflict", "idempotency_conflict", "service_in_use", "market_required", "collection_limit":
			status = 409
		case "operation_failed":
			status = 500
		}
		writeError(w, status, public.Code, public.Message)
		return
	}
	w.Header().Set("X-Mutation-Receipt", receipt.ReceiptID)
	if receipt.Revision > 0 {
		w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(receipt.Revision, 10)))
	}
	if strings.HasPrefix(operation, "delete_") || strings.HasPrefix(operation, "reorder_") {
		w.WriteHeader(204)
		return
	}
	status := 200
	if strings.HasPrefix(operation, "create_") {
		status = 201
	}
	data := receipt.Data
	if strings.Contains(operation, "service_section") {
		var item ServiceSectionItem
		if json.Unmarshal(data, &item) == nil {
			item.CoverImageURL = h.signedMediaURL(r.Context(), item.CoverImageURL)
			data, _ = json.Marshal(item)
		}
	} else {
		var item ManagedServiceItem
		if json.Unmarshal(data, &item) == nil {
			item.ImageURL = h.signedMediaURL(r.Context(), item.ImageURL)
			data, _ = json.Marshal(item)
		}
	}
	writeJSON(w, status, data)
}

func (h *Handler) getCatalogReceipt(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, 401, "unauthorized", "Sign in to Tellbook.")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "receiptID"))
	if err != nil {
		writeError(w, 404, "not_found", "Receipt was not found.")
		return
	}
	var data json.RawMessage
	if err = h.repo.db.QueryRow(r.Context(), `SELECT result FROM catalog_mutation_receipts WHERE id=$1 AND provider_id=$2`, id, user.ID).Scan(&data); err != nil {
		writeError(w, 404, "not_found", "Receipt was not found.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, data)
}
