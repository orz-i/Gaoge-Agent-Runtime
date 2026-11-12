package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

type issue107PendingExternalEffect struct {
	calls int
}

func (e *issue107PendingExternalEffect) Execute(context.Context, workflow.EffectRequest) (workflow.EffectResult, error) {
	e.calls++
	// The Host has begun an asynchronous external operation but returned
	// neither its trusted receipt nor an original local child authority.
	return workflow.EffectResult{Disposition: workflow.DispositionPending}, nil
}
func issue107CreateParent(t *testing.T, rt *kernel.Runtime) kernel.Snapshot {
	t.Helper()
	parent, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "physical-parent", Kind: workflow.RunKind,
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "physical boundary", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return parent
}

func TestIssue107NestedWorkflowUnknownExternalEffectKeepsAncestorGrant(t *testing.T) {
	rt := newWorkflowRuntime(t)
	parent := issue107CreateParent(t, rt)
	external := &issue107PendingExternalEffect{}
	runner := newWorkflowRunner(t, rt, external)
	definition := compileWorkflow(t, []workflow.Node{
		effectNode("external", "external.pending"),
		returnNode(json.RawMessage(`{"done":true}`)),
	}, workflow.Limits{})
	request := workflowRequest(definition)
	request.ID = "nested-unknown-effect"
	request.ParentRunID = parent.Run.ID
	child, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, kernel.ErrChildExecutionBusy) || !errors.Is(err, workflow.ErrEffectPending) {
		t.Fatalf("original unknown external effect admitted as settled: run=%s status=%s err=%v", child.Run.ID, child.Run.Status, err)
	}
	if external.calls != 1 {
		t.Fatalf("expected exactly one first external dispatch, got %d", external.calls)
	}
	original, err := rt.LookupChildExecution(t.Context(), child.Run.ID)
	if err != nil || original.ChildRunID != child.Run.ID {
		t.Fatalf("lost unknown-effect Grant: %+v err=%v", original, err)
	}
	_, err = rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "cancel pending unknown effect")
	if !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("parent terminal falsely approved unknown effect: %v", err)
	}
	resumed, resumeErr := runner.Resume(t.Context(), child.Run.ID, child.Run.Revision)
	if !errors.Is(resumeErr, kernel.ErrChildExecutionBusy) || external.calls != 1 {
		t.Fatalf("unknown physical effect re-dispatched: resumed=%s err=%v externalCalls=%d", resumed.Run.Status, resumeErr, external.calls)
	}
}

func TestIssue107NestedWorkflowWaitQuiescentGrantRetiresBeforeParentCancel(t *testing.T) {
	rt := newWorkflowRuntime(t)
	parent := issue107CreateParent(t, rt)
	external := &issue107PendingExternalEffect{}
	runner := newWorkflowRunner(t, rt, external)
	definition := compileWorkflow(t, []workflow.Node{
		{ID: "wait", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "host.wait", Payload: json.RawMessage(`{"required":true}`)}},
		effectNode("after_wait", "tool.write"),
		returnNode(json.RawMessage(`{"done":true}`)),
	}, workflow.Limits{})
	request := workflowRequest(definition)
	request.ID = "nested-wait-no-effect"
	request.ParentRunID = parent.Run.ID
	waiting, err := runner.StartRun(t.Context(), request)
	if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("waiting nested Workflow wrongly pinned as unknown: status=%s err=%v", waiting.Run.Status, err)
	}
	if _, err = rt.LookupChildExecution(t.Context(), waiting.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("quiescent Wait retained physical Grant: %v", err)
	}
	_, err = rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "parent can safely cancel waiting child")
	if err != nil {
		t.Fatalf("quiescent Wait should not prevent parent Cancel: %v", err)
	}
	_, err = runner.ResolveWait(t.Context(), waiting.Run.ID, waiting.Run.Revision, json.RawMessage(`{"approved":true}`))
	if !errors.Is(err, kernel.ErrExecutionConflict) || external.calls != 0 {
		t.Fatalf("terminated parent dispatched fresh Effect on Wait resume: err=%v calls=%d", err, external.calls)
	}
}

