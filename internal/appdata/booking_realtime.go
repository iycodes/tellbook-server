package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"booking/go-server/internal/auth"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const bookingEventDrainLimit = 100

type BookingRealtimeEvent struct {
	Cursor    string          `json:"cursor"`
	Type      string          `json:"type"`
	BookingID string          `json:"booking_id"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type BookingEventDrain struct {
	Events       []BookingRealtimeEvent
	Cursor       string
	LatestCursor string
	HasMore      bool
	Reset        bool
}

type BookingRealtimeReset struct {
	Cursor string `json:"cursor"`
	Reason string `json:"reason"`
}

func (repo *Repository) ListBookingEventsAfter(
	ctx context.Context,
	clientID uuid.UUID,
	after int64,
	limit int,
) (BookingEventDrain, error) {
	if repo == nil || clientID == uuid.Nil || after < 0 || limit < 1 || limit > bookingEventDrainLimit {
		return BookingEventDrain{}, errors.New("invalid booking event query")
	}
	tx, err := repo.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return BookingEventDrain{}, fmt.Errorf("begin booking event drain: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var latest int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(sequence),0) FROM booking_domain_events WHERE client_id=$1
	`, clientID).Scan(&latest); err != nil {
		return BookingEventDrain{}, fmt.Errorf("load booking event high-water cursor: %w", err)
	}
	if after > latest {
		if err := tx.Commit(ctx); err != nil {
			return BookingEventDrain{}, fmt.Errorf("commit booking event reset: %w", err)
		}
		cursor := strconv.FormatInt(latest, 10)
		return BookingEventDrain{Events: []BookingRealtimeEvent{}, Cursor: cursor, LatestCursor: cursor, Reset: true}, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT sequence, event_type, booking_id, payload, created_at
		FROM booking_domain_events
		WHERE client_id = $1 AND sequence > $2
		ORDER BY sequence
		LIMIT $3
	`, clientID, after, limit+1)
	if err != nil {
		return BookingEventDrain{}, fmt.Errorf("list booking events: %w", err)
	}
	defer rows.Close()
	events := make([]BookingRealtimeEvent, 0, limit+1)
	for rows.Next() {
		var sequence int64
		var bookingID uuid.UUID
		var event BookingRealtimeEvent
		if err := rows.Scan(&sequence, &event.Type, &bookingID, &event.Payload, &event.CreatedAt); err != nil {
			return BookingEventDrain{}, fmt.Errorf("scan booking event: %w", err)
		}
		event.Cursor = strconv.FormatInt(sequence, 10)
		event.BookingID = bookingID.String()
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return BookingEventDrain{}, fmt.Errorf("iterate booking events: %w", err)
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	cursor := strconv.FormatInt(after, 10)
	if len(events) > 0 {
		cursor = events[len(events)-1].Cursor
	}
	if err := tx.Commit(ctx); err != nil {
		return BookingEventDrain{}, fmt.Errorf("commit booking event drain: %w", err)
	}
	return BookingEventDrain{
		Events: events, Cursor: cursor, LatestCursor: strconv.FormatInt(latest, 10), HasMore: hasMore,
	}, nil
}

func (h *Handler) streamProviderBookingEvents(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if h.repo == nil || h.bookingEvents == nil {
		writeError(w, http.StatusServiceUnavailable, "booking_stream_unavailable", "Booking updates are unavailable.")
		return
	}
	afterRaw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if afterRaw == "" {
		afterRaw = strings.TrimSpace(r.URL.Query().Get("after"))
	}
	after, err := strconv.ParseInt(afterRaw, 10, 64)
	if err != nil || after < 0 {
		writeError(w, http.StatusBadRequest, "invalid_booking_cursor", "Booking event cursor is invalid.")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream_unsupported", "Booking streaming is unavailable.")
		return
	}
	if err := disableResponseWriteDeadline(w); err != nil {
		writeError(w, http.StatusInternalServerError, "stream_deadline_failed", "Booking streaming is unavailable.")
		return
	}
	releaseBudget, budgetErr := h.streamBudget.Acquire(inboxRemoteIP(r), "")
	if errors.Is(budgetErr, ErrStreamBudgetExceeded) {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many realtime streams are open.")
		return
	}
	defer releaseBudget()
	signals, unsubscribe, err := h.bookingEvents.Subscribe(client.ID)
	if errors.Is(err, ErrBookingStreamLimit) {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many booking streams are open.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "booking_stream_unavailable", "Booking updates are unavailable.")
		return
	}
	defer unsubscribe()
	if h.operationalMetrics != nil {
		defer h.operationalMetrics.SSEConnectionOpened("booking_provider")()
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Referrer-Policy", "no-referrer")
	cursor := after
	sendReset := func(reason, resetCursor string) bool {
		payload, marshalErr := json.Marshal(BookingRealtimeReset{Cursor: resetCursor, Reason: reason})
		return marshalErr == nil && writeInboxSSE(w, flusher, "reset", resetCursor, payload)
	}
	drain := func() bool {
		page, drainErr := h.repo.ListBookingEventsAfter(r.Context(), client.ID, cursor, bookingEventDrainLimit)
		if drainErr != nil {
			return false
		}
		if page.Reset {
			sendReset("cursor_expired", page.Cursor)
			return false
		}
		if page.HasMore {
			sendReset("catchup_limit", page.LatestCursor)
			return false
		}
		for _, event := range page.Events {
			payload, marshalErr := json.Marshal(event)
			if marshalErr != nil || !writeInboxSSE(w, flusher, "booking", event.Cursor, payload) {
				return false
			}
			cursor, _ = strconv.ParseInt(event.Cursor, 10, 64)
			if h.operationalMetrics != nil {
				h.operationalMetrics.ObserveSSEEventLag("booking_provider", event.CreatedAt)
			}
		}
		return true
	}
	if !drain() {
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
			if signal == BookingBrokerReset || !drain() {
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
