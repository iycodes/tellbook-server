package appdata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	inboxEventChannel          = "tellbook_inbox_events"
	inboxBrokerSignalBuffer    = 16
	maxInboxStreamsPerActor    = 3
	maxInboxStreamsPerRemoteIP = 20
)

var ErrInboxStreamLimit = errors.New("inbox stream connection limit reached")

type InboxBrokerSignal uint8

const (
	InboxBrokerWake InboxBrokerSignal = iota + 1
	InboxBrokerReset
)

type inboxEventSubscriptionKey struct {
	ActorType string
	ActorID   uuid.UUID
}

type inboxEventSubscription struct {
	signals chan InboxBrokerSignal
	remote  string
}

type InboxEventBroker struct {
	db          *pgxpool.Pool
	logger      *slog.Logger
	mu          sync.RWMutex
	subscribers map[inboxEventSubscriptionKey]map[*inboxEventSubscription]struct{}
	remoteCount map[string]int
}

func NewInboxEventBroker(db *pgxpool.Pool, logger *slog.Logger) *InboxEventBroker {
	if logger == nil {
		logger = slog.Default()
	}
	return &InboxEventBroker{
		db:          db,
		logger:      logger,
		subscribers: make(map[inboxEventSubscriptionKey]map[*inboxEventSubscription]struct{}),
		remoteCount: make(map[string]int),
	}
}

func (b *InboxEventBroker) Subscribe(
	actorType string,
	actorID uuid.UUID,
	remoteIP string,
) (<-chan InboxBrokerSignal, func(), error) {
	if b == nil || (actorType != "provider" && actorType != "marketplace_customer") || actorID == uuid.Nil {
		return nil, nil, ErrInboxStreamLimit
	}
	key := inboxEventSubscriptionKey{ActorType: actorType, ActorID: actorID}
	remoteIP = strings.TrimSpace(remoteIP)
	subscription := &inboxEventSubscription{
		signals: make(chan InboxBrokerSignal, inboxBrokerSignalBuffer),
		remote:  remoteIP,
	}

	b.mu.Lock()
	if len(b.subscribers[key]) >= maxInboxStreamsPerActor ||
		(remoteIP != "" && b.remoteCount[remoteIP] >= maxInboxStreamsPerRemoteIP) {
		b.mu.Unlock()
		return nil, nil, ErrInboxStreamLimit
	}
	if b.subscribers[key] == nil {
		b.subscribers[key] = make(map[*inboxEventSubscription]struct{})
	}
	b.subscribers[key][subscription] = struct{}{}
	if remoteIP != "" {
		b.remoteCount[remoteIP]++
	}
	b.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subscribers[key], subscription)
			if len(b.subscribers[key]) == 0 {
				delete(b.subscribers, key)
			}
			if subscription.remote != "" {
				b.remoteCount[subscription.remote]--
				if b.remoteCount[subscription.remote] <= 0 {
					delete(b.remoteCount, subscription.remote)
				}
			}
			b.mu.Unlock()
		})
	}
	return subscription.signals, unsubscribe, nil
}

func (b *InboxEventBroker) Start(ctx context.Context) {
	if b == nil || b.db == nil {
		return
	}
	for ctx.Err() == nil {
		if err := b.listen(ctx); err != nil && ctx.Err() == nil {
			b.logger.Warn("inbox event listener disconnected", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func (b *InboxEventBroker) listen(ctx context.Context) error {
	connection, err := b.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, "LISTEN "+inboxEventChannel); err != nil {
		return err
	}
	b.publishAll()
	for {
		notification, err := connection.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		clientID, marketplaceCustomerID, ok := parseInboxNotificationPayload(notification.Payload)
		if !ok {
			b.logger.Warn("ignored malformed inbox notification")
			continue
		}
		b.publish(inboxEventSubscriptionKey{ActorType: "provider", ActorID: clientID})
		b.publish(inboxEventSubscriptionKey{
			ActorType: "marketplace_customer", ActorID: marketplaceCustomerID,
		})
	}
}

func (b *InboxEventBroker) publishAll() {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, subscriptions := range b.subscribers {
		for subscription := range subscriptions {
			select {
			case subscription.signals <- InboxBrokerWake:
			default:
			}
		}
	}
}

func (b *InboxEventBroker) publish(key inboxEventSubscriptionKey) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for subscription := range b.subscribers[key] {
		select {
		case subscription.signals <- InboxBrokerWake:
		default:
			for {
				select {
				case <-subscription.signals:
				default:
					select {
					case subscription.signals <- InboxBrokerReset:
					default:
					}
					goto nextSubscription
				}
			}
		}
	nextSubscription:
	}
}

func inboxNotificationPayload(sequence int64, clientID, marketplaceCustomerID uuid.UUID) string {
	return fmt.Sprintf("%d|%s|%s", sequence, clientID, marketplaceCustomerID)
}

func parseInboxNotificationPayload(payload string) (uuid.UUID, uuid.UUID, bool) {
	parts := strings.Split(payload, "|")
	if len(parts) != 3 {
		return uuid.Nil, uuid.Nil, false
	}
	sequence, sequenceErr := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	clientID, clientErr := uuid.Parse(parts[1])
	marketplaceCustomerID, customerErr := uuid.Parse(parts[2])
	return clientID, marketplaceCustomerID, sequenceErr == nil && sequence > 0 && clientErr == nil && customerErr == nil
}

type inboxEventNotifier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func notifyInboxEvent(
	ctx context.Context,
	execer inboxEventNotifier,
	sequence int64,
	clientID, marketplaceCustomerID uuid.UUID,
) error {
	_, err := execer.Exec(
		ctx,
		`SELECT pg_notify('tellbook_inbox_events',$1)`,
		inboxNotificationPayload(sequence, clientID, marketplaceCustomerID),
	)
	return err
}
