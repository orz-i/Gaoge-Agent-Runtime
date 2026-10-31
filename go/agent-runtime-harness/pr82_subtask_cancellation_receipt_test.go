package harness_test

import (
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

func TestIndependentPR82DirectRemoteRelationSubtaskMustNotSettle(t *testing.T) {
	runner, turnID, _, relations, rootRunID, store, rt := newFeatureInvocationHarness(t)
	root, err := rt.Load(t.Context(), rootRunID)
	if err != nil {
		t.Fatal(err)
	}
	children := &pr82OrphanMemberChildren{runs: map[string]kernel.Snapshot{}}
	remote, err := children.StartRun(t.Context(), agent.StartRequest{ID: "independent-remote-direct-target", Actor: root.Run.Actor, Thread: root.Run.Thread, RequestID: "remote-target", Goal: "continues"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = relations.Ensure(t.Context(), runrelation.Draft{ParentRunID: rootRunID, ChildRunID: remote.Run.ID, Kind: runrelation.KindTeamMember, OwnerNodeID: "remote-member"})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := runner.Load(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	taskID := ""
	for _, task := range observed.Subtasks {
		if task.RunID == remote.Run.ID {
			taskID = task.ID
		}
	}
	if taskID == "" {
		t.Fatal("remote relation missing from Subtasks")
	}
	_, cancelErr := runner.CancelSubtask(t.Context(), turnID, taskID, "stop remote member")
	still, err := children.LoadRun(t.Context(), remote.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.ListItems(t.Context(), turnID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(cancelErr, harness.ErrRuntimeCancellationUnverified) || !harness.ActiveCancellationFence(items) || still.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("direct remote target cancellation must remain unknown: err=%v fence=%v physical=%s", cancelErr, harness.ActiveCancellationFence(items), still.Run.Status)
	}
}

func TestIndependentPR82FailedSubtaskMustNotConfirmUnknownDescendants(t *testing.T) {
	_, turnID, parentItemID, relations, _, store, rt := newFeatureInvocationHarness(t)
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: loadingFeatureAgent{runtime: rt}, Store: store, Clock: featureInvocationClock{}, Teams: auditRunningTeamFeature{runtime: rt}, Relations: relations})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{ParentItemID: parentItemID, RequestID: "independent-failed-parent", Goal: "failed team", Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "continues"}}})
	if err != nil {
		t.Fatal(err)
	}
	child := teamChildInvocation(t, started)
	parent, err := rt.Load(t.Context(), child.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	children := &pr82OrphanMemberChildren{runs: map[string]kernel.Snapshot{}}
	remote, err := children.StartRun(t.Context(), agent.StartRequest{ID: "independent-remote-descendant", Actor: parent.Run.Actor, Thread: parent.Run.Thread, RequestID: "remote-child", Goal: "continues"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = relations.Ensure(t.Context(), runrelation.Draft{ParentRunID: parent.Run.ID, ChildRunID: remote.Run.ID, Kind: runrelation.KindTeamMember, OwnerNodeID: "remote-member"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = rt.Apply(t.Context(), parent.Run.ID, parent.Run.Revision, kernel.Mutation{Status: kernel.RunStatusFailed, State: parent.State, ErrorCode: "team.failed", ErrorDetail: "remote member still running"})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := runner.Load(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	taskID := ""
	for _, task := range observed.Subtasks {
		if task.RunID == parent.Run.ID {
			taskID = task.ID
		}
	}
	if taskID == "" {
		t.Fatal("failed Team missing from Subtasks")
	}
	_, cancelErr := runner.CancelSubtask(t.Context(), turnID, taskID, "stop descendants")
	if !errors.Is(cancelErr, harness.ErrRuntimeCancellationUnverified) {
		t.Fatalf("expected unverified cleanup, got %v", cancelErr)
	}
	after, err := runner.Load(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range after.Subtasks {
		if task.ID == taskID {
			if task.CancelStatus != "failed" || task.CancelError == "" {
				t.Fatalf("unknown subtree cleanup must remain visible: status=%s cancelStatus=%s cancelError=%q", task.Status, task.CancelStatus, task.CancelError)
			}
			return
		}
	}
	t.Fatal("failed Team disappeared")
}
