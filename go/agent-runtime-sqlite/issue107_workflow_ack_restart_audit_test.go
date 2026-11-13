package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

type auditWorkflowAckLossSQLiteStore struct{ *sqlite.Store }

func (s *auditWorkflowAckLossSQLiteStore) MarkChildExecutionSettled(
	ctx context.Context, grant kernel.ChildExecutionGrant, permit kernel.ChildExecutionSettlement) error {
	if err := s.Store.MarkChildExecutionSettled(ctx, grant, permit); err != nil {
		return err
	}
	return kernel.ErrExecutionBusy
}

type auditWorkflowExternalOnceSQLite struct{ calls int }

func (e *auditWorkflowExternalOnceSQLite) Execute(context.Context, workflow.EffectRequest) (workflow.EffectResult, error) {
	e.calls++
	return workflow.EffectResult{Disposition: workflow.DispositionCompleted, ReceiptID: "original_receipt",
		Output: json.RawMessage(`{"ok":true}`)}, nil
}

func TestAuditIssue107CompletedWorkflowSettlementAckLostAfterSQLiteRestart(t *testing.T) {
	path := filepath.Join(privateDir(t), "workflow-ack-lost.sqlite")
	first, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &auditWorkflowAckLossSQLiteStore{Store: first}
	rt, err := kernel.New(kernel.Dependencies{Store: wrapped})
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
	parent, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "workflow-parent", Kind: workflow.RunKind, Actor: actor, Thread: thread,
		Goal: "parent", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	external := &auditWorkflowExternalOnceSQLite{}
	runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: external})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "one-external", Revision: 1, Name: "one durable effect",
		Nodes: []workflow.Node{
			{ID: "effect", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{Kind: "external.once", Input: json.RawMessage(`{"a":1}`)}},
			{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`{"done":true}`)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, startErr := runner.StartRun(t.Context(), workflow.StartRequest{
		ID: "workflow-child", ParentRunID: parent.Run.ID, RequestID: "frozen-effect",
		Actor: actor, Thread: thread, Goal: "child", Definition: definition, Input: json.RawMessage(`{"input":true}`),
	})
	if completed.Run.Status != kernel.RunStatusCompleted || !errors.Is(startErr, kernel.ErrChildExecutionBusy) || external.calls != 1 {
		t.Fatalf("original Effect callback not fully complete before simulated ACK loss: child=%s err=%v calls=%d",
			completed.Run.Status, startErr, external.calls)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	newRuntime, err := kernel.New(kernel.Dependencies{Store: reopened})
	if err != nil {
		t.Fatal(err)
	}
	recoveredWorkflow, err := workflow.NewRunner(workflow.Dependencies{Runtime: newRuntime, Effects: &auditWorkflowExternalOnceSQLite{}})
	if err != nil {
		t.Fatal(err)
	}
	if err = recoveredWorkflow.ReconcileChildPhysicalGrant(t.Context(), completed.Run.ID); err != nil {
		t.Fatalf("fresh Workflow Runner failed to reconcile the exact original durable marker: %v", err)
	}
	_, cancelErr := newRuntime.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision,
		"finished child acknowledged before crash")
	if cancelErr != nil {
		t.Fatalf("durably marked Workflow Grant blocked parent cancellation after recovery: %v", cancelErr)
	}
	if _, lookupErr := newRuntime.LookupChildExecution(t.Context(), completed.Run.ID); !errors.Is(lookupErr, kernel.ErrNotFound) {
		t.Fatalf("settled original Workflow Grant remained active: %v", lookupErr)
	}
	if external.calls != 1 {
		t.Fatalf("recovered Workflow replayed already completed external Effect: calls=%d", external.calls)
	}
}
