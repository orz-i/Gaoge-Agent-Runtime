package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Trigger a parent Kernel termination immediately AFTER the original
// Harness Store committed an approval dispatch grant, but BEFORE the Agent
// middleware is called. The post-grant check must still reject approval,
// settle the rejected grant and leave the child available for safe cleanup.
type issue107TerminalAfterGrantStore struct {
	harness.Store
	runtime     *kernel.Runtime
	parentRunID string
	triggered   bool
	failed      error
}

func (store *issue107TerminalAfterGrantStore) AppendItem(
	ctx context.Context, item harness.Item,
) (harness.Item, bool, error) {
	written, fresh, err := store.Store.AppendItem(ctx, item)
	if err != nil || !fresh || item.ParentItemID != harness.ExecutionDispatchParentItemID ||
		store.triggered {
		return written, fresh, err
	}
	var grant struct {
		Phase string `json:"phase"`
	}
	if decodeErr := json.Unmarshal(item.Payload, &grant); decodeErr != nil ||
		grant.Phase != "resolve-subtask-approval" {
		return written, fresh, err
	}
	store.triggered = true
	parent, loadErr := store.runtime.Load(ctx, store.parentRunID)
	if loadErr != nil {
		store.failed = loadErr
		return written, fresh, err
	}
	_, store.failed = store.runtime.Cancel(ctx, parent.Run.ID, parent.Run.Revision, "interleaved parent termination")
	return written, fresh, err
}

func TestIssue107ParentTerminalAfterApprovalGrantFailsBeforeMiddleware(t *testing.T) {
	f := newIssue107HarnessFixture(t, true, true)
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
		t.Fatalf("fixture: %+v err=%v", waiting.Subtasks, err)
	}
	root, ok := harness.TopLevelInvocation(waiting)
	if !ok {
		t.Fatal("missing root Invocation")
	}
	store := &issue107TerminalAfterGrantStore{Store: f.store, runtime: f.runtime, parentRunID: root.ExecutionRefID}
	approver, err := harness.NewRunner(harness.Dependencies{
		Runtime: f.runtime, Agent: f.agent, Workflows: f.workflow,
		Store: store, Relations: f.relations, Clock: featureInvocationClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	task := waiting.Subtasks[0]
	_, err = approver.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID,
		task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
	if !store.triggered || store.failed != nil || !errors.Is(err, harness.ErrConflict) || f.toolCalls != 0 {
		t.Fatalf("post-grant ancestor check failed: trigger=%v cancelErr=%v approvalErr=%v tools=%d",
			store.triggered, store.failed, err, f.toolCalls)
	}
	child, err := f.runtime.Load(t.Context(), task.RunID)
	if err != nil || child.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("grant wrongly performed physical approval: %+v err=%v", child.Run, err)
	}
	// The rejected physical Grant must have a committed no-effect receipt.
	// A terminated parent still permits child cleanup through its original
	// owner chain, rather than stranding a waiting Tool approval.
	cleaned, err := f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "clean terminal parent's child")
	if err != nil {
		t.Fatalf("no-effect grant incorrectly fenced genuine cleanup: %v", err)
	}
	child, err = f.runtime.Load(t.Context(), task.RunID)
	if err != nil || child.Run.Status != kernel.RunStatusCancelled || f.toolCalls != 0 ||
		len(cleaned.Subtasks) != 1 || cleaned.Subtasks[0].CancelStatus != "confirmed" {
		t.Fatalf("safe child cleanup failed: child=%+v err=%v tools=%d subtasks=%+v",
			child.Run, err, f.toolCalls, cleaned.Subtasks)
	}
}
