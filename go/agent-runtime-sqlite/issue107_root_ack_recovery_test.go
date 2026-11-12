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

type rootWorkflowAckEffect struct{ calls int }

func (e *rootWorkflowAckEffect) Execute(context.Context, workflow.EffectRequest) (workflow.EffectResult, error) {
	e.calls++
	return workflow.EffectResult{Disposition: workflow.DispositionCompleted,
		ReceiptID: "root-original-external-receipt", Output: json.RawMessage(`{"ok":true}`)}, nil
}

func TestIssue107SQLiteRootWorkflowOriginalMarkAckLostReopenRecovery(t *testing.T) {
	path := filepath.Join(privateDir(t), "root-grant-ack-lost-v3.sqlite")
	first, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := issue107PostMarkAckLossSQLite{Store: first}
	rt, err := kernel.New(kernel.Dependencies{Store: wrapped})
	if err != nil {
		t.Fatal(err)
	}
	external := &rootWorkflowAckEffect{}
	runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: external})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "root-ack", Revision: 1, Name: "Original Root acknowledgement loss",
		Nodes: []workflow.Node{
			{ID: "effect", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{
				Kind: "external.once", Input: json.RawMessage(`{"single":true}`)}},
			{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{
				Value: json.RawMessage(`{"completed":true}`)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, invokeErr := runner.StartRun(t.Context(), workflow.StartRequest{
		ID: "root-once", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "root physical",
		Definition: definition, Input: json.RawMessage(`{}`),
	})
	if root.Run.Status != kernel.RunStatusCompleted || !errors.Is(invokeErr, kernel.ErrChildExecutionBusy) ||
		external.calls != 1 {
		t.Fatalf("Store ACK-loss fixture never committed original physical result: root=%s err=%v calls=%d",
			root.Run.Status, invokeErr, external.calls)
	}
	original, err := rt.LookupChildExecution(t.Context(), root.Run.ID)
	if err != nil || original.ExpectedRevision == 0 {
		t.Fatalf("original Root Grant not visible before restart: %+v err=%v", original, err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	after, err := kernel.New(kernel.Dependencies{Store: reopened})
	if err != nil {
		t.Fatal(err)
	}
	newEffect := &rootWorkflowAckEffect{}
	recovery, err := workflow.NewRunner(workflow.Dependencies{Runtime: after, Effects: newEffect})
	if err != nil {
		t.Fatal(err)
	}
	if err = recovery.ReconcileChildPhysicalGrant(t.Context(), root.Run.ID); err != nil {
		t.Fatalf("first-party Root Workflow could not reconcile durable post-callback marker: %v", err)
	}
	if _, err = after.LookupChildExecution(t.Context(), root.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("retired Root physical Grant remains live/replayable: %v", err)
	}
	if _, err = recovery.Resume(t.Context(), root.Run.ID, root.Run.Revision); !errors.Is(err, workflow.ErrWorkflowTerminal) {
		t.Fatalf("completed Root replay unexpectedly resumed: %v", err)
	}
	if newEffect.calls != 0 || external.calls != 1 {
		t.Fatalf("Root recovery dispatched a duplicate Effect: orig=%d new=%d", external.calls, newEffect.calls)
	}
}
