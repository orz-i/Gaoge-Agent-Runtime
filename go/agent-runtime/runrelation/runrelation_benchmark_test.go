package runrelation_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func BenchmarkRunRelationEnsureMemory(b *testing.B) {
	draft := runrelation.Draft{
		ParentRunID: "parent", ChildRunID: "child",
		Kind: runrelation.KindCapability, OwnerNodeID: "step",
	}
	b.ReportAllocs()
	for range b.N {
		b.StopTimer()
		registry := benchmarkRelationRegistry(b)
		b.StartTimer()
		if _, err := registry.Ensure(context.Background(), draft); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRunRelationGetByChildMemory(b *testing.B) {
	registry := benchmarkRelationRegistry(b)
	if _, err := registry.Ensure(context.Background(), runrelation.Draft{
		ParentRunID: "parent", ChildRunID: "child",
		Kind: runrelation.KindCapability, OwnerNodeID: "step",
	}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := registry.GetByChild(context.Background(), "child"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRunRelationListChildrenMemory(b *testing.B) {
	registry := benchmarkRelationRegistry(b)
	for index := range 100 {
		if _, err := registry.Ensure(context.Background(), runrelation.Draft{
			ParentRunID: "parent", ChildRunID: "child-" + strconv.Itoa(index),
			Kind: runrelation.KindCapability, OwnerNodeID: "step-" + strconv.Itoa(index),
		}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		items, err := registry.ListChildren(context.Background(), "parent")
		if err != nil {
			b.Fatal(err)
		}
		if len(items) != 100 {
			b.Fatalf("children = %d, want 100", len(items))
		}
	}
}

func benchmarkRelationRegistry(b *testing.B) *runrelation.Registry {
	b.Helper()
	registry, err := runrelation.New(memory.NewRunRelationStore(), relationBenchmarkClock{})
	if err != nil {
		b.Fatal(err)
	}
	return registry
}

type relationBenchmarkClock struct{}

func (relationBenchmarkClock) Now() time.Time {
	return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
}
