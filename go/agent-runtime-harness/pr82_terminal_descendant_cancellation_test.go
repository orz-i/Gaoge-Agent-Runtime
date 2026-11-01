package harness_test

import (
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

// Independent final-HEAD merge audit: child Feature root is physically
// Failed but still has an active, related Kernel member without a Harness
// Invocation. CancelInvocation must not settle as though descendants ended.
func TestAuditPR82FailedChildInvocationCannotSkipLiveRelationDescendantCancellation(t *testing.T) {
	_, turnID, parentItemID, relations, _, store, rt := newFeatureInvocationHarness(t)
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: loadingFeatureAgent{runtime: rt}, Store: store,
		Clock: featureInvocationClock{}, Teams: pr82LocalTeam(t, rt, relations), Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	child := pr82StartLocalTeamChild(t, runner, turnID, parentItemID)
	parent, err := rt.Load(t.Context(), child.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	member := pr82LocalMember(t, rt, parent)
	failed, err := rt.Apply(t.Context(), parent.Run.ID, parent.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusFailed, State: parent.State, ErrorCode: "feature.failed", ErrorDetail: "failure while member remains running",
	})
	if err != nil || failed.Run.Status != kernel.RunStatusFailed {
		t.Fatalf("fixture failed: %v %s", err, failed.Run.Status)
	}
	result, cancelErr := runner.CancelInvocation(t.Context(), child.ID, "cancel child and all members")
	living, loadErr := rt.Load(t.Context(), member.Run.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	items, err := store.ListItems(t.Context(), turnID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if cancelErr != nil || living.Run.Status != kernel.RunStatusCancelled || harness.ActiveCancellationFence(items) {
		t.Fatalf("child CancelInvocation did not clean local related member: child=%s grandchild=%s intent=%v err=%v", child.Status, living.Run.Status, harness.ActiveCancellationFence(items), cancelErr)
	}
	if result.Turn.Status != harness.TurnRunning {
		t.Fatalf("child-only cleanup incorrectly terminalized root Turn: %s", result.Turn.Status)
	}
}

// The Subtask route uses a materialized parent status as an early return.
// A failed composite parent does not prove its RunRelation descendants ended.
func TestAuditPR82CancelSubtaskFailedCompositeDoesNotSkipLiveMember(t *testing.T) {
	_, turnID, parentItemID, relations, _, store, rt := newFeatureInvocationHarness(t)
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: loadingFeatureAgent{runtime: rt}, Store: store,
		Clock: featureInvocationClock{}, Teams: pr82LocalTeam(t, rt, relations), Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	child := pr82StartLocalTeamChild(t, runner, turnID, parentItemID)
	parent, err := rt.Load(t.Context(), child.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	member := pr82LocalMember(t, rt, parent)
	if _, err = rt.Apply(t.Context(), parent.Run.ID, parent.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusFailed, State: parent.State, ErrorCode: "feature.failed", ErrorDetail: "member still running",
	}); err != nil {
		t.Fatal(err)
	}
	observed, err := runner.Load(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	taskID := ""
	for _, task := range observed.Subtasks {
		if task.RunID == child.ExecutionRefID {
			taskID = task.ID
			if task.Status != "failed" {
				t.Fatalf("expected already-failed composite Subtask: %s", task.Status)
			}
		}
	}
	if taskID == "" {
		t.Fatalf("no relevant Subtask: %+v", observed.Subtasks)
	}
	_, cancelErr := runner.CancelSubtask(t.Context(), turnID, taskID, "cancel member after failed Team")
	living, err := rt.Load(t.Context(), member.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.ListItems(t.Context(), turnID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if cancelErr != nil || living.Run.Status != kernel.RunStatusCancelled || harness.ActiveCancellationFence(items) {
		t.Fatalf("failed Subtask cleanup did not terminate local Member: err=%v member=%s fence=%v", cancelErr, living.Run.Status, harness.ActiveCancellationFence(items))
	}
}

// Parent Kernel and Harness Turn are both Failed, but RunRelation still owns
// a live Kernel child. Root Cancel must not silently return the cached Turn.
func TestAuditPR82FailedRootTurnCannotSkipLiveRunRelationDescendant(t *testing.T) {
	rt := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	relations := newFeatureInvocationRelations(t)
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: loadingFeatureAgent{runtime: rt}, Store: store, Clock: featureInvocationClock{}, Teams: pr82LocalTeam(t, rt, relations), Relations: relations})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runner.StartTeamTurn(t.Context(), harness.TeamTurnRequest{StartRequest: harness.StartRequest{
		HostThread: harness.HostRef{Kind: testThreadKind, ID: "local-root-thread"}, HostTurn: harness.HostRef{Kind: testContextHostKind, ID: "local-root-turn"}, Actor: kernel.ActorRef{TenantID: testTenant, ActorID: testActor}, Thread: kernel.ThreadRef{Kind: testThreadKind, ID: "local-root-thread"}, RequestID: "local-root", Goal: "local root", Config: harness.ConfigSnapshot{Model: "fixture-model"}}, Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "continues"}}})
	if err != nil && !errors.Is(err, team.ErrMemberPending) {
		t.Fatal(err)
	}
	rootInvocation, ok := harness.TopLevelInvocation(started)
	if !ok {
		t.Fatal("no root")
	}
	turnID, rootRunID := started.Turn.ID, rootInvocation.ExecutionRefID
	root, err := rt.Load(t.Context(), rootRunID)
	if err != nil {
		t.Fatal(err)
	}
	member := pr82LocalMember(t, rt, root)
	if _, err = rt.Apply(t.Context(), rootRunID, root.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusFailed, State: root.State, ErrorCode: "root.failed", ErrorDetail: "member remains active",
	}); err != nil {
		t.Fatal(err)
	}
	projected, err := runner.Refresh(t.Context(), turnID)
	if err != nil || projected.Turn.Status != harness.TurnFailed {
		t.Fatalf("failed root projection: status=%s err=%v", projected.Turn.Status, err)
	}
	cancelled, cancelErr := runner.Cancel(t.Context(), turnID, "cancel residual children")
	still, err := rt.Load(t.Context(), member.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.ListItems(t.Context(), turnID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if cancelErr != nil || still.Run.Status != kernel.RunStatusCancelled || harness.ActiveCancellationFence(items) || cancelled.Turn.Status != harness.TurnFailed {
		t.Fatalf("failed root cleanup must cancel Member and preserve Failed outcome: err=%v member=%s intent=%v root=%s", cancelErr, still.Run.Status, harness.ActiveCancellationFence(items), cancelled.Turn.Status)
	}
}
