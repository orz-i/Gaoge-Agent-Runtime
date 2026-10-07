package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestDelegationChildRunExhaustionBecomesRecoverableModelGuidance(t *testing.T) {
	store := harness.NewMemoryStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := harness.NewBudgetMiddleware(harness.BudgetMiddlewareDependencies{
		Store: store, Relations: relations, Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	delegation := harness.NewDelegationToolHandler()
	registry, err := tools.NewRegistry([]tools.Registration{harness.DelegationToolRegistration(delegation)})
	if err != nil {
		t.Fatal(err)
	}
	roleSchema, err := harness.NewRoleModelMiddleware(store)
	if err != nil {
		t.Fatal(err)
	}
	client := &childRunLimitModel{t: t}
	direct, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: client, Catalog: registry, Executor: registry,
		RunMiddleware:   []plugin.RunMiddleware{shared},
		ModelMiddleware: []plugin.ModelMiddleware{roleSchema, shared},
		ToolMiddleware:  []plugin.ToolMiddleware{shared},
		Limits:          agent.Limits{MaxLLMCalls: 6, MaxToolCalls: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	handoffs, err := handoff.New(direct)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{},
		Handoffs: handoffs, Relations: relations, Budget: shared,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = delegation.Bind(runner); err != nil {
		t.Fatal(err)
	}

	request := testStartRequest()
	request.Config.SharedBudget = &budget.Limits{MaxChildRuns: 1}
	request.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
	request.Config.ToolKeys = []string{harness.DelegationToolKey}
	request.Config.ToolPolicies = []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot()}
	request.Config.Roles = []harness.RoleSnapshot{{
		ID: "researcher", Revision: 1, Name: "Researcher", Model: "specialist-model",
	}}

	snapshot, err := runner.Start(t.Context(), request)
	if err != nil || snapshot.Turn.Status != harness.TurnCompleted {
		t.Fatalf("turn=%#v err=%v", snapshot.Turn, err)
	}
	if snapshot.Budget == nil || snapshot.Budget.Usage.ChildRuns != 1 {
		t.Fatalf("budget=%#v", snapshot.Budget)
	}
	children, err := relations.ListChildren(t.Context(), snapshotExecutionRef(t, snapshot))
	if err != nil || len(children) != 1 {
		t.Fatalf("relations=%#v err=%v", children, err)
	}
	delegations := itemsOfKind(snapshot.Items, harness.ItemDelegation)
	if len(delegations) != 2 {
		t.Fatalf("exhausted delegation left topology facts: %#v", delegations)
	}
	client.assertRecovered(t)
}

type childRunLimitModel struct {
	t          *testing.T
	mu         sync.Mutex
	rootCalls  int
	childCalls int
	final      model.Request
}

func (client *childRunLimitModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if request.Model == "specialist-model" {
		client.childCalls++
		return model.Response{Content: "child evidence"}, nil
	}
	client.rootCalls++
	switch client.rootCalls {
	case 1, 2:
		callID := "delegate-" + string(rune('0'+client.rootCalls))
		return model.Response{ToolCalls: []tools.Call{{
			ID: callID, ToolKey: harness.DelegationToolKey,
			Arguments: json.RawMessage(`{"roleID":"researcher","goal":"collect evidence"}`),
		}}}, nil
	default:
		client.final = model.CloneRequest(request)
		return model.Response{Content: "finished with available evidence"}, nil
	}
}

func (client *childRunLimitModel) assertRecovered(t *testing.T) {
	t.Helper()
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.rootCalls != 3 || client.childCalls != 1 {
		t.Fatalf("root calls=%d child calls=%d", client.rootCalls, client.childCalls)
	}
	if len(client.final.Tools) != 0 || client.final.RequireToolCall {
		t.Fatalf("delegation remained callable after hard exhaustion: %#v", client.final)
	}
	last := client.final.Messages[len(client.final.Messages)-1]
	if last.Role != model.RoleTool || !strings.Contains(last.Content, "delegation.child_runs_exhausted") {
		t.Fatalf("missing recoverable exhaustion guidance: %#v", last)
	}
}
