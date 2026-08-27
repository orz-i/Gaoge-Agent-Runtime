package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/planexecute"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

const (
	retryTeamMemberGoal = "draft"
	retryTeamMemberID   = "writer"
)

func TestTypedFeatureInvocationsShareHarnessTurnAndRecoverByTurnID(t *testing.T) {
	t.Parallel()
	runner, turnID, parentItemID, relations, parentRunID, _, _ := newFeatureInvocationHarness(t)
	assertStartedTeamInvocation(t, runner, turnID, parentItemID)
	assertStartedPlanInvocation(t, runner, turnID, parentItemID)
	assertStartedWorkflowInvocation(t, runner, turnID, parentItemID)
	assertRecoveredFeatureInvocationTree(t, runner, turnID, relations, parentRunID)
}

func TestRetryInvocationAdvancesOneAttemptAndReplaysTerminalAttempt(t *testing.T) {
	t.Parallel()
	runner, turnID, parentItemID, _, _, store, _ := newFeatureInvocationHarness(t)
	started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "retry-team", Goal: "retry this team",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: retryTeamMemberID, Goal: retryTeamMemberGoal}},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := teamChildInvocation(t, started)
	failed := markInvocationFailed(t, store, first)

	retried, err := runner.RetryInvocation(t.Context(), failed.ID)
	if err != nil {
		t.Fatalf("retry invocation: %v", err)
	}
	second := teamChildInvocation(t, retried)
	if second.Attempt != 2 || second.Status != harness.InvocationCompleted || second.ExecutionRefID == first.ExecutionRefID {
		t.Fatalf("retry did not advance exactly one attempt: first=%#v second=%#v", first, second)
	}
	if got := invocationArtifactCount(retried.Items, second.ID); got != 2 {
		t.Fatalf("retry artifacts=%d items=%#v", got, retried.Items)
	}

	replayed, err := runner.RetryInvocation(t.Context(), second.ID)
	if err != nil {
		t.Fatalf("replay terminal retry: %v", err)
	}
	replayedInvocation := teamChildInvocation(t, replayed)
	if replayedInvocation.Attempt != 2 || replayedInvocation.ExecutionRefID != second.ExecutionRefID {
		t.Fatalf("terminal retry replay allocated another attempt: %#v", replayedInvocation)
	}
}

func TestRetryInvocationRecoversAcceptedAttemptWithoutAllocatingAnother(t *testing.T) {
	t.Parallel()
	runner, turnID, parentItemID, _, _, store, _ := newFeatureInvocationHarness(t)
	started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "retry-crash-team", Goal: "recover retry",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: retryTeamMemberID, Goal: retryTeamMemberGoal}},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := teamChildInvocation(t, started)
	failed := markInvocationFailed(t, store, first)
	accepted, err := store.RetryInvocation(t.Context(), failed.ID, failed.Revision, "retry-crash-execution", featureInvocationClock{}.Now())
	if err != nil || accepted.Attempt != 2 || accepted.Status != harness.InvocationAccepted {
		t.Fatalf("seed accepted retry: %#v err=%v", accepted, err)
	}

	recovered, err := runner.RetryInvocation(t.Context(), accepted.ID)
	if err != nil {
		t.Fatalf("recover accepted retry: %v", err)
	}
	invocation := teamChildInvocation(t, recovered)
	if invocation.Attempt != 2 || invocation.ExecutionRefID != accepted.ExecutionRefID || invocation.Status != harness.InvocationCompleted {
		t.Fatalf("accepted retry recovery allocated a new attempt: %#v", invocation)
	}
}

func teamChildInvocation(t *testing.T, snapshot harness.Snapshot) harness.Invocation {
	t.Helper()
	for _, invocation := range snapshot.Invocations {
		if invocation.ExecutionClass == harness.ExecutionTeam && invocation.ParentItemID != "" {
			return invocation
		}
	}
	t.Fatalf("missing child Team invocation: %#v", snapshot.Invocations)
	return harness.Invocation{}
}

