package appdata

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"booking/go-server/internal/auth"
)

type uploadImageInput struct {
	DataURL     string `json:"data_url"`
	ContentType string `json:"content_type"`
	Category    string `json:"category"`
}

type uploadDocumentResponse struct {
	URL string `json:"url"`
	Key string `json:"key"`
}

type uploadImageResponse struct {
	URL        string `json:"url"`
	BrowserURL string `json:"browser_url"`
}

type decodedImagePayload struct {
	ContentType string
	Data        []byte
	Extension   string
}

func (h *Handler) uploadImage(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if h.storage == nil {
		writeError(w, http.StatusBadRequest, "uploads_not_configured", "Image uploads are not configured.")
		return
	}

	input, err := decodeJSON[uploadImageInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	payload, err := decodeImageDataURL(input.DataURL, input.ContentType)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_image", err.Error())
		return
	}

	category := normalizeUploadCategory(input.Category)
	objectKey := fmt.Sprintf("clients/%s/%s/%d%s", authedClient.ID.String(), category, time.Now().UTC().UnixNano(), payload.Extension)
	bucketName := h.storage.PrivateBucketName()

	objectURL, err := h.storage.Upload(r.Context(), payload.Data, objectKey, payload.ContentType, bucketName)
	if err != nil {
		slog.Error(
			"image upload failed",
			"error",
			err,
			"client_id",
			authedClient.ID.String(),
			"bucket",
			bucketName,
			"object_key",
			objectKey,
			"category",
			category,
			"content_type",
			payload.ContentType,
			"bytes",
			len(payload.Data),
		)
		writeError(w, http.StatusInternalServerError, "upload_failed", "Could not upload image.")
		return
	}

	browserURL, err := h.storage.ResolveBrowserURL(r.Context(), objectURL, signedMediaURLTTL)
	if err != nil {
		slog.Error(
			"image preview URL signing failed",
			"error", err,
			"client_id", authedClient.ID.String(),
			"bucket", bucketName,
			"object_key", objectKey,
		)
		if deleteErr := h.storage.Delete(r.Context(), objectKey, bucketName); deleteErr != nil {
			slog.Warn("uploaded image cleanup failed", "error", deleteErr, "object_key", objectKey)
		}
		writeError(w, http.StatusInternalServerError, "upload_failed", "Could not prepare image preview.")
		return
	}

	writeJSON(w, http.StatusCreated, uploadImageResponse{
		URL:        objectURL,
		BrowserURL: browserURL,
	})
}

func (h *Handler) uploadDocument(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if h.storage == nil {
		writeError(w, http.StatusBadRequest, "uploads_not_configured", "Document uploads are not configured.")
		return
	}

	input, err := decodeJSON[uploadImageInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	payload, err := decodeDocumentDataURL(input.DataURL, input.ContentType)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_document", err.Error())
		return
	}

	objectKey := fmt.Sprintf("clients/%s/templates/%d%s", authedClient.ID.String(), time.Now().UTC().UnixNano(), payload.Extension)
	bucketName := h.storage.PrivateBucketName()

	objectURL, err := h.storage.Upload(r.Context(), payload.Data, objectKey, payload.ContentType, bucketName)
	if err != nil {
		slog.Error(
			"document upload failed",
			"error",
			err,
			"client_id",
			authedClient.ID.String(),
			"bucket",
			bucketName,
			"object_key",
			objectKey,
			"content_type",
			payload.ContentType,
			"bytes",
			len(payload.Data),
		)
		writeError(w, http.StatusInternalServerError, "upload_failed", "Could not upload document.")
		return
	}

	writeJSON(w, http.StatusCreated, uploadDocumentResponse{
		URL: objectURL,
		Key: objectKey,
	})
}

func decodeImageDataURL(dataURL, providedContentType string) (decodedImagePayload, error) {
	contentType := strings.TrimSpace(providedContentType)
	rawBase64 := strings.TrimSpace(dataURL)

	if strings.HasPrefix(rawBase64, "data:") {
		parts := strings.SplitN(rawBase64, ",", 2)
		if len(parts) != 2 {
			return decodedImagePayload{}, errors.New("image must be a valid data URL")
		}

		metadata := strings.TrimPrefix(parts[0], "data:")
		rawBase64 = parts[1]
		if contentType == "" {
			contentType = strings.TrimSuffix(metadata, ";base64")
		}
	}

	switch contentType {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
	default:
		return decodedImagePayload{}, errors.New("image must be jpeg, png, webp, or gif")
	}

	data, err := base64.StdEncoding.DecodeString(rawBase64)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(rawBase64)
	}
	if err != nil {
		return decodedImagePayload{}, errors.New("image must be valid base64")
	}
	if len(data) == 0 {
		return decodedImagePayload{}, errors.New("image cannot be empty")
	}
	if len(data) > 4<<20 {
		return decodedImagePayload{}, errors.New("image must be 4 MB or smaller")
	}

	detectedContentType := http.DetectContentType(data)
	if !strings.HasPrefix(detectedContentType, "image/") {
		return decodedImagePayload{}, errors.New("image content type is invalid")
	}
	contentType = detectedContentType

	extensions, _ := mime.ExtensionsByType(contentType)
	extension := ".bin"
	if len(extensions) > 0 {
		extension = extensions[0]
	}

	return decodedImagePayload{
		ContentType: contentType,
		Data:        data,
		Extension:   filepath.Clean(extension),
	}, nil
}

func decodeDocumentDataURL(dataURL, providedContentType string) (decodedImagePayload, error) {
	contentType := strings.TrimSpace(providedContentType)
	rawBase64 := strings.TrimSpace(dataURL)

	if strings.HasPrefix(rawBase64, "data:") {
		parts := strings.SplitN(rawBase64, ",", 2)
		if len(parts) != 2 {
			return decodedImagePayload{}, errors.New("document must be a valid data URL")
		}

		metadata := strings.TrimPrefix(parts[0], "data:")
		rawBase64 = parts[1]
		if contentType == "" {
			contentType = strings.TrimSuffix(metadata, ";base64")
		}
	}

	if contentType != "application/pdf" {
		return decodedImagePayload{}, errors.New("document must be a PDF")
	}

	data, err := base64.StdEncoding.DecodeString(rawBase64)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(rawBase64)
	}
	if err != nil {
		return decodedImagePayload{}, errors.New("document must be valid base64")
	}
	if len(data) == 0 {
		return decodedImagePayload{}, errors.New("document cannot be empty")
	}
	if len(data) > 10<<20 {
		return decodedImagePayload{}, errors.New("document must be 10 MB or smaller")
	}

	detectedContentType := http.DetectContentType(data)
	if detectedContentType != "application/pdf" {
		return decodedImagePayload{}, errors.New("document content type is invalid")
	}

	return decodedImagePayload{
		ContentType: "application/pdf",
		Data:        data,
		Extension:   ".pdf",
	}, nil
}

func normalizeUploadCategory(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "services":
		return "services"
	case "sections":
		return "sections"
	default:
		return "misc"
	}
}
