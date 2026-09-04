package appdata

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// InboxAIGenerationLimiter bounds inference inside one AI worker process. The
// AI-worker deployment replica/concurrency budget is the cluster admission
// control; inference never holds a PostgreSQL connection.
type InboxAIGenerationLimiter struct {
	slots chan struct{}
}

type InboxAIGenerationSlot struct {
	limiter *InboxAIGenerationLimiter
	once    sync.Once
}

func NewInboxAIGenerationLimiter(maxConcurrency int, pools ...*pgxpool.Pool) *InboxAIGenerationLimiter {
	if maxConcurrency < 1 {
		maxConcurrency = 1
	}
	_ = pools
	return &InboxAIGenerationLimiter{slots: make(chan struct{}, maxConcurrency)}
}

// TryAcquire returns nil without an error when all inference slots are busy.
func (limiter *InboxAIGenerationLimiter) TryAcquire(ctx context.Context) (*InboxAIGenerationSlot, error) {
	if limiter == nil {
		return nil, nil
	}
	select {
	case limiter.slots <- struct{}{}:
	default:
		return nil, nil
	}

	return &InboxAIGenerationSlot{limiter: limiter}, nil
}

// Acquire waits without holding a database connection. Durable workers use it
// before leasing a job so local capacity pressure cannot consume job attempts.
func (limiter *InboxAIGenerationLimiter) Acquire(ctx context.Context) (*InboxAIGenerationSlot, error) {
	if limiter == nil {
		return nil, nil
	}
	select {
	case limiter.slots <- struct{}{}:
		return &InboxAIGenerationSlot{limiter: limiter}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (slot *InboxAIGenerationSlot) Release() {
	if slot == nil || slot.limiter == nil {
		return
	}
	slot.once.Do(func() {
		<-slot.limiter.slots
	})
}
