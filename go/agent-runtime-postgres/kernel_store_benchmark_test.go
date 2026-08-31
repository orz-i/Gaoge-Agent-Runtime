package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func BenchmarkKernelStoreLoadIndependentOfEventHistory(b *testing.B) {
	for _, eventCount := range []int{10_000, 100_000} {
		b.Run(fmt.Sprintf("events_%d", eventCount), func(b *testing.B) {
			store := benchmarkKernelStoreWithEvents(b, eventCount)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				snapshot, err := store.Load(context.Background(), "benchmark-run")
				if err != nil {
					b.Fatal(err)
				}
				if snapshot.EventHead != int64(eventCount) {
					b.Fatalf("event head = %d, want %d", snapshot.EventHead, eventCount)
				}
			}
		})
	}
}

func benchmarkKernelStoreWithEvents(b *testing.B, eventCount int) *KernelStore {
	b.Helper()
	db := openKernelStoreTestDB(b)
	store := NewKernelStore(db)
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	record := kernel.Record{
		Run: kernel.Run{
			ID: "benchmark-run", Kind: kernel.RunKind("benchmark"),
			Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "benchmark", ID: "thread"},
			Goal:   "benchmark", Status: kernel.RunStatusRunning, Revision: 1,
			CreatedAt: now, UpdatedAt: now,
		},
		State: json.RawMessage(`{}`),
	}
	if _, err := store.Create(context.Background(), record, nil); err != nil {
		b.Fatal(err)
	}
	events := make([]models.KernelEventRecord, eventCount)
	for index := range eventCount {
		events[index] = models.KernelEventRecord{
			RunID: "benchmark-run", Seq: int64(index + 1), Type: "benchmark.event", CreatedAt: now,
		}
	}
	if err := db.CreateInBatches(events, 100).Error; err != nil {
		b.Fatal(err)
	}
	if err := db.Model(&models.KernelRunRecord{}).Where("run_id = ?", "benchmark-run").
		Update("last_event_seq", eventCount).Error; err != nil {
		b.Fatal(err)
	}
	return store
}
