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

// RunEA1WaitUnknownSuite covers only WR-W-P2/WC-W-P2: Start sends nothing;
// the sole post-Wait physical request belongs to actual ResolveWait and has no receipt.
func RunEA1WaitUnknownSuite(t *testing.T, makeStore func(*testing.T) (kernel.Store, func() kernel.Store)) {
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
				parent, err = rt.Create(t.Context(), kernel.CreateRequest{ID: "wait-parent", Kind: workflow.RunKind, Actor: actor, Thread: thread, Goal: "original ancestor", State: json.RawMessage("{}")})
				if err != nil {
					t.Fatal(err)
				}
				parentID = parent.Run.ID
			}
			external := &ea1WaitUnknownExecutor{t: t, rt: rt}
			runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: external})
			if err != nil {
				t.Fatal(err)
			}
			definition, err := workflow.CompileDefinition(ea1UnknownWaitDraft())
			if err != nil {
				t.Fatal(err)
			}
			yielded, err := runner.StartRun(t.Context(), workflow.StartRequest{ID: "wait-resolved-run", ParentRunID: parentID, RequestID: "original-source", Actor: actor, Thread: thread, Goal: "unknown ResolveWait", Definition: definition, Input: json.RawMessage("{}")})
			if err != nil || yielded.Run.Status != kernel.RunStatusWaitingInput || len(external.requests) != 0 {
				t.Fatalf("Start must wait without dispatch: %v requests=%d", err, len(external.requests))
			}
			if _, err = rt.LookupChildExecution(t.Context(), yielded.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("Start retained grant: %v", err)
			}
			pending, err := runner.ResolveWait(t.Context(), yielded.Run.ID, yielded.Run.Revision, json.RawMessage(`{"approved":true}`))
			if !errors.Is(err, kernel.ErrChildExecutionBusy) || !errors.Is(err, workflow.ErrEffectPending) || len(external.requests) != 1 {
				t.Fatalf("actual ResolveWait unknown not fenced: %v requests=%d", err, len(external.requests))
			}
			grant, err := rt.LookupChildExecution(t.Context(), pending.Run.ID)
			if err != nil || grant.ExpectedRevision != yielded.Run.Revision {
				t.Fatalf("original ResolveWait grant missing: %+v %v", grant, err)
			}
			assertEA1WaitUnknownSource(t, pending, yielded, definition, external.requests)
			fresh := reopen()
			ownerRT, err := kernel.New(kernel.Dependencies{Store: fresh})
			if err != nil {
				t.Fatal(err)
			}
			replay := &ea1WaitUnknownExecutor{t: t, rt: ownerRT}
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
			assertEA1WaitUnknownSource(t, durable, yielded, definition, external.requests)
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
				if _, err = owner.ResolveWait(t.Context(), durable.Run.ID, durable.Run.Revision, json.RawMessage(`{"approved":false}`)); !errors.Is(err, workflow.ErrWaitPending) {
					t.Fatalf("duplicate resolved Wait accepted: %v", err)
				}
				assertFenced()
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
			if len(replay.requests) != 0 || len(external.requests) != 1 {
				t.Fatal("unknown physical request replayed")
			}
		})
	}
}

type ea1WaitUnknownExecutor struct {
	t        *testing.T
	rt       *kernel.Runtime
	requests []workflow.EffectRequest
}

func (e *ea1WaitUnknownExecutor) Execute(ctx context.Context, req workflow.EffectRequest) (workflow.EffectResult, error) {
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
	if len(view.Waits) != 1 || view.Waits[0].Status != workflow.WaitResolved || !bytes.Equal(view.Waits[0].Response, json.RawMessage(`{"approved":true}`)) {
		e.t.Fatal("physical request before original Wait response committed")
	}
	return workflow.EffectResult{Disposition: workflow.DispositionPending}, nil
}

func ea1UnknownWaitDraft() workflow.DefinitionDraft {
	return workflow.DefinitionDraft{ID: "wait-unknown", Revision: 1, Name: "original ResolveWait source", Nodes: []workflow.Node{
		{ID: "wait", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "approval", Payload: json.RawMessage(`{"scope":"original"}`)}},
		{ID: "second", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{Kind: "external.second", Input: json.RawMessage(`{"source":"second"}`)}},
		{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage("{}")}},
	}}
}

func assertEA1WaitUnknownSource(t *testing.T, snapshot, waiting kernel.Snapshot, definition workflow.Definition, requests []workflow.EffectRequest) {
	t.Helper()
	view, err := workflow.ViewState(snapshot)
	before, beforeErr := workflow.ViewState(waiting)
	if err != nil || beforeErr != nil || len(view.Effects) != 1 || len(requests) != 1 || len(before.Waits) != 1 || len(view.Waits) != 1 ||
		view.Definition.ID != definition.ID || view.Definition.Hash != definition.Hash || view.Definition.Revision != definition.Revision {
		t.Fatal("original ResolveWait source missing")
	}
	req, effect := requests[0], view.Effects[0]
	if effect.ID == "" || effect.ID != req.EffectID || effect.Attempt != 1 || req.Attempt != 1 || effect.NodeID != req.NodeID || effect.Kind != req.Kind || effect.Class != req.Class ||
		effect.Compensation || req.Compensation || !bytes.Equal(effect.Input, req.Input) || req.RunID != snapshot.Run.ID || req.Actor != snapshot.Run.Actor || req.Thread != snapshot.Run.Thread ||
		req.DefinitionID != definition.ID || req.DefinitionHash != definition.Hash || req.Kind != "external.second" || !bytes.Equal(req.Input, json.RawMessage(`{"source":"second"}`)) ||
		effect.Status != workflow.EffectPending || effect.ReceiptID != "" || effect.PhysicalNoEffect || effect.PhysicalNoEffectAttempt != 0 {
		t.Fatal("unknown original Wait Effect/attempt/source or settlement changed")
	}
	if before.Waits[0].ID != view.Waits[0].ID || before.Waits[0].Status != workflow.WaitPending || view.Waits[0].Status != workflow.WaitResolved ||
		!bytes.Equal(view.Waits[0].Response, json.RawMessage(`{"approved":true}`)) || waiting.Run.Actor != snapshot.Run.Actor || waiting.Run.Thread != snapshot.Run.Thread ||
		waiting.Run.ParentRunID != snapshot.Run.ParentRunID || snapshot.Checkpoint == nil || waiting.Checkpoint == nil ||
		snapshot.Checkpoint.ID != waiting.Checkpoint.ID || snapshot.Checkpoint.Kind != waiting.Checkpoint.Kind ||
		!bytes.Equal(snapshot.Checkpoint.Payload, waiting.Checkpoint.Payload) || !bytes.Equal(snapshot.Checkpoint.Response, json.RawMessage(`{"approved":true}`)) ||
		snapshot.Checkpoint.Status != kernel.CheckpointResolved {
		t.Fatal("original Wait/checkpoint/response ancestry changed")
	}
}
