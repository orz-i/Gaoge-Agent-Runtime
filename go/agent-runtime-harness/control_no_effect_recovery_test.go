package harness_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

var (
	errControlReadOutage = errors.New("transient Kernel Store read outage")
	errControlLostACK    = errors.New("control CAS reply lost after commit")
)

type preDispatchReadFaultStore struct {
	kernel.Store
	armed atomic.Bool
	reads atomic.Int32
}

func (store *preDispatchReadFaultStore) Load(ctx context.Context, runID string) (kernel.Snapshot, error) {
	if store.armed.Load() && store.reads.Add(1) == 2 {
		return kernel.Snapshot{}, errControlReadOutage
	}
	return store.Store.Load(ctx, runID)
}

func assertNoUnfinishedControlGrant(t *testing.T, store harness.Store, turnID, invocationID string) {
	t.Helper()
	items, err := store.ListItems(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if harness.PendingExecutionDispatch(items, invocationID) {
		t.Fatal("pre-dispatch failure left an unresolved physical grant")
	}
}

func newReadFaultAllowanceHarness(t *testing.T) (*harness.Runner, *harness.MemoryStore, *kernel.Runtime, *preDispatchReadFaultStore, harness.Snapshot) {
	t.Helper()
	fault := &preDispatchReadFaultStore{Store: memory.NewStore()}
	runtime, err := kernel.New(kernel.Dependencies{Store: fault, Clock: fixedClock{}})
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
		t.Fatalf("failed to create paused Agent: status=%s err=%v", paused.Turn.Status, err)
	}
	return runner, store, runtime, fault, paused
}

