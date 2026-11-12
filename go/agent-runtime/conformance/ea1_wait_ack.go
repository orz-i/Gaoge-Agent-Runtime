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

// RunEA1WaitMarkAckSuite exercises ONLY WR-W-P4 and WC-W-P4.
// reopen bypasses the fault wrapper. Persistent backends return a fresh Store;
// Memory retains backing state and exercises only fresh Kernel/Runner recovery.
func RunEA1WaitMarkAckSuite(t *testing.T, makeStore func(*testing.T) (kernel.Store, func() kernel.Store)) {
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
			wrapped := &ea1WaitLostAck{Store: first, ChildExecutionStore: physical}
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
					ID: "wait-parent", Kind: workflow.RunKind, Actor: actor,
					Thread: thread, Goal: "original ancestor", State: json.RawMessage("{}"),
				})
				if err != nil {
					t.Fatal(err)
				}
				parentID = parent.Run.ID
			}
			external := &ea1WaitCounter{}
			runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: external})
			if err != nil {
				t.Fatal(err)
			}
			definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
				ID: "wait-ack", Revision: 1, Name: "original ResolveWait P4",
				Limits: workflow.Limits{MaxActivationsPerSegment: 8},
				Policy: workflow.DefinitionPolicy{MaxCostUnits: 2, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectWrite},
				Nodes: []workflow.Node{
					{ID: "wait", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "approval", Payload: json.RawMessage("{\"scope\":\"original\"}")}},
					{ID: "second", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{Kind: "external.second", Input: json.RawMessage("{\"source\":\"second\"}")}},
					{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage("{}")}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			waiting, err := runner.StartRun(t.Context(), workflow.StartRequest{
				ID: "wait-resolved-run", ParentRunID: parentID, RequestID: "original-source",
				Actor: actor, Thread: thread, Goal: "Wait then physical Effect",
				Definition: definition, Input: json.RawMessage("{}"),
			})
			if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput || len(external.requests) != 0 {
				t.Fatalf("actual Start did not reach zero-effect Wait: status=%s err=%v requests=%d", waiting.Run.Status, err, len(external.requests))
			}
			if _, err = rt.LookupChildExecution(t.Context(), waiting.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("pre-ResolveWait grant not retired: %v", err)
			}
			wrapped.armed.Store(true)
			completed, waitErr := runner.ResolveWait(t.Context(), waiting.Run.ID, waiting.Run.Revision, json.RawMessage("{\"approved\":true}"))
			if !errors.Is(waitErr, kernel.ErrChildExecutionBusy) || completed.Run.Status != kernel.RunStatusCompleted || wrapped.lost.Load() != 1 {
				t.Fatalf("P4 ResolveWait Mark-commit ACK-loss not reached: status=%s err=%v lost=%d", completed.Run.Status, waitErr, wrapped.lost.Load())
			}
			original, err := rt.LookupChildExecution(t.Context(), waiting.Run.ID)
			if err != nil || original.ExpectedRevision != waiting.Run.Revision {
				t.Fatalf("original ResolveWait admission missing/wrong revision: grant=%+v err=%v", original, err)
			}
			assertEA1WaitReceipt(t, completed, waiting, definition, external.requests)
			if len(external.requests) != 1 {
				t.Fatalf("ResolveWait must send only original Effect: requests=%d", len(external.requests))
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
			if err != nil || durable.Run.Revision != completed.Run.Revision || !bytes.Equal(durable.State, completed.State) {
				t.Fatalf("durable completion/receipt changed across restart: err=%v", err)
			}
			assertEA1WaitReceipt(t, durable, waiting, definition, external.requests)
			loadedGrant, err := recovered.LookupChildExecution(t.Context(), durable.Run.ID)
			if err != nil || loadedGrant != original {
				t.Fatalf("original physical identity not durable: %+v err=%v", loadedGrant, err)
			}
			selfBefore := ea1WaitSnapshotBytes(t, recovered, durable.Run.ID)
			if _, err = recovered.Cancel(t.Context(), durable.Run.ID, durable.Run.Revision, "self before P4 recovery"); !errors.Is(err, kernel.ErrTerminal) {
				t.Fatalf("completed original owner accepted self Cancel: %v", err)
			}
			if !bytes.Equal(selfBefore, ea1WaitSnapshotBytes(t, recovered, durable.Run.ID)) {
				t.Fatal("rejected self Cancel mutated original snapshot")
			}
			forged := original
			forged.ExpectedRevision++
			if err = freshPhysical.FinishChildExecution(t.Context(), forged); !errors.Is(err, kernel.ErrExecutionConflict) {
				t.Fatalf("wrong original revision retired Resume grant: %v", err)
			}
			if child {
				parentBefore := ea1WaitSnapshotBytes(t, recovered, parent.Run.ID)
				_, err = recovered.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "before recovery")
				if !errors.Is(err, kernel.ErrChildExecutionBusy) {
					t.Fatalf("ancestor unfenced before original Resume recovery: %v", err)
				}
				unchanged, loadErr := recovered.Load(t.Context(), parent.Run.ID)
				if loadErr != nil || unchanged.Run.Revision != parent.Run.Revision || unchanged.Run.Status != parent.Run.Status || !bytes.Equal(parentBefore, ea1WaitSnapshotBytes(t, recovered, parent.Run.ID)) {
					t.Fatalf("rejected ancestor cancellation mutated state: %+v err=%v", unchanged.Run, loadErr)
				}
			}
			afterForged, err := recovered.LookupChildExecution(t.Context(), durable.Run.ID)
			if err != nil || afterForged != original {
				t.Fatalf("rejected recovery changed original grant: %+v err=%v", afterForged, err)
			}
			if !bytes.Equal(selfBefore, ea1WaitSnapshotBytes(t, recovered, durable.Run.ID)) {
				t.Fatal("rejected forged retirement or parent Cancel mutated original snapshot")
			}
			newExternal := &ea1WaitCounter{}
			owner, err := workflow.NewRunner(workflow.Dependencies{Runtime: recovered, Effects: newExternal})
			if err != nil {
				t.Fatal(err)
			}
			if err = owner.ReconcileChildPhysicalGrant(t.Context(), durable.Run.ID); err != nil {
				t.Fatalf("first-party post-Mark ResolveWait recovery failed: %v", err)
			}
			// Repeating reconciliation after confirmed successful retirement yields
			// the documented typed absence, with no state/effect mutation.
			if err = owner.ReconcileChildPhysicalGrant(t.Context(), durable.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("retired original Grant must report typed absence: %v", err)
			}
			if _, err = recovered.LookupChildExecution(t.Context(), durable.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("settled Resume grant remains: %v", err)
			}
			if _, err = owner.ResolveWait(t.Context(), durable.Run.ID, durable.Run.Revision, json.RawMessage("{\"approved\":true}")); !errors.Is(err, workflow.ErrWaitPending) {
				t.Fatalf("resolved original Wait accepted duplicate completion: %v", err)
			}
			if _, err = owner.Resume(t.Context(), durable.Run.ID, durable.Run.Revision); !errors.Is(err, workflow.ErrWorkflowTerminal) {
				t.Fatalf("terminal Resume resumed: %v", err)
			}
			if _, err = owner.Cancel(t.Context(), durable.Run.ID, durable.Run.Revision, "duplicate cancellation"); !errors.Is(err, workflow.ErrWorkflowTerminal) {
				t.Fatalf("terminal Resume repeated: %v", err)
			}
			final, err := recovered.Load(t.Context(), durable.Run.ID)
			if err != nil || final.Run.Revision != durable.Run.Revision || !bytes.Equal(final.State, durable.State) || !bytes.Equal(selfBefore, ea1WaitSnapshotBytes(t, recovered, durable.Run.ID)) {
				t.Fatalf("recovery/duplicate control mutated original receipt: err=%v", err)
			}
			if child {
				if _, err = recovered.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "after original recovery"); err != nil {
					t.Fatalf("ancestor remained fenced after recovery: %v", err)
				}
			}
			if len(external.requests) != 1 || len(newExternal.requests) != 0 {
				t.Fatal("physical post-Wait Effect replayed during recovery")
			}
		})
	}
}

