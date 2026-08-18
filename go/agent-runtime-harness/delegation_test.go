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
	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const (
	delegationTestModelName        = "frozen-delegation-model"
	delegationTestSpecialistResult = "specialist result"
	delegationTestRootSynthesis    = "root synthesis"
	delegationContextTurnID        = "turn-delegation-context"
)

func TestDelegationToolNameIsProviderPortable(t *testing.T) {
	t.Parallel()
	registration := harness.DelegationToolRegistration(harness.NewDelegationToolHandler())
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(registration.Definition.Name) {
		t.Fatalf("delegation Tool name is not provider-portable: %q", registration.Definition.Name)
	}
}

func TestHarnessDelegationChildDoesNotInheritParentContextSnapshot(t *testing.T) {
	t.Parallel()
	capture := &delegationModel{}
	runner, relations := newDelegationHarnessWithOptions(t, capture, 2, true)
	goal := "delegate the research and synthesize it"
	completed, err := runner.Start(t.Context(), harness.StartRequest{
		HostThread: harness.HostRef{Kind: testThreadKind, ID: "thread-delegation-context"},
		HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: delegationContextTurnID},
		Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
		Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "thread-delegation-context"},
		Goal:       goal,
		Config: harness.ConfigSnapshot{
			Model:        delegationTestModelName,
			ToolKeys:     []string{harness.DelegationToolKey},
			ToolPolicies: []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot()},
		},
		Context: &harness.ContextSeed{
			ThreadPathHash: harness.ContextPathHash("message-delegation-context", delegationContextTurnID),
			CurrentTurnID:  delegationContextTurnID,
			Items: []runtimecontext.Item{{
				ID: "message-delegation-context", TurnID: delegationContextTurnID,
				Kind: runtimecontext.ItemMessage, Role: runtimecontext.RoleUser, Content: goal, Required: true,
			}},
		},
	})
	if err != nil {
		t.Fatalf("run context-bound delegated Harness turn: %v", err)
	}
	assertDelegationCompleted(t, completed, relations, capture)
	requests := capture.requestsCopy()
	if len(requests) != 3 || requests[1].Messages[len(requests[1].Messages)-1].Content != "analyze the evidence" {
		t.Fatalf("delegated child inherited parent context: %#v", requests)
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

func TestHarnessDelegationRelationsWaitForRootTerminalState(t *testing.T) {
	t.Parallel()
	client := &blockingDelegationModel{
		secondChildStarted: make(chan struct{}),
		releaseSecondChild: make(chan struct{}),
	}
	runner, relations := newDelegationHarnessWithModel(t, client, 4)
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "thread-delegation-terminal"}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "turn-delegation-terminal"}
	rootRunID := delegationRootRunID(t, hostThread, hostTurn)
	result := startBlockingDelegationHarness(t, runner, hostThread, hostTurn)
	waitForSecondDelegation(t, client.secondChildStarted)
	assertNoDelegationRelations(t, relations, rootRunID)
	close(client.releaseSecondChild)
	assertTerminalDelegationRelations(t, relations, result)
}

func TestHarnessDelegatesDifferentGoalsToSameMember(t *testing.T) {
	t.Parallel()
	client := &sameMemberDelegationModel{}
	runner, relations := newDelegationHarnessWithModel(t, client, 4)
	completed, err := runner.Start(t.Context(), harness.StartRequest{
		HostThread: harness.HostRef{Kind: testThreadKind, ID: "thread-delegation-same-member"},
		HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "turn-delegation-same-member"},
		Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
		Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "thread-delegation-same-member"},
		Goal:       "delegate two goals to the same specialist and synthesize them",
		Config: harness.ConfigSnapshot{
			Model:        delegationTestModelName,
			ToolKeys:     []string{harness.DelegationToolKey},
			ToolPolicies: []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot()},
		},
	})
	if err != nil || completed.Turn.Status != harness.TurnCompleted {
		t.Fatalf("completed snapshot=%#v err=%v", completed.Turn, err)
	}
	items := itemsOfKind(completed.Items, harness.ItemDelegation)
	if len(items) != 4 || items[1].RunID == items[3].RunID {
		t.Fatalf("same-member delegation items = %#v", items)
	}
	children, err := relations.ListChildren(t.Context(), completed.Turn.RootRunID)
	if err != nil || len(children) != 2 {
		t.Fatalf("same-member delegation relations=%#v err=%v", children, err)
	}
}

type blockingDelegationGuard struct {
	calls int
}

