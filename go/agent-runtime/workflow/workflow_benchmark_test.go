package workflow_test

import (
	"context"
	"encoding/json"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func BenchmarkWorkflowDeterministicReturnMemory(b *testing.B) {
	definition := benchmarkWorkflowDefinition(b)
	b.ReportAllocs()
	for range b.N {
		b.StopTimer()
		runner := benchmarkWorkflowRunner(b)
		b.StartTimer()
		snapshot, err := runner.StartRun(context.Background(), workflow.StartRequest{
			ID: "benchmark-workflow", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "benchmark", ID: "thread"},
			Goal:   "return deterministically", Definition: definition, Input: json.RawMessage(`{"value":1}`),
		})
		if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted {
			b.Fatalf("snapshot=%#v err=%v", snapshot.Run, err)
		}
	}
}

func benchmarkWorkflowDefinition(b *testing.B) workflow.Definition {
	b.Helper()
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "benchmark-workflow", Revision: 1, Name: "Benchmark workflow",
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
		Nodes: []workflow.Node{{
			ID: "return", Type: workflow.NodeReturn,
			Return: &workflow.ReturnNode{Value: json.RawMessage(`{"ok":true}`)},
		}},
	})
	if err != nil {
		b.Fatal(err)
	}
	return definition
}

func benchmarkWorkflowRunner(b *testing.B) *workflow.Runner {
	b.Helper()
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: workflowBenchmarkClock{}, IDs: &workflowBenchmarkIDs{},
	})
	if err != nil {
		b.Fatal(err)
	}
	runner, err := workflow.NewRunner(workflow.Dependencies{
		Runtime: runtime, Effects: workflowBenchmarkEffects{},
	})
	if err != nil {
		b.Fatal(err)
	}
	return runner
}

type workflowBenchmarkEffects struct{}

func (workflowBenchmarkEffects) Execute(context.Context, workflow.EffectRequest) (workflow.EffectResult, error) {
	return workflow.EffectResult{}, nil
}

type workflowBenchmarkClock struct{}

func (workflowBenchmarkClock) Now() time.Time {
	return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
}

type workflowBenchmarkIDs struct{ next atomic.Uint64 }

func (ids *workflowBenchmarkIDs) NewID(prefix string) (string, error) {
	return prefix + "-" + strconv.FormatUint(ids.next.Add(1), 10), nil
}
