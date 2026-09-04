package appdata

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type InboxMetrics struct {
	commandRateLimited        atomic.Uint64
	sendAttempts              atomic.Uint64
	sendFailures              atomic.Uint64
	sendReplays               atomic.Uint64
	sendLatencyNanos          atomic.Uint64
	sendLatencyMax            atomic.Uint64
	unreadRequests            atomic.Uint64
	unreadFailures            atomic.Uint64
	unreadLatencyNanos        atomic.Uint64
	streamsCurrent            atomic.Int64
	streamsOpened             atomic.Uint64
	streamsRejected           atomic.Uint64
	streamFailures            atomic.Uint64
	streamResets              atomic.Uint64
	eventsDelivered           atomic.Uint64
	eventLagNanos             atomic.Uint64
	eventLagMax               atomic.Uint64
	reservationExpiryAttempts atomic.Uint64
	reservationsExpired       atomic.Uint64
	reservationExpiryDeferred atomic.Uint64
	reservationExpiryFailures atomic.Uint64
}

type InboxMetricsSnapshot struct {
	CommandRateLimited        uint64
	SendAttempts              uint64
	SendFailures              uint64
	SendReplays               uint64
	SendLatency               time.Duration
	SendLatencyMax            time.Duration
	UnreadRequests            uint64
	UnreadFailures            uint64
	UnreadLatency             time.Duration
	StreamsCurrent            int64
	StreamsOpened             uint64
	StreamsRejected           uint64
	StreamFailures            uint64
	StreamResets              uint64
	EventsDelivered           uint64
	EventLag                  time.Duration
	EventLagMax               time.Duration
	ReservationExpiryAttempts uint64
	ReservationsExpired       uint64
	ReservationExpiryDeferred uint64
	ReservationExpiryFailures uint64
}

func NewInboxMetrics() *InboxMetrics { return &InboxMetrics{} }

func (metrics *InboxMetrics) ObserveSend(duration time.Duration, err error, replayed bool) {
	if metrics == nil {
		return
	}
	metrics.sendAttempts.Add(1)
	metrics.sendLatencyNanos.Add(uint64(max(duration, 0)))
	updateAtomicMaximum(&metrics.sendLatencyMax, uint64(max(duration, 0)))
	if err != nil {
		metrics.sendFailures.Add(1)
	}
	if replayed {
		metrics.sendReplays.Add(1)
	}
}

func (metrics *InboxMetrics) ObserveUnread(duration time.Duration, err error) {
	if metrics == nil {
		return
	}
	metrics.unreadRequests.Add(1)
	metrics.unreadLatencyNanos.Add(uint64(max(duration, 0)))
	if err != nil {
		metrics.unreadFailures.Add(1)
	}
}

func (metrics *InboxMetrics) StreamOpened() {
	if metrics == nil {
		return
	}
	metrics.streamsCurrent.Add(1)
	metrics.streamsOpened.Add(1)
}

func (metrics *InboxMetrics) StreamClosed() {
	if metrics != nil {
		metrics.streamsCurrent.Add(-1)
	}
}

func (metrics *InboxMetrics) EventDelivered(createdAt time.Time) {
	if metrics == nil {
		return
	}
	metrics.eventsDelivered.Add(1)
	lag := time.Since(createdAt)
	if lag < 0 {
		lag = 0
	}
	metrics.eventLagNanos.Add(uint64(lag))
	updateAtomicMaximum(&metrics.eventLagMax, uint64(lag))
}

func (metrics *InboxMetrics) ObserveReservationExpiry(expired, deferred bool, err error) {
	if metrics == nil {
		return
	}
	metrics.reservationExpiryAttempts.Add(1)
	if expired {
		metrics.reservationsExpired.Add(1)
	}
	if deferred {
		metrics.reservationExpiryDeferred.Add(1)
	}
	if err != nil {
		metrics.reservationExpiryFailures.Add(1)
	}
}

func updateAtomicMaximum(target *atomic.Uint64, candidate uint64) {
	for current := target.Load(); candidate > current; current = target.Load() {
		if target.CompareAndSwap(current, candidate) {
			return
		}
	}
}

