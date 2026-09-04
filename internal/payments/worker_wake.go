package payments

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const coreWorkerWakeChannel = "tellbook_worker_core"
const aiWorkerWakeChannel = "tellbook_worker_ai"

// CoreWorkerWakeBroker turns PostgreSQL notifications into coalesced local
// wake-ups. Leases and SKIP LOCKED remain the correctness mechanism; the
// periodic worker timer is only recovery for a lost notification.
type CoreWorkerWakeBroker struct {
	db      *pgxpool.Pool
	logger  *slog.Logger
	channel string

	mu          sync.RWMutex
	subscribers map[chan struct{}]struct{}
}

func NewCoreWorkerWakeBroker(db *pgxpool.Pool, logger *slog.Logger) *CoreWorkerWakeBroker {
	if logger == nil {
		logger = slog.Default()
	}
	return &CoreWorkerWakeBroker{
		db: db, logger: logger, channel: coreWorkerWakeChannel,
		subscribers: make(map[chan struct{}]struct{}),
	}
}

func NewAIWorkerWakeBroker(db *pgxpool.Pool, logger *slog.Logger) *CoreWorkerWakeBroker {
	if logger == nil {
		logger = slog.Default()
	}
	return &CoreWorkerWakeBroker{
		db: db, logger: logger, channel: aiWorkerWakeChannel,
		subscribers: make(map[chan struct{}]struct{}),
	}
}

func (broker *CoreWorkerWakeBroker) Subscribe() (<-chan struct{}, func()) {
	if broker == nil {
		return nil, func() {}
	}
	wake := make(chan struct{}, 1)
	broker.mu.Lock()
	broker.subscribers[wake] = struct{}{}
	broker.mu.Unlock()
	var once sync.Once
	return wake, func() {
		once.Do(func() {
			broker.mu.Lock()
			delete(broker.subscribers, wake)
			broker.mu.Unlock()
		})
	}
}

func (broker *CoreWorkerWakeBroker) Start(ctx context.Context) {
	if broker == nil || broker.db == nil {
		return
	}
	for ctx.Err() == nil {
		if err := broker.listen(ctx); err != nil && ctx.Err() == nil {
			broker.logger.Warn("core worker wake listener disconnected", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func (broker *CoreWorkerWakeBroker) listen(ctx context.Context) error {
	connection, err := broker.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, "LISTEN "+broker.channel); err != nil {
		return err
	}
	for {
		if _, err := connection.Conn().WaitForNotification(ctx); err != nil {
			return err
		}
		broker.mu.RLock()
		for wake := range broker.subscribers {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
		broker.mu.RUnlock()
	}
}

func firstWorkerWake(wakes []<-chan struct{}) <-chan struct{} {
	if len(wakes) == 0 {
		return nil
	}
	return wakes[0]
}