func newReadFaultApprovalHarness(t *testing.T) (*harness.Runner, *harness.MemoryStore, *kernel.Runtime, *preDispatchReadFaultStore, harness.Snapshot) {
	t.Helper()
	fault := &preDispatchReadFaultStore{Store: memory.NewStore()}
	runtime, err := kernel.New(kernel.Dependencies{Store: fault})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	policy, err := harness.NewFrozenApprovalPolicy(store)
	if err != nil {
		t.Fatal(err)
	}
	registry := newApprovalTestRegistry(t)
	approvals, err := interaction.New(runtime)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: &approvalTestModel{}, Catalog: registry, Executor: registry,
		Approvals: interactionadapter.New(approvals), ApprovalPolicies: []plugin.ApprovalPolicy{policy},
		Limits: agent.Limits{MaxLLMCalls: 3, MaxToolCalls: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: direct, Store: store, Clock: approvalTestClock{}, Catalog: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	config := harness.ConfigSnapshot{
		ToolKeys: []string{approvalTestToolKey},
		ToolPolicies: []harness.ToolPolicySnapshot{{
			Key: approvalTestToolKey, DefinitionVersion: "v1",
			ApprovalCapability: approvalTestCapabilityPerCall, ApprovalMode: "always",
		}},
	}
	waiting := startApprovalHarness(t, runner, config)
	return runner, store, runtime, fault, waiting
}

func TestNativeControlReadFailureClosesOnlyProvenNoEffectGrant(t *testing.T) {
	for _, control := range []string{"allowance", "approval"} {
		for _, recovery := range []string{"retry", "cancel"} {
			t.Run(control+"/"+recovery, func(t *testing.T) {
				var runner *harness.Runner
				var store *harness.MemoryStore
				var runtime *kernel.Runtime
				var fault *preDispatchReadFaultStore
				var initial harness.Snapshot
				if control == "allowance" {
					runner, store, runtime, fault, initial = newReadFaultAllowanceHarness(t)
				} else {
					runner, store, runtime, fault, initial = newReadFaultApprovalHarness(t)
				}
				owner, ok := harness.TopLevelInvocation(initial)
				if !ok {
					t.Fatal("missing root Invocation")
				}
				var checkpointID string
				if control == "approval" {
					checkpointID = latestApprovalCheckpoint(t, initial)
				}
				fault.armed.Store(true)
				var firstErr error
				if control == "allowance" {
					_, firstErr = runner.GrantCallAllowance(t.Context(), initial.Turn.ID, initial.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
				} else {
					_, firstErr = runner.ResolveApproval(t.Context(), initial.Turn.ID, harness.ResolveApprovalRequest{
						CheckpointID: checkpointID, Decision: harness.ApprovalApprove,
					})
				}
				fault.armed.Store(false)
				if fault.reads.Load() != 2 || !errors.Is(firstErr, errControlReadOutage) ||
					!errors.Is(firstErr, agent.ErrControlNoEffect) {
					t.Fatalf("first-party pre-dispatch proof missing: calls=%d err=%v", fault.reads.Load(), firstErr)
				}
				run, err := runtime.Load(t.Context(), owner.ExecutionRefID)
				if err != nil {
					t.Fatal(err)
				}
				expectedStatus := kernel.RunStatusPausedBudget
				if control == "approval" {
					expectedStatus = kernel.RunStatusWaitingInput
				}
				if run.Run.Status != expectedStatus {
					t.Fatalf("failed read mutated Kernel: run=%s expected=%s", run.Run.Status, expectedStatus)
				}
				assertNoUnfinishedControlGrant(t, store, initial.Turn.ID, owner.ID)

				switch recovery {
				case "retry":
					var resumed harness.Snapshot
					if control == "allowance" {
						resumed, err = runner.GrantCallAllowance(t.Context(), initial.Turn.ID, initial.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
					} else {
						resumed, err = runner.ResolveApproval(t.Context(), initial.Turn.ID, harness.ResolveApprovalRequest{
							CheckpointID: checkpointID, Decision: harness.ApprovalApprove,
						})
					}
					if err != nil {
						t.Fatalf("verified no-effect preflight could not retry: %v", err)
					}
					if control == "allowance" && resumed.Turn.Status != harness.TurnRunning ||
						control == "approval" && resumed.Turn.Status != harness.TurnWaitingInput {
						t.Fatalf("unexpected resumed control projection: %s", resumed.Turn.Status)
					}
				case "cancel":
					cancelled, cancelErr := runner.Cancel(t.Context(), initial.Turn.ID, "cancel after transient read")
					if cancelErr != nil || cancelled.Turn.Status != harness.TurnCancelled {
						t.Fatalf("failed preflight blocked operator cancellation: status=%s err=%v", cancelled.Turn.Status, cancelErr)
					}
				}
			})
		}
	}
}

type forgedNoEffectAllowanceAgent struct {
	*agent.Runner
	calls atomic.Int32
}

func (feature *forgedNoEffectAllowanceAgent) GrantCallAllowance(context.Context, string, uint64, agent.CallAllowance) (kernel.Snapshot, error) {
	feature.calls.Add(1)
	return kernel.Snapshot{}, errors.Join(agent.ErrControlNoEffect, errors.New("custom adapter cannot prove native no-effect"))
}

func TestCustomAgentCannotForgeNativeNoEffectProof(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
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
	custom := &forgedNoEffectAllowanceAgent{Runner: native}
	store := harness.NewMemoryStore()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Store: store, Agent: custom, Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
	paused, err := runner.Start(t.Context(), request)
	if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
		t.Fatalf("failed to create paused Agent: %v", err)
	}
	owner, _ := harness.TopLevelInvocation(paused)
	_, err = runner.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
	if custom.calls.Load() != 1 || !errors.Is(err, agent.ErrControlNoEffect) {
		t.Fatalf("custom no-effect fixture: calls=%d err=%v", custom.calls.Load(), err)
	}
	items, err := store.ListItems(t.Context(), paused.Turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !harness.PendingExecutionDispatch(items, owner.ID) {
		t.Fatal("custom adapter forged a first-party control no-effect receipt")
	}
	if _, err = runner.Cancel(t.Context(), paused.Turn.ID, "unknown custom effect"); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("unknown custom effect was incorrectly considered safe to cancel: %v", err)
	}
}

type lostAllowanceACKStore struct {
	*memory.Store
	armed atomic.Bool
}

func (store *lostAllowanceACKStore) ApplyControl(
	ctx context.Context, runID string, revision uint64, guard kernel.ControlGuard, change kernel.StoreMutation,
) (kernel.Snapshot, bool, error) {
	snapshot, revoked, err := store.Store.ApplyControl(ctx, runID, revision, guard, change)
	if err == nil && store.armed.CompareAndSwap(true, false) {
		return kernel.Snapshot{}, revoked, errControlLostACK
	}
	return snapshot, revoked, err
}

func TestPostCommitLostControlReplyRemainsFailClosed(t *testing.T) {
	store := &lostAllowanceACKStore{Store: memory.NewStore()}
	runtime, err := kernel.New(kernel.Dependencies{Store: store, Clock: fixedClock{}})
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
	harnessStore := harness.NewMemoryStore()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Store: harnessStore, Agent: direct, Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
	paused, err := runner.Start(t.Context(), request)
	if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
		t.Fatalf("failed to create paused Agent: %v", err)
	}
	owner, _ := harness.TopLevelInvocation(paused)
	store.armed.Store(true)
	_, firstErr := runner.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
	if !errors.Is(firstErr, errControlLostACK) || errors.Is(firstErr, agent.ErrControlNoEffect) {
		t.Fatalf("lost post-CAS acknowledgment was incorrectly classified: %v", firstErr)
	}
	run, err := runtime.Load(t.Context(), owner.ExecutionRefID)
	if err != nil || run.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("fixture did not physically commit before ACK was lost: status=%s err=%v", run.Run.Status, err)
	}
	items, err := harnessStore.ListItems(t.Context(), paused.Turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !harness.PendingExecutionDispatch(items, owner.ID) {
		t.Fatal("lost post-CAS ACK incorrectly released the uncertain control grant")
	}
	if _, err := runner.Cancel(t.Context(), paused.Turn.ID, "uncertain control"); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("ambiguous control outcome was cancelled as if proven no-effect: %v", err)
	}
}

