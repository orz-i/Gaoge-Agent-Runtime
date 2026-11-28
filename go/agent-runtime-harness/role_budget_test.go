package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const roleEvidenceTool = "role.evidence"

type pr82WrongApprovalRoute struct {
	*agent.Runner
	calls int
}

func (r *pr82WrongApprovalRoute) ResolveApproval(ctx context.Context, id string, revision uint64, response plugin.ApprovalResponse) (kernel.Snapshot, error) {
	r.calls++
	return r.Runner.ResolveApproval(ctx, id, revision, response)
}

func TestRoleDelegationFreezesExecutionAndProjectsChildApproval(t *testing.T) {
	for _, wrongRoute := range []bool{false, true} {
		name := "original-child"
		if wrongRoute {
			name = "substitute-root"
		}
		t.Run(name, func(t *testing.T) { testRoleDelegationChildApproval(t, wrongRoute) })
	}
}

// An active child physical grant must prevent the parent from reaching a
// terminal state while a delegated approval remains pending.
func TestRoleDelegationParentCancellationFencedByActiveChild(t *testing.T) {
	testRoleDelegationChildApproval(t, false, true)
}

func testRoleDelegationChildApproval(t *testing.T, wrongRoute bool, terminalParent ...bool) {
	t.Helper()
	store := harness.NewMemoryStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	meteredModel := &roleBudgetModel{}
	shared, err := harness.NewBudgetAuthority(harness.BudgetAuthorityDependencies{Runtime: runtime, Store: store, Relations: relations, Meter: meteredModel, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	hostDelegationPolicy0 := hostSelectedAgentPolicy{
		MemberID: "researcher", MemberRevision: "v3",
		Agent: agent.StartRequest{Model: "specialist-model", Instructions: "Use frozen evidence.\n\n## Evidence\nQuote the evidence.",
			ToolKeys: []string{roleEvidenceTool}, Limits: budget.Limits{MaxLLMCalls: 2}},
	}
	delegation := harness.NewDelegationToolHandler()
	var duringEvidence func()
	registry, err := tools.NewRegistry([]tools.Registration{harness.DelegationToolRegistration(delegation), {
		Definition: tools.Definition{Key: roleEvidenceTool, Name: "read_evidence", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(ctx context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
			if duringEvidence != nil {
				duringEvidence()
			}
			return (roleEvidenceHandler{}).Execute(ctx, q)
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := interaction.New(runtime)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := harness.NewFrozenApprovalPolicy(store, relations)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: meteredModel, Catalog: registry, Executor: registry,
		Approvals: interactionadapter.New(approvals), ApprovalPolicies: []plugin.ApprovalPolicy{policy},
		ToolOutcomeVerifier: delegation,
		BoundedBudget:       mustBudgetAdapter(t, shared)})
	if err != nil {
		t.Fatal(err)
	}
	handoffs, err := handoff.New(handoff.Config{Local: direct})
	if err != nil {
		t.Fatal(err)
	}
	wrong := &pr82WrongApprovalRoute{Runner: direct}
	var rootAgent harness.AgentStarter = direct
	if wrongRoute {
		rootAgent = wrong
	}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: rootAgent, Store: store, Clock: fixedClock{},
		Handoffs: handoffs, Relations: relations, Budget: shared, DelegationPolicy: hostDelegationPolicy0})
	if wrongRoute {
		if !errors.Is(err, harness.ErrInvalidRequest) || wrong.calls != 0 || len(meteredModel.requests) != 0 {
			t.Fatalf("substitute root acquired native budget authority: err=%v approvals=%d models=%d", err, wrong.calls, len(meteredModel.requests))
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = delegation.Bind(runner); err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 8, MaxToolCalls: 16, MaxTotalTokens: 5000, MaxChildRuns: 3, MaxConcurrentRuns: 1}
	request.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
	request.Config.ToolKeys = []string{harness.DelegationToolKey}
	request.Config.DelegationPolicy.LocalToolGrants = []harness.DelegatedLocalToolGrant{{MemberID: "researcher", MemberRevision: "v3", ToolKey: roleEvidenceTool}}
	request.Config.ToolPolicies = []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot(), {
		Key: roleEvidenceTool, DefinitionVersion: "v1", ExecutionMode: "local_dispatch", ApprovalCapability: "per_call", ApprovalMode: "always",
	}}
	started, err := runner.StartAgentTurn(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(started.Subtasks) != 1 || started.Subtasks[0].Approval == nil {
		t.Fatalf("subtasks=%+v", started.Subtasks)
	}
	rootSeen, childSeen := false, false
	for _, req := range meteredModel.requests {
		offered := false
		for _, tool := range req.Tools {
			if tool.Key == roleEvidenceTool {
				offered = true
			}
		}
		if req.Model == "specialist-model" {
			childSeen = childSeen || offered
		} else {
			rootSeen = true
			if offered {
				t.Fatal("the original root model received a member-only tool")
			}
		}
	}
	if !rootSeen || !childSeen {
		t.Fatal("missing original root or authorized child request")
	}
	task := started.Subtasks[0]
	if task.Model != "specialist-model" || task.MemberRevision != "v3" || task.Status != "waiting_input" {
		t.Fatalf("role=%+v", task)
	}
	if _, nestedErr := runner.DelegateByExecutionRefID(t.Context(), task.RunID, harness.DelegateRequest{MemberID: "researcher", Goal: "Nested analysis"}); !errors.Is(nestedErr, harness.ErrInvalidRequest) {
		t.Fatalf("nested delegation bypassed depth policy: %v", nestedErr)
	}
	if started.Budget == nil || started.Budget.ActiveRuns != 0 || started.Budget.Usage.ChildRuns != 1 {
		t.Fatalf("budget=%+v", started.Budget)
	}
	if _, err = runner.ResolveSubtaskApproval(t.Context(), started.Turn.ID, task.ID, "old-checkpoint", harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove}); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("stale approval=%v", err)
	}
	// The delegated Agent has a frozen Child Invocation. Its approval
	// must still compete with the same Turn-wide cancellation intent.
	rootInvocation, ok := harness.TopLevelInvocation(started)
	if !ok {
		t.Fatal("missing owning Invocation")
	}
	now := fixedClock{}.Now()
	intent := harness.Item{
		ID: "role-cancellation-admission", TurnID: started.Turn.ID, RunID: rootInvocation.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.CancellationFenceParentItemID,
		Payload:      json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"` + rootInvocation.ExecutionRefID + `"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, fresh, appendErr := store.AppendItem(t.Context(), intent); appendErr != nil || !fresh {
		t.Fatalf("create cancellation intent: fresh=%v err=%v", fresh, appendErr)
	}
	beforeApprovalCalls := len(meteredModel.requests)
	if _, blockedErr := runner.ResolveSubtaskApproval(t.Context(), started.Turn.ID, task.ID, task.Approval.CheckpointID,
		harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove}); !errors.Is(blockedErr, harness.ErrConflict) ||
		len(meteredModel.requests) != beforeApprovalCalls {
		t.Fatalf("delegated approval crossed cancellation intent: err=%v calls=%d->%d", blockedErr, beforeApprovalCalls, len(meteredModel.requests))
	}
	if _, _, closeErr := store.AppendItem(t.Context(), harness.Item{
		ID: testAuthorityReceiptID("hcancelclosed", intent.ID), TurnID: started.Turn.ID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		ParentItemID: intent.ID, RunID: intent.RunID, Payload: json.RawMessage(`{"boundary":"cancel_scope_settled"}`),
		CreatedAt: now, UpdatedAt: now,
	}); closeErr != nil {
		t.Fatal(closeErr)
	}
	// A Host grant cannot impersonate the delegated child's native admission.
	grant := harness.Item{
		ID: "role-child-approval-grant", TurnID: started.Turn.ID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		InvocationID: rootInvocation.ID, RunID: rootInvocation.ExecutionRefID,
		ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"resolve-subtask-approval","physicalRunID":"` + task.RunID + `"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, fresh, appendErr := store.AppendItem(t.Context(), grant); !errors.Is(appendErr, harness.ErrConflict) || fresh {
		t.Fatalf("forged Host child grant accepted: fresh=%v err=%v", fresh, appendErr)
	}
	if len(terminalParent) > 0 && terminalParent[0] {
		root, readErr := runtime.Load(t.Context(), rootInvocation.ExecutionRefID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		before := len(meteredModel.requests)
		if _, cancelErr := runtime.Cancel(t.Context(), root.Run.ID, root.Run.Revision, "parent terminal before child approval"); !errors.Is(cancelErr, kernel.ErrChildExecutionBusy) {
			t.Fatalf("parent terminal CAS bypassed pending child physical grant: %v", cancelErr)
		}
		after, readErr := runtime.Load(t.Context(), root.Run.ID)
		if readErr != nil || after.Run.Status != root.Run.Status || after.Run.Revision != root.Run.Revision || len(meteredModel.requests) != before {
			t.Fatalf("denied parent cancellation mutated run or executed a model: after=%+v err=%v calls=%d->%d", after.Run, readErr, before, len(meteredModel.requests))
		}
		return
	}
	checkedNativeOwner := false
	duringEvidence = func() {
		checkedNativeOwner = true
		before, readErr := runtime.Load(t.Context(), task.RunID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		_, blockedErr := runner.CancelSubtask(t.Context(), started.Turn.ID, task.ID, "cancel during delegated approval")
		after, readErr := runtime.Load(t.Context(), task.RunID)
		if (!errors.Is(blockedErr, kernel.ErrChildExecutionBusy) && !errors.Is(blockedErr, harness.ErrConflict)) || readErr != nil || kernel.ManagedDigest(before) != kernel.ManagedDigest(after) {
			t.Fatalf("subtask cancellation crossed original native approval callback: cancel=%v read=%v", blockedErr, readErr)
		}
	}
	approved, err := runner.ResolveSubtaskApproval(t.Context(), started.Turn.ID, task.ID, task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
	if err != nil || !checkedNativeOwner || len(approved.Subtasks) != 1 || approved.Subtasks[0].Status != "completed" {
		t.Fatalf("approval=%+v %v", approved.Subtasks, err)
	}
	root, err := runtime.Load(t.Context(), snapshotExecutionRef(t, started))
	if err != nil {
		t.Fatal(err)
	}
	verified := reconcileDelegationRoot(t, direct, root)
	if _, err = direct.Resume(t.Context(), root.Run.ID, verified.Run.Revision); err != nil {
		t.Fatal(err)
	}
	finished, err := runner.Refresh(t.Context(), started.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Turn.Status != harness.TurnCompleted || finished.Budget.Usage.LLMCalls != 4 || finished.Budget.Usage.ToolCalls != 2 || finished.Budget.Reserved.TotalTokens != 0 {
		t.Fatalf("finished=%+v budget=%+v", finished.Turn, finished.Budget)
	}
	for _, call := range meteredModel.requests {
		if call.MaxOutputTokens <= 0 {
			t.Fatal("provider did not receive enforceable output cap")
		}
		if call.Model != "specialist-model" {
			continue
		}
		if len(call.Tools) != 1 || call.Tools[0].Key != roleEvidenceTool || !strings.Contains(model.TextValue(call.Messages[0].Content), "Quote the evidence.") {
			t.Fatalf("role escaped frozen configuration: %+v", call)
		}
	}
}

type roleEvidenceHandler struct{}

func (roleEvidenceHandler) Execute(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
	return tools.ExecutionResult{Content: json.RawMessage(`{"evidence":"frozen"}`), Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
}

type roleBudgetModel struct {
	mu       sync.Mutex
	requests []model.Request
}

func (*roleBudgetModel) PlanTokenAdmission(context.Context, model.Request) (model.TokenAdmission, error) {
	return model.TokenAdmission{InputUpperBound: 128, MaxOutputTokens: 64}, nil
}

func (client *roleBudgetModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.requests = append(client.requests, model.CloneRequest(request))
	response := model.Response{Usage: &model.Usage{InputTokens: 20, OutputTokens: 10}}
	last := request.Messages[len(request.Messages)-1]
	if last.Role == model.RoleTool {
		response.Content = "Completed using evidence."
		return response, nil
	}
	if request.Model == "specialist-model" {
		response.ToolCalls = []tools.Call{{ID: "lookup", ToolKey: roleEvidenceTool, Arguments: json.RawMessage(`{}`)}}
	} else {
		response.ToolCalls = []tools.Call{{ID: "delegate", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"memberID":"researcher","goal":"Find evidence"}`)}}
	}
	return response, nil
}
