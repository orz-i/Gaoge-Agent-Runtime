package harness_test

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func BenchmarkHarnessStartDirectAgentMemory(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		b.StopTimer()
		runner := benchmarkHarnessRunner(b)
		request := benchmarkHarnessStartRequest()
		b.StartTimer()
		snapshot, err := runner.Start(context.Background(), request)
		if err != nil || snapshot.Turn.Status != harness.TurnCompleted {
			b.Fatalf("turn=%#v err=%v", snapshot.Turn, err)
		}
	}
}

func BenchmarkHarnessLoadCompletedTurnMemory(b *testing.B) {
	runner := benchmarkHarnessRunner(b)
	snapshot, err := runner.Start(context.Background(), benchmarkHarnessStartRequest())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		loaded, loadErr := runner.Load(context.Background(), snapshot.Turn.ID)
		if loadErr != nil || loaded.Turn.Status != harness.TurnCompleted {
			b.Fatalf("turn=%#v err=%v", loaded.Turn, loadErr)
		}
	}
}

func benchmarkHarnessRunner(b *testing.B) *harness.Runner {
	b.Helper()
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: harnessBenchmarkClock{}, IDs: &harnessBenchmarkIDs{},
	})
	if err != nil {
		b.Fatal(err)
	}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: harnessBenchmarkModel{}, Clock: harnessBenchmarkClock{},
	})
	if err != nil {
		b.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: harness.NewMemoryStore(), Clock: harnessBenchmarkClock{},
	})
	if err != nil {
		b.Fatal(err)
	}
	return runner
}

func benchmarkHarnessStartRequest() harness.StartRequest {
	return harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "thread"},
		HostTurn:   harness.HostRef{Kind: "conversation_turn", ID: "turn"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		RequestID:  "benchmark-request", Goal: "answer deterministically",
		Config: harness.ConfigSnapshot{
			Environment: harness.VersionRef{ID: "benchmark", Revision: 1}, Model: "fixture",
		},
	}
}

type harnessBenchmarkModel struct{}

func (harnessBenchmarkModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "done", ResponseID: "benchmark-response"}, nil
}

type harnessBenchmarkClock struct{}

func (harnessBenchmarkClock) Now() time.Time {
	return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
}

type harnessBenchmarkIDs struct{ next atomic.Uint64 }

func (ids *harnessBenchmarkIDs) NewID(prefix string) (string, error) {
	return prefix + "-" + strconv.FormatUint(ids.next.Add(1), 10), nil
}
