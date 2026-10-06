package continuation_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func BenchmarkContinuationProjectMemory(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		b.StopTimer()
		scheduler := benchmarkContinuationScheduler(b)
		b.StartTimer()
		if err := scheduler.Project(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkContinuationScheduler(b *testing.B) *continuation.Scheduler {
	b.Helper()
	store := memory.NewStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: store, Clock: continuationBenchmarkClock{}})
	if err != nil {
		b.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), continuationBenchmarkClock{})
	if err != nil {
		b.Fatal(err)
	}
	delivery := queuecore.NewMemory(queuecore.Dependencies{Clock: continuationBenchmarkClock{}})
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: store, Queue: delivery, Relations: relations, Runs: runtime,
		Clock: continuationBenchmarkClock{}, ProjectorID: "benchmark-projector",
	})
	if err != nil {
		b.Fatal(err)
	}
	created, err := runtime.Create(context.Background(), kernel.CreateRequest{
		ID: "benchmark-continuation", Kind: kernel.RunKind("benchmark"),
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "benchmark", ID: "thread"},
		Goal:   "benchmark continuation", State: json.RawMessage(`{"phase":"created"}`),
	})
	if err != nil {
		b.Fatal(err)
	}
	if _, err = runtime.Apply(context.Background(), created.Run.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{"phase":"ready"}`),
		Events: []kernel.EventDraft{{Type: "benchmark.ready", Wakeup: true}},
	}); err != nil {
		b.Fatal(err)
	}
	return scheduler
}

type continuationBenchmarkClock struct{}

func (continuationBenchmarkClock) Now() time.Time {
	return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
}
