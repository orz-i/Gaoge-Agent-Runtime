package harness_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/planexecute"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/team"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/workflow"
)

func TestTypedFeatureInvocationsShareHarnessTurnAndRecoverByTurnID(t *testing.T) {
	t.Parallel()
	runner, turnID, parentItemID, relations, parentRunID := newFeatureInvocationHarness(t)
	assertStartedTeamInvocation(t, runner, turnID, parentItemID)
	assertStartedPlanInvocation(t, runner, turnID, parentItemID)
	assertStartedWorkflowInvocation(t, runner, turnID, parentItemID)
	assertRecoveredFeatureInvocationTree(t, runner, turnID, relations, parentRunID)
}

func TestWorkflowCanOwnTopLevelHarnessTurnWithoutAgentRoot(t *testing.T) {
	t.Parallel()
	runtime := newFeatureInvocationRuntime(t)
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: unusedFeatureAgent{}, Store: harness.NewMemoryStore(), Clock: featureInvocationClock{},
		Workflows: completedWorkflowFeature{runtime},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := harness.WorkflowTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: "command-workflow-thread"},
			HostTurn: harness.HostRef{Kind: testContextHostKind, ID: "command-workflow-turn"},
			Actor: kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread: kernel.ThreadRef{Kind: testThreadKind, ID: "command-workflow-thread"},
			RequestID: "command-workflow-request", Goal: "run the explicit workflow command",
			Config: harness.ConfigSnapshot{Model: "fixture-model"},
		},
		Input: json.RawMessage(`{"goal":"run the explicit workflow command"}`),
	}
	first, err := runner.StartWorkflowTurn(t.Context(), request)
	assertTopLevelWorkflowTurn(t, first, err)
	replayed, err := runner.StartWorkflowTurn(t.Context(), request)
	assertTopLevelWorkflowTurn(t, replayed, err)
	if replayed.Turn.ID != first.Turn.ID || replayed.Invocations[0].ID != first.Invocations[0].ID {
		t.Fatalf("replay changed durable identity: first=%#v replayed=%#v", first, replayed)
	}
}

func assertTopLevelWorkflowTurn(t *testing.T, snapshot harness.Snapshot, err error) {
	t.Helper()
	if err != nil || snapshot.Turn.Status != harness.TurnCompleted || snapshot.Output == nil {
		t.Fatalf("top-level workflow snapshot=%#v err=%v", snapshot, err)
	}
	assertTopLevelWorkflowInvocation(t, snapshot.Invocations)
	assertNoPlaceholderAgentItem(t, snapshot.Items)
}

func assertTopLevelWorkflowInvocation(t *testing.T, invocations []harness.Invocation) {
	t.Helper()
	if len(invocations) != 1 || invocations[0].ExecutionClass != harness.ExecutionWorkflow ||
		invocations[0].CapabilityKey != harness.CapabilityWorkflow || invocations[0].ParentItemID != "" ||
		invocations[0].Status != harness.InvocationCompleted {
		t.Fatalf("top-level workflow invocations=%#v", invocations)
	}
}

func assertNoPlaceholderAgentItem(t *testing.T, items []harness.Item) {
	t.Helper()
	for _, item := range items {
		if item.Kind == harness.ItemAgentRun {
			t.Fatalf("explicit workflow command created placeholder Agent item: %#v", items)
		}
	}
}

func assertStartedTeamInvocation(t *testing.T, runner *harness.Runner, turnID, parentItemID string) {
	t.Helper()
	snapshot, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "team-1", Goal: "compare approaches",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "writer", Goal: "draft"}},
	})
	if err != nil {
		t.Fatalf("start Team invocation: %v", err)
	}
	assertChildInvocation(t, snapshot, harness.ExecutionTeam)
}

func assertStartedPlanInvocation(t *testing.T, runner *harness.Runner, turnID, parentItemID string) {
	t.Helper()
	snapshot, err := runner.StartPlanExecuteInvocation(t.Context(), turnID, harness.PlanExecuteInvocationRequest{
		ParentItemID: parentItemID, RequestID: "plan-1", Goal: "plan work", Model: "fixture-model",
		ApprovalPolicy: planexecute.ApprovalAuto, MaxSteps: 2,
	})
	if err != nil {
		t.Fatalf("start PlanExecute invocation: %v", err)
	}
	assertChildInvocation(t, snapshot, harness.ExecutionPlanExecute)
}

