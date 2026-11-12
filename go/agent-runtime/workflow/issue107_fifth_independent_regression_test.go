package workflow_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// This is a first-party Workflow effect dispatch boundary, with a synthetic
// external write. No real external provider or Tool is called.
type auditNestedWorkflowEffect struct {
	rt              *kernel.Runtime
	parent          kernel.Snapshot
	toolCalls       int
	parentCancelErr error
}

func (e *auditNestedWorkflowEffect) Execute(ctx context.Context, request workflow.EffectRequest) (workflow.EffectResult, error) {
	_, e.parentCancelErr = e.rt.Cancel(ctx, e.parent.Run.ID, e.parent.Run.Revision, "parent terminated during nested Workflow effect")
	// An externally dispatched effect cannot be rolled back by a failed later
	// child Kernel CAS. The test counts the physical send separately.
	e.toolCalls++
	return workflow.EffectResult{Disposition: workflow.DispositionCompleted,
		ReceiptID: "external_tool_receipt", Output: json.RawMessage(`{"effect":"sent"}`)}, nil
}

func TestAuditIssue107NestedSubworkflowPhysicalEffectOutlivesParentCancel(t *testing.T) {
	rt := newWorkflowRuntime(t)
	parent, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "nested-parent", Kind: workflow.RunKind,
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "parent", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	exec := &auditNestedWorkflowEffect{rt: rt, parent: parent}
	runner := newWorkflowRunner(t, rt, exec)
	definition := compileWorkflow(t, []workflow.Node{
		effectNode("invoke", "external.effect"),
		returnNode(json.RawMessage(`{"done":true}`)),
	}, workflow.Limits{})
	request := workflowRequest(definition)
	request.ID = "nested-child"
	request.ParentRunID = parent.Run.ID
	request.Actor = parent.Run.Actor
	request.Thread = parent.Run.Thread
	child, childErr := runner.StartRun(t.Context(), request)
	parentAfter, readErr := rt.Load(t.Context(), parent.Run.ID)
	t.Logf("parent=%s cancelErr=%v child=%s childErr=%v physical_effect_count=%d",
		parentAfter.Run.Status, exec.parentCancelErr, child.Run.Status, childErr, exec.toolCalls)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if exec.parentCancelErr == nil && exec.toolCalls > 0 && parentAfter.Run.Status == kernel.RunStatusCancelled {
		t.Fatal("parent Workflow terminal CAS committed while an unfenced nested Workflow physically dispatched its external Effect")
	}
}

type auditResumeEffectExecutor struct{ calls int }

func (e *auditResumeEffectExecutor) Execute(_ context.Context, _ workflow.EffectRequest) (workflow.EffectResult, error) {
	e.calls++
	return workflow.EffectResult{Disposition: workflow.DispositionCompleted, ReceiptID: "simulated_tool_done",
		Output: json.RawMessage(`{"ok":true}`)}, nil
}

// The parent has already reached a terminal status BEFORE child Resume.
// There is no concurrency uncertainty: a persisted nested Workflow Wait can
// still continue and physically dispatch a new Effect after the parent died.
func TestAuditIssue107NestedWorkflowWaitResumeAfterParentAlreadyCancelled(t *testing.T) {
	rt := newWorkflowRuntime(t)
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
	parent, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "parent-wait", Kind: workflow.RunKind, Actor: actor, Thread: thread,
		Goal: "parent", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	exec := &auditResumeEffectExecutor{}
	runner := newWorkflowRunner(t, rt, exec)
	definition := compileWorkflow(t, []workflow.Node{
		{ID: "wait", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "input.approval", Payload: json.RawMessage(`{"required":true}`)}},
		effectNode("post_cancel_tool", "message.send"),
		returnNode(json.RawMessage(`{"done":true}`)),
	}, workflow.Limits{})
	request := workflowRequest(definition)
	request.ID = "nested-waiting-child"
	request.ParentRunID = parent.Run.ID
	request.Actor = actor
	request.Thread = thread
	waiting, waitErr := runner.StartRun(t.Context(), request)
	if waiting.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("expected nested waiting checkpoint; status=%s err=%v", waiting.Run.Status, waitErr)
	}
	killed, cancelErr := rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "parent terminated before child Resume")
	if cancelErr != nil || killed.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("could not cancel original parent before child Resume: %+v err=%v", killed.Run, cancelErr)
	}
	child, resumeErr := runner.ResolveWait(t.Context(), waiting.Run.ID, waiting.Run.Revision, json.RawMessage(`{"approved":true}`))
	t.Logf("parent=%s child=%s afterResumeErr=%v toolCalls=%d",
		killed.Run.Status, child.Run.Status, resumeErr, exec.calls)
	if exec.calls != 0 {
		t.Fatal("already cancelled parent authorized fresh physical nested Workflow Effect during ResolveWait")
	}
}
