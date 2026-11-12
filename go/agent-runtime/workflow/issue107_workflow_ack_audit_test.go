package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

type auditWorkflowPostMarkAckLossStore struct {
	*memory.Store
	marks int
}

func (s *auditWorkflowPostMarkAckLossStore) MarkChildExecutionSettled(
	ctx context.Context, grant kernel.ChildExecutionGrant, permit kernel.ChildExecutionSettlement) error {
	if err := s.Store.MarkChildExecutionSettled(ctx, grant, permit); err != nil {
		return err
	}
	s.marks++
	return kernel.ErrExecutionBusy // physical callback finished, marker committed, ACK lost
}

// The Agent has ReconcileChildPhysicalGrant for this exact durable state.
// Workflow does not. It cannot call StartRun again on the existing Run,
// and Resume cannot drive a terminal child. Even though the Store safely
// recorded a settled revision, no public Workflow Runner recovery operation
// can retire this *already proven* original Grant after a lost Mark ACK.
func TestAuditIssue107WorkflowCompletedMarkAckLostCannotReconcileParent(t *testing.T) {
	store := &auditWorkflowPostMarkAckLossStore{Store: memory.NewStore()}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "parent", Kind: workflow.RunKind, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "original root", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	effect := &auditRevisionEffect{}
	runner := newWorkflowRunner(t, rt, effect)
	definition := compileWorkflow(t, []workflow.Node{
		effectNode("external", "external.once"),
		returnNode(json.RawMessage(`{"ok":true}`)),
	}, workflow.Limits{})
	req := workflowRequest(definition)
	req.ID = "completed-child"
	req.ParentRunID = parent.Run.ID
	req.Actor = parent.Run.Actor
	req.Thread = parent.Run.Thread
	completed, startErr := runner.StartRun(t.Context(), req)
	if !errors.Is(startErr, kernel.ErrChildExecutionBusy) || completed.Run.Status != kernel.RunStatusCompleted ||
		store.marks != 1 || len(effect.calls) != 1 {
		t.Fatalf("test failed to simulate committed mark + missing ACK: child=%s err=%v marks=%d calls=%v",
			completed.Run.Status, startErr, store.marks, effect.calls)
	}
	original, lookupErr := rt.LookupChildExecution(t.Context(), completed.Run.ID)
	if lookupErr != nil || original.ChildRunID != completed.Run.ID {
		t.Fatalf("original post-commit Grant not observable: %+v err=%v", original, lookupErr)
	}
	if err = runner.ReconcileChildPhysicalGrant(t.Context(), completed.Run.ID); err != nil {
		t.Fatalf("completed Workflow could not retire a durably marked original Grant: %v", err)
	}
	if _, err = rt.LookupChildExecution(t.Context(), completed.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("settled original Grant was still live or replayable: %v", err)
	}
	_, cancelErr := rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "after completed child marker")
	if cancelErr != nil {
		t.Fatalf("parent remained locked after trusted Workflow receipt: %v", cancelErr)
	}
	if len(effect.calls) != 1 {
		t.Fatalf("Workflow recovery retriggered a physical Effect: calls=%v", effect.calls)
	}
}