func assertStartedWorkflowInvocation(t *testing.T, runner *harness.Runner, turnID, parentItemID string) {
	t.Helper()
	snapshot, err := runner.StartWorkflowInvocation(t.Context(), turnID, harness.WorkflowInvocationRequest{
		ParentItemID: parentItemID, RequestID: "workflow-1", Goal: "run workflow", Input: json.RawMessage(`{"value":1}`),
	})
	if err != nil {
		t.Fatalf("start Workflow invocation: %v", err)
	}
	assertChildInvocation(t, snapshot, harness.ExecutionWorkflow)
}

func assertRecoveredFeatureInvocationTree(
	t *testing.T,
	runner *harness.Runner,
	turnID string,
	relations *runrelation.Registry,
	parentRunID string,
) {
	t.Helper()
	reloaded, err := runner.Load(t.Context(), turnID)
	if err != nil {
		t.Fatalf("reload Harness Turn: %v", err)
	}
	assertRecoveredInvocationClasses(t, reloaded)
	assertRecoveredInvocationArtifacts(t, reloaded)
	assertRecoveredCapabilityRelations(t, relations, parentRunID)
}

func assertRecoveredInvocationClasses(t *testing.T, reloaded harness.Snapshot) {
	t.Helper()
	if len(reloaded.Invocations) != 4 {
		t.Fatalf("durable invocations=%#v", reloaded.Invocations)
	}
	want := map[harness.ExecutionClass]bool{
		harness.ExecutionAgent: false, harness.ExecutionTeam: false,
		harness.ExecutionPlanExecute: false, harness.ExecutionWorkflow: false,
	}
	for _, invocation := range reloaded.Invocations {
		want[invocation.ExecutionClass] = true
	}
	for class, found := range want {
		if !found {
			t.Fatalf("missing durable %s invocation: %#v", class, reloaded.Invocations)
		}
	}
}

func assertRecoveredInvocationArtifacts(t *testing.T, reloaded harness.Snapshot) {
	t.Helper()
	if got := completedChildArtifactCount(reloaded.Items); got != 3 {
		t.Fatalf("completed child artifacts=%d items=%#v", got, reloaded.Items)
	}
}

func assertRecoveredCapabilityRelations(t *testing.T, relations *runrelation.Registry, parentRunID string) {
	t.Helper()
	children, err := relations.ListChildren(t.Context(), parentRunID)
	if err != nil || len(children) != 3 {
		t.Fatalf("capability relations=%#v err=%v", children, err)
	}
	for _, relation := range children {
		if relation.Kind != runrelation.KindCapability || relation.OwnerNodeID == "" {
			t.Fatalf("invalid capability relation: %#v", relation)
		}
	}
}

func assertChildInvocation(t *testing.T, snapshot harness.Snapshot, class harness.ExecutionClass) {
	t.Helper()
	for _, invocation := range snapshot.Invocations {
		if invocation.ExecutionClass == class {
			if invocation.Status != harness.InvocationCompleted || invocation.ExecutionRefID == "" || invocation.ParentItemID == "" {
				t.Fatalf("invalid %s invocation: %#v", class, invocation)
			}
			return
		}
	}
	t.Fatalf("missing %s invocation: %#v", class, snapshot.Invocations)
}

func completedChildArtifactCount(items []harness.Item) int {
	count := 0
	for _, item := range items {
		if item.Kind == harness.ItemArtifact && item.Status == harness.ItemCompleted && item.InvocationID != "" {
			count++
		}
	}
	return count
}

func newFeatureInvocationHarness(t *testing.T) (*harness.Runner, string, string, *runrelation.Registry, string) {
	t.Helper()
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	relations := newFeatureInvocationRelations(t)
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: unusedFeatureAgent{}, Store: store, Clock: featureInvocationClock{},
		Teams: completedTeamFeature{runtime}, Plans: completedPlanFeature{runtime}, Workflows: completedWorkflowFeature{runtime},
		Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "feature-thread"}
	sessionID, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "feature-turn"}
	turnID, err := harness.TurnID(sessionID, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: "feature-thread"}
	seedFeatureInvocationEnvelope(t, store, sessionID, turnID, hostThread, hostTurn, actor, now)
	parentRunID, parentItemID := seedFeatureInvocationParent(t, runtime, store, turnID, actor, thread, now)
	return runner, turnID, parentItemID, relations, parentRunID
}

