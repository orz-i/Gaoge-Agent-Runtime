package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// RunEA1CompensationUnknownSuite covers only WR-C-P2/WC-C-P2: the second
// physical request belongs to actual Cancel, and has no settlement receipt.
func RunEA1CompensationUnknownSuite(t *testing.T, makeStore func(*testing.T) (kernel.Store, func() kernel.Store)) {
	for _, child := range []bool{false, true} {
		name := "root"
		if child {
			name = "child"
		}
		t.Run(name, func(t *testing.T) {
			first, reopen := makeStore(t)
			rt, err := kernel.New(kernel.Dependencies{Store: first})
			if err != nil {
				t.Fatal(err)
			}
			actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
			thread := kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}
			var parent kernel.Snapshot
			parentID := ""
			if child {
				parent, err = rt.Create(t.Context(), kernel.CreateRequest{ID: "compensation-parent", Kind: workflow.RunKind, Actor: actor, Thread: thread, Goal: "original ancestor", State: json.RawMessage("{}")})
				if err != nil {
					t.Fatal(err)
				}
				parentID = parent.Run.ID
			}
			external := &ea1CompensationUnknownExecutor{t: t, rt: rt}
			runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: external})
			if err != nil {
				t.Fatal(err)
			}
			definition, err := workflow.CompileDefinition(ea1CompensationDraft())
			if err != nil {
				t.Fatal(err)
			}
			yielded, err := runner.StartRun(t.Context(), workflow.StartRequest{ID: "compensated-run", ParentRunID: parentID, RequestID: "original-source", Actor: actor, Thread: thread, Goal: "unknown compensation", Definition: definition, Input: json.RawMessage("{}")})
			if err != nil || yielded.Run.Status != kernel.RunStatusWaitingInput || len(external.requests) != 1 {
				t.Fatalf("Start must wait before Cancel: %v requests=%d", err, len(external.requests))
			}
			if _, err = rt.LookupChildExecution(t.Context(), yielded.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("Start retained grant: %v", err)
			}
			pending, err := runner.Cancel(t.Context(), yielded.Run.ID, yielded.Run.Revision, "original cancellation")
			if !errors.Is(err, kernel.ErrChildExecutionBusy) || !errors.Is(err, workflow.ErrSegmentYielded) || len(external.requests) != 2 {
				t.Fatalf("actual Cancel unknown not fenced: %v requests=%d", err, len(external.requests))
			}
			grant, err := rt.LookupChildExecution(t.Context(), pending.Run.ID)
			if err != nil || grant.ExpectedRevision != yielded.Run.Revision {
				t.Fatalf("original Cancel grant missing: %+v %v", grant, err)
			}
			assertEA1CompensationSource(t, pending, definition, external.requests, true)
			fresh := reopen()
			ownerRT, err := kernel.New(kernel.Dependencies{Store: fresh})
			if err != nil {
				t.Fatal(err)
			}
			replay := &ea1CompensationUnknownExecutor{t: t, rt: ownerRT}
			owner, err := workflow.NewRunner(workflow.Dependencies{Runtime: ownerRT, Effects: replay})
			if err != nil {
				t.Fatal(err)
			}
			durable, err := ownerRT.Load(t.Context(), pending.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if durable.Run.Revision != pending.Run.Revision || durable.Run.Status != pending.Run.Status || !bytes.Equal(durable.State, pending.State) {
				t.Fatal("unknown changed across reopen")
			}
			assertEA1CompensationSource(t, durable, definition, external.requests, true)
			ea1CompensationSnapshotUnchanged(t, ownerRT, pending)
			assertFenced := func() {
				ea1CompensationSnapshotUnchanged(t, ownerRT, durable)
				if child {
					ea1CompensationSnapshotUnchanged(t, ownerRT, parent)
				}
				same, e := ownerRT.LookupChildExecution(t.Context(), durable.Run.ID)
				if e != nil || same != grant {
					t.Fatal("negative operation changed original grant")
				}
			}
			assertFenced()
			for i := 0; i < 2; i++ {
				if err = owner.ReconcileChildPhysicalGrant(t.Context(), durable.Run.ID); !errors.Is(err, kernel.ErrChildExecutionBusy) {
					t.Fatalf("unknown reconcile retired grant: %v", err)
				}
				assertFenced()
				if _, err = owner.Resume(t.Context(), durable.Run.ID, durable.Run.Revision); !errors.Is(err, kernel.ErrChildExecutionBusy) {
					t.Fatalf("unknown Resume readmitted: %v", err)
				}
				assertFenced()
				if _, err = owner.Cancel(t.Context(), durable.Run.ID, durable.Run.Revision, "competing cancel"); !errors.Is(err, kernel.ErrChildExecutionBusy) {
					t.Fatalf("unknown Cancel bypass: %v", err)
				}
				assertFenced()
				if err = fresh.(kernel.ChildExecutionStore).FinishChildExecution(t.Context(), grant); !errors.Is(err, kernel.ErrExecutionConflict) {
					t.Fatalf("unknown Finish retired grant: %v", err)
				}
				assertFenced()
				targets := []kernel.Snapshot{durable}
				if child {
					targets = append(targets, parent)
				}
				for _, before := range targets {
					if _, err = ownerRT.Cancel(t.Context(), before.Run.ID, before.Run.Revision, "unknown control"); !errors.Is(err, kernel.ErrChildExecutionBusy) {
						t.Fatalf("unknown self/ancestor cancel: %v", err)
					}
					assertFenced()
					if before.Run.ID != durable.Run.ID {
						_, nonterminalErr := ownerRT.Apply(t.Context(), before.Run.ID, before.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: before.State})
						if !errors.Is(nonterminalErr, kernel.ErrChildExecutionBusy) {
							t.Fatalf("unknown ancestor nonterminal Apply bypass: %v", nonterminalErr)
						}
						assertFenced()
					}
					nextStatus := kernel.RunStatusRunning
					if before.Run.ID != durable.Run.ID {
						nextStatus = kernel.RunStatusCancelled
					}
					if _, err = ownerRT.Apply(t.Context(), before.Run.ID, before.Run.Revision, kernel.Mutation{Status: nextStatus, State: before.State}); !errors.Is(err, kernel.ErrChildExecutionBusy) {
						t.Fatalf("unknown self/ancestor apply: %v", err)
					}
					assertFenced()
					after, loadErr := ownerRT.Load(t.Context(), before.Run.ID)
					if loadErr != nil || after.Run.Revision != before.Run.Revision || after.Run.Status != before.Run.Status || !bytes.Equal(after.State, before.State) {
						t.Fatal("denied control mutated original self/ancestor")
					}
				}
				same, e := ownerRT.LookupChildExecution(t.Context(), durable.Run.ID)
				if e != nil || same != grant {
					t.Fatal("unknown original grant changed")
				}
			}
			if len(replay.requests) != 0 || len(external.requests) != 2 {
				t.Fatal("unknown physical request replayed")
			}
		})
	}
}

