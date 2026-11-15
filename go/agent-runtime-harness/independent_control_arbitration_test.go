package harness_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

func TestIndependentApprovalRequiresCancellationDispatchAuthority(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
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
	client := &approvalTestModel{}
	feature, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, Catalog: registry, Executor: registry, Approvals: interactionadapter.New(approvals), ApprovalPolicies: []plugin.ApprovalPolicy{policy}, Limits: agent.Limits{MaxLLMCalls: 3, MaxToolCalls: 2}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: feature, Store: store, Clock: approvalTestClock{}, Catalog: registry})
	if err != nil {
		t.Fatal(err)
	}
	config := harness.ConfigSnapshot{ToolKeys: []string{approvalTestToolKey}, ToolPolicies: []harness.ToolPolicySnapshot{{Key: approvalTestToolKey, DefinitionVersion: "v1", ApprovalCapability: approvalTestCapabilityPerCall, ApprovalMode: "always"}}}
	waiting := startApprovalHarness(t, runner, config)
	checkpoint := latestApprovalCheckpoint(t, waiting)
	root, ok := harness.TopLevelInvocation(waiting)
	if !ok {
		t.Fatal("missing top Invocation")
	}
	now := time.Now().UTC()
	_, created, err := store.AppendItem(t.Context(), harness.Item{ID: "audit-active-cancel", TurnID: waiting.Turn.ID, RunID: root.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.CancellationFenceParentItemID, Payload: json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"` + root.ExecutionRefID + `"}`), CreatedAt: now, UpdatedAt: now})
	if err != nil || !created {
		t.Fatalf("cancel intent setup: %v", err)
	}
	before := client.calls
	_, resolveErr := runner.ResolveApproval(t.Context(), waiting.Turn.ID, harness.ResolveApprovalRequest{CheckpointID: checkpoint, Decision: harness.ApprovalApprove, Comment: "approval raced with cancel"})
	if !errors.Is(resolveErr, harness.ErrConflict) || client.calls != before {
		t.Fatalf("approval dispatched after persisted cancellation intent: err=%v modelCalls=%d->%d", resolveErr, before, client.calls)
	}
	if _, _, closeErr := store.AppendItem(t.Context(), harness.Item{ID: testAuthorityReceiptID("hcancelclosed", "audit-active-cancel"), TurnID: waiting.Turn.ID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: "audit-active-cancel", RunID: root.ExecutionRefID, Payload: json.RawMessage(`{"boundary":"cancel_scope_settled"}`), CreatedAt: now, UpdatedAt: now}); closeErr != nil {
		t.Fatal(closeErr)
	}
	resumed, resumeErr := runner.ResolveApproval(t.Context(), waiting.Turn.ID, harness.ResolveApprovalRequest{CheckpointID: checkpoint, Decision: harness.ApprovalApprove, Comment: "approval after no-effect intent"})
	if resumeErr != nil || resumed.Turn.Status != harness.TurnWaitingInput || client.calls != before+1 {
		t.Fatalf("settled cancel intent prevented authorized approval: err=%v turn=%s calls=%d->%d", resumeErr, resumed.Turn.Status, before, client.calls)
	}
}
func TestIndependentAllowanceGrantRequiresCancellationDispatchAuthority(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	completion := &allowanceCompletionPolicy{}
	native, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}, CompletionPolicies: []agent.CompletionPolicy{completion}, Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 6}})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: native, Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
	paused, err := runner.StartAgentTurn(t.Context(), request)
	if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
		t.Fatalf("setup pause=%s err=%v", paused.Turn.Status, err)
	}
	owner, ok := harness.TopLevelInvocation(paused)
	if !ok {
		t.Fatal("missing Invocation")
	}
	now := time.Now().UTC()
	_, fresh, err := store.AppendItem(t.Context(), harness.Item{ID: "audit-budget-cancel-intent", TurnID: paused.Turn.ID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.CancellationFenceParentItemID, RunID: owner.ExecutionRefID, Payload: json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"` + owner.ExecutionRefID + `"}`), CreatedAt: now, UpdatedAt: now})
	if err != nil || !fresh {
		t.Fatalf("cancel intent setup: %v", err)
	}
	_, grantErr := runner.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
	state, err := runtime.Load(t.Context(), owner.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(grantErr, harness.ErrConflict) || state.Run.Status != kernel.RunStatusPausedBudget {
		t.Fatalf("allowance wakeup crossed committed cancellation intent: err=%v run=%s", grantErr, state.Run.Status)
	}
	if _, _, closeErr := store.AppendItem(t.Context(), harness.Item{ID: testAuthorityReceiptID("hcancelclosed", "audit-budget-cancel-intent"), TurnID: paused.Turn.ID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: "audit-budget-cancel-intent", RunID: owner.ExecutionRefID, Payload: json.RawMessage(`{"boundary":"cancel_scope_settled"}`), CreatedAt: now, UpdatedAt: now}); closeErr != nil {
		t.Fatal(closeErr)
	}
	resumed, resumeErr := runner.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
	if resumeErr != nil || resumed.Turn.Status != harness.TurnRunning {
		t.Fatalf("settled cancellation intent failed to release safe allowance: err=%v turn=%s", resumeErr, resumed.Turn.Status)
	}
}
