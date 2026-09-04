package appdata

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
)

func (h *Handler) isOwnedPublicImage(clientID uuid.UUID, rawURL, category string) bool {
	if h.storage == nil {
		return false
	}
	parsed, ok := h.storage.ParseStorageURL(strings.TrimSpace(rawURL))
	if !ok || parsed.BucketName != h.storage.PublicBucketName() {
		return false
	}
	wantPrefix := fmt.Sprintf("clients/%s/%s/", clientID, category)
	return strings.HasPrefix(parsed.ObjectKey, wantPrefix)
}

func (h *Handler) validOptionalOwnedPublicImage(clientID uuid.UUID, rawURL, category string) bool {
	return strings.TrimSpace(rawURL) == "" || h.isOwnedPublicImage(clientID, rawURL, category)
}

func (h *Handler) deleteReplacedPublicImage(ctx context.Context, clientID uuid.UUID, oldURL, newURL, category string) {
	oldURL = strings.TrimSpace(oldURL)
	if oldURL == "" || oldURL == strings.TrimSpace(newURL) || !h.isOwnedPublicImage(clientID, oldURL, category) {
		return
	}
	parsed, _ := h.storage.ParseStorageURL(oldURL)
	if err := h.storage.Delete(ctx, parsed.ObjectKey, parsed.BucketName); err != nil {
		slog.Warn(
			"replaced public image cleanup failed",
			"error", err,
			"client_id", clientID,
			"category", category,
			"object_key", parsed.ObjectKey,
		)
	}
}
