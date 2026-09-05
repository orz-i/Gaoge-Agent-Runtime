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

func TestRoleDelegationFreezesExecutionAndProjectsChildApproval(t *testing.T) {
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
	roleSchema, err := harness.NewRoleModelMiddleware(store)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: meteredModel, Catalog: registry, Executor: registry,
		Approvals: interactionadapter.New(approvals), ApprovalPolicies: []plugin.ApprovalPolicy{policy},
		RunMiddleware: []plugin.RunMiddleware{shared}, ModelMiddleware: []plugin.ModelMiddleware{roleSchema, shared}, ToolMiddleware: []plugin.ToolMiddleware{shared}})
	if err != nil {
		t.Fatal(err)
	}
	handoffs, err := handoff.New(direct)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{},
		Handoffs: handoffs, Relations: relations, Budget: shared})
	if err != nil {
		t.Fatal(err)
	}
	if err = delegation.Bind(runner); err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 8, MaxToolCalls: 16, MaxTotalTokens: 5000, MaxChildRuns: 3, MaxConcurrentRuns: 1}
	request.Config.ToolKeys = []string{harness.DelegationToolKey, roleEvidenceTool}
	request.Config.ToolPolicies = []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot(), {
		Key: roleEvidenceTool, DefinitionVersion: "v1", ApprovalCapability: "per_call", ApprovalMode: "always",
	}}
	request.Config.Roles = []harness.RoleSnapshot{{ID: "researcher", Revision: 3, Name: "Researcher", Model: "specialist-model", Instructions: "Use frozen evidence.",
		ToolKeys: []string{roleEvidenceTool}, Limits: budget.Limits{MaxLLMCalls: 2},
		Skills: []harness.SkillSnapshot{{ID: "skill-1", Revision: 2, Title: "Evidence", Markdown: "Quote the evidence."}},
	}}
	started, err := runner.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(started.Subtasks) != 1 || started.Subtasks[0].Approval == nil {
		t.Fatalf("subtasks=%+v", started.Subtasks)
	}
	task := started.Subtasks[0]
	if task.Model != "specialist-model" || task.RoleRevision != 3 || task.Status != "waiting_input" {
		t.Fatalf("role=%+v", task)
	}
	if started.Budget == nil || started.Budget.ActiveRuns != 0 || started.Budget.Usage.ChildRuns != 1 {
		t.Fatalf("budget=%+v", started.Budget)
	}
	if _, err = runner.ResolveSubtaskApproval(t.Context(), started.Turn.ID, task.ID, "old-checkpoint", harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove}); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("stale approval=%v", err)
	}
	approved, err := runner.ResolveSubtaskApproval(t.Context(), started.Turn.ID, task.ID, task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
	if err != nil || approved.Subtasks[0].Status != "completed" {
		t.Fatalf("approval=%+v %v", approved.Subtasks, err)
	}
	root, err := runtime.Load(t.Context(), snapshotExecutionRef(t, started))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = direct.Resume(t.Context(), root.Run.ID, root.Run.Revision); err != nil {
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
		if len(call.Tools) != 1 || call.Tools[0].Key != roleEvidenceTool || !strings.Contains(call.Messages[0].Content, "Quote the evidence.") {
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
		response.ToolCalls = []tools.Call{{ID: "delegate", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"roleID":"researcher","goal":"Find evidence"}`)}}
	}
	return response, nil
}
