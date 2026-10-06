package harness_test

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
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

func BenchmarkHarnessContextPathLookupMemory(b *testing.B) {
	store := harness.NewMemoryStore()
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	const scopeID = "conversation:benchmark"
	fingerprint := runtimecontext.StaticFingerprint("benchmark-context")
	fullPath := make([]string, 0, 32)
	var checkpoint runtimecontext.Checkpoint
	for index := range 32 {
		sourceID := "source-" + strconv.Itoa(index)
		entry := runtimecontext.Entry{
			ID: "entry-" + strconv.Itoa(index), SourceID: sourceID,
			Message: model.Message{Role: model.RoleUser, Content: "message-" + strconv.Itoa(index)},
		}
		request := runtimecontext.OpenRequest{
			ScopeID: scopeID, StaticFingerprint: fingerprint,
			SourcePath: []string{sourceID}, Entries: []runtimecontext.Entry{entry},
			SourceDelta: index > 0,
		}
		if index > 0 {
			request.Previous = &checkpoint
		}
		var err error
		checkpoint, err = manager.Open(context.Background(), request)
		if err != nil {
			b.Fatal(err)
		}
		if _, _, err = store.PutContextCheckpoint(context.Background(), checkpoint); err != nil {
			b.Fatal(err)
		}
		fullPath = append(fullPath, sourceID)
	}
	query := harness.ContextCheckpointPathQuery{
		ScopeID: scopeID, StaticFingerprint: fingerprint, SourcePath: fullPath,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		resolved, err := store.FindContextCheckpointForPath(context.Background(), query)
		if err != nil || resolved.ID != checkpoint.ID {
			b.Fatalf("checkpoint=%q want=%q err=%v", resolved.ID, checkpoint.ID, err)
		}
	}
}

func BenchmarkHarnessLoadRelatedSubtasksMemory(b *testing.B) {
	runner, runtime, relations := benchmarkHarnessRunnerWithRelations(b)
	started, err := runner.Start(context.Background(), benchmarkHarnessStartRequest())
	if err != nil || len(started.Invocations) != 1 {
		b.Fatalf("start=%#v err=%v", started.Turn, err)
	}
	parentRunID := started.Invocations[0].ExecutionRefID
	for index := range 100 {
		childRunID := "benchmark-child-" + strconv.Itoa(index)
		if _, err = runtime.Create(context.Background(), kernel.CreateRequest{
			ID: childRunID, Kind: kernel.RunKind("benchmark_child"),
			Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
			Goal:   "child", State: []byte("{}"),
		}); err != nil {
			b.Fatal(err)
		}
		if _, err = relations.Ensure(context.Background(), runrelation.Draft{
			ParentRunID: parentRunID, ChildRunID: childRunID,
			Kind: runrelation.KindCapability, OwnerNodeID: "child-" + strconv.Itoa(index),
		}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		loaded, loadErr := runner.Load(context.Background(), started.Turn.ID)
		if loadErr != nil || len(loaded.Subtasks) != 100 {
			b.Fatalf("subtasks=%d err=%v", len(loaded.Subtasks), loadErr)
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

func benchmarkHarnessRunnerWithRelations(b *testing.B) (*harness.Runner, *kernel.Runtime, *runrelation.Registry) {
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
	relations, err := runrelation.New(memory.NewRunRelationStore(), harnessBenchmarkClock{})
	if err != nil {
		b.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: harness.NewMemoryStore(),
		Clock: harnessBenchmarkClock{}, Relations: relations,
	})
	if err != nil {
		b.Fatal(err)
	}
	return runner, runtime, relations
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
