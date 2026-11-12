package conformance

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

// EA-1 P3: callback has returned and its exact original Effect receipt is
// ALREADY persisted, but the first-party post-callback settlement marker
// has NOT yet committed. A clean persisted Run is not physical settlement.
// The same controlled barrier runs on Memory, SQLite and PostgreSQL.
func RunEA1ReceiptBeforeMarkSuite(t *testing.T, makeStore func(*testing.T) kernel.Store) {
	t.Helper()
	for _, root := range []bool{true, false} {
		name := "child"
		if root {
			name = "root"
		}
		t.Run(name+" receipt committed before Mark", func(t *testing.T) {
			backing := makeStore(t)
			physical, ok := backing.(kernel.ChildExecutionStore)
			if !ok {
				t.Fatal("Store lacks authoritative physical execution port")
			}
			intercept := &ea1BeforeMarkBarrier{
				Store: backing, ChildExecutionStore: physical,
				entered: make(chan struct{}, 1), release: make(chan struct{}),
			}
			defer intercept.unblock()
			rt, err := kernel.New(kernel.Dependencies{Store: intercept})
			if err != nil {
				t.Fatal(err)
			}
			actor := kernel.ActorRef{TenantID: "original-tenant", ActorID: "owner"}
			thread := kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}
			parentID := ""
			var parent kernel.Snapshot
			if !root {
				parent, err = rt.Create(t.Context(), kernel.CreateRequest{
					ID: "workflow-parent", Kind: workflow.RunKind, Actor: actor, Thread: thread,
					Goal: "parent", State: json.RawMessage(`{}`),
				})
				if err != nil {
					t.Fatal(err)
				}
				parentID = parent.Run.ID
			}
			feature := &ea1P3Effect{}
			runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: feature})
			if err != nil {
				t.Fatal(err)
			}
			definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
				ID: "receipt-before-mark", Revision: 1, Name: "one exact original Effect",
				Nodes: []workflow.Node{
					{ID: "effect", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{
						Kind: "external.once", Input: json.RawMessage(`{"original":"intent"}`)}},
					{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{
						Value: json.RawMessage(`{"ok":true}`)}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				_, callErr := runner.StartRun(context.Background(), workflow.StartRequest{
					ID: "original-workflow", RequestID: "original-effect", ParentRunID: parentID,
					Actor: actor, Thread: thread, Goal: "one physical attempt",
					Definition: definition, Input: json.RawMessage(`{}`),
				})
				result <- callErr
			}()
			select {
			case <-intercept.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("Workflow callback never reached the receipt-before-Mark barrier")
			}
			record, err := rt.Load(t.Context(), "original-workflow")
			if err != nil {
				t.Fatal(err)
			}
			view, viewErr := workflow.ViewState(record)
			if viewErr != nil || record.Run.Status != kernel.RunStatusCompleted ||
				len(view.Effects) != 1 || view.Effects[0].ReceiptID == "" ||
				view.Effects[0].Status != workflow.EffectCompleted {
				t.Fatalf("P3 fixture did not persist actual original receipt: status=%s effects=%+v err=%v",
					record.Run.Status, view.Effects, viewErr)
			}
			original, err := rt.LookupChildExecution(t.Context(), record.Run.ID)
			if err != nil || original.ExpectedRevision == 0 {
				t.Fatalf("original physical Grant missing at P3: %+v err=%v", original, err)
			}
			if feature.calls.Load() != 1 {
				t.Fatalf("original external callback fired %d times before Mark", feature.calls.Load())
			}
			if err = runner.ReconcileChildPhysicalGrant(t.Context(), record.Run.ID); !errors.Is(err, kernel.ErrChildExecutionBusy) {
				t.Fatalf("clean receipt was falsely sufficient to retire unmarked Grant: %v", err)
			}
			if err = physical.FinishChildExecution(t.Context(), original); !errors.Is(err, kernel.ErrExecutionConflict) {
				t.Fatalf("direct Store Finish retired a Grant before Mark: %v", err)
			}
			terminalID := record.Run.ID
			terminalRevision := record.Run.Revision
			if !root {
				terminalID = parent.Run.ID
				terminalRevision = parent.Run.Revision
			}
			_, cancelErr := rt.Cancel(t.Context(), terminalID, terminalRevision,
				"cancel during original receipt-before-settlement window")
			if root && !errors.Is(cancelErr, kernel.ErrTerminal) {
				t.Fatalf("completed Root accepted Cancel or returned unrelated error: %v", cancelErr)
			}
			if !root && !errors.Is(cancelErr, kernel.ErrChildExecutionBusy) {
				t.Fatalf("ancestor terminal CAS committed during unmarked child callback: %v", cancelErr)
			}
			after, afterErr := rt.Load(t.Context(), terminalID)
			if afterErr != nil || after.Run.Revision != terminalRevision {
				t.Fatalf("rejected Cancel unexpectedly mutated original Run: %+v err=%v", after.Run, afterErr)
			}
			if remaining, lookupErr := rt.LookupChildExecution(t.Context(), record.Run.ID); lookupErr != nil || remaining != original {
				t.Fatalf("recovery/Cancel retired unmarked original Grant: %+v err=%v", remaining, lookupErr)
			}
			if feature.calls.Load() != 1 {
				t.Fatal("P3 recovery issued an additional external Effect")
			}
			intercept.unblock()
			select {
			case finalErr := <-result:
				if finalErr != nil {
					t.Fatalf("trusted original Workflow completion failed: %v", finalErr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original callback did not finish after releasing Mark barrier")
			}
			if _, err = rt.LookupChildExecution(t.Context(), record.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("settled original Grant is still present: %v", err)
			}
			if feature.calls.Load() != 1 {
				t.Fatalf("duplicate physical callback after settlement: %d", feature.calls.Load())
			}
		})
	}
}

type ea1P3Effect struct{ calls atomic.Int32 }

func (e *ea1P3Effect) Execute(context.Context, workflow.EffectRequest) (workflow.EffectResult, error) {
	e.calls.Add(1)
	return workflow.EffectResult{
		Disposition: workflow.DispositionCompleted,
		ReceiptID:   "trusted-external-receipt",
		Output:      json.RawMessage(`{"receipt":true}`),
	}, nil
}

type ea1BeforeMarkBarrier struct {
	kernel.Store
	kernel.ChildExecutionStore
	entered   chan struct{}
	release   chan struct{}
	unblocked atomic.Bool
}

func (s *ea1BeforeMarkBarrier) unblock() {
	if s.unblocked.CompareAndSwap(false, true) {
		close(s.release)
	}
}
func (s *ea1BeforeMarkBarrier) MarkChildExecutionSettled(
	ctx context.Context, grant kernel.ChildExecutionGrant, settlement kernel.ChildExecutionSettlement) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.ChildExecutionStore.MarkChildExecutionSettled(ctx, grant, settlement)
}
