package appdata

import (
	"net/http"
	"strings"

	"booking/go-server/internal/auth"
)

func (h *Handler) getStatsOverview(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	rangeName, days, ok := parseAnalyticsRange(r.URL.Query().Get("range"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_stats_range", "Stats range must be 7d, 30d, or 90d.")
		return
	}

	response, err := h.repo.GetStatsOverview(r.Context(), authedClient.ID, rangeName, days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats_failed", "Could not load business stats.")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func parseAnalyticsRange(value string) (string, int, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "30d":
		return "30d", 30, true
	case "7d":
		return "7d", 7, true
	case "90d":
		return "90d", 90, true
	default:
		return "", 0, false
	}
}
