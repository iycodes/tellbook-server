package appdata

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	bookingEventChannel        = "tellbook_booking_events"
	bookingBrokerSignalBuffer  = 16
	maxBookingStreamsPerClient = 3
)

var ErrBookingStreamLimit = errors.New("booking stream connection limit reached")

type BookingBrokerSignal uint8

const (
	BookingBrokerWake BookingBrokerSignal = iota + 1
	BookingBrokerReset
)

type BookingEventBroker struct {
	db          *pgxpool.Pool
	logger      *slog.Logger
	mu          sync.RWMutex
	subscribers map[uuid.UUID]map[chan BookingBrokerSignal]struct{}
}

func NewBookingEventBroker(db *pgxpool.Pool, logger *slog.Logger) *BookingEventBroker {
	if logger == nil {
		logger = slog.Default()
	}
	return &BookingEventBroker{
		db: db, logger: logger,
		subscribers: make(map[uuid.UUID]map[chan BookingBrokerSignal]struct{}),
	}
}

func (broker *BookingEventBroker) Subscribe(clientID uuid.UUID) (<-chan BookingBrokerSignal, func(), error) {
	if broker == nil || clientID == uuid.Nil {
		return nil, nil, ErrBookingStreamLimit
	}
	signals := make(chan BookingBrokerSignal, bookingBrokerSignalBuffer)
	broker.mu.Lock()
	if len(broker.subscribers[clientID]) >= maxBookingStreamsPerClient {
		broker.mu.Unlock()
		return nil, nil, ErrBookingStreamLimit
	}
	if broker.subscribers[clientID] == nil {
		broker.subscribers[clientID] = make(map[chan BookingBrokerSignal]struct{})
	}
	broker.subscribers[clientID][signals] = struct{}{}
	broker.mu.Unlock()

	var once sync.Once
	return signals, func() {
		once.Do(func() {
			broker.mu.Lock()
			delete(broker.subscribers[clientID], signals)
			if len(broker.subscribers[clientID]) == 0 {
				delete(broker.subscribers, clientID)
			}
			broker.mu.Unlock()
		})
	}, nil
}

func (broker *BookingEventBroker) Start(ctx context.Context) {
	if broker == nil || broker.db == nil {
		return
	}
	for ctx.Err() == nil {
		if err := broker.listen(ctx); err != nil && ctx.Err() == nil {
			broker.logger.Warn("booking event listener disconnected", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func (broker *BookingEventBroker) listen(ctx context.Context) error {
	connection, err := broker.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, "LISTEN "+bookingEventChannel); err != nil {
		return err
	}
	broker.publishAll()
	for {
		notification, err := connection.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		parts := strings.Split(notification.Payload, "|")
		if len(parts) != 2 {
			continue
		}
		sequence, sequenceErr := strconv.ParseInt(parts[0], 10, 64)
		clientID, clientErr := uuid.Parse(parts[1])
		if sequenceErr != nil || sequence <= 0 || clientErr != nil {
			continue
		}
		broker.publish(clientID)
	}
}

func (broker *BookingEventBroker) publishAll() {
	broker.mu.RLock()
	defer broker.mu.RUnlock()
	for _, subscribers := range broker.subscribers {
		for signals := range subscribers {
			select {
			case signals <- BookingBrokerWake:
			default:
			}
		}
	}
}

func (broker *BookingEventBroker) publish(clientID uuid.UUID) {
	broker.mu.RLock()
	defer broker.mu.RUnlock()
	for signals := range broker.subscribers[clientID] {
		select {
		case signals <- BookingBrokerWake:
		default:
			for {
				select {
				case <-signals:
				default:
					select {
					case signals <- BookingBrokerReset:
					default:
					}
					goto next
				}
			}
		}
	next:
	}
}
