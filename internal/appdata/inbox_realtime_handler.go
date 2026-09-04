package appdata

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"booking/go-server/internal/auth"
	"booking/go-server/internal/marketplaceauth"

	"github.com/google/uuid"
)

const (
	inboxHeartbeatInterval = 25 * time.Second
	inboxMaximumStreamAge  = 30 * time.Minute
)

func (h *Handler) streamProviderInboxEvents(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	h.streamInboxEvents(w, r, "provider", client.ID)
}

func (h *Handler) streamMarketplaceInboxEvents(w http.ResponseWriter, r *http.Request) {
	customer, ok := marketplaceauth.CustomerFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
		return
	}
	h.streamInboxEvents(w, r, "marketplace_customer", customer.ID)
}

func (h *Handler) streamInboxEvents(
	w http.ResponseWriter,
	r *http.Request,
	actorType string,
	actorID uuid.UUID,
) {
	if h.repo == nil || h.inboxEvents == nil {
		writeError(w, http.StatusServiceUnavailable, "server_unavailable", "Inbox updates are unavailable.")
		return
	}
	after := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if after == "" {
		after = strings.TrimSpace(r.URL.Query().Get("after"))
	}
	if _, err := decodeInboxSyncCursor(after); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "Inbox event cursor is invalid.")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream_unsupported", "Inbox streaming is unsupported.")
		return
	}
	if err := disableResponseWriteDeadline(w); err != nil {
		writeError(w, http.StatusInternalServerError, "stream_deadline_failed", "Inbox streaming is unavailable.")
		return
	}
	releaseBudget, budgetErr := h.streamBudget.Acquire(inboxRemoteIP(r), "")
	if errors.Is(budgetErr, ErrStreamBudgetExceeded) {
		w.Header().Set("Retry-After", "5")
		if h.inboxMetrics != nil {
			h.inboxMetrics.streamsRejected.Add(1)
		}
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many realtime streams are open.")
		return
	}
	defer releaseBudget()

	signals, unsubscribe, err := h.inboxEvents.Subscribe(actorType, actorID, inboxRemoteIP(r))
	if errors.Is(err, ErrInboxStreamLimit) {
		w.Header().Set("Retry-After", "5")
		if h.inboxMetrics != nil {
			h.inboxMetrics.streamsRejected.Add(1)
		}
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many inbox streams are open.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "server_unavailable", "Inbox updates are unavailable.")
		return
	}
	defer unsubscribe()
	if h.operationalMetrics != nil {
		stream := "inbox_customer"
		if actorType == "provider" {
			stream = "inbox_provider"
		}
		defer h.operationalMetrics.SSEConnectionOpened(stream)()
	}
	h.inboxMetrics.StreamOpened()
	defer h.inboxMetrics.StreamClosed()

	initial, err := h.repo.ListInboxEventsAfter(r.Context(), actorType, actorID, after, inboxEventDrainLimit)
	if errors.Is(err, ErrInboxInvalidCursor) {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "Inbox event cursor is invalid.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not load inbox updates.")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Referrer-Policy", "no-referrer")

	cursor := after
	sendReset := func(reason string, resetCursor string) bool {
		payload, marshalErr := json.Marshal(InboxRealtimeReset{Cursor: resetCursor, Reason: reason})
		if marshalErr != nil {
			return false
		}
		if h.inboxMetrics != nil {
			h.inboxMetrics.streamResets.Add(1)
		}
		return writeInboxSSE(w, flusher, "reset", resetCursor, payload)
	}
	sendDrain := func(drain InboxEventDrain) bool {
		if drain.Reset {
			sendReset("cursor_expired", drain.Cursor)
			return false
		}
		if drain.HasMore {
			resetCursor := drain.LatestCursor
			if resetCursor == "" {
				resetCursor = drain.Cursor
			}
			sendReset("catchup_limit", resetCursor)
			return false
		}
		lastSentCursor := cursor
		for index := range drain.Events {
			drain.Events[index].Conversation.Counterparty.AvatarURL = h.signedMediaURL(
				r.Context(), drain.Events[index].Conversation.Counterparty.AvatarURL,
			)
			payload, marshalErr := json.Marshal(drain.Events[index])
			if marshalErr != nil || !writeInboxSSE(
				w, flusher, "inbox", drain.Events[index].Cursor, payload,
			) {
				return false
			}
			lastSentCursor = drain.Events[index].Cursor
			h.inboxMetrics.EventDelivered(drain.Events[index].CreatedAt)
			if h.operationalMetrics != nil {
				h.operationalMetrics.ObserveSSEEventLag("inbox", drain.Events[index].CreatedAt)
			}
		}
		if drain.Cursor != "" && drain.Cursor != lastSentCursor {
			if !writeInboxSSE(w, flusher, "cursor", drain.Cursor, []byte(`{}`)) {
				return false
			}
		}
		cursor = drain.Cursor
		return true
	}
	if !sendDrain(initial) {
		return
	}
	if _, err := w.Write([]byte(": connected\n\n")); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(inboxHeartbeatInterval)
	defer heartbeat.Stop()
	maximumAge := time.NewTimer(inboxMaximumStreamAge)
	defer maximumAge.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case signal := <-signals:
			if signal == InboxBrokerReset {
				sendReset("broker_overflow", cursor)
				return
			}
			drain, drainErr := h.repo.ListInboxEventsAfter(
				r.Context(), actorType, actorID, cursor, inboxEventDrainLimit,
			)
			if drainErr != nil || !sendDrain(drain) {
				if h.inboxMetrics != nil {
					h.inboxMetrics.streamFailures.Add(1)
				}
				return
			}
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-maximumAge.C:
			return
		}
	}
}

func writeInboxSSE(
	w http.ResponseWriter,
	flusher http.Flusher,
	eventType string,
	cursor string,
	payload []byte,
) bool {
	if _, err := w.Write([]byte("id: " + cursor + "\nevent: " + eventType + "\ndata: ")); err != nil {
		return false
	}
	if _, err := w.Write(payload); err != nil {
		return false
	}
	if _, err := w.Write([]byte("\n\n")); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func inboxRemoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
