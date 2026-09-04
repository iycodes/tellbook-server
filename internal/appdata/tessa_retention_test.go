package appdata

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type scriptedTessaRetentionExecer struct {
	rows  []int64
	calls int
}

func (execer *scriptedTessaRetentionExecer) Exec(
	_ context.Context,
	_ string,
	_ ...any,
) (pgconn.CommandTag, error) {
	rows := int64(0)
	if execer.calls < len(execer.rows) {
		rows = execer.rows[execer.calls]
	}
	execer.calls++
	return pgconn.NewCommandTag(fmt.Sprintf("DELETE %d", rows)), nil
}

func TestDrainTessaRetentionCatchesUpInBoundedBatches(t *testing.T) {
	execer := &scriptedTessaRetentionExecer{
		rows: []int64{
			tessaRetentionBatch, tessaRetentionBatch,
			tessaRetentionBatch, 10,
			200, 0,
		},
	}
	now := time.Now().UTC()
	events, threads, err := drainTessaRetention(
		context.Background(), execer, now, now, tessaRetentionBatch, tessaRetentionBatchesPerRun,
	)
	if err != nil {
		t.Fatal(err)
	}
	if events != 2*tessaRetentionBatch+200 || threads != tessaRetentionBatch+10 {
		t.Fatalf("deleted events=%d threads=%d", events, threads)
	}
	if execer.calls != 6 {
		t.Fatalf("retention calls=%d, want 6", execer.calls)
	}
}

func TestDrainTessaRetentionHonorsBatchLimit(t *testing.T) {
	execer := &scriptedTessaRetentionExecer{
		rows: []int64{
			tessaRetentionBatch, tessaRetentionBatch,
			tessaRetentionBatch, tessaRetentionBatch,
			tessaRetentionBatch, tessaRetentionBatch,
		},
	}
	now := time.Now().UTC()
	events, threads, err := drainTessaRetention(
		context.Background(), execer, now, now, tessaRetentionBatch, 2,
	)
	if err != nil {
		t.Fatal(err)
	}
	if events != 2*tessaRetentionBatch || threads != 2*tessaRetentionBatch {
		t.Fatalf("deleted events=%d threads=%d", events, threads)
	}
	if execer.calls != 4 {
		t.Fatalf("retention calls=%d, want 4", execer.calls)
	}
}
