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
	// Duplicates share the stored image. Delete only once no catalog resource refers to it.
	// Catalog mutations lock this same provider row, preventing a concurrent duplicate
	// from creating a reference between the check and the storage deletion.
	tx, err := h.repo.db.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	var provider uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM clients WHERE id=$1 FOR UPDATE`, clientID).Scan(&provider); err != nil {
		return
	}
	var referenced bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services WHERE image_url=$1 UNION ALL SELECT 1 FROM service_sections WHERE cover_image_url=$1 UNION ALL SELECT 1 FROM bookings WHERE image_url=$1 UNION ALL SELECT 1 FROM booking_quotes WHERE service_image_url=$1 UNION ALL SELECT 1 FROM service_wizard_drafts WHERE payload->>'imageUrl'=$1)`, oldURL).Scan(&referenced); err != nil || referenced {
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
