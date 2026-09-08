package appdata

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"booking/go-server/internal/auth"

	"github.com/google/uuid"
)

func (h *Handler) tessaClient(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return uuid.Nil, false
	}
	if !h.tessaAvailable(client.ID) {
		writeError(w, http.StatusNotFound, "tessa_unavailable", "Tessa is not available for this account yet.")
		return uuid.Nil, false
	}
	return client.ID, true
}

func (h *Handler) getTessaBootstrap(w http.ResponseWriter, r *http.Request) {
	clientID, ok := h.tessaClient(w, r)
	if !ok {
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 50 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "Tessa message limit must be between 1 and 50.")
			return
		}
		limit = parsed
	}
	response, err := h.repo.GetTessaBootstrap(r.Context(), clientID, h.tessaNoticeRevision, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tessa_bootstrap_failed", "Could not open Tessa.")
		return
	}
	if h.tessaWhatsApp != nil {
		state, stateErr := h.tessaWhatsApp.State(r.Context(), clientID)
		if stateErr != nil {
			writeError(w, 500, "tessa_bootstrap_failed", "Could not load the WhatsApp connection.")
			return
		}
		response.WhatsApp = &state
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) completeTessaIntroduction(w http.ResponseWriter, r *http.Request) {
	clientID, ok := h.tessaClient(w, r)
	if !ok {
		return
	}
	input, err := decodeJSON[TessaIntroductionInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Tessa introduction request is invalid.")
		return
	}
	if err := h.repo.CompleteTessaIntroduction(r.Context(), clientID, input.NoticeRevision, h.tessaNoticeRevision); errors.Is(err, ErrTessaNoticeRevision) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":                   map[string]string{"code": "tessa_notice_changed", "message": "The Tessa notice has changed. Review the current notice to continue."},
			"current_notice_revision": h.tessaNoticeRevision,
		})
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "tessa_introduction_failed", "Could not start Tessa.")
		return
	}
	response, err := h.repo.GetTessaBootstrap(r.Context(), clientID, h.tessaNoticeRevision, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tessa_bootstrap_failed", "Could not open Tessa.")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) createTessaThread(w http.ResponseWriter, r *http.Request) {
	clientID, ok := h.tessaClient(w, r)
	if !ok {
		return
	}
	input, err := decodeJSON[TessaNewThreadInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "New conversation request is invalid.")
		return
	}
	requestID, err := uuid.Parse(strings.TrimSpace(input.ClientRequestID))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_client_request_id", "Client request ID must be a UUID.")
		return
	}
	thread, err := h.repo.CreateTessaThread(r.Context(), clientID, requestID, h.tessaNoticeRevision)
	if errors.Is(err, ErrTessaNoticeRevision) {
		writeError(w, http.StatusConflict, "tessa_notice_changed", "Review the current Tessa notice to continue.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tessa_thread_failed", "Could not start a new Tessa conversation.")
		return
	}
	writeJSON(w, http.StatusOK, thread)
}

