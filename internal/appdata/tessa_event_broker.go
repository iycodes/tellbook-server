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
	tessaBrokerSignalBuffer  = 16
	maxTessaStreamsPerClient = 3
)

var ErrTessaStreamLimit = errors.New("Tessa stream connection limit reached")

type TessaBrokerSignal uint8

const (
	TessaBrokerWake TessaBrokerSignal = iota + 1
	TessaBrokerReset
)

type TessaEventBroker struct {
	db          *pgxpool.Pool
	logger      *slog.Logger
	mu          sync.RWMutex
	subscribers map[uuid.UUID]map[chan TessaBrokerSignal]struct{}
}

func NewTessaEventBroker(db *pgxpool.Pool, logger *slog.Logger) *TessaEventBroker {
	if logger == nil {
		logger = slog.Default()
	}
	return &TessaEventBroker{db: db, logger: logger, subscribers: make(map[uuid.UUID]map[chan TessaBrokerSignal]struct{})}
}

func (broker *TessaEventBroker) Subscribe(clientID uuid.UUID) (<-chan TessaBrokerSignal, func(), error) {
	if broker == nil || clientID == uuid.Nil {
		return nil, nil, ErrTessaStreamLimit
	}
	signals := make(chan TessaBrokerSignal, tessaBrokerSignalBuffer)
	broker.mu.Lock()
	if len(broker.subscribers[clientID]) >= maxTessaStreamsPerClient {
		broker.mu.Unlock()
		return nil, nil, ErrTessaStreamLimit
	}
	if broker.subscribers[clientID] == nil {
		broker.subscribers[clientID] = make(map[chan TessaBrokerSignal]struct{})
	}
	broker.subscribers[clientID][signals] = struct{}{}
	broker.mu.Unlock()
	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			broker.mu.Lock()
			delete(broker.subscribers[clientID], signals)
			if len(broker.subscribers[clientID]) == 0 {
				delete(broker.subscribers, clientID)
			}
			broker.mu.Unlock()
		})
	}
	return signals, unsubscribe, nil
}

func (broker *TessaEventBroker) Start(ctx context.Context) {
	if broker == nil || broker.db == nil {
		return
	}
	for ctx.Err() == nil {
		if err := broker.listen(ctx); err != nil && ctx.Err() == nil {
			broker.logger.Warn("Tessa event listener disconnected", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func (broker *TessaEventBroker) listen(ctx context.Context) error {
	connection, err := broker.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, "LISTEN "+tessaEventChannel); err != nil {
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

func (broker *TessaEventBroker) publishAll() {
	broker.mu.RLock()
	defer broker.mu.RUnlock()
	for _, subscribers := range broker.subscribers {
		for signals := range subscribers {
			select {
			case signals <- TessaBrokerWake:
			default:
			}
		}
	}
}

func (broker *TessaEventBroker) publish(clientID uuid.UUID) {
	broker.mu.RLock()
	defer broker.mu.RUnlock()
	for signals := range broker.subscribers[clientID] {
		select {
		case signals <- TessaBrokerWake:
		default:
			for {
				select {
				case <-signals:
				default:
					select {
					case signals <- TessaBrokerReset:
					default:
					}
					goto next
				}
			}
		}
	next:
	}
}
