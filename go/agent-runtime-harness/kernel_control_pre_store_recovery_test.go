package harness_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

var errKernelPreStoreRead = errors.New("injected Kernel ApplyControl read error before Store CAS")

// The third Kernel Load in a Harness allowance grant belongs to
// Runtime.ApplyControl, AFTER the native Agent preflight has succeeded.
// Embed the concrete Store to retain the guarded ControlStore capability.
type thirdKernelReadFaultStore struct {
	*memory.Store
	armed        atomic.Bool
	reads        atomic.Int32
	controlCalls atomic.Int32
}

func (store *thirdKernelReadFaultStore) Load(ctx context.Context, runID string) (kernel.Snapshot, error) {
	if store.armed.Load() && store.reads.Add(1) == 3 {
		return kernel.Snapshot{}, errKernelPreStoreRead
	}
	return store.Store.Load(ctx, runID)
}

func (store *thirdKernelReadFaultStore) ApplyControl(
	ctx context.Context, runID string, expectedRevision uint64,
	guard kernel.ControlGuard, mutation kernel.StoreMutation,
) (kernel.Snapshot, bool, error) {
	store.controlCalls.Add(1)
	return store.Store.ApplyControl(ctx, runID, expectedRevision, guard, mutation)
}

func TestKernelControlPreStoreReadFailureReleasesNativeDispatch(t *testing.T) {
	for _, recovery := range []string{"retry", "cancel"} {
		t.Run(recovery, func(t *testing.T) {
			kernelStore := &thirdKernelReadFaultStore{Store: memory.NewStore()}
			runtime, err := kernel.New(kernel.Dependencies{Store: kernelStore, Clock: fixedClock{}})
			if err != nil {
				t.Fatal(err)
			}
			native, err := agent.NewRunner(agent.Dependencies{
				Runtime: runtime, Model: directModel{},
				CompletionPolicies: []agent.CompletionPolicy{&allowanceCompletionPolicy{}},
				Limits:             agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 6},
			})
			if err != nil {
				t.Fatal(err)
			}
			store := harness.NewMemoryStore()
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: native, Store: store, Clock: fixedClock{},
			})
			if err != nil {
				t.Fatal(err)
			}
			request := testStartRequest()
			request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
			paused, err := runner.Start(t.Context(), request)
			if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
				t.Fatalf("setup paused status=%s err=%v", paused.Turn.Status, err)
			}
			owner, ok := harness.TopLevelInvocation(paused)
			if !ok {
				t.Fatal("missing root Invocation")
			}
			current, err := runtime.Load(t.Context(), owner.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			beforeWrites := kernelStore.controlCalls.Load()
			kernelStore.armed.Store(true)
			_, firstErr := runner.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
			kernelStore.armed.Store(false)
			if kernelStore.reads.Load() != 3 || !errors.Is(firstErr, errKernelPreStoreRead) ||
				!errors.Is(firstErr, agent.ErrControlNoEffect) ||
				kernelStore.controlCalls.Load() != beforeWrites {
				t.Fatalf("Kernel pre-Store proof missing or Store CAS entered: reads=%d calls=%d->%d err=%v",
					kernelStore.reads.Load(), beforeWrites, kernelStore.controlCalls.Load(), firstErr)
			}
			after, err := runtime.Load(t.Context(), owner.ExecutionRefID)
			if err != nil || after.Run.Revision != current.Run.Revision ||
				after.Run.Status != kernel.RunStatusPausedBudget {
				t.Fatalf("failed read changed Kernel state: before=%+v after=%+v err=%v", current.Run, after.Run, err)
			}
			items, err := store.ListItems(t.Context(), paused.Turn.ID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			if harness.PendingExecutionDispatch(items, owner.ID) {
				t.Fatal("no-effect Kernel pre-Store failure stranded physical dispatch")
			}
			switch recovery {
			case "retry":
				resumed, retryErr := runner.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
				if retryErr != nil || resumed.Turn.Status != harness.TurnRunning ||
					kernelStore.controlCalls.Load() != beforeWrites+1 {
					t.Fatalf("safe retry after no-effect failure: status=%s calls=%d err=%v", resumed.Turn.Status, kernelStore.controlCalls.Load(), retryErr)
				}
			case "cancel":
				cancelled, cancelErr := runner.Cancel(t.Context(), paused.Turn.ID, "operator after Kernel pre-Store read failure")
				if cancelErr != nil || cancelled.Turn.Status != harness.TurnCancelled {
					t.Fatalf("operator cancellation was stranded: status=%s err=%v", cancelled.Turn.Status, cancelErr)
				}
			}
		})
	}
}

// A write-capable Store might return a CAS conflict before applying changes,
// but the Kernel cannot prove whether this or another Store changed external
// state. Even a no-effect Store conflict is intentionally outcome-unknown.
type conflictingKernelControlStore struct {
	*memory.Store
	rejectNext atomic.Bool
}

