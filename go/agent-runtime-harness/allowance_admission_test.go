package harness_test

import (
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func newPausedAllowanceAdmissionFixture(t *testing.T) (*harness.Runner, *harness.MemoryStore, *kernel.Runtime, harness.Snapshot, kernel.Snapshot) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: directModel{},
		CompletionPolicies: []agent.CompletionPolicy{&allowanceCompletionPolicy{}},
		Limits:             agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
	paused, err := runner.Start(t.Context(), request)
	if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
		t.Fatalf("start paused Agent: status=%s err=%v", paused.Turn.Status, err)
	}
	owner, ok := harness.TopLevelInvocation(paused)
	if !ok {
		t.Fatal("missing root Invocation")
	}
	run, err := runtime.Load(t.Context(), owner.ExecutionRefID)
	if err != nil || run.Run.Status != kernel.RunStatusPausedBudget {
		t.Fatalf("root Run not paused: run=%s err=%v", run.Run.Status, err)
	}
	return runner, store, runtime, paused, run
}

func TestInvalidAllowanceRejectedBeforePhysicalDispatchAndValidGrantStillWorks(t *testing.T) {
	runner, store, runtime, paused, original := newPausedAllowanceAdmissionFixture(t)
	invalid := []struct {
		name      string
		increment agent.CallAllowance
	}{
		{"empty", agent.CallAllowance{}},
		{"negative-model", agent.CallAllowance{LLMCalls: -1}},
		{"negative-tool", agent.CallAllowance{ToolCalls: -1}},
		{"negative-model-with-positive-tool", agent.CallAllowance{LLMCalls: -1, ToolCalls: 2}},
		{"positive-model-with-negative-tool", agent.CallAllowance{LLMCalls: 2, ToolCalls: -1}},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runner.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, tc.increment)
			if !errors.Is(err, harness.ErrInvalidRequest) {
				t.Fatalf("malformed allowance not rejected as invalid request: %v", err)
			}
			items, err := store.ListItems(t.Context(), paused.Turn.ID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != len(paused.Items) || harness.PendingExecutionDispatch(items, paused.Invocations[0].ID) {
				t.Fatalf("malformed allowance persisted a dispatch grant: before=%d after=%d", len(paused.Items), len(items))
			}
			after, err := runtime.Load(t.Context(), original.Run.ID)
			if err != nil || after.Run.Status != kernel.RunStatusPausedBudget || after.Run.Revision != original.Run.Revision {
				t.Fatalf("malformed allowance changed Kernel: before=%+v after=%+v err=%v", original.Run, after.Run, err)
			}
		})
	}
	accepted, err := runner.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
	if err != nil || accepted.Turn.Status != harness.TurnRunning {
		t.Fatalf("a malformed request poisoned the subsequent legal allowance grant: status=%s err=%v", accepted.Turn.Status, err)
	}
}

func TestInvalidAllowanceDoesNotBlockOperatorCancel(t *testing.T) {
	runner, store, runtime, paused, original := newPausedAllowanceAdmissionFixture(t)
	_, err := runner.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{})
	if !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("invalid allowance: %v", err)
	}
	cancelled, err := runner.Cancel(t.Context(), paused.Turn.ID, "operator cancelled after rejected allowance")
	if err != nil || cancelled.Turn.Status != harness.TurnCancelled {
		t.Fatalf("rejected allowance blocked operator cancellation: status=%s err=%v", cancelled.Turn.Status, err)
	}
	run, err := runtime.Load(t.Context(), original.Run.ID)
	if err != nil || run.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("operator cancellation did not terminate the Kernel Run: status=%s err=%v", run.Run.Status, err)
	}
	items, err := store.ListItems(t.Context(), paused.Turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if harness.PendingExecutionDispatch(items, paused.Invocations[0].ID) {
		t.Fatal("operator cancellation left a physical dispatch grant")
	}
}
