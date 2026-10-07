package kernel_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func BenchmarkRuntimeCreateMemory(b *testing.B) {
	request := benchmarkCreateRequest("benchmark-run")
	b.ReportAllocs()
	for range b.N {
		b.StopTimer()
		runtime := benchmarkRuntime(b)
		b.StartTimer()
		if _, err := runtime.Create(context.Background(), request); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRuntimeLoadMemory(b *testing.B) {
	runtime := benchmarkRuntime(b)
	if _, err := runtime.Create(context.Background(), benchmarkCreateRequest("benchmark-run")); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := runtime.Load(context.Background(), "benchmark-run"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRuntimeApplyCASMemory(b *testing.B) {
	runtime := benchmarkRuntime(b)
	current, err := runtime.Create(context.Background(), benchmarkCreateRequest("benchmark-run"))
	if err != nil {
		b.Fatal(err)
	}
	mutation := kernel.Mutation{
		Status: kernel.RunStatusRunning,
		State:  json.RawMessage(`{"phase":"running"}`),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		current, err = runtime.Apply(context.Background(), current.Run.ID, current.Run.Revision, mutation)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRuntimeListEventsMemory(b *testing.B) {
	runtime := benchmarkRuntime(b)
	current, err := runtime.Create(context.Background(), benchmarkCreateRequest("benchmark-run"))
	if err != nil {
		b.Fatal(err)
	}
	for range 1_000 {
		current, err = runtime.Apply(context.Background(), current.Run.ID, current.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning,
			State:  json.RawMessage(`{"phase":"running"}`),
			Events: []kernel.EventDraft{{Type: "benchmark.event"}},
		})
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		events, listErr := runtime.ListEvents(context.Background(), current.Run.ID, 400, 100)
		if listErr != nil {
			b.Fatal(listErr)
		}
		if len(events) != 100 {
			b.Fatalf("event count = %d, want 100", len(events))
		}
	}
}

func benchmarkRuntime(b *testing.B) *kernel.Runtime {
	b.Helper()
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(),
		Clock: benchmarkClock{},
	})
	if err != nil {
		b.Fatal(err)
	}
	return runtime
}

func benchmarkCreateRequest(id string) kernel.CreateRequest {
	return kernel.CreateRequest{
		ID: id, Kind: kernel.RunKind("benchmark"),
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "benchmark", ID: "thread"},
		Goal:   "benchmark runtime", State: json.RawMessage(`{"phase":"created"}`),
	}
}

type benchmarkClock struct{}

func (benchmarkClock) Now() time.Time {
	return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
}