func (metrics *InboxMetrics) Snapshot() InboxMetricsSnapshot {
	if metrics == nil {
		return InboxMetricsSnapshot{}
	}
	return InboxMetricsSnapshot{
		CommandRateLimited: metrics.commandRateLimited.Load(),
		SendAttempts:       metrics.sendAttempts.Load(), SendFailures: metrics.sendFailures.Load(),
		SendReplays:    metrics.sendReplays.Load(),
		SendLatency:    time.Duration(metrics.sendLatencyNanos.Load()),
		SendLatencyMax: time.Duration(metrics.sendLatencyMax.Load()),
		UnreadRequests: metrics.unreadRequests.Load(), UnreadFailures: metrics.unreadFailures.Load(),
		UnreadLatency:  time.Duration(metrics.unreadLatencyNanos.Load()),
		StreamsCurrent: metrics.streamsCurrent.Load(), StreamsOpened: metrics.streamsOpened.Load(),
		StreamsRejected: metrics.streamsRejected.Load(), StreamFailures: metrics.streamFailures.Load(),
		StreamResets: metrics.streamResets.Load(), EventsDelivered: metrics.eventsDelivered.Load(),
		EventLag:                  time.Duration(metrics.eventLagNanos.Load()),
		EventLagMax:               time.Duration(metrics.eventLagMax.Load()),
		ReservationExpiryAttempts: metrics.reservationExpiryAttempts.Load(),
		ReservationsExpired:       metrics.reservationsExpired.Load(),
		ReservationExpiryDeferred: metrics.reservationExpiryDeferred.Load(),
		ReservationExpiryFailures: metrics.reservationExpiryFailures.Load(),
	}
}

type InboxTelemetryWorker struct {
	db       *pgxpool.Pool
	metrics  *InboxMetrics
	logger   *slog.Logger
	interval time.Duration
}

func NewInboxTelemetryWorker(
	db *pgxpool.Pool,
	metrics *InboxMetrics,
	logger *slog.Logger,
) *InboxTelemetryWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &InboxTelemetryWorker{
		db: db, metrics: metrics, logger: logger, interval: time.Minute,
	}
}

func (worker *InboxTelemetryWorker) Start(ctx context.Context) {
	if worker == nil || worker.db == nil || worker.metrics == nil {
		return
	}
	ticker := time.NewTicker(worker.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			worker.logSnapshot()
		}
	}
}

func (worker *InboxTelemetryWorker) logSnapshot() {
	metrics := worker.metrics.Snapshot()
	pool := worker.db.Stat()
	worker.logger.Info(
		"inbox telemetry",
		"pool_acquired", pool.AcquiredConns(), "pool_idle", pool.IdleConns(),
		"pool_total", pool.TotalConns(), "pool_max", pool.MaxConns(),
		"pool_acquire_count", pool.AcquireCount(),
		"pool_acquire_duration", pool.AcquireDuration(),
		"pool_empty_acquire_count", pool.EmptyAcquireCount(),
		"pool_canceled_acquire_count", pool.CanceledAcquireCount(),
		"commands_rate_limited", metrics.CommandRateLimited,
		"send_attempts", metrics.SendAttempts, "send_failures", metrics.SendFailures,
		"send_replays", metrics.SendReplays,
		"send_latency_average", averageDuration(metrics.SendLatency, metrics.SendAttempts),
		"send_latency_max", metrics.SendLatencyMax,
		"unread_requests", metrics.UnreadRequests, "unread_failures", metrics.UnreadFailures,
		"unread_latency_average", averageDuration(metrics.UnreadLatency, metrics.UnreadRequests),
		"streams_current", metrics.StreamsCurrent, "streams_opened", metrics.StreamsOpened,
		"streams_rejected", metrics.StreamsRejected, "stream_failures", metrics.StreamFailures,
		"stream_resets", metrics.StreamResets, "events_delivered", metrics.EventsDelivered,
		"event_lag_average", averageDuration(metrics.EventLag, metrics.EventsDelivered),
		"event_lag_max", metrics.EventLagMax,
		"reservation_expiry_attempts", metrics.ReservationExpiryAttempts,
		"reservations_expired", metrics.ReservationsExpired,
		"reservation_expiry_deferred", metrics.ReservationExpiryDeferred,
		"reservation_expiry_failures", metrics.ReservationExpiryFailures,
	)
}

func averageDuration(total time.Duration, count uint64) time.Duration {
	if count == 0 {
		return 0
	}
	return total / time.Duration(count)
}