func markInvocationFailed(t *testing.T, store *harness.MemoryStore, invocation harness.Invocation) harness.Invocation {
	t.Helper()
	invocation.Status = harness.InvocationFailed
	invocation.ErrorCode = "fixture.failed"
	invocation.ErrorDetail = "retry fixture"
	invocation.UpdatedAt = featureInvocationClock{}.Now()
	updated, err := store.UpdateInvocation(t.Context(), invocation, invocation.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func invocationArtifactCount(items []harness.Item, invocationID string) int {
	count := 0
	for _, item := range items {
		if item.Kind == harness.ItemArtifact && item.InvocationID == invocationID {
			count++
		}
	}
	return count
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
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "command-workflow-turn"},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "command-workflow-thread"},
			RequestID:  "command-workflow-request", Goal: "run the explicit workflow command",
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

func TestTopLevelFeatureReplayResumesPersistedRunningExecution(t *testing.T) {
	runner, feature := newBlockedWorkflowHarness(t, true)
	request := blockedWorkflowTurnRequest("resume")
	result := make(chan error, 1)
	go func() {
		_, startErr := runner.StartWorkflowTurn(t.Context(), request)
		result <- startErr
	}()
	defer finishBlockedWorkflowStart(t, feature, result)
	waitForBlockedWorkflowStart(t, feature)

	replayed, err := runner.StartWorkflowTurn(t.Context(), request)
	assertRunningWorkflowResumed(t, replayed, feature, err)
}

func TestCancelAcceptedTopLevelInvocationWithoutRuntimeRunIsDurable(t *testing.T) {
	runner, feature := newBlockedWorkflowHarness(t, false)
	request := blockedWorkflowTurnRequest("cancel-before-run")
	result := make(chan error, 1)
	go func() {
		_, startErr := runner.StartWorkflowTurn(t.Context(), request)
		result <- startErr
	}()
	defer finishBlockedWorkflowStart(t, feature, result)
	waitForBlockedWorkflowStart(t, feature)

	turnID := workflowTurnID(t, request)
	cancelled, err := runner.Cancel(t.Context(), turnID, "cancel before runtime create")
	assertDurableTopLevelCancellation(t, cancelled, err)
	assertSyntheticCancellationLoadable(t, runner, turnID)
	assertCancelledWorkflowDoesNotReplay(t, runner, request, feature)
}

func TestTopLevelFeatureStartFailureWithoutRuntimeRunRemainsLoadable(t *testing.T) {
	t.Parallel()
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: unusedFeatureAgent{}, Store: store, Clock: featureInvocationClock{},
		Workflows: rejectedWorkflowFeature{},
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, startErr := runner.StartWorkflowTurn(t.Context(), harness.WorkflowTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: "rejected-workflow-thread"},
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "rejected-workflow-turn"},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "rejected-workflow-thread"},
			RequestID:  "rejected-workflow-request", Goal: "reject before runtime create",
			Config: harness.ConfigSnapshot{Model: "fixture-model"},
		},
		Input: json.RawMessage(`{"goal":"reject before runtime create"}`),
	})
	if !errors.Is(startErr, errRejectedTopLevelWorkflow) || failed.Turn.Status != harness.TurnFailed {
		t.Fatalf("failed start snapshot=%#v err=%v", failed, startErr)
	}
	reloaded, err := runner.Load(t.Context(), failed.Turn.ID)
	if err != nil || reloaded.Turn.Status != harness.TurnFailed {
		t.Fatalf("reload failed start snapshot=%#v err=%v", reloaded, err)
	}
}