func assertEA1WaitReceipt(t *testing.T, snapshot kernel.Snapshot, waiting kernel.Snapshot, definition workflow.Definition, requests []workflow.EffectRequest) {
	t.Helper()
	view, err := workflow.ViewState(snapshot)
	if err != nil || len(view.Effects) != 1 || len(requests) != 1 ||
		view.Definition.ID != definition.ID || view.Definition.Hash != definition.Hash || view.Definition.Revision != definition.Revision {
		t.Fatalf("original definition or actual ResolveWait receipt lost: effects=%d requests=%d err=%v", len(view.Effects), len(requests), err)
	}
	for i, effect := range view.Effects {
		req := requests[i]
		if effect.ID == "" || effect.ID != req.EffectID || effect.Attempt != 1 || req.Attempt != 1 ||
			effect.Status != workflow.EffectCompleted || effect.ReceiptID != ea1EntryReceiptID(req) ||
			effect.Kind != req.Kind || effect.NodeID != req.NodeID || effect.Class != req.Class || effect.Compensation || req.Compensation ||
			!bytes.Equal(effect.Input, req.Input) || !bytes.Equal(effect.Output, req.Input) ||
			req.RunID != snapshot.Run.ID || req.Actor != snapshot.Run.Actor || req.Thread != snapshot.Run.Thread ||
			req.DefinitionID != definition.ID || req.DefinitionHash != definition.Hash {
			t.Fatalf("receipt not bound to original ResolveWait source: effect=%+v request=%+v", effect, req)
		}
	}

	before, beforeErr := workflow.ViewState(waiting)
	if beforeErr != nil || len(before.Waits) != 1 || len(view.Waits) != 1 ||
		before.Waits[0].ID != view.Waits[0].ID || before.Waits[0].Status != workflow.WaitPending ||
		view.Waits[0].Status != workflow.WaitResolved ||
		!bytes.Equal(view.Waits[0].Response, json.RawMessage(`{"approved":true}`)) ||
		requests[0].Kind != "external.second" ||
		!bytes.Equal(requests[0].Input, json.RawMessage(`{"source":"second"}`)) {
		t.Fatal("original Wait identity/response or post-Wait physical source lost")
	}
}

