package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// RunParentedWorkflowSuite checks the actual Workflow physical execution
// protocol against the same authoritative Store as the parent CAS. It is
// intentionally independent of the SDK's internal in-memory test fixtures.
func RunParentedWorkflowSuite(t *testing.T, create func(*testing.T) kernel.Store) {
	t.Helper()
	t.Run("completed Effect fences parent then retires original Grant", func(t *testing.T) {
		rt, parent := childWorkflowRuntime(t, create)
		executor := &physicalWorkflowExecutor{runtime: rt, parent: parent}
		runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: executor})
		if err != nil {
			t.Fatal(err)
		}
		child, err := runner.StartRun(t.Context(), childWorkflowRequest(t, parent, false))
		if err != nil || child.Run.Status != kernel.RunStatusCompleted || executor.calls != 1 {
			t.Fatalf("physical Workflow callback failed: %+v err=%v calls=%d", child.Run, err, executor.calls)
		}
		if !errors.Is(executor.cancelErr, kernel.ErrChildExecutionBusy) {
			t.Fatalf("parent Cancel committed while nested Effect physically executing: %v", executor.cancelErr)
		}
		if _, err = rt.LookupChildExecution(t.Context(), child.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("verified completed Workflow pinned physical Grant: %v", err)
		}
		if _, err = rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "after effect receipt"); err != nil {
			t.Fatalf("settled parent could not cancel after callback: %v", err)
		}
	})
	t.Run("unknown remote Pending pins parent and rejects replay", func(t *testing.T) {
		rt, parent := childWorkflowRuntime(t, create)
		executor := &physicalWorkflowExecutor{pending: true}
		runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: executor})
		if err != nil {
			t.Fatal(err)
		}
		child, err := runner.StartRun(t.Context(), childWorkflowRequest(t, parent, false))
		if !errors.Is(err, kernel.ErrChildExecutionBusy) || !errors.Is(err, workflow.ErrEffectPending) || executor.calls != 1 {
			t.Fatalf("unknown physical effect was not isolated: status=%s err=%v calls=%d", child.Run.Status, err, executor.calls)
		}
		if _, err = rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "external outcome unknown"); !errors.Is(err, kernel.ErrChildExecutionBusy) {
			t.Fatalf("unknown effect released parent terminal right: %v", err)
		}
		_, err = runner.Resume(t.Context(), child.Run.ID, child.Run.Revision)
		if !errors.Is(err, kernel.ErrChildExecutionBusy) || executor.calls != 1 {
			t.Fatalf("unknown external Effect replayed or accepted another Grant: err=%v calls=%d", err, executor.calls)
		}
	})
	t.Run("quiescent Wait releases Grant, denies later root cancellation bypass", func(t *testing.T) {
		rt, parent := childWorkflowRuntime(t, create)
		executor := &physicalWorkflowExecutor{}
		runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: executor})
		if err != nil {
			t.Fatal(err)
		}
		child, err := runner.StartRun(t.Context(), childWorkflowRequest(t, parent, true))
		if err != nil || child.Run.Status != kernel.RunStatusWaitingInput {
			t.Fatalf("quiescent Workflow Wait failed: status=%s err=%v", child.Run.Status, err)
		}
		if _, err = rt.LookupChildExecution(t.Context(), child.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("quiescent Wait pinned external execution authority: %v", err)
		}
		if _, err = rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "cancel while child waiting"); err != nil {
			t.Fatalf("parent could not cancel after quiescent Wait: %v", err)
		}
		_, err = runner.ResolveWait(t.Context(), child.Run.ID, child.Run.Revision, json.RawMessage(`{"approved":true}`))
		if !errors.Is(err, kernel.ErrExecutionConflict) || executor.calls != 0 {
			t.Fatalf("terminal parent spawned new Workflow effect: err=%v effects=%d", err, executor.calls)
		}
	})
}

type physicalWorkflowExecutor struct {
	runtime   *kernel.Runtime
	parent    kernel.Snapshot
	calls     int
	cancelErr error
	pending   bool
}

func (e *physicalWorkflowExecutor) Execute(ctx context.Context, request workflow.EffectRequest) (workflow.EffectResult, error) {
	e.calls++
	if e.parent.Run.ID != "" {
		_, e.cancelErr = e.runtime.Cancel(ctx, e.parent.Run.ID, e.parent.Run.Revision, "concurrent nested Workflow external effect")
	}
	if e.pending {
		return workflow.EffectResult{Disposition: workflow.DispositionPending}, nil
	}
	return workflow.EffectResult{Disposition: workflow.DispositionCompleted,
		ReceiptID: "original-physical-receipt", Output: json.RawMessage(`{"ok":true}`)}, nil
}

func childWorkflowRuntime(t *testing.T, create func(*testing.T) kernel.Store) (*kernel.Runtime, kernel.Snapshot) {
	t.Helper()
	rt, err := kernel.New(kernel.Dependencies{Store: create(t)})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "workflow-parent", Kind: workflow.RunKind,
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "conformance root", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt, parent
}
func childWorkflowRequest(t *testing.T, parent kernel.Snapshot, wait bool) workflow.StartRequest {
	t.Helper()
	nodes := []workflow.Node{
		{ID: "effect", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{Kind: "original.external", Input: json.RawMessage(`{"cost":0}`)}},
		{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`{"done":true}`)}},
	}
	if wait {
		nodes = append([]workflow.Node{{ID: "wait", Type: workflow.NodeWait,
			Wait: &workflow.WaitNode{Kind: "await.approval", Payload: json.RawMessage(`{"reason":"test"}`)}}}, nodes...)
	}
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "original-effect", Revision: 1, Name: "original parented Workflow", Nodes: nodes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return workflow.StartRequest{
		ID: "workflow-child", RequestID: "effect-id", ParentRunID: parent.Run.ID,
		Actor: parent.Run.Actor, Thread: parent.Run.Thread, Goal: "run nested workflow",
		Definition: definition, Input: json.RawMessage(`{"input":"frozen"}`),
	}
}