func TestCancelCascadesAcrossCapabilityRunTree(t *testing.T) {
	t.Parallel()
	runner, turnID, parentItemID, relations, parentRunID, store, runtime := newFeatureInvocationHarness(t)
	now := featureInvocationClock{}.Now()
	childRun := createRunningFeatureRun(t, runtime, "cancel-child-run", team.RunKind)
	nestedRun := createRunningFeatureRun(t, runtime, "cancel-nested-run", agent.RunKind)
	childInvocation := harness.Invocation{
		ID: "hiv_cancel_child", TurnID: turnID, ParentItemID: parentItemID,
		CapabilityKey: harness.CapabilityTeam, DefinitionVersion: harness.RuntimeCapabilityVersion,
		ExecutionClass: harness.ExecutionTeam, InputHash: "cancel-child", ExecutionRefID: childRun.Run.ID,
		Status: harness.InvocationRunning, Attempt: 1, OutputRefs: []harness.HostRef{}, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateInvocation(t.Context(), childInvocation); err != nil {
		t.Fatal(err)
	}
	for _, draft := range []runrelation.Draft{
		{ParentRunID: parentRunID, ChildRunID: childRun.Run.ID, Kind: runrelation.KindCapability, OwnerNodeID: childInvocation.ID},
		{ParentRunID: childRun.Run.ID, ChildRunID: nestedRun.Run.ID, Kind: runrelation.KindTeamMember, OwnerNodeID: "writer"},
	} {
		if _, err := relations.Ensure(t.Context(), draft); err != nil {
			t.Fatal(err)
		}
	}

	cancelled, err := runner.Cancel(t.Context(), turnID, "operator request")
	if err != nil || cancelled.Turn.Status != harness.TurnCancelled {
		t.Fatalf("cancel tree snapshot=%#v err=%v", cancelled, err)
	}
	if invocationByID(t, cancelled, childInvocation.ID).Status != harness.InvocationCancelled {
		t.Fatalf("child invocation was not cancelled: %#v", cancelled.Invocations)
	}
	for _, runID := range []string{parentRunID, childRun.Run.ID, nestedRun.Run.ID} {
		loaded, loadErr := runtime.Load(t.Context(), runID)
		if loadErr != nil || loaded.Run.Status != kernel.RunStatusCancelled {
			t.Fatalf("run %s was not cancelled: %#v err=%v", runID, loaded.Run, loadErr)
		}
	}
}

func TestRetryInvocationReopensTopLevelFeatureTurn(t *testing.T) {
	t.Parallel()
	runtime := newFeatureInvocationRuntime(t)
	feature := &retryTopLevelWorkflowFeature{runtime: runtime}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: unusedFeatureAgent{}, Store: harness.NewMemoryStore(),
		Clock: featureInvocationClock{}, Workflows: feature,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, startErr := runner.StartWorkflowTurn(t.Context(), harness.WorkflowTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: "retry-command-thread"},
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "retry-command-turn"},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "retry-command-thread"},
			RequestID:  "retry-command-request", Goal: "retry explicit workflow",
			Config: harness.ConfigSnapshot{Model: "fixture-model"},
		},
		Input: json.RawMessage(`{"goal":"retry explicit workflow"}`),
	})
	invocation := requireFailedTopLevelWorkflow(t, failed, startErr)
	retried, err := runner.RetryInvocation(t.Context(), invocation.ID)
	if err != nil {
		t.Fatalf("retry top-level invocation: %v", err)
	}
	assertCompletedTopLevelRetry(t, retried, invocation)
}

func requireFailedTopLevelWorkflow(t *testing.T, failed harness.Snapshot, err error) harness.Invocation {
	t.Helper()
	if !errors.Is(err, errRetryTopLevelWorkflow) || failed.Turn.Status != harness.TurnFailed {
		t.Fatalf("initial top-level failure = %#v, err=%v", failed.Turn, err)
	}
	invocation, ok := harness.TopLevelInvocation(failed)
	if !ok || invocation.ParentItemID != "" {
		t.Fatalf("top-level invocation = %#v", failed.Invocations)
	}
	return invocation
}

func assertCompletedTopLevelRetry(t *testing.T, retried harness.Snapshot, previous harness.Invocation) {
	t.Helper()
	retriedInvocation, ok := harness.TopLevelInvocation(retried)
	if !ok || retried.Turn.Status != harness.TurnCompleted || retriedInvocation.Status != harness.InvocationCompleted ||
		retriedInvocation.Attempt != 2 || retriedInvocation.ExecutionRefID == previous.ExecutionRefID {
		t.Fatalf("retried top-level snapshot = %#v", retried)
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
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: retryTeamMemberID, Goal: retryTeamMemberGoal}},
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

func newFeatureInvocationHarness(t *testing.T) (*harness.Runner, string, string, *runrelation.Registry, string, *harness.MemoryStore, *kernel.Runtime) {
	t.Helper()
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	relations := newFeatureInvocationRelations(t)
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store, Clock: featureInvocationClock{},
		Teams: completedTeamFeature{runtime}, Plans: completedPlanFeature{runtime}, Workflows: completedWorkflowFeature{runtime},
		Relations: relations, Interactions: noopInteractionResponseHandler{},
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
	return runner, turnID, parentItemID, relations, parentRunID, store, runtime
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

func (unusedFeatureAgent) Resume(context.Context, string, uint64) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, harness.ErrInvalidRequest
}

func (unusedFeatureAgent) ResolveApproval(context.Context, string, uint64, plugin.ApprovalResponse) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, harness.ErrInvalidRequest
}

type loadingFeatureAgent struct {
	unusedFeatureAgent
	runtime *kernel.Runtime
}

