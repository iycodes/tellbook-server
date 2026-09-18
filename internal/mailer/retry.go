package mailer

import (
	"github.com/google/uuid"
	"time"
)

// RetryAt is the existing booking email retry policy, shared by financial notices.
func RetryAt(now time.Time, deliveryID uuid.UUID, attempt int) time.Time {
	shift := min(max(attempt-1, 0), 7)
	delay := 15 * time.Second * time.Duration(1<<shift)
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	// Stable per-delivery jitter avoids synchronized retries without shared RNG contention.
	jitterRange := max(delay/5, time.Millisecond)
	jitter := time.Duration(int64(deliveryID[0])<<8|int64(deliveryID[1])) % jitterRange
	return now.Add(delay + jitter)
}
