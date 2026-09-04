package appdata

import (
	"context"
	"testing"
)

func TestInboxAIGenerationSlotsRejectExcessConcurrency(t *testing.T) {
	limiter := NewInboxAIGenerationLimiter(1)
	first, err := limiter.TryAcquire(context.Background())
	if err != nil || first == nil {
		t.Fatal("first AI generation slot was rejected")
	}
	second, err := limiter.TryAcquire(context.Background())
	if err != nil || second != nil {
		t.Fatal("AI generation exceeded configured concurrency")
	}
	first.Release()
	third, err := limiter.TryAcquire(context.Background())
	if err != nil || third == nil {
		t.Fatal("released AI generation slot was not reusable")
	}
	third.Release()
}
