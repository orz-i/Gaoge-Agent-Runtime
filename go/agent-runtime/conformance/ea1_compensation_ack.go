package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// RunEA1CompensationMarkAckSuite exercises ONLY WR-C-P4 and WC-C-P4.
// reopen must return a new persistent Store instance, not the first wrapper.
func RunEA1CompensationMarkAckSuite(t *testing.T, makeStore func(*testing.T) (kernel.Store, func() kernel.Store)) {
	t.Helper()
	for _, child := range []bool{false, true} {
		name := "root"
		if child {
			name = "child"
		}
		t.Run(name, func(t *testing.T) {
			first, reopen := makeStore(t)
			physical, ok := first.(kernel.ChildExecutionStore)
			if !ok {
				t.Fatal("missing physical admission Store")
			}
			wrapped := &ea1CompensationLostAck{Store: first, ChildExecutionStore: physical}
			rt, err := kernel.New(kernel.Dependencies{Store: wrapped})
			if err != nil {
				t.Fatal(err)
			}
			actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
			thread := kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}
			parentID := ""
			var parent kernel.Snapshot
			if child {
				parent, err = rt.Create(t.Context(), kernel.CreateRequest{
					ID: "compensation-parent", Kind: workflow.RunKind, Actor: actor,
					Thread: thread, Goal: "original ancestor", State: json.RawMessage("{}"),
				})
				if err != nil {
					t.Fatal(err)
				}
				parentID = parent.Run.ID
			}
			external := &ea1CompensationCounter{}
			runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: external})
			if err != nil {
				t.Fatal(err)
			}
			definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
				ID: "compensation-ack", Revision: 1, Name: "original compensation P4",
				Policy: workflow.DefinitionPolicy{MaxCostUnits: 2, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectWrite},
				Nodes: []workflow.Node{
					{ID: "reserve", Type: workflow.NodeCompensation, Compensation: &workflow.CompensationNode{
						Do: workflow.EffectCall{Class: workflow.EffectClassGeneric, Kind: "resource.reserve",
							Input: workflow.ValueSource{Kind: workflow.ValueLiteral, Value: json.RawMessage("{\"original\":true}")}, MaxCostUnits: 1},
						Undo: workflow.EffectCall{Class: workflow.EffectClassGeneric, Kind: "resource.release",
							Input: workflow.ValueSource{Kind: workflow.ValueNodeOutput, NodeID: "reserve"}},
					}},
					{ID: "wait", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "approval", Payload: json.RawMessage("{}")}},
					{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage("{}")}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			waiting, err := runner.StartRun(t.Context(), workflow.StartRequest{
				ID: "compensated-run", ParentRunID: parentID, RequestID: "original-source",
				Actor: actor, Thread: thread, Goal: "reserve then cancel",
				Definition: definition, Input: json.RawMessage("{}"),
			})
			if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput || external.do.Load() != 1 || external.undo.Load() != 0 {
				t.Fatalf("quiescent compensation fixture: status=%s err=%v do=%d undo=%d", waiting.Run.Status, err, external.do.Load(), external.undo.Load())
			}
			if _, err = rt.LookupChildExecution(t.Context(), waiting.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("pre-Cancel grant not retired: %v", err)
			}
			wrapped.armed.Store(true)
			cancelled, cancelErr := runner.Cancel(t.Context(), waiting.Run.ID, waiting.Run.Revision, "original cancellation")
			if !errors.Is(cancelErr, kernel.ErrChildExecutionBusy) || cancelled.Run.Status != kernel.RunStatusCancelled || wrapped.lost.Load() != 1 {
				t.Fatalf("P4 compensation Mark-commit ACK-loss not reached: status=%s err=%v lost=%d", cancelled.Run.Status, cancelErr, wrapped.lost.Load())
			}
			original, err := rt.LookupChildExecution(t.Context(), waiting.Run.ID)
			if err != nil || original.ExpectedRevision != waiting.Run.Revision {
				t.Fatalf("original Cancel admission missing/wrong revision: grant=%+v err=%v", original, err)
			}
			assertEA1CompensationReceipt(t, cancelled)
			if external.do.Load() != 1 || external.undo.Load() != 1 {
				t.Fatalf("physical compensation must run exactly once before restart: do=%d undo=%d", external.do.Load(), external.undo.Load())
			}

			fresh := reopen()
			recovered, err := kernel.New(kernel.Dependencies{Store: fresh})
			if err != nil {
				t.Fatal(err)
			}
			freshPhysical, ok := fresh.(kernel.ChildExecutionStore)
			if !ok {
				t.Fatal("reopened Store has no physical port")
			}
			durable, err := recovered.Load(t.Context(), waiting.Run.ID)
			if err != nil || durable.Run.Revision != cancelled.Run.Revision || !bytes.Equal(durable.State, cancelled.State) {
				t.Fatalf("durable cancellation/receipt changed across restart: err=%v", err)
			}
			assertEA1CompensationReceipt(t, durable)
			loadedGrant, err := recovered.LookupChildExecution(t.Context(), durable.Run.ID)
			if err != nil || loadedGrant != original {
				t.Fatalf("original physical identity not durable: %+v err=%v", loadedGrant, err)
			}
			forged := original
			forged.ExpectedRevision++
			if err = freshPhysical.FinishChildExecution(t.Context(), forged); !errors.Is(err, kernel.ErrExecutionConflict) {
				t.Fatalf("wrong original revision retired compensation grant: %v", err)
			}
			if child {
				_, err = recovered.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "before recovery")
				if !errors.Is(err, kernel.ErrChildExecutionBusy) {
					t.Fatalf("ancestor unfenced before original compensation recovery: %v", err)
				}
				unchanged, loadErr := recovered.Load(t.Context(), parent.Run.ID)
				if loadErr != nil || unchanged.Run.Revision != parent.Run.Revision || unchanged.Run.Status != parent.Run.Status {
					t.Fatalf("rejected ancestor cancellation mutated state: %+v err=%v", unchanged.Run, loadErr)
				}
			}
			afterForged, err := recovered.LookupChildExecution(t.Context(), durable.Run.ID)
			if err != nil || afterForged != original {
				t.Fatalf("rejected recovery changed original grant: %+v err=%v", afterForged, err)
			}
			newExternal := &ea1CompensationCounter{}
			owner, err := workflow.NewRunner(workflow.Dependencies{Runtime: recovered, Effects: newExternal})
			if err != nil {
				t.Fatal(err)
			}
			if err = owner.ReconcileChildPhysicalGrant(t.Context(), durable.Run.ID); err != nil {
				t.Fatalf("first-party post-Mark compensation recovery failed: %v", err)
			}
			// Repeating reconciliation after confirmed successful retirement yields
			// the documented typed absence, with no state/effect mutation.
			if err = owner.ReconcileChildPhysicalGrant(t.Context(), durable.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("retired original Grant must report typed absence: %v", err)
			}
			if _, err = recovered.LookupChildExecution(t.Context(), durable.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("settled compensation grant remains: %v", err)
			}
			if _, err = owner.Resume(t.Context(), durable.Run.ID, durable.Run.Revision); !errors.Is(err, workflow.ErrWorkflowTerminal) {
				t.Fatalf("terminal compensation resumed: %v", err)
			}
			if _, err = owner.Cancel(t.Context(), durable.Run.ID, durable.Run.Revision, "duplicate cancellation"); !errors.Is(err, workflow.ErrWorkflowTerminal) {
				t.Fatalf("terminal compensation repeated: %v", err)
			}
			final, err := recovered.Load(t.Context(), durable.Run.ID)
			if err != nil || final.Run.Revision != durable.Run.Revision || !bytes.Equal(final.State, durable.State) {
				t.Fatalf("recovery/duplicate control mutated original receipt: err=%v", err)
			}
			if child {
				if _, err = recovered.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "after original recovery"); err != nil {
					t.Fatalf("ancestor remained fenced after recovery: %v", err)
				}
			}
			if external.do.Load() != 1 || external.undo.Load() != 1 || newExternal.do.Load() != 0 || newExternal.undo.Load() != 0 {
				t.Fatal("physical Effect/compensation replayed during recovery")
			}
		})
	}
}

