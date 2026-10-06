package agent_test

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func BenchmarkAgentDeterministicLoopMemory(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		b.StopTimer()
		runner := benchmarkAgentRunner(b)
		b.StartTimer()
		snapshot, err := runner.StartRun(context.Background(), agent.StartRequest{
			ID: "benchmark-agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "benchmark", ID: "thread"},
			Goal:   "complete deterministically", Model: "fixture",
		})
		if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted {
			b.Fatalf("snapshot=%#v err=%v", snapshot.Run, err)
		}
	}
}

func benchmarkAgentRunner(b *testing.B) *agent.Runner {
	b.Helper()
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: agentBenchmarkClock{}, IDs: &agentBenchmarkIDs{},
	})
	if err != nil {
		b.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: agentBenchmarkModel{}, Clock: agentBenchmarkClock{},
	})
	if err != nil {
		b.Fatal(err)
	}
	return runner
}

type agentBenchmarkModel struct{}

func (agentBenchmarkModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{
		Content: "done", ResponseID: "benchmark-response",
		Usage: &model.Usage{InputTokens: 8, OutputTokens: 1},
	}, nil
}

type agentBenchmarkClock struct{}

func (agentBenchmarkClock) Now() time.Time {
	return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
}

type agentBenchmarkIDs struct{ next atomic.Uint64 }

func (ids *agentBenchmarkIDs) NewID(prefix string) (string, error) {
	return prefix + "-" + strconv.FormatUint(ids.next.Add(1), 10), nil
}