func (h *Handler) sendTessaMessage(w http.ResponseWriter, r *http.Request) {
	clientID, ok := h.tessaClient(w, r)
	if !ok {
		return
	}
	threadID, err := uuidFromURLParam("threadID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_thread_id", "Tessa thread ID is invalid.")
		return
	}
	input, err := decodeJSON[TessaSendMessageInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Tessa message request is invalid.")
		return
	}
	clientMessageID, err := uuid.Parse(strings.TrimSpace(input.ClientMessageID))
	content := strings.TrimSpace(input.Content)
	if err != nil || content == "" || utf8.RuneCountInString(content) > 4000 {
		writeError(w, http.StatusBadRequest, "invalid_message", "Enter a message of at most 4,000 characters.")
		return
	}
	response, err := h.repo.SendTessaMessage(
		r.Context(), clientID, threadID, clientMessageID, content,
		h.tessaPrimaryProvider, h.tessaPrimaryModel, h.tessaConfigHash, h.tessaNoticeRevision,
	)
	if errors.Is(err, ErrTessaNoticeRevision) {
		writeError(w, http.StatusConflict, "tessa_notice_changed", "Review the current Tessa notice to continue.")
		return
	}
	if errors.Is(err, ErrTessaIdempotencyConflict) {
		writeError(w, http.StatusConflict, "tessa_idempotency_conflict", "That message ID was already used for different content.")
		return
	}
	if errors.Is(err, ErrTessaRunInProgress) {
		bootstrap, loadErr := h.repo.GetTessaBootstrap(r.Context(), clientID, h.tessaNoticeRevision, 1)
		if loadErr != nil {
			writeError(w, http.StatusConflict, "tessa_run_in_progress", "Tessa is still working on the previous message.")
			return
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": map[string]string{"code": "tessa_run_in_progress", "message": "Tessa is still working on the previous message."},
			"run":   bootstrap.CurrentRun,
		})
		return
	}
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "tessa_thread_not_found", "Tessa conversation was not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tessa_send_failed", "Could not send your message to Tessa.")
		return
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (h *Handler) listOlderTessaMessages(w http.ResponseWriter, r *http.Request) {
	clientID, ok := h.tessaClient(w, r)
	if !ok {
		return
	}
	threadID, err := uuidFromURLParam("threadID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_thread_id", "Tessa thread ID is invalid.")
		return
	}
	before, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("before")), 10, 64)
	if err != nil || before <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "Tessa message cursor is invalid.")
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 50 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "Tessa message limit must be between 1 and 50.")
			return
		}
		limit = parsed
	}
	page, err := h.repo.ListOlderTessaMessages(r.Context(), clientID, threadID, before, limit)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "tessa_thread_not_found", "Tessa conversation was not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tessa_history_failed", "Could not load earlier Tessa messages.")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) cancelTessaRun(w http.ResponseWriter, r *http.Request) {
	clientID, ok := h.tessaClient(w, r)
	if !ok {
		return
	}
	runID, err := uuidFromURLParam("runID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_run_id", "Tessa run ID is invalid.")
		return
	}
	run, err := h.repo.CancelTessaRun(r.Context(), clientID, runID)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "tessa_run_not_found", "Active Tessa run was not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tessa_cancel_failed", "Could not stop Tessa.")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *Handler) streamTessaEvents(w http.ResponseWriter, r *http.Request) {
	clientID, ok := h.tessaClient(w, r)
	if !ok {
		return
	}
	after := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if after == "" {
		after = strings.TrimSpace(r.URL.Query().Get("after"))
	}
	if _, err := decodeInboxSyncCursor(after); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "Tessa event cursor is invalid.")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream_unsupported", "Tessa streaming is unavailable.")
		return
	}
	if err := disableResponseWriteDeadline(w); err != nil {
		writeError(w, http.StatusInternalServerError, "stream_deadline_failed", "Tessa streaming is unavailable.")
		return
	}
	releaseBudget, budgetErr := h.streamBudget.Acquire(inboxRemoteIP(r), "")
	if errors.Is(budgetErr, ErrStreamBudgetExceeded) {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many realtime streams are open.")
		return
	}
	defer releaseBudget()
	signals, unsubscribe, err := h.tessaEvents.Subscribe(clientID)
	if errors.Is(err, ErrTessaStreamLimit) {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many Tessa streams are open.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "tessa_stream_unavailable", "Tessa updates are unavailable.")
		return
	}
	defer unsubscribe()
	if h.operationalMetrics != nil {
		defer h.operationalMetrics.SSEConnectionOpened("tessa")()
	}
	initial, err := h.repo.ListTessaEventsAfter(r.Context(), clientID, after, tessaEventDrainLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tessa_stream_unavailable", "Tessa updates are unavailable.")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Referrer-Policy", "no-referrer")
	cursor := after
	sendReset := func(reason, resetCursor string) bool {
		payload, _ := json.Marshal(TessaRealtimeReset{Cursor: resetCursor, Reason: reason})
		return writeInboxSSE(w, flusher, "reset", resetCursor, payload)
	}
	sendDrain := func(drain TessaEventDrain) bool {
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
		lastSent := cursor
		for _, event := range drain.Events {
			payload, marshalErr := json.Marshal(event)
			if marshalErr != nil || !writeInboxSSE(w, flusher, "tessa", event.Cursor, payload) {
				return false
			}
			lastSent = event.Cursor
			if h.operationalMetrics != nil {
				h.operationalMetrics.ObserveSSEEventLag("tessa", event.CreatedAt)
			}
		}
		if drain.Cursor != "" && drain.Cursor != lastSent && !writeInboxSSE(w, flusher, "cursor", drain.Cursor, []byte(`{}`)) {
			return false
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
			if signal == TessaBrokerReset {
				sendReset("broker_overflow", cursor)
				return
			}
			drain, drainErr := h.repo.ListTessaEventsAfter(r.Context(), clientID, cursor, tessaEventDrainLimit)
			if drainErr != nil || !sendDrain(drain) {
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