// Even the concrete native Agent can run host-defined middleware. A middleware
// that returns a public sentinel after an external side effect is NOT native
// preflight evidence, and must leave Harness cancellation admission blocked.
type forgedNativeRunMiddleware struct {
	calls   atomic.Int32
	wrapped bool
}

func (*forgedNativeRunMiddleware) Name() string { return "test.forged-no-effect" }

func (m *forgedNativeRunMiddleware) Run(ctx context.Context, invocation plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	if invocation.Operation != plugin.RunResolveApproval {
		return next(ctx)
	}
	m.calls.Add(1)
	if m.wrapped {
		return kernel.Snapshot{}, errors.Join(agent.ErrControlNoEffect, errors.New("middleware effect may have occurred"))
	}
	return kernel.Snapshot{}, agent.ErrControlNoEffect
}

func TestNativeAgentRunMiddlewareCannotForgeControlNoEffectProof(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		name := "sentinel"
		if wrapped {
			name = "wrapped-sentinel"
		}
		t.Run(name, func(t *testing.T) {
			runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			store := harness.NewMemoryStore()
			policy, err := harness.NewFrozenApprovalPolicy(store)
			if err != nil {
				t.Fatal(err)
			}
			registry := newApprovalTestRegistry(t)
			approvals, err := interaction.New(runtime)
			if err != nil {
				t.Fatal(err)
			}
			middleware := &forgedNativeRunMiddleware{wrapped: wrapped}
			direct, err := agent.NewRunner(agent.Dependencies{
				Runtime: runtime, Model: &approvalTestModel{}, Catalog: registry, Executor: registry,
				Approvals: interactionadapter.New(approvals), ApprovalPolicies: []plugin.ApprovalPolicy{policy},
				RunMiddleware: []plugin.RunMiddleware{middleware},
				Limits:        agent.Limits{MaxLLMCalls: 3, MaxToolCalls: 2},
			})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: direct, Store: store, Clock: approvalTestClock{}, Catalog: registry,
			})
			if err != nil {
				t.Fatal(err)
			}
			config := harness.ConfigSnapshot{
				ToolKeys: []string{approvalTestToolKey},
				ToolPolicies: []harness.ToolPolicySnapshot{{
					Key: approvalTestToolKey, DefinitionVersion: "v1",
					ApprovalCapability: approvalTestCapabilityPerCall, ApprovalMode: "always",
				}},
			}
			waiting := startApprovalHarness(t, runner, config)
			owner, _ := harness.TopLevelInvocation(waiting)
			_, approvalErr := runner.ResolveApproval(t.Context(), waiting.Turn.ID,
				harness.ResolveApprovalRequest{CheckpointID: latestApprovalCheckpoint(t, waiting), Decision: harness.ApprovalApprove})
			if middleware.calls.Load() != 1 || !errors.Is(approvalErr, agent.ErrControlNoEffect) {
				t.Fatalf("middleware did not emit an adversarial marker: calls=%d err=%v", middleware.calls.Load(), approvalErr)
			}
			if agent.IsNativeControlNoEffect(approvalErr) {
				t.Fatal("middleware error was incorrectly identified as native pre-dispatch proof")
			}
			items, err := store.ListItems(t.Context(), waiting.Turn.ID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			if !harness.PendingExecutionDispatch(items, owner.ID) {
				t.Fatal("Host middleware bypassed fail-closed admission using an exported sentinel")
			}
			if _, err = runner.Cancel(t.Context(), waiting.Turn.ID, "unknown middleware effect"); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("unknown middleware side effect was incorrectly cancelled: %v", err)
			}
		})
	}
}

