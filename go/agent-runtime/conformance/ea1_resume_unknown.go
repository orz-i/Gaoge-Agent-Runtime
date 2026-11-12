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

// RunEA1ResumeUnknownSuite covers only WR-R-P2/WC-R-P2: the second
// physical request belongs to actual Resume, and has no settlement receipt.
func RunEA1ResumeUnknownSuite(t *testing.T, makeStore func(*testing.T) (kernel.Store, func() kernel.Store)) {
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
				parent, err = rt.Create(t.Context(), kernel.CreateRequest{ID: "resume-parent", Kind: workflow.RunKind, Actor: actor, Thread: thread, Goal: "original ancestor", State: json.RawMessage("{}")})
				if err != nil {
					t.Fatal(err)
				}
				parentID = parent.Run.ID
			}
			external := &ea1ResumeUnknownExecutor{t: t, rt: rt}
			runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: external})
			if err != nil {
				t.Fatal(err)
			}
			definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
				ID: "resume-unknown", Revision: 1, Name: "original Resume unknown",
				Limits: workflow.Limits{MaxActivationsPerSegment: 8},
				Nodes: []workflow.Node{
					{ID: "first", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{Kind: "external.first", Input: json.RawMessage(`{"source":"first"}`)}},
					{ID: "second", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{Kind: "external.second", Input: json.RawMessage(`{"source":"second"}`)}},
					{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage("{}")}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			yielded, err := runner.StartRun(t.Context(), workflow.StartRequest{ID: "resumed-run", ParentRunID: parentID, RequestID: "original-source", Actor: actor, Thread: thread, Goal: "unknown Resume", Definition: definition, Input: json.RawMessage("{}")})
			if !errors.Is(err, workflow.ErrSegmentYielded) || len(external.requests) != 1 {
				t.Fatalf("Start must yield before Resume: %v requests=%d", err, len(external.requests))
			}
			if _, err = rt.LookupChildExecution(t.Context(), yielded.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("Start retained grant: %v", err)
			}
			pending, err := runner.Resume(t.Context(), yielded.Run.ID, yielded.Run.Revision)
			if !errors.Is(err, kernel.ErrChildExecutionBusy) || !errors.Is(err, workflow.ErrEffectPending) || len(external.requests) != 2 {
				t.Fatalf("actual Resume unknown not fenced: %v requests=%d", err, len(external.requests))
			}
			grant, err := rt.LookupChildExecution(t.Context(), pending.Run.ID)
			if err != nil || grant.ExpectedRevision != yielded.Run.Revision {
				t.Fatalf("original Resume grant missing: %+v %v", grant, err)
			}
			assertEA1ResumeUnknownSource(t, pending, definition, external.requests)
			fresh := reopen()
			ownerRT, err := kernel.New(kernel.Dependencies{Store: fresh})
			if err != nil {
				t.Fatal(err)
			}
			replay := &ea1ResumeUnknownExecutor{t: t, rt: ownerRT}
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
			assertEA1ResumeUnknownSource(t, durable, definition, external.requests)
			for i := 0; i < 2; i++ {
				if err = owner.ReconcileChildPhysicalGrant(t.Context(), durable.Run.ID); !errors.Is(err, kernel.ErrChildExecutionBusy) {
					t.Fatalf("unknown reconcile retired grant: %v", err)
				}
				if _, err = owner.Resume(t.Context(), durable.Run.ID, durable.Run.Revision); !errors.Is(err, kernel.ErrChildExecutionBusy) {
					t.Fatalf("unknown Resume readmitted: %v", err)
				}
				if _, err = owner.Cancel(t.Context(), durable.Run.ID, durable.Run.Revision, "competing cancel"); !errors.Is(err, kernel.ErrChildExecutionBusy) {
					t.Fatalf("unknown Cancel bypass: %v", err)
				}
				if err = fresh.(kernel.ChildExecutionStore).FinishChildExecution(t.Context(), grant); !errors.Is(err, kernel.ErrExecutionConflict) {
					t.Fatalf("unknown Finish retired grant: %v", err)
				}
				targets := []kernel.Snapshot{durable}
				if child {
					targets = append(targets, parent)
				}
				for _, before := range targets {
					if _, err = ownerRT.Cancel(t.Context(), before.Run.ID, before.Run.Revision, "unknown control"); !errors.Is(err, kernel.ErrChildExecutionBusy) {
						t.Fatalf("unknown self/ancestor cancel: %v", err)
					}
					if before.Run.ID != durable.Run.ID {
						_, nonterminalErr := ownerRT.Apply(t.Context(), before.Run.ID, before.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: before.State})
						if !errors.Is(nonterminalErr, kernel.ErrChildExecutionBusy) {
							t.Fatalf("unknown ancestor nonterminal Apply bypass: %v", nonterminalErr)
						}
					}
					nextStatus := kernel.RunStatusRunning
					if before.Run.ID != durable.Run.ID {
						nextStatus = kernel.RunStatusCancelled
					}
					if _, err = ownerRT.Apply(t.Context(), before.Run.ID, before.Run.Revision, kernel.Mutation{Status: nextStatus, State: before.State}); !errors.Is(err, kernel.ErrChildExecutionBusy) {
						t.Fatalf("unknown self/ancestor apply: %v", err)
					}
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

func assertEA1ResumeUnknownSource(t *testing.T, snapshot kernel.Snapshot, definition workflow.Definition, requests []workflow.EffectRequest) {
	t.Helper()
	view, err := workflow.ViewState(snapshot)
	if err != nil || len(view.Effects) != 2 || len(requests) != 2 || view.Definition.ID != definition.ID || view.Definition.Hash != definition.Hash {
		t.Fatalf("unknown source missing: %v", err)
	}
	for i, effect := range view.Effects {
		req := requests[i]
		if req.RunID != snapshot.Run.ID || req.Actor != snapshot.Run.Actor || req.Thread != snapshot.Run.Thread || req.DefinitionID != definition.ID || req.DefinitionHash != definition.Hash || req.Attempt != 1 || req.Compensation ||
			effect.ID != req.EffectID || effect.NodeID != req.NodeID || effect.Attempt != req.Attempt || effect.Kind != req.Kind || effect.Class != req.Class || effect.Compensation || !bytes.Equal(effect.Input, req.Input) {
			t.Fatalf("unknown attempt/source mismatch: %+v %+v", effect, req)
		}
		if i == 0 && (effect.Status != workflow.EffectCompleted || effect.ReceiptID != ea1EntryReceiptID(req)) {
			t.Fatal("original first receipt changed")
		}
		if i == 1 && (effect.Status != workflow.EffectPending || effect.ReceiptID != "" || effect.PhysicalNoEffect || effect.PhysicalNoEffectAttempt != 0) {
			t.Fatal("unknown second send fabricated settlement")
		}
	}
	if requests[0].EffectID == requests[1].EffectID || requests[0].Kind != "external.first" || requests[1].Kind != "external.second" || !bytes.Equal(requests[1].Input, json.RawMessage(`{"source":"second"}`)) {
		t.Fatal("Resume did not preserve distinct physical source")
	}
}

type ea1ResumeUnknownExecutor struct {
	t        *testing.T
	rt       *kernel.Runtime
	requests []workflow.EffectRequest
}

func (e *ea1ResumeUnknownExecutor) Execute(ctx context.Context, req workflow.EffectRequest) (workflow.EffectResult, error) {
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
	if req.Kind == "external.second" {
		return workflow.EffectResult{Disposition: workflow.DispositionPending}, nil
	}
	return workflow.EffectResult{Disposition: workflow.DispositionCompleted, ReceiptID: ea1EntryReceiptID(req), Output: append(json.RawMessage(nil), req.Input...), CostUnits: req.MaxCostUnits}, nil
}
