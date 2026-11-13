package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

type auditParentedPhysicalEffectBarrier struct {
	entered, release chan struct{}
	calls            atomic.Int32
}

func (e *auditParentedPhysicalEffectBarrier) Execute(ctx context.Context, req workflow.EffectRequest) (workflow.EffectResult, error) {
	e.calls.Add(1)
	e.entered <- struct{}{}
	select {
	case <-e.release:
	case <-ctx.Done():
		return workflow.EffectResult{}, ctx.Err()
	}
	return workflow.EffectResult{Disposition: workflow.DispositionCompleted,
		ReceiptID: "physical_receipt_" + req.EffectID, Output: json.RawMessage(`{"done":true}`)}, nil
}
func TestAuditIssue107ParentedChildSelfCancelBypassesItsOwnPhysicalGrant(t *testing.T) {
	rt := newWorkflowRuntime(t)
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
	parent, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "physical-parent", Kind: workflow.RunKind, Actor: actor, Thread: thread,
		Goal: "parent", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := &auditParentedPhysicalEffectBarrier{
		entered: make(chan struct{}, 1), release: make(chan struct{}),
	}
	runner := newWorkflowRunner(t, rt, executor)
	definition := compileWorkflow(t, []workflow.Node{
		effectNode("external", "physical.tool"),
		returnNode(json.RawMessage(`{"done":true}`)),
	}, workflow.Limits{})
	request := workflowRequest(definition)
	request.ID = "parented-physical-child"
	request.ParentRunID = parent.Run.ID
	request.Actor = actor
	request.Thread = thread
	startDone := make(chan error, 1)
	go func() {
		_, startErr := runner.StartRun(context.Background(), request)
		startDone <- startErr
	}()
	select {
	case <-executor.entered:
	case <-time.After(4 * time.Second):
		close(executor.release)
		t.Fatal("parented original Effect never entered")
	}
	child, loadErr := rt.Load(t.Context(), request.ID)
	if loadErr != nil {
		close(executor.release)
		t.Fatal(loadErr)
	}
	grant, grantErr := rt.LookupChildExecution(t.Context(), request.ID)
	if grantErr != nil {
		close(executor.release)
		t.Fatalf("parented physical Grant was never persisted: %v", grantErr)
	}
	_, parentCancelErr := rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "must stay physically fenced")
	childTerminal, childCancelErr := rt.Cancel(t.Context(), child.Run.ID, child.Run.Revision, "own physical Effect still in flight")
	t.Logf("originalPhysicalGrant=%s parentCancelErr=%v childCancelErr=%v childTerminal=%s effectCalls=%d",
		grant.ID, parentCancelErr, childCancelErr, childTerminal.Run.Status, executor.calls.Load())
	close(executor.release)
	select {
	case <-startDone:
	case <-time.After(4 * time.Second):
		t.Fatal("parented effect callback did not return")
	}
	if !errors.Is(parentCancelErr, kernel.ErrChildExecutionBusy) {
		t.Fatalf("fixture could not prove original parent fence: %v", parentCancelErr)
	}
	if childCancelErr == nil && childTerminal.Run.Status == kernel.RunStatusCancelled {
		t.Fatal("child Kernel self-Cancel committed WHILE its own original physical Grant and external Effect were in flight")
	}
}
