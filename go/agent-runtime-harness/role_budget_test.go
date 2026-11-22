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
	shared, err := harness.NewBudgetMiddleware(harness.BudgetMiddlewareDependencies{Store: store, Relations: relations, Meter: meteredModel, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	hostDelegationPolicy0 := hostSelectedAgentPolicy{
		MemberID: "researcher", MemberRevision: "v3",
		Agent: agent.StartRequest{Model: "specialist-model", Instructions: "Use frozen evidence.\n\n## Evidence\nQuote the evidence.",
			ToolKeys: []string{roleEvidenceTool}, Limits: budget.Limits{MaxLLMCalls: 2}},
	}
	delegation := harness.NewDelegationToolHandler()
	registry, err := tools.NewRegistry([]tools.Registration{harness.DelegationToolRegistration(delegation), {
		Definition: tools.Definition{Key: roleEvidenceTool, Name: "read_evidence", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: roleEvidenceHandler{},
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
	verifiedTool, err := shared.VerifiedToolOutcomeVerifier(delegation)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: meteredModel, Catalog: registry, Executor: registry,
		Approvals: interactionadapter.New(approvals), ApprovalPolicies: []plugin.ApprovalPolicy{policy},
		ToolOutcomeVerifier: verifiedTool,
		RunMiddleware:       []plugin.RunMiddleware{shared}, ModelMiddleware: []plugin.ModelMiddleware{shared}, ToolMiddleware: []plugin.ToolMiddleware{shared}})
	if err != nil {
		t.Fatal(err)
	}
	handoffs, err := handoff.New(direct)
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
	// The same physical grant identifies the delegated child Run and its
	// original frozen child owner; a forged root-only grant is not authority.
	grant := harness.Item{
		ID: "role-child-approval-grant", TurnID: started.Turn.ID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		InvocationID: rootInvocation.ID, RunID: rootInvocation.ExecutionRefID,
		ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"resolve-subtask-approval","physicalRunID":"` + task.RunID + `"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, fresh, appendErr := store.AppendItem(t.Context(), grant); appendErr != nil || !fresh {
		t.Fatalf("create delegated grant: fresh=%v err=%v", fresh, appendErr)
	}
	if _, blockedErr := runner.CancelSubtask(t.Context(), started.Turn.ID, task.ID, "cancel during delegated approval"); !errors.Is(blockedErr, harness.ErrConflict) {
		t.Fatalf("subtask cancellation ignored physical child Run grant: %v", blockedErr)
	}
	if _, _, closeErr := store.AppendItem(t.Context(), harness.Item{
		ID: testAuthorityReceiptID("hdispatchdone", grant.ID), TurnID: started.Turn.ID,
		InvocationID: grant.InvocationID, RunID: grant.RunID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		ParentItemID: grant.ID, Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
		CreatedAt: now, UpdatedAt: now,
	}); closeErr != nil {
		t.Fatal(closeErr)
	}
	beforeItems, err := store.ListItems(t.Context(), started.Turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
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
	approved, err := runner.ResolveSubtaskApproval(t.Context(), started.Turn.ID, task.ID, task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
	if wrongRoute {
		afterItems, listErr := store.ListItems(t.Context(), started.Turn.ID, 0, 500)
		if !errors.Is(err, harness.ErrConflict) || wrong.calls != 0 || listErr != nil || len(afterItems) != len(beforeItems) {
			t.Fatalf("substitute approval route dispatched or minted grant: calls=%d err=%v items=%d->%d listErr=%v", wrong.calls, err, len(beforeItems), len(afterItems), listErr)
		}
		return
	}
	if err != nil || approved.Subtasks[0].Status != "completed" {
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
