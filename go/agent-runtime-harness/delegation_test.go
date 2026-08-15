package harness_test

import (
	"context"
	"encoding/json"
	"regexp"
	"sync"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const delegationTestModelName = "frozen-delegation-model"

func TestDelegationToolNameIsProviderPortable(t *testing.T) {
	t.Parallel()
	registration := harness.DelegationToolRegistration(harness.NewDelegationToolHandler())
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(registration.Definition.Name) {
		t.Fatalf("delegation Tool name is not provider-portable: %q", registration.Definition.Name)
	}
}

func TestHarnessDelegationToolStartsStableChildAndRecordsRelation(t *testing.T) {
	t.Parallel()
	runner, relations, capture := newDelegationHarness(t)
	completed, err := runner.Start(t.Context(), harness.StartRequest{
		HostThread: harness.HostRef{Kind: testThreadKind, ID: "thread-delegation"},
		HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "turn-delegation"},
		Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
		Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "thread-delegation"},
		Goal:       "delegate the research and synthesize it",
		Config: harness.ConfigSnapshot{
			Model:        delegationTestModelName,
			ToolKeys:     []string{harness.DelegationToolKey},
			ToolPolicies: []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot()},
		},
	})
	if err != nil {
		t.Fatalf("run delegated Harness turn: %v", err)
	}
	assertDelegationCompleted(t, completed, relations, capture)
}

func newDelegationHarness(t *testing.T) (*harness.Runner, *runrelation.Registry, *delegationModel) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	policy, err := harness.NewFrozenApprovalPolicy(store)
	if err != nil {
		t.Fatal(err)
	}
	delegationTool := harness.NewDelegationToolHandler()
	registry, err := tools.NewRegistry([]tools.Registration{harness.DelegationToolRegistration(delegationTool)})
	if err != nil {
		t.Fatal(err)
	}
	capture := &delegationModel{}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: capture, Catalog: registry, Executor: registry,
		ApprovalPolicies: []plugin.ApprovalPolicy{policy}, Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	handoffs, err := handoff.New(agentRunner)
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), delegationTestClock{})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: store, Clock: delegationTestClock{}, Catalog: registry,
		Handoffs: handoffs, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = delegationTool.Bind(runner); err != nil {
		t.Fatal(err)
	}
	return runner, relations, capture
}

func assertDelegationCompleted(
	t *testing.T,
	snapshot harness.Snapshot,
	relations *runrelation.Registry,
	capture *delegationModel,
) {
	t.Helper()
	if snapshot.Turn.Status != harness.TurnCompleted {
		t.Fatalf("delegated Harness turn status = %s", snapshot.Turn.Status)
	}
	delegationItems := itemsOfKind(snapshot.Items, harness.ItemDelegation)
	if len(delegationItems) != 2 || delegationItems[0].Status != harness.ItemStarted ||
		delegationItems[1].Status != harness.ItemCompleted || delegationItems[1].ParentItemID != delegationItems[0].ID {
		t.Fatalf("delegation items = %#v", delegationItems)
	}
	children, err := relations.ListChildren(t.Context(), snapshot.Turn.RootRunID)
	if err != nil || len(children) != 1 || children[0].Kind != runrelation.KindDelegation ||
		children[0].ChildRunID != delegationItems[1].RunID {
		t.Fatalf("delegation relations = %#v, err=%v", children, err)
	}
	assertDelegationModelRequests(t, capture.requestsCopy())
}

func itemsOfKind(items []harness.Item, kind harness.ItemKind) []harness.Item {
	result := make([]harness.Item, 0, len(items))
	for _, item := range items {
		if item.Kind == kind {
			result = append(result, item)
		}
	}
	return result
}

func assertDelegationModelRequests(t *testing.T, requests []model.Request) {
	t.Helper()
	if len(requests) != 3 {
		t.Fatalf("model request count = %d, want 3", len(requests))
	}
	for _, request := range requests {
		if request.Model != delegationTestModelName {
			t.Fatalf("delegated model changed: %#v", request)
		}
	}
	if len(requests[0].Tools) != 1 || requests[0].Tools[0].Key != harness.DelegationToolKey {
		t.Fatalf("root delegation Tool missing: %#v", requests[0].Tools)
	}
	if len(requests[1].Tools) != 0 {
		t.Fatalf("child inherited root Tools: %#v", requests[1].Tools)
	}
	if len(requests[2].Tools) != 1 {
		t.Fatalf("root Tool set changed after delegation: %#v", requests[2].Tools)
	}
}

type delegationModel struct {
	mu       sync.Mutex
	requests []model.Request
}

func (client *delegationModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.mu.Lock()
	client.requests = append(client.requests, model.CloneRequest(request))
	client.mu.Unlock()
	if len(request.Tools) == 0 {
		return model.Response{Content: "specialist result"}, nil
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role == model.RoleTool {
		return model.Response{Content: "root synthesis"}, nil
	}
	return model.Response{ToolCalls: []tools.Call{{
		ToolKey:   harness.DelegationToolKey,
		Arguments: json.RawMessage(`{"memberID":"researcher","goal":"analyze the evidence"}`),
	}}}, nil
}

func (client *delegationModel) requestsCopy() []model.Request {
	client.mu.Lock()
	defer client.mu.Unlock()
	result := make([]model.Request, len(client.requests))
	for index, request := range client.requests {
		result[index] = model.CloneRequest(request)
	}
	return result
}

type delegationTestClock struct{}

func (delegationTestClock) Now() time.Time {
	return time.Date(2026, 8, 15, 7, 0, 0, 0, time.UTC)
}