// A pending *local* nested Subworkflow is not an unknown external operation:
// the original child owns an immutable Kernel ParentRunID and is quiescent at
// a durable Wait checkpoint. The intermediate Workflow Grant can be retired
// without pretending that arbitrary remote Effect Pending is settled.
func TestIssue107NestedSubworkflowPendingLocalChildSettlesIntermediateGrant(t *testing.T) {
	rt := newWorkflowRuntime(t)
	grand := issue107CreateParent(t, rt)
	registry, err := workflow.NewDefinitionRegistry(workflow.NewMemoryDefinitionStore(), registryClock{})
	if err != nil {
		t.Fatal(err)
	}
	scope := workflow.DefinitionScope{Kind: workflow.DefinitionScopeActor, TenantID: "tenant", ActorID: "actor"}
	childDraft := workflow.DefinitionDraft{ID: "local-wait-child", Name: "Local waiting child", Nodes: []workflow.Node{
		{ID: "wait", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "host.wait", Payload: json.RawMessage(`{"reason":"approval"}`)}},
		{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`{"ok":true}`)}},
	}}
	published, _, _, err := registry.Publish(t.Context(), workflow.PublishDefinitionRequest{
		Scope: scope, ExpectedRevision: 0, IdempotencyKey: "issue107-local-subworkflow", PublishedBy: "actor", Draft: childDraft,
	})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := workflow.NewRunner(workflow.Dependencies{
		Runtime: rt, Registry: registry, Relations: relations, Effects: &issue107PendingExternalEffect{},
	})
	if err != nil {
		t.Fatal(err)
	}
	parentDefinition := compileAdvancedWorkflow(t, []workflow.Node{
		{ID: "child", Type: workflow.NodeSubworkflow,
			Subworkflow: &workflow.SubworkflowNode{
				Definition: workflow.DefinitionReference{ID: published.Definition.ID, Revision: published.Definition.Revision, Hash: published.Definition.Hash},
				Input:      workflow.ValueSource{Kind: workflow.ValueWorkflowInput},
			}},
		{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`{"done":true}`)}},
	}, workflow.DefinitionPolicy{})
	req := workflowRequest(parentDefinition)
	req.ID = "intermediate-parented-workflow"
	req.ParentRunID = grand.Run.ID
	req.Actor = grand.Run.Actor
	req.Thread = grand.Run.Thread
	intermediate, startErr := runner.StartRun(t.Context(), req)
	if !errors.Is(startErr, workflow.ErrEffectPending) {
		t.Fatalf("expected local waiting child, got %v status=%s", startErr, intermediate.Run.Status)
	}
	state, err := workflow.ViewState(intermediate)
	if err != nil || len(state.Effects) != 1 || state.Effects[0].ChildRunID == "" {
		t.Fatalf("missing original child relation: %+v %v", state.Effects, err)
	}
	nested, err := rt.Load(t.Context(), state.Effects[0].ChildRunID)
	if err != nil || nested.Run.Status != kernel.RunStatusWaitingInput || nested.Run.ParentRunID != intermediate.Run.ID {
		t.Fatalf("local child was not durable and quiescent: %+v err=%v", nested.Run, err)
	}
	if _, err = rt.LookupChildExecution(t.Context(), intermediate.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("intermediate Workflow retained a physical Grant despite an original local quiescent child: %v", err)
	}
	if _, err = rt.LookupChildExecution(t.Context(), nested.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("nested waiting Workflow retained physical Grant: %v", err)
	}
	if _, err = rt.Cancel(t.Context(), grand.Run.ID, grand.Run.Revision, "parent cancelled during local child Wait"); err != nil {
		t.Fatalf("original root could not be terminal after all physical callbacks returned: %v", err)
	}
	_, err = runner.Resume(t.Context(), intermediate.Run.ID, intermediate.Run.Revision)
	if !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("intermediate child executed after grandparent cancellation: %v", err)
	}
}