func assertEA1CompensationReceipt(t *testing.T, snapshot kernel.Snapshot) {
	t.Helper()
	view, err := workflow.ViewState(snapshot)
	if err != nil || len(view.Compensations) != 1 || len(view.Effects) != 2 {
		t.Fatalf("wrong durable compensation state: %+v err=%v", view, err)
	}
	comp := view.Compensations[0]
	if comp.Status != workflow.CompensationCompleted || comp.ReceiptID != "receipt-"+comp.EffectID {
		t.Fatalf("original compensation receipt missing: %+v", comp)
	}
	var matches int
	for _, effect := range view.Effects {
		if effect.ID == comp.EffectID {
			matches++
			if !effect.Compensation || effect.Attempt != 1 || effect.Status != workflow.EffectCompleted || effect.ReceiptID != comp.ReceiptID || effect.Kind != "resource.release" {
				t.Fatalf("compensation receipt not bound to original effect/attempt: %+v", effect)
			}
		}
	}
	if matches != 1 {
		t.Fatalf("expected exactly one original compensation receipt, got %d", matches)
	}
}

type ea1CompensationCounter struct{ do, undo atomic.Int32 }

func (e *ea1CompensationCounter) Execute(_ context.Context, req workflow.EffectRequest) (workflow.EffectResult, error) {
	switch req.Kind {
	case "resource.reserve":
		e.do.Add(1)
	case "resource.release":
		e.undo.Add(1)
	default:
		return workflow.EffectResult{}, errors.New("unexpected effect")
	}
	return workflow.EffectResult{Disposition: workflow.DispositionCompleted,
		ReceiptID: "receipt-" + req.EffectID, Output: json.RawMessage("{\"original\":true}"), CostUnits: req.MaxCostUnits}, nil
}

type ea1CompensationLostAck struct {
	kernel.Store
	kernel.ChildExecutionStore
	armed atomic.Bool
	lost  atomic.Int32
}

func (s *ea1CompensationLostAck) MarkChildExecutionSettled(ctx context.Context, grant kernel.ChildExecutionGrant, settlement kernel.ChildExecutionSettlement) error {
	if err := s.ChildExecutionStore.MarkChildExecutionSettled(ctx, grant, settlement); err != nil {
		return err
	}
	if s.armed.CompareAndSwap(true, false) {
		s.lost.Add(1)
		return errors.New("test-only original compensation Mark committed; ACK lost")
	}
	return nil
}
