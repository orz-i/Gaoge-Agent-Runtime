package harness_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
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
	paused, err := runner.StartAgentTurn(t.Context(), request)
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

		ToolPolicies: []harness.ToolPolicySnapshot{{
			Key: approvalTestToolKey, DefinitionVersion: "v1",
			ApprovalCapability: approvalTestCapabilityPerCall, ApprovalMode: "always",
		}}, AgentSpec: harness.AgentSpec{ToolKeys: []string{approvalTestToolKey}},
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
	paused, err := runner.StartAgentTurn(t.Context(), request)
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

func (store *lostAllowanceACKStore) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Control != nil {
		snapshot, revoked, err := store.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
		if err == nil && store.armed.CompareAndSwap(true, false) {
			return kernel.Snapshot{}, revoked, errControlLostACK
		}
		return snapshot, revoked, err
	}
	return store.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
}

func TestNativeAllowanceLostCASReplyDoesNotNeedHostRetirement(t *testing.T) {
	for _, mode := range []string{"native", "unrelated-interaction-handler", "unused-budget-on-reopen", "both-on-reopen", "unexpected-ledger", "missing-authority"} {
		t.Run(mode, func(t *testing.T) { testNativeAllowanceLostReply(t, mode) })
	}
}

func testNativeAllowanceLostReply(t *testing.T, mode string) {
	t.Helper()
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
	handler := &recordingInteractionResponseHandler{}
	deps := harness.Dependencies{Runtime: runtime, Store: harnessStore, Agent: direct, Clock: fixedClock{}}
	if mode == "unrelated-interaction-handler" || mode == "both-on-reopen" {
		deps.Interactions = handler
	}
	shared, err := harness.NewBudgetAuthority(harness.BudgetAuthorityDependencies{Store: harnessStore, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	if mode == "missing-authority" {
		deps.Budget = shared
	}
	runner, err := harness.NewRunner(deps)
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
	paused, err := runner.StartAgentTurn(t.Context(), request)
	if mode == "missing-authority" {
		if !errors.Is(err, harness.ErrInvalidRequest) || paused.Turn.ID != "" {
			t.Fatalf("native admission accepted an independent budget authority: %+v %v", paused.Turn, err)
		}
		sessionID, e := harness.SessionID(request.HostThread)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = harnessStore.GetSession(t.Context(), sessionID); !errors.Is(e, harness.ErrNotFound) {
			t.Fatalf("rejected budget composition created Host identity: %v", e)
		}
		return
	}
	if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
		t.Fatalf("failed to create paused Agent: %v", err)
	}
	owner, _ := harness.TopLevelInvocation(paused)
	switch mode {
	case "unused-budget-on-reopen", "both-on-reopen", "unexpected-ledger":
		if paused.Config.SharedBudget != nil {
			t.Fatal("original is not unbudgeted")
		}
		deps.Budget = shared
	case "missing-authority":
		deps.Budget = nil
	}
	if mode == "unexpected-ledger" {
		if _, err = (budget.Coordinator{Store: harnessStore}).Ensure(t.Context(), paused.Turn.ID, budget.Limits{MaxChildRuns: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if mode != "native" && mode != "unrelated-interaction-handler" {
		runner, err = harness.NewRunner(deps)
		if err != nil {
			t.Fatal(err)
		}
	}
	original, err := runtime.Load(t.Context(), owner.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "unexpected-ledger" || mode == "missing-authority" {
		_, err = runner.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
		after, e := runtime.Load(t.Context(), owner.ExecutionRefID)
		items, readErr := harnessStore.ListItems(t.Context(), paused.Turn.ID, 0, 500)
		if !errors.Is(err, harness.ErrExecutionOutcomeUnknown) || e != nil || readErr != nil || kernel.ManagedDigest(after) != kernel.ManagedDigest(original) || len(items) != len(paused.Items) {
			t.Fatalf("contradictory accounting admitted control: %v %v %v", err, e, readErr)
		}
		return
	}
	store.armed.Store(true)
	_, firstErr := runner.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
	if !errors.Is(firstErr, errControlLostACK) || errors.Is(firstErr, agent.ErrControlNoEffect) {
		t.Fatalf("lost post-CAS acknowledgment was incorrectly classified: %v", firstErr)
	}
	run, err := runtime.Load(t.Context(), owner.ExecutionRefID)
	if err != nil || run.Run.Status != kernel.RunStatusRunning || run.Run.Revision != original.Run.Revision+1 {
		t.Fatalf("fixture did not physically commit before ACK was lost: status=%s err=%v", run.Run.Status, err)
	}
	items, err := harnessStore.ListItems(t.Context(), paused.Turn.ID, 0, 500)
	if err != nil || harness.PendingExecutionDispatch(items, owner.ID) {
		t.Fatalf("pure native control created Host dispatch authority or unreadable evidence: %v", err)
	}
	view, err := agent.ViewState(run)
	if err != nil || view.CallAllowance == nil || view.CallAllowance.LLMCalls != 2 || view.Budget.Usage.LLMCalls != 1 {
		t.Fatalf("original allowance not committed exactly once: %+v %v", view, err)
	}
	claims, err := store.ClaimTransitions(t.Context(), kernel.TransitionClaimRequest{WorkerID: "allowance-check", Limit: 500, LeaseDuration: time.Minute, Now: runtime.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	granted := 0
	for _, claim := range claims {
		for _, event := range claim.Transition.Events {
			if event.Type == "agent.allowance_granted" {
				if !event.Wakeup || claim.Transition.RunID != run.Run.ID || claim.Transition.Revision != run.Run.Revision {
					t.Fatal("allowance wakeup did not commit with its original native CAS")
				}
				granted++
			}
		}
	}
	if granted != 1 {
		t.Fatalf("native allowance wakeup count = %d", granted)
	}
	reopened, e := harness.NewRunner(deps)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = reopened.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1}); !errors.Is(e, harness.ErrConflict) {
		t.Fatalf("stale allowance accepted: %v", e)
	}
	complete, e := reopened.ResumeTurnAtRevision(t.Context(), paused.Turn.ID, run.Run.ID, run.Run.Revision)
	if e != nil || complete.Turn.Status != harness.TurnCompleted {
		t.Fatalf("committed native allowance did not resume: %s %v", complete.Turn.Status, e)
	}
	after, e := runtime.Load(t.Context(), run.Run.ID)
	if e != nil {
		t.Fatal(e)
	}
	view, e = agent.ViewState(after)
	if e != nil || view.CallAllowance == nil || view.CallAllowance.LLMCalls != 2 || view.Budget.Usage.LLMCalls != 2 {
		t.Fatalf("allowance replayed: %+v %v", view, e)
	}
	items, e = harnessStore.ListItems(t.Context(), paused.Turn.ID, 0, 500)
	if e != nil || handler.calls != 0 || handler.validationCalls != 0 {
		t.Fatalf("unrelated interaction was invoked: %v %+v", e, handler)
	}
	if _, e = harnessStore.LoadBudget(t.Context(), paused.Turn.ID); !errors.Is(e, budget.ErrLedgerNotFound) {
		t.Fatalf("unbudgeted execution acquired a ledger: %v", e)
	}
	for _, item := range items {
		if phase, valid := harness.VerifiedExecutionDispatchGrant(item); valid && (phase == "grant-call-allowance" || phase == "resume") {
			t.Fatalf("native command elected Host dispatch: %s", phase)
		}
	}
}

func TestNativeAllowancePreflightHasNoHostReceiptToLose(t *testing.T) {
	runner, backing, runtime, fault, paused := newReadFaultAllowanceHarness(t)
	owner, _ := harness.TopLevelInvocation(paused)
	fault.armed.Store(true)
	_, e := runner.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
	fault.armed.Store(false)
	if !errors.Is(e, errControlReadOutage) || !errors.Is(e, agent.ErrControlNoEffect) {
		t.Fatalf("wrong native preflight failure: %v", e)
	}
	items, e := backing.ListItems(t.Context(), paused.Turn.ID, 0, 500)
	if e != nil || harness.PendingExecutionDispatch(items, owner.ID) {
		t.Fatalf("read-only rejection left Host authority: %v", e)
	}
	for _, i := range items {
		if phase, ok := harness.VerifiedExecutionDispatchGrant(i); ok && phase == "grant-call-allowance" {
			t.Fatal("unexpected native allowance grant")
		}
	}
	run, e := runtime.Load(t.Context(), owner.ExecutionRefID)
	if e != nil || run.Run.Status != kernel.RunStatusPausedBudget {
		t.Fatalf("preflight mutated Run: %v", e)
	}
	stopped, e := runner.Cancel(t.Context(), paused.Turn.ID, "stop after rejected allowance")
	if e != nil || stopped.Turn.Status != harness.TurnCancelled {
		t.Fatalf("rejected native control blocked cancellation: %v", e)
	}
}