func newFeatureInvocationRuntime(t *testing.T) *kernel.Runtime {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func newFeatureInvocationRelations(t *testing.T) *runrelation.Registry {
	t.Helper()
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return relations
}

func seedFeatureInvocationEnvelope(
	t *testing.T,
	store *harness.MemoryStore,
	sessionID, turnID string,
	hostThread, hostTurn harness.HostRef,
	actor kernel.ActorRef,
	now time.Time,
) {
	t.Helper()
	if _, _, err := store.CreateSession(t.Context(), harness.Session{
		ID: sessionID, HostThread: hostThread, Actor: actor, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	config, err := harness.SealConfigSnapshot(turnID, harness.ConfigSnapshot{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateTurn(t.Context(), harness.Turn{
		ID: turnID, SessionID: sessionID, HostTurn: hostTurn, ConfigSnapshotID: config.ID,
		Status: harness.TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func seedFeatureInvocationParent(
	t *testing.T,
	runtime *kernel.Runtime,
	store *harness.MemoryStore,
	turnID string,
	actor kernel.ActorRef,
	thread kernel.ThreadRef,
	now time.Time,
) (string, string) {
	t.Helper()
	parentRunID := "parent-agent-run"
	if _, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: parentRunID, Kind: agent.RunKind, Actor: actor, Thread: thread, RequestID: "parent", Goal: "coordinate", State: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	parent := harness.Invocation{
		ID: "hiv_parent", TurnID: turnID, CapabilityKey: "runtime.agent", DefinitionVersion: "v1",
		ExecutionClass: harness.ExecutionAgent, InputHash: "parent", ExecutionRefID: parentRunID,
		Status: harness.InvocationRunning, Attempt: 1, OutputRefs: []harness.HostRef{}, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateInvocation(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	parentItemID := "parent-invocation-item"
	if _, _, err := store.AppendItem(t.Context(), harness.Item{
		ID: parentItemID, TurnID: turnID, Kind: harness.ItemInvocation, Status: harness.ItemStarted,
		RunID: parentRunID, InvocationID: parent.ID, Payload: json.RawMessage(`{"executionClass":"agent"}`),
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return parentRunID, parentItemID
}

type unusedFeatureAgent struct{}

func (unusedFeatureAgent) StartRun(context.Context, agent.StartRequest) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, harness.ErrInvalidRequest
}

func (unusedFeatureAgent) ResolveApproval(context.Context, string, uint64, plugin.ApprovalResponse) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, harness.ErrInvalidRequest
}

type completedTeamFeature struct{ runtime *kernel.Runtime }

func (feature completedTeamFeature) StartRun(ctx context.Context, request team.StartRequest) (kernel.Snapshot, error) {
	return completeFeatureRun(ctx, feature.runtime, request.ID, team.RunKind, request.Actor, request.Thread, request.RequestID, request.Goal)
}

func (feature completedTeamFeature) Resume(ctx context.Context, runID string, _ uint64) (kernel.Snapshot, error) {
	return feature.runtime.Load(ctx, runID)
}

type completedPlanFeature struct{ runtime *kernel.Runtime }

func (feature completedPlanFeature) StartRun(ctx context.Context, request planexecute.StartRequest) (kernel.Snapshot, error) {
	return completeFeatureRun(ctx, feature.runtime, request.ID, planexecute.RunKind, request.Actor, request.Thread, request.RequestID, request.Goal)
}

func (feature completedPlanFeature) Resume(ctx context.Context, runID string, _ uint64) (kernel.Snapshot, error) {
	return feature.runtime.Load(ctx, runID)
}

type completedWorkflowFeature struct{ runtime *kernel.Runtime }

func (feature completedWorkflowFeature) StartRun(ctx context.Context, request workflow.StartRequest) (kernel.Snapshot, error) {
	return completeFeatureRun(ctx, feature.runtime, request.ID, workflow.RunKind, request.Actor, request.Thread, request.RequestID, request.Goal)
}

func (feature completedWorkflowFeature) Resume(ctx context.Context, runID string, _ uint64) (kernel.Snapshot, error) {
	return feature.runtime.Load(ctx, runID)
}

func completeFeatureRun(
	ctx context.Context,
	runtime *kernel.Runtime,
	id string,
	kind kernel.RunKind,
	actor kernel.ActorRef,
	thread kernel.ThreadRef,
	requestID, goal string,
) (kernel.Snapshot, error) {
	started, err := runtime.Create(ctx, kernel.CreateRequest{
		ID: id, Kind: kind, Actor: actor, Thread: thread, RequestID: requestID, Goal: goal, State: json.RawMessage(`{}`),
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runtime.Apply(ctx, id, started.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: started.State,
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`{"ok":true}`)},
	})
}

type featureInvocationClock struct{}

func (featureInvocationClock) Now() time.Time {
	return time.Date(2026, time.August, 19, 4, 0, 0, 0, time.UTC)
}
