package harness_test

import (
	"context"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestIssue107RecoveryCannotRetireActiveApprovalGrantBeforeMiddleware(t *testing.T) {
	f := newIssue107HarnessFixture(t, true, true, true)
	f.approvalPauseStarted = make(chan struct{})
	f.approvalPauseRelease = make(chan struct{})
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
		t.Fatalf("fixture %v %+v", err, waiting.Subtasks)
	}
	root, ok := harness.TopLevelInvocation(waiting)
	if !ok {
		t.Fatal("missing root")
	}
	task := waiting.Subtasks[0]
	completed := make(chan error, 1)
	go func() {
		_, e := f.harness.ResolveSubtaskApproval(context.Background(), waiting.Turn.ID, task.ID, task.Approval.CheckpointID,
			harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
		completed <- e
	}()
	select {
	case <-f.approvalPauseStarted:
	case <-time.After(4 * time.Second):
		close(f.approvalPauseRelease)
		t.Fatal("approval not paused")
	}
	// The pending checkpoint is clean, but the original physical callback is
	// still active before its first CAS. State cleanliness CANNOT certify that
	// this callback has returned or that no future Tool work can occur.
	err = f.agent.ReconcileChildPhysicalGrant(t.Context(), task.RunID)
	parent, cancelErr := f.runtime.Load(t.Context(), root.ExecutionRefID)
	if cancelErr != nil {
		close(f.approvalPauseRelease)
		t.Fatal(cancelErr)
	}
	_, cancelErr = f.runtime.Cancel(t.Context(), root.ExecutionRefID, parent.Run.Revision, "parent after recovery attempt")
	close(f.approvalPauseRelease)
	select {
	case <-completed:
	case <-time.After(4 * time.Second):
		t.Fatal("approval callback blocked")
	}
	if !errors.Is(err, kernel.ErrChildExecutionBusy) || !errors.Is(cancelErr, kernel.ErrChildExecutionBusy) {
		t.Fatalf("unsettled live Grant incorrectly retired: recovery=%v parentCancel=%v", err, cancelErr)
	}
}
