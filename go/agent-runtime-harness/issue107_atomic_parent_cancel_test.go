package harness_test

import (
	"context"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestIssue107ParentCancelCannotDeclareTerminalWithLiveChildTool(t *testing.T) {
	f := newIssue107HarnessFixture(t, true, true)
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
		t.Fatalf("fixture: %+v %v", waiting.Subtasks, err)
	}
	root, ok := harness.TopLevelInvocation(waiting)
	if !ok {
		t.Fatal("missing original root")
	}
	task := waiting.Subtasks[0]
	_, err = f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID,
		task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
	if err != nil {
		t.Fatal(err)
	}
	child, err := f.runtime.Load(t.Context(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	f.toolStarted = make(chan struct{})
	f.toolRelease = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, e := f.agent.Resume(context.Background(), child.Run.ID, child.Run.Revision); done <- e }()
	select {
	case <-f.toolStarted:
	case <-time.After(4 * time.Second):
		close(f.toolRelease)
		t.Fatal("Tool did not start")
	}
	_, cancelErr := f.harness.Cancel(t.Context(), waiting.Turn.ID, "stop while local Tool is active")
	parent, err := f.runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil {
		close(f.toolRelease)
		t.Fatal(err)
	}
	if cancelErr == nil || parent.Run.Status != kernel.RunStatusRunning {
		close(f.toolRelease)
		t.Fatalf("parent was declared cancelled during an admitted physical Tool: err=%v status=%s", cancelErr, parent.Run.Status)
	}
	close(f.toolRelease)
	select {
	case e := <-done:
		if e != nil {
			t.Logf("late Tool return was conservatively rejected: %v", e)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Tool callback did not return")
	}
	// A later request must be allowed to reconcile the same original Turn
	// after the child Tool has committed its receipt and released the Grant.
	final, err := f.harness.Cancel(t.Context(), waiting.Turn.ID, "retry after Tool receipt")
	if err != nil || final.Turn.Status != harness.TurnCancelled {
		t.Fatalf("previous busy parent could not be safely reconciled: turn=%+v err=%v", final.Turn, err)
	}
	if _, err = f.runtime.LookupChildExecution(t.Context(), child.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("physical Grant was not retired after valid receipt: %v", err)
	}
}