type ea1WaitCounter struct{ requests []workflow.EffectRequest }

func (e *ea1WaitCounter) Execute(_ context.Context, req workflow.EffectRequest) (workflow.EffectResult, error) {
	req.Input = append(json.RawMessage(nil), req.Input...)
	e.requests = append(e.requests, req)
	return workflow.EffectResult{Disposition: workflow.DispositionCompleted,
		ReceiptID: ea1EntryReceiptID(req), Output: append(json.RawMessage(nil), req.Input...), CostUnits: req.MaxCostUnits}, nil
}

type ea1WaitLostAck struct {
	kernel.Store
	kernel.ChildExecutionStore
	armed atomic.Bool
	lost  atomic.Int32
}

func (s *ea1WaitLostAck) MarkChildExecutionSettled(ctx context.Context, grant kernel.ChildExecutionGrant, settlement kernel.ChildExecutionSettlement) error {
	if err := s.ChildExecutionStore.MarkChildExecutionSettled(ctx, grant, settlement); err != nil {
		return err
	}
	if s.armed.CompareAndSwap(true, false) {
		s.lost.Add(1)
		return errors.New("test-only original ResolveWait Mark committed; ACK lost")
	}
	return nil
}

// Compare the whole serialized snapshot (including checkpoint/identity), not
// only RunStatus, around rejected controls and idempotent recovery.
func ea1WaitSnapshotBytes(t *testing.T, rt *kernel.Runtime, id string) []byte {
	t.Helper()
	snapshot, err := rt.Load(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
