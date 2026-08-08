package payments

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const paymentStatusChannel = "tellbook_payment_status"

type PaymentEventBroker struct {
	db          *pgxpool.Pool
	logger      *slog.Logger
	mu          sync.RWMutex
	subscribers map[string]map[chan struct{}]struct{}
}

func NewPaymentEventBroker(db *pgxpool.Pool, logger *slog.Logger) *PaymentEventBroker {
	if logger == nil {
		logger = slog.Default()
	}
	return &PaymentEventBroker{db: db, logger: logger, subscribers: make(map[string]map[chan struct{}]struct{})}
}

func (b *PaymentEventBroker) Subscribe(paymentToken string) (<-chan struct{}, func()) {
	paymentToken = strings.TrimSpace(paymentToken)
	notifications := make(chan struct{}, 1)
	b.mu.Lock()
	if b.subscribers[paymentToken] == nil {
		b.subscribers[paymentToken] = make(map[chan struct{}]struct{})
	}
	b.subscribers[paymentToken][notifications] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	return notifications, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subscribers[paymentToken], notifications)
			if len(b.subscribers[paymentToken]) == 0 {
				delete(b.subscribers, paymentToken)
			}
			b.mu.Unlock()
		})
	}
}

func (b *PaymentEventBroker) Start(ctx context.Context) {
	if b == nil || b.db == nil {
		return
	}
	for ctx.Err() == nil {
		if err := b.listen(ctx); err != nil && ctx.Err() == nil {
			b.logger.Warn("payment event listener disconnected", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func (b *PaymentEventBroker) listen(ctx context.Context) error {
	connection, err := b.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, "LISTEN "+paymentStatusChannel); err != nil {
		return err
	}
	for {
		notification, err := connection.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		b.publish(notification.Payload)
	}
}

func (b *PaymentEventBroker) publish(paymentToken string) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for subscriber := range b.subscribers[paymentToken] {
		select {
		case subscriber <- struct{}{}:
		default:
		}
	}
}