func (guard *blockingDelegationGuard) GuardDelegation(
	_ context.Context,
	_ tools.ExecutionRequest,
	input harness.DelegateRequest,
) error {
	guard.calls++
	if input.MemberID != "researcher" {
		return tools.ErrInvalidCall
	}
	return tools.NewRecoverableCallErrorWithBlockedTools(
		"story.delegation_budget_exhausted",
		"publish now",
		tools.ErrInvalidCall,
		harness.DelegationToolKey,
	)
}

func TestHarnessDelegationGuardRemovesBlockedToolFromLaterTurns(t *testing.T) {
	t.Parallel()
	capture := &delegationModel{}
	guard := &blockingDelegationGuard{}
	runner, relations := newDelegationHarnessWithModel(t, capture, 2, guard)
	completed, err := runner.Start(t.Context(), harness.StartRequest{
		HostThread: harness.HostRef{Kind: testThreadKind, ID: "thread-delegation-guard"},
		HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "turn-delegation-guard"},
		Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
		Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "thread-delegation-guard"},
		Goal:       "respect the product delegation budget",
		Config: harness.ConfigSnapshot{
			Model:        delegationTestModelName,
			ToolKeys:     []string{harness.DelegationToolKey},
			ToolPolicies: []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot()},
		},
	})
	if err != nil || completed.Turn.Status != harness.TurnCompleted || guard.calls != 1 {
		t.Fatalf("completed=%#v err=%v guard calls=%d", completed.Turn, err, guard.calls)
	}
	children, err := relations.ListChildren(t.Context(), completed.Turn.RootRunID)
	if err != nil || len(children) != 0 {
		t.Fatalf("guarded delegation children=%#v err=%v", children, err)
	}
	requests := capture.requestsCopy()
	if len(requests) != 2 || len(requests[0].Tools) != 1 || len(requests[1].Tools) != 0 {
		t.Fatalf("guarded delegation requests=%#v", requests)
	}
}

type delegationStartResult struct {
	snapshot harness.Snapshot
	err      error
}

func delegationRootRunID(t *testing.T, hostThread harness.HostRef, hostTurn harness.HostRef) string {
	t.Helper()
	sessionID, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	return harness.RootRunID(turnID)
}

func startBlockingDelegationHarness(
	t *testing.T,
	runner *harness.Runner,
	hostThread harness.HostRef,
	hostTurn harness.HostRef,
) <-chan delegationStartResult {
	t.Helper()
	result := make(chan delegationStartResult, 1)
	go func() {
		snapshot, err := runner.Start(t.Context(), harness.StartRequest{
			HostThread: hostThread,
			HostTurn:   hostTurn,
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "thread-delegation-terminal"},
			Goal:       "delegate two analyses and synthesize them",
			Config: harness.ConfigSnapshot{
				Model:        delegationTestModelName,
				ToolKeys:     []string{harness.DelegationToolKey},
				ToolPolicies: []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot()},
			},
		})
		result <- delegationStartResult{snapshot: snapshot, err: err}
	}()
	return result
}

func waitForSecondDelegation(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("second delegated child did not start")
	}
}

func assertNoDelegationRelations(t *testing.T, relations *runrelation.Registry, rootRunID string) {
	t.Helper()
	children, err := relations.ListChildren(t.Context(), rootRunID)
	if err != nil || len(children) != 0 {
		t.Fatalf("relations were projected before root terminal state: %#v, err=%v", children, err)
	}
}

func assertTerminalDelegationRelations(
	t *testing.T,
	relations *runrelation.Registry,
	result <-chan delegationStartResult,
) {
	t.Helper()
	select {
	case completed := <-result:
		if completed.err != nil || completed.snapshot.Turn.Status != harness.TurnCompleted {
			t.Fatalf("completed snapshot=%#v err=%v", completed.snapshot.Turn, completed.err)
		}
		children, err := relations.ListChildren(t.Context(), completed.snapshot.Turn.RootRunID)
		if err != nil || len(children) != 2 {
			t.Fatalf("terminal delegation relations=%#v err=%v", children, err)
		}
	case <-time.After(time.Second):
		t.Fatal("delegated Harness turn did not complete")
	}
}

func newDelegationHarness(t *testing.T) (*harness.Runner, *runrelation.Registry, *delegationModel) {
	t.Helper()
	capture := &delegationModel{}
	runner, relations := newDelegationHarnessWithModel(t, capture, 2)
	return runner, relations, capture
}

