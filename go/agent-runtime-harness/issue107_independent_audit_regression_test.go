package harness_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestFinalAuditParentTerminalCannotApproveLiveChild(t *testing.T) {
	for _, terminalKind := range []string{"cancelled"} {
		t.Run(terminalKind, func(t *testing.T) {
			f := newIssue107HarnessFixture(t, true, true)
			waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
			if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
				t.Fatalf("fixture %v %+v", err, waiting.Subtasks)
			}
			root, ok := harness.TopLevelInvocation(waiting)
			if !ok {
				t.Fatal("missing root")
			}
			parent, err := f.runtime.Load(t.Context(), root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.runtime.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "parent terminated before approval")
			if err != nil {
				t.Fatal(err)
			}
			childID := waiting.Subtasks[0].RunID
			task := waiting.Subtasks[0]
			after, err := f.runtime.Load(t.Context(), childID)
			if err != nil || after.Run.Status != kernel.RunStatusWaitingInput {
				t.Fatalf("child fixture %+v %v", after.Run, err)
			}
			_, err = f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID, task.Approval.CheckpointID,
				harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
			t.Logf("approval outcome parent=cancelled: %v", err)
			if err == nil {
				approved, loadErr := f.runtime.Load(t.Context(), childID)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				_, resumeErr := f.agent.Resume(t.Context(), childID, approved.Run.Revision)
				t.Logf("post-cancellation child Tool execution: resumeErr=%v toolCalls=%d", resumeErr, f.toolCalls)
				t.Fatal("child approval authorized after parent Workflow cancelled")
			}
		})
	}
}

func TestFinalAuditCancelledAgentWithInflightToolCannotSettleAsCleanup(t *testing.T) {
	f := newIssue107HarnessFixture(t, true, true)
	f.toolStarted = make(chan struct{})
	f.toolRelease = make(chan struct{})
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
		t.Fatalf("fixture: %v %+v", err, waiting.Subtasks)
	}
	task := waiting.Subtasks[0]
	_, err = f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID, task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	child, err := f.runtime.Load(t.Context(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := f.agent.Resume(context.Background(), child.Run.ID, child.Run.Revision); done <- e }()
	select {
	case <-f.toolStarted:
	case <-time.After(3 * time.Second):
		close(f.toolRelease)
		t.Fatal("Tool handler did not begin")
	}
	physical, err := f.runtime.Load(t.Context(), task.RunID)
	if err != nil {
		close(f.toolRelease)
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	if err = json.Unmarshal(physical.State, &state); err != nil {
		close(f.toolRelease)
		t.Fatal(err)
	}
	if string(state["toolDispatchID"]) == "" || string(state["toolDispatchID"]) == "null" {
		close(f.toolRelease)
		t.Fatal("test did not persist Tool dispatch ID")
	}
	_, err = f.runtime.Cancel(t.Context(), task.RunID, physical.Run.Revision, "cancel during unresolved Tool")
	if err != nil {
		close(f.toolRelease)
		t.Fatalf("Kernel CAS cancel: %v", err)
	}
	confirmed, cancelErr := f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "reconcile physical child")
	t.Logf("Harness CancelSubtask after terminal Kernel cancel while Tool callback unreturned: cancelErr=%v; subtasks=%+v", cancelErr, confirmed.Subtasks)
	close(f.toolRelease)
	select {
	case e := <-done:
		t.Logf("late Tool callback completed; resumeErr=%v; externalCalls=%d", e, f.toolCalls)
	case <-time.After(3 * time.Second):
		t.Fatal("late Tool callback did not return")
	}
	if cancelErr == nil {
		t.Fatal("Harness confirmed cleanup although persisted Tool dispatch was not reconciled and callback was still active")
	}
}

func TestFinalAuditFailedWorkflowCannotApproveLiveAgentChild(t *testing.T) {
	f := newIssue107HarnessFixture(t, true, true)
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
		t.Fatalf("fixture: %+v err=%v", waiting.Subtasks, err)
	}
	root, ok := harness.TopLevelInvocation(waiting)
	if !ok {
		t.Fatal("missing root")
	}
	parent, err := f.runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.runtime.Apply(t.Context(), parent.Run.ID, parent.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusFailed, State: parent.State, ErrorCode: "workflow.failed", ErrorDetail: "unrecoverable parent fault",
	})
	if err != nil {
		t.Fatal(err)
	}
	task := waiting.Subtasks[0]
	_, err = f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID, task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
	t.Logf("child approval after parent failure: %v", err)
	if err == nil {
		t.Fatal("failed parent Run granted a new approval")
	}
}
