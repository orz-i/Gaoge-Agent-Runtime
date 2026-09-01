package memory_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func BenchmarkStoreLoadIndependentOfEventHistory(b *testing.B) {
	for _, eventCount := range []int{10_000, 100_000} {
		b.Run(fmt.Sprintf("events_%d", eventCount), func(b *testing.B) {
			store := benchmarkStoreWithEvents(b, eventCount)
			b.ResetTimer()
			for range b.N {
				if _, err := store.Load(context.Background(), "benchmark-run"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchmarkStoreWithEvents(b *testing.B, eventCount int) *memory.Store {
	b.Helper()
	store := memory.NewStore()
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
	created, err := store.Create(context.Background(), record, []kernel.EventDraft{{Type: "event"}})
	if err != nil {
		b.Fatal(err)
	}
	current := created
	for index := 1; index < eventCount; index++ {
		record.Run.Revision++
		record.Run.UpdatedAt = now.Add(time.Duration(index) * time.Microsecond)
		current, err = store.Apply(context.Background(), record.Run.ID, current.Run.Revision, kernel.StoreMutation{
			Record: record, Events: []kernel.EventDraft{{Type: "event"}},
		})
		if err != nil {
			b.Fatal(err)
		}
	}
	if current.EventHead != int64(eventCount) {
		b.Fatalf("event head = %d, want %d", current.EventHead, eventCount)
	}
	return store
}