func (feature loadingFeatureAgent) Resume(ctx context.Context, runID string, _ uint64) (kernel.Snapshot, error) {
	return feature.runtime.Load(ctx, runID)
}

type noopInteractionResponseHandler struct{}

func (noopInteractionResponseHandler) HandleInteractionResponse(
	context.Context,
	harness.InteractionResponseContext,
) (harness.InteractionResponseResult, error) {
	return harness.InteractionResponseResult{}, nil
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

func (feature completedWorkflowFeature) ResolveWait(
	ctx context.Context,
	runID string,
	_ uint64,
	_ json.RawMessage,
) (kernel.Snapshot, error) {
	return feature.runtime.Load(ctx, runID)
}

var errRejectedTopLevelWorkflow = errors.New("rejected before runtime create")

type rejectedWorkflowFeature struct{}

func (rejectedWorkflowFeature) StartRun(context.Context, workflow.StartRequest) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, errRejectedTopLevelWorkflow
}

func (rejectedWorkflowFeature) Resume(context.Context, string, uint64) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, kernel.ErrNotFound
}

func (rejectedWorkflowFeature) ResolveWait(
	context.Context,
	string,
	uint64,
	json.RawMessage,
) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, kernel.ErrNotFound
}

type blockedWorkflowFeature struct {
	runtime     *kernel.Runtime
	createRun   bool
	entered     chan struct{}
	release     chan struct{}
	startCalls  atomic.Int32
	resumeCalls atomic.Int32
}

func newBlockedWorkflowFeature(runtime *kernel.Runtime, createRun bool) *blockedWorkflowFeature {
	return &blockedWorkflowFeature{
		runtime: runtime, createRun: createRun, entered: make(chan struct{}, 1), release: make(chan struct{}),
	}
}

func newBlockedWorkflowHarness(t *testing.T, createRun bool) (*harness.Runner, *blockedWorkflowFeature) {
	t.Helper()
	runtime := newFeatureInvocationRuntime(t)
	feature := newBlockedWorkflowFeature(runtime, createRun)
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: unusedFeatureAgent{}, Store: harness.NewMemoryStore(), Clock: featureInvocationClock{},
		Workflows: feature,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner, feature
}

func (feature *blockedWorkflowFeature) StartRun(
	ctx context.Context,
	request workflow.StartRequest,
) (kernel.Snapshot, error) {
	call := feature.startCalls.Add(1)
	var snapshot kernel.Snapshot
	var err error
	if feature.createRun {
		snapshot, err = feature.runtime.Create(ctx, kernel.CreateRequest{
			ID: request.ID, Kind: workflow.RunKind, Actor: request.Actor, Thread: request.Thread,
			RequestID: request.RequestID, Goal: request.Goal, State: json.RawMessage(`{}`),
		})
		if err != nil {
			return kernel.Snapshot{}, err
		}
	}
	if call == 1 {
		feature.entered <- struct{}{}
		select {
		case <-feature.release:
		case <-ctx.Done():
			return kernel.Snapshot{}, ctx.Err()
		}
	}
	if !feature.createRun {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	return snapshot, nil
}

func (feature *blockedWorkflowFeature) Resume(
	ctx context.Context,
	runID string,
	_ uint64,
) (kernel.Snapshot, error) {
	feature.resumeCalls.Add(1)
	return feature.runtime.Load(ctx, runID)
}

func (feature *blockedWorkflowFeature) ResolveWait(
	ctx context.Context,
	runID string,
	_ uint64,
	_ json.RawMessage,
) (kernel.Snapshot, error) {
	return feature.runtime.Load(ctx, runID)
}

func blockedWorkflowTurnRequest(suffix string) harness.WorkflowTurnRequest {
	threadID := "blocked-workflow-thread-" + suffix
	return harness.WorkflowTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: threadID},
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "blocked-workflow-turn-" + suffix},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: threadID},
			RequestID:  "blocked-workflow-request-" + suffix,
			Goal:       "recover blocked workflow " + suffix,
			Config:     harness.ConfigSnapshot{Model: "fixture-model"},
		},
		Input: json.RawMessage(`{"fixture":true}`),
	}
}