type ea1CompensationUnknownExecutor struct {
	t        *testing.T
	rt       *kernel.Runtime
	requests []workflow.EffectRequest
}

func (e *ea1CompensationUnknownExecutor) Execute(ctx context.Context, req workflow.EffectRequest) (workflow.EffectResult, error) {
	// Observe committed intent and original grant before recording physical send.
	snapshot, err := e.rt.Load(ctx, req.RunID)
	if err != nil {
		e.t.Fatal(err)
	}
	view, err := workflow.ViewState(snapshot)
	if err != nil {
		e.t.Fatal(err)
	}
	found := false
	for _, intent := range view.Effects {
		if intent.ID == req.EffectID {
			found = intent.Attempt == req.Attempt && intent.Status == workflow.EffectPending && intent.NodeID == req.NodeID && intent.Kind == req.Kind && intent.Class == req.Class && intent.Compensation == req.Compensation && bytes.Equal(intent.Input, req.Input)
		}
	}
	if !found {
		e.t.Fatal("physical request before exact durable intent")
	}
	if _, err = e.rt.LookupChildExecution(ctx, req.RunID); err != nil {
		e.t.Fatal("physical request without original grant")
	}
	req.Input = append(json.RawMessage(nil), req.Input...)
	e.requests = append(e.requests, req)
	if req.Kind == "resource.release" {
		if view.Failure == nil || view.Failure.Status != kernel.RunStatusCancelled || view.Failure.Code != "workflow.cancelled" || view.Failure.Detail != "original cancellation" || !req.Compensation || len(view.Compensations) != 1 || view.Compensations[0].Status != workflow.CompensationRunning || view.Compensations[0].EffectID != req.EffectID {
			e.t.Fatal("undo before durable registered compensation intent")
		}
		return workflow.EffectResult{Disposition: workflow.DispositionPending}, nil
	}
	return workflow.EffectResult{Disposition: workflow.DispositionCompleted, ReceiptID: ea1EntryReceiptID(req), Output: append(json.RawMessage(nil), req.Input...), CostUnits: req.MaxCostUnits}, nil
}
