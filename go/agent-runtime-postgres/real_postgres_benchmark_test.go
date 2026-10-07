package postgres

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func BenchmarkRealPostgresKernelLoad10KHistory(b *testing.B) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		b.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, _ := openIsolatedRealPostgres(b, dsn)
	if err := Migrate(db); err != nil {
		b.Fatal(err)
	}
	store := NewKernelStore(db)
	now := realPostgresBenchmarkClock{}.Now()
	runID := "benchmark_real_pg_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	record := kernel.Record{
		Run: kernel.Run{
			ID: runID, Kind: kernel.RunKind("benchmark"),
			Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "benchmark", ID: "thread"},
			Goal:   "benchmark real postgres load", Status: kernel.RunStatusRunning, Revision: 1,
			CreatedAt: now, UpdatedAt: now,
		},
		State: json.RawMessage(`{"hot":true}`),
	}
	if _, err := store.Create(context.Background(), record, nil); err != nil {
		b.Fatal(err)
	}
	const eventCount = 10_000
	events := make([]models.KernelEventRecord, eventCount)
	for index := range events {
		events[index] = models.KernelEventRecord{
			RunID: runID, Seq: int64(index + 1), Type: "benchmark.event", CreatedAt: now,
		}
	}
	if err := db.CreateInBatches(events, 1_000).Error; err != nil {
		b.Fatal(err)
	}
	if err := db.Model(&models.KernelRunRecord{}).Where("run_id = ?", runID).
		Update("last_event_seq", eventCount).Error; err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		snapshot, err := store.Load(context.Background(), runID)
		if err != nil || snapshot.EventHead != eventCount {
			b.Fatalf("eventHead=%d err=%v", snapshot.EventHead, err)
		}
	}
}

func BenchmarkRealPostgresKernelApplyCAS(b *testing.B) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		b.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, _ := openIsolatedRealPostgres(b, dsn)
	if err := Migrate(db); err != nil {
		b.Fatal(err)
	}
	runtime, err := kernel.New(kernel.Dependencies{
		Store: NewKernelStore(db), Clock: realPostgresBenchmarkClock{},
	})
	if err != nil {
		b.Fatal(err)
	}
	runID := "benchmark_real_pg_apply_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	current, err := runtime.Create(context.Background(), kernel.CreateRequest{
		ID: runID, Kind: kernel.RunKind("benchmark"),
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "benchmark", ID: "thread"},
		Goal:   "benchmark real postgres apply", State: json.RawMessage(`{"step":0}`),
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		current, err = runtime.Apply(context.Background(), runID, current.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: json.RawMessage(`{"step":1}`),
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

type realPostgresBenchmarkClock struct{}

func (realPostgresBenchmarkClock) Now() time.Time {
	return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
}