func workflowTurnID(t *testing.T, request harness.WorkflowTurnRequest) string {
	t.Helper()
	sessionID, err := harness.SessionID(request.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, request.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	return turnID
}

func waitForBlockedWorkflowStart(t *testing.T, feature *blockedWorkflowFeature) {
	t.Helper()
	select {
	case <-feature.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("workflow feature did not enter StartRun")
	}
}

func finishBlockedWorkflowStart(t *testing.T, feature *blockedWorkflowFeature, result <-chan error) {
	t.Helper()
	close(feature.release)
	select {
	case <-result:
	case <-time.After(5 * time.Second):
		t.Error("blocked workflow StartRun did not finish")
	}
}

func assertRunningWorkflowResumed(
	t *testing.T,
	snapshot harness.Snapshot,
	feature *blockedWorkflowFeature,
	err error,
) {
	t.Helper()
	if err != nil || snapshot.Turn.Status != harness.TurnRunning || feature.resumeCalls.Load() != 1 {
		t.Fatalf("running workflow replay did not resume: turn=%#v resumeCalls=%d err=%v", snapshot.Turn, feature.resumeCalls.Load(), err)
	}
}

func assertDurableTopLevelCancellation(t *testing.T, snapshot harness.Snapshot, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("cancel accepted invocation without runtime run: %v", err)
	}
	invocation, ok := harness.TopLevelInvocation(snapshot)
	if !ok || snapshot.Turn.Status != harness.TurnCancelled || invocation.Status != harness.InvocationCancelled {
		t.Fatalf("missing durable cancellation: turn=%#v invocations=%#v", snapshot.Turn, snapshot.Invocations)
	}
}

func assertSyntheticCancellationLoadable(t *testing.T, runner *harness.Runner, turnID string) {
	t.Helper()
	loaded, err := runner.Load(t.Context(), turnID)
	if err != nil || loaded.Turn.Status != harness.TurnCancelled {
		t.Fatalf("load synthetic cancellation: turn=%#v err=%v", loaded.Turn, err)
	}
}

func assertCancelledWorkflowDoesNotReplay(
	t *testing.T,
	runner *harness.Runner,
	request harness.WorkflowTurnRequest,
	feature *blockedWorkflowFeature,
) {
	t.Helper()
	replayed, err := runner.StartWorkflowTurn(t.Context(), request)
	if err != nil || replayed.Turn.Status != harness.TurnCancelled || feature.startCalls.Load() != 1 {
		t.Fatalf("cancelled start replayed execution: turn=%#v startCalls=%d err=%v", replayed.Turn, feature.startCalls.Load(), err)
	}
}

var errRetryTopLevelWorkflow = errors.New("retry top-level workflow fixture")

type retryTopLevelWorkflowFeature struct {
	runtime *kernel.Runtime
	starts  int
}

func (feature *retryTopLevelWorkflowFeature) StartRun(ctx context.Context, request workflow.StartRequest) (kernel.Snapshot, error) {
	feature.starts++
	if feature.starts > 1 {
		return completeFeatureRun(ctx, feature.runtime, request.ID, workflow.RunKind, request.Actor, request.Thread, request.RequestID, request.Goal)
	}
	started, err := feature.runtime.Create(ctx, kernel.CreateRequest{
		ID: request.ID, Kind: workflow.RunKind, Actor: request.Actor, Thread: request.Thread,
		RequestID: request.RequestID, Goal: request.Goal, State: json.RawMessage(`{}`),
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	failed, failErr := feature.runtime.Apply(ctx, request.ID, started.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusFailed, State: started.State,
		ErrorCode: "fixture.failed", ErrorDetail: errRetryTopLevelWorkflow.Error(),
	})
	return failed, errors.Join(errRetryTopLevelWorkflow, failErr)
}

func (feature *retryTopLevelWorkflowFeature) Resume(ctx context.Context, runID string, _ uint64) (kernel.Snapshot, error) {
	return feature.runtime.Load(ctx, runID)
}

func (feature *retryTopLevelWorkflowFeature) ResolveWait(
	ctx context.Context,
	runID string,
	_ uint64,
	_ json.RawMessage,
) (kernel.Snapshot, error) {
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

func createRunningFeatureRun(t *testing.T, runtime *kernel.Runtime, id string, kind kernel.RunKind) kernel.Snapshot {
	t.Helper()
	created, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: id, Kind: kind,
		Actor:     kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
		Thread:    kernel.ThreadRef{Kind: testThreadKind, ID: "feature-thread"},
		RequestID: id, Goal: id, State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

type featureInvocationClock struct{}

func (featureInvocationClock) Now() time.Time {
	return time.Date(2026, time.August, 19, 4, 0, 0, 0, time.UTC)
}