func newDelegationHarnessWithModel(
	t *testing.T,
	client model.Client,
	maxToolCalls int,
	guards ...harness.DelegationToolGuard,
) (*harness.Runner, *runrelation.Registry) {
	return newDelegationHarnessWithOptions(t, client, maxToolCalls, false, guards...)
}

func newDelegationHarnessWithOptions(
	t *testing.T,
	client model.Client,
	maxToolCalls int,
	contextAware bool,
	guards ...harness.DelegationToolGuard,
) (*harness.Runner, *runrelation.Registry) {
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
	delegationTool := harness.NewDelegationToolHandler(guards...)
	registry, err := tools.NewRegistry([]tools.Registration{harness.DelegationToolRegistration(delegationTool)})
	if err != nil {
		t.Fatal(err)
	}
	dependencies := delegationAgentDependencies(runtime, client, registry, policy, maxToolCalls, contextAware)
	agentRunner, err := agent.NewRunner(dependencies)
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
	harnessDependencies := delegationHarnessDependencies(runtime, agentRunner, store, registry, handoffs, relations, contextAware)
	runner, err := harness.NewRunner(harnessDependencies)
	if err != nil {
		t.Fatal(err)
	}
	if err = delegationTool.Bind(runner); err != nil {
		t.Fatal(err)
	}
	return runner, relations
}

func delegationAgentDependencies(
	runtime *kernel.Runtime,
	client model.Client,
	registry *tools.Registry,
	policy plugin.ApprovalPolicy,
	maxToolCalls int,
	contextAware bool,
) agent.Dependencies {
	dependencies := agent.Dependencies{
		Runtime: runtime, Model: client, Catalog: registry, Executor: registry,
		ApprovalPolicies: []plugin.ApprovalPolicy{policy},
		Limits:           agent.Limits{MaxLLMCalls: 4, MaxToolCalls: maxToolCalls},
	}
	if contextAware {
		dependencies.ModelMiddleware = []plugin.ModelMiddleware{harness.NewContextModelMiddleware()}
	}
	return dependencies
}

func delegationHarnessDependencies(
	runtime *kernel.Runtime,
	agentRunner *agent.Runner,
	store harness.Store,
	registry *tools.Registry,
	handoffs *handoff.Coordinator,
	relations *runrelation.Registry,
	contextAware bool,
) harness.Dependencies {
	dependencies := harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: store, Clock: delegationTestClock{}, Catalog: registry,
		Handoffs: handoffs, Relations: relations,
	}
	if contextAware {
		dependencies.Context = runtimecontext.NewBuilder(runtimecontext.Dependencies{})
	}
	return dependencies
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

type blockingDelegationModel struct {
	mu                 sync.Mutex
	rootCalls          int
	childCalls         int
	secondChildStarted chan struct{}
	releaseSecondChild chan struct{}
}

type sameMemberDelegationModel struct {
	rootCalls int
}

func (client *sameMemberDelegationModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	if len(request.Tools) == 0 {
		return model.Response{Content: delegationTestSpecialistResult}, nil
	}
	client.rootCalls++
	if client.rootCalls > 1 {
		return model.Response{Content: delegationTestRootSynthesis}, nil
	}
	return model.Response{ToolCalls: []tools.Call{
		{ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"memberID":"architect","goal":"analyze structure"}`)},
		{ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"memberID":"architect","goal":"analyze visuals"}`)},
	}}, nil
}

func (client *blockingDelegationModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.mu.Lock()
	if len(request.Tools) > 0 {
		client.rootCalls++
		rootCall := client.rootCalls
		client.mu.Unlock()
		if rootCall == 1 {
			return model.Response{ToolCalls: []tools.Call{
				{ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"memberID":"architect","goal":"analyze structure"}`)},
				{ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"memberID":"previs","goal":"analyze visuals"}`)},
			}}, nil
		}
		return model.Response{Content: delegationTestRootSynthesis}, nil
	}
	client.childCalls++
	childCall := client.childCalls
	client.mu.Unlock()
	if childCall == 2 {
		close(client.secondChildStarted)
		<-client.releaseSecondChild
	}
	return model.Response{Content: delegationTestSpecialistResult}, nil
}

func (client *delegationModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.mu.Lock()
	client.requests = append(client.requests, model.CloneRequest(request))
	client.mu.Unlock()
	if len(request.Tools) == 0 {
		return model.Response{Content: delegationTestSpecialistResult}, nil
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role == model.RoleTool {
		return model.Response{Content: delegationTestRootSynthesis}, nil
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