func (store *conflictingKernelControlStore) ApplyControl(
	ctx context.Context, runID string, expectedRevision uint64,
	guard kernel.ControlGuard, mutation kernel.StoreMutation,
) (kernel.Snapshot, bool, error) {
	if store.rejectNext.CompareAndSwap(true, false) {
		return kernel.Snapshot{}, false, kernel.ErrConflict
	}
	return store.Store.ApplyControl(ctx, runID, expectedRevision, guard, mutation)
}

func TestKernelStoreControlConflictNeverProvesNativeNoEffect(t *testing.T) {
	kernelStore := &conflictingKernelControlStore{Store: memory.NewStore()}
	runtime, err := kernel.New(kernel.Dependencies{Store: kernelStore, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	native, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: directModel{},
		CompletionPolicies: []agent.CompletionPolicy{&allowanceCompletionPolicy{}},
		Limits:             agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: native, Store: store, Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
	paused, err := runner.Start(t.Context(), request)
	if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
		t.Fatalf("setup paused status=%s err=%v", paused.Turn.Status, err)
	}
	kernelStore.rejectNext.Store(true)
	_, firstErr := runner.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
	if !errors.Is(firstErr, kernel.ErrConflict) || errors.Is(firstErr, agent.ErrControlNoEffect) {
		t.Fatalf("Store CAS error incorrectly classified as no-effect: %v", firstErr)
	}
	owner, _ := harness.TopLevelInvocation(paused)
	items, err := store.ListItems(t.Context(), paused.Turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !harness.PendingExecutionDispatch(items, owner.ID) {
		t.Fatal("Store CAS conflict in write-capable phase released grant")
	}
	if _, err := runner.Cancel(t.Context(), paused.Turn.ID, "uncertain Store control conflict"); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("uncertain Store CAS conflict was allowed to cancel: %v", err)
	}
}

// A custom Store can accidentally forward a direct no-effect result produced
// by another Kernel operation AFTER committing this control. Kernel must
// normalize every Store error at the write boundary so that the forwarded
// private proof cannot discharge this unrelated Harness dispatch.
type forwardedKernelNoEffectStore struct {
	*memory.Store
	forwardOnce atomic.Bool
}

func (store *forwardedKernelNoEffectStore) ApplyControl(
	ctx context.Context, runID string, expectedRevision uint64,
	guard kernel.ControlGuard, mutation kernel.StoreMutation,
) (kernel.Snapshot, bool, error) {
	snapshot, revoked, err := store.Store.ApplyControl(ctx, runID, expectedRevision, guard, mutation)
	if err == nil && store.forwardOnce.CompareAndSwap(true, false) {
		var unrelatedRuntime *kernel.Runtime
		_, _, forwarded := unrelatedRuntime.ApplyControl(ctx, runID, expectedRevision, guard, kernel.Mutation{}, nil)
		if !kernel.IsControlNoEffect(forwarded) {
			panic("fixture did not create a genuine unrelated pre-Store proof")
		}
		return kernel.Snapshot{}, revoked, forwarded
	}
	return snapshot, revoked, err
}

func TestCommittedStoreCannotForwardUnrelatedNoEffectProof(t *testing.T) {
	kernelStore := &forwardedKernelNoEffectStore{Store: memory.NewStore()}
	runtime, err := kernel.New(kernel.Dependencies{Store: kernelStore, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	native, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: directModel{},
		CompletionPolicies: []agent.CompletionPolicy{&allowanceCompletionPolicy{}},
		Limits:             agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: native, Store: store, Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
	paused, err := runner.Start(t.Context(), request)
	if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
		t.Fatalf("setup paused status=%s err=%v", paused.Turn.Status, err)
	}
	kernelStore.forwardOnce.Store(true)
	_, err = runner.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
	if !errors.Is(err, kernel.ErrInvalidInput) || errors.Is(err, agent.ErrControlNoEffect) {
		t.Fatalf("Store-forwarded proof incorrectly released native authority: %v", err)
	}
	owner, _ := harness.TopLevelInvocation(paused)
	run, err := runtime.Load(t.Context(), owner.ExecutionRefID)
	if err != nil || run.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("test did not commit physical Kernel CAS: status=%s err=%v", run.Run.Status, err)
	}
	items, err := store.ListItems(t.Context(), paused.Turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !harness.PendingExecutionDispatch(items, owner.ID) {
		t.Fatal("unrelated direct Kernel proof crossed a committed Store CAS")
	}
	if _, err := runner.Cancel(t.Context(), paused.Turn.ID, "uncertain Store outcome"); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("Kernel forwarded proof accidentally allowed cancel: %v", err)
	}
}
