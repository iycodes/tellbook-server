package appdata

import (
	"net/http"
	"strings"

	"booking/go-server/internal/auth"
)

func (h *Handler) getRevenueOverview(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	rangeName, days, ok := parseRevenueRange(r.URL.Query().Get("range"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_revenue_range", "Revenue range must be 7d, 30d, 90d, 180d, or 365d.")
		return
	}

	response, err := h.repo.GetRevenueOverview(r.Context(), authedClient.ID, rangeName, days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "revenue_failed", "Could not load revenue.")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func parseRevenueRange(value string) (string, int, bool) {
	if name, days, ok := parseAnalyticsRange(value); ok {
		return name, days, true
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "180d":
		return "180d", 180, true
	case "365d":
		return "365d", 365, true
	default:
		return "", 0, false
	}
}