var errControlReceiptWrite = errors.New("injected Harness dispatch completion write failure")

type interruptedNoEffectReceiptStore struct {
	harness.Store
	rejectNext atomic.Bool
}

func (store *interruptedNoEffectReceiptStore) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	if item.Kind == harness.ItemDiagnostic && item.Status == harness.ItemCompleted &&
		strings.HasPrefix(item.ParentItemID, "hdispatch_") &&
		store.rejectNext.CompareAndSwap(true, false) {
		return harness.Item{}, false, errControlReceiptWrite
	}
	return store.Store.AppendItem(ctx, item)
}

func TestLostNativeNoEffectReceiptRemainsFailClosed(t *testing.T) {
	kernelStore := &preDispatchReadFaultStore{Store: memory.NewStore()}
	runtime, err := kernel.New(kernel.Dependencies{Store: kernelStore, Clock: fixedClock{}})
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
	backing := harness.NewMemoryStore()
	store := &interruptedNoEffectReceiptStore{Store: backing}
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
		t.Fatalf("setup pause failed: status=%s err=%v", paused.Turn.Status, err)
	}
	owner, _ := harness.TopLevelInvocation(paused)
	kernelStore.armed.Store(true)
	store.rejectNext.Store(true)
	_, firstErr := runner.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
	kernelStore.armed.Store(false)
	if !errors.Is(firstErr, errControlReadOutage) || !errors.Is(firstErr, errControlReceiptWrite) ||
		!errors.Is(firstErr, agent.ErrControlNoEffect) {
		t.Fatalf("wrong failure boundary for lost proof receipt: %v", firstErr)
	}
	items, err := backing.ListItems(t.Context(), paused.Turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !harness.PendingExecutionDispatch(items, owner.ID) {
		t.Fatal("a failed no-effect receipt write incorrectly released a control grant")
	}
	run, err := runtime.Load(t.Context(), owner.ExecutionRefID)
	if err != nil || run.Run.Status != kernel.RunStatusPausedBudget {
		t.Fatalf("unexpected Kernel effect despite preflight failure: status=%s err=%v", run.Run.Status, err)
	}
	if _, err := runner.Cancel(t.Context(), paused.Turn.ID, "unknown incomplete proof"); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("incomplete proof receipt must remain fail-closed: %v", err)
	}
}
