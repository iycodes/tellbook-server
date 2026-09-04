package appdata

import (
	"errors"
	"testing"
	"time"
)

func TestInboxMetricsSnapshotTracksFailuresReplaysAndActiveStreams(t *testing.T) {
	metrics := NewInboxMetrics()
	metrics.ObserveSend(10*time.Millisecond, nil, true)
	metrics.ObserveSend(30*time.Millisecond, errors.New("failed"), false)
	metrics.ObserveUnread(5*time.Millisecond, nil)
	metrics.StreamOpened()
	metrics.StreamOpened()
	metrics.StreamClosed()
	metrics.EventDelivered(time.Now().Add(-20 * time.Millisecond))
	metrics.ObserveReservationExpiry(true, false, nil)
	metrics.ObserveReservationExpiry(false, true, errors.New("provider unavailable"))

	snapshot := metrics.Snapshot()
	if snapshot.SendAttempts != 2 || snapshot.SendFailures != 1 || snapshot.SendReplays != 1 {
		t.Fatalf("send metrics = %+v", snapshot)
	}
	if snapshot.SendLatency != 40*time.Millisecond || snapshot.SendLatencyMax != 30*time.Millisecond {
		t.Fatalf("send latency metrics = %+v", snapshot)
	}
	if snapshot.StreamsCurrent != 1 || snapshot.StreamsOpened != 2 || snapshot.EventsDelivered != 1 {
		t.Fatalf("stream metrics = %+v", snapshot)
	}
	if snapshot.EventLag <= 0 || snapshot.EventLagMax <= 0 {
		t.Fatalf("event lag metrics = %+v", snapshot)
	}
	if snapshot.ReservationExpiryAttempts != 2 || snapshot.ReservationsExpired != 1 ||
		snapshot.ReservationExpiryDeferred != 1 || snapshot.ReservationExpiryFailures != 1 {
		t.Fatalf("reservation expiry metrics = %+v", snapshot)
	}
}
