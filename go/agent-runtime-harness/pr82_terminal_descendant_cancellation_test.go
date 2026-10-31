package harness_test

import (
	"encoding/json"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

// Independent final-HEAD merge audit: child Feature root is physically
// Failed but still has an active, related Kernel member without a Harness
// Invocation. CancelInvocation must not settle as though descendants ended.
func TestAuditPR82FailedChildInvocationCannotSkipLiveRelationDescendantCancellation(t *testing.T) {
	_, turnID, parentItemID, relations, _, store, rt := newFeatureInvocationHarness(t)
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: loadingFeatureAgent{runtime: rt}, Store: store,
		Clock: featureInvocationClock{}, Teams: auditRunningTeamFeature{runtime: rt}, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "audit-child-failed-owner", Goal: "team with active member",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "remain running"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	child := teamChildInvocation(t, started)
	parent, err := rt.Load(t.Context(), child.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	member, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "audit-child-grandchild-still-running", Kind: agent.RunKind, Actor: parent.Run.Actor,
		Thread: parent.Run.Thread, RequestID: "related-member", Goal: "do not skip cancellation", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = relations.Ensure(t.Context(), runrelation.Draft{ParentRunID: child.ExecutionRefID,
		ChildRunID: member.Run.ID, Kind: runrelation.KindTeamMember, OwnerNodeID: "member"})
	if err != nil {
		t.Fatal(err)
	}
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
		Clock: featureInvocationClock{}, Teams: auditRunningTeamFeature{runtime: rt}, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "audit-subtask-failed-owner", Goal: "failed team still owns member",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "still running"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	child := teamChildInvocation(t, started)
	parent, err := rt.Load(t.Context(), child.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	member, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "audit-subtask-member-still-running", Kind: agent.RunKind, Actor: parent.Run.Actor,
		Thread: parent.Run.Thread, RequestID: "related-member", Goal: "must be cancelled", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = relations.Ensure(t.Context(), runrelation.Draft{ParentRunID: child.ExecutionRefID,
		ChildRunID: member.Run.ID, Kind: runrelation.KindTeamMember, OwnerNodeID: "member"})
	if err != nil {
		t.Fatal(err)
	}
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
	runner, turnID, _, relations, rootRunID, store, rt := newFeatureInvocationHarness(t)
	root, err := rt.Load(t.Context(), rootRunID)
	if err != nil {
		t.Fatal(err)
	}
	member, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "audit-root-failed-member-still-running", Kind: agent.RunKind, Actor: root.Run.Actor,
		Thread: root.Run.Thread, RequestID: "related-member", Goal: "root failed but member active", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = relations.Ensure(t.Context(), runrelation.Draft{ParentRunID: rootRunID,
		ChildRunID: member.Run.ID, Kind: runrelation.KindTeamMember, OwnerNodeID: "member"})
	if err != nil {
		t.Fatal(err)
	}
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
