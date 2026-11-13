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

// RunEA1CompensationAdmissionSuite covers only WR-C-P0/WC-C-P0. A peer Store
// wins an authoritative revision/terminal transaction before physical admission.
func RunEA1CompensationAdmissionSuite(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store)) {
	for _, child := range []bool{false, true} {
		for _, race := range []string{"revision", "terminal"} {
			name := "root"
			if child {
				name = "child"
			}
			t.Run(name+"/"+race, func(t *testing.T) {
				backing, peer := factory(t)
				store := &ea1P0BeginInterleave{Store: backing, ChildExecutionStore: backing.(kernel.ChildExecutionStore)}
				rt, err := kernel.New(kernel.Dependencies{Store: store})
				if err != nil {
					t.Fatal(err)
				}
				peerRT, err := kernel.New(kernel.Dependencies{Store: peer})
				if err != nil {
					t.Fatal(err)
				}
				actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
				thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
				parentID := ""
				var parent kernel.Snapshot
				if child {
					parent, err = rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: workflow.RunKind, Actor: actor, Thread: thread, Goal: "parent", State: json.RawMessage("{}")})
					if err != nil {
						t.Fatal(err)
					}
					parentID = parent.Run.ID
				}
				effects := &ea1CompensationReceiptEffect{}
				runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: effects})
				if err != nil {
					t.Fatal(err)
				}
				definition, err := workflow.CompileDefinition(ea1CompensationDraft())
				if err != nil {
					t.Fatal(err)
				}
				original, err := runner.StartRun(t.Context(), workflow.StartRequest{ID: "original", RequestID: "request", ParentRunID: parentID, Actor: actor, Thread: thread, Goal: "cancel with original compensation", Definition: definition, Input: json.RawMessage("{}")})
				if err != nil || original.Run.Status != kernel.RunStatusWaitingInput || effects.calls.Load() != 1 {
					t.Fatalf("quiescent pre-Cancel setup: %v calls=%d", err, effects.calls.Load())
				}
				assertPreflight := func() {
					ea1CompensationSnapshotUnchanged(t, rt, original)
					if child {
						ea1CompensationSnapshotUnchanged(t, rt, parent)
					}
					if _, e := rt.LookupChildExecution(t.Context(), original.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
						t.Fatalf("preflight stranded grant: %v", e)
					}
				}
				for _, revision := range []uint64{0, original.Run.Revision - 1, original.Run.Revision + 1} {
					if _, e := runner.Cancel(t.Context(), original.Run.ID, revision, "original cancellation"); !errors.Is(e, kernel.ErrConflict) {
						t.Fatalf("invalid revision accepted: %d %v", revision, e)
					}
					assertPreflight()
				}
				if _, e := runner.Cancel(t.Context(), "missing", original.Run.Revision, "original cancellation"); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("foreign/missing origin adopted: %v", e)
				}
				assertPreflight()
				unchanged, e := rt.Load(t.Context(), original.Run.ID)
				if e != nil || unchanged.Run.Revision != original.Run.Revision || !bytes.Equal(unchanged.State, original.State) || effects.calls.Load() != 1 {
					t.Fatal("preflight mutated original dispatch state")
				}
				var winner kernel.Snapshot
				store.before = func(ctx context.Context) error {
					var e error
					if race == "revision" {
						winner, e = peerRT.Apply(ctx, original.Run.ID, original.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: original.State, Checkpoint: original.Checkpoint})
					} else if child {
						winner, e = peerRT.Cancel(ctx, parent.Run.ID, parent.Run.Revision, "terminal ancestor wins admission")
					} else {
						winner, e = peerRT.Cancel(ctx, original.Run.ID, original.Run.Revision, "terminal root wins admission")
					}
					return e
				}
				_, err = runner.Cancel(t.Context(), original.Run.ID, original.Run.Revision, "original cancellation")
				if !errors.Is(err, kernel.ErrExecutionConflict) {
					t.Fatalf("stale original admission error=%v", err)
				}
				if effects.calls.Load() != 1 {
					t.Fatal("losing admission sent compensating physical Effect")
				}
				if _, err = rt.LookupChildExecution(t.Context(), original.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("losing admission stranded Grant: %v", err)
				}
				ea1CompensationSnapshotUnchanged(t, rt, winner)
				if child {
					if race == "terminal" {
						ea1CompensationSnapshotUnchanged(t, rt, original)
					} else {
						ea1CompensationSnapshotUnchanged(t, rt, parent)
					}
				}
				got, err := rt.Load(t.Context(), winner.Run.ID)
				if err != nil || got.Run.Revision != winner.Run.Revision || got.Run.Status != winner.Run.Status || !bytes.Equal(got.State, winner.State) {
					t.Fatal("losing admission changed winning transaction")
				}
				if race == "terminal" && child {
					self, e := rt.Load(t.Context(), original.Run.ID)
					if e != nil || self.Run.Revision != original.Run.Revision || self.Run.Status != original.Run.Status || !bytes.Equal(self.State, original.State) {
						t.Fatal("rejected child Compensation mutated original child")
					}
				}
				if race == "revision" {
					finished, e := runner.Cancel(t.Context(), original.Run.ID, winner.Run.Revision, "original cancellation")
					if e != nil || finished.Run.Status != kernel.RunStatusCancelled || effects.calls.Load() != 2 {
						t.Fatalf("original winner cannot continue: %v calls=%d", e, effects.calls.Load())
					}
					if _, e = runner.Cancel(t.Context(), finished.Run.ID, finished.Run.Revision, "original cancellation"); !errors.Is(e, workflow.ErrWorkflowTerminal) {
						t.Fatalf("duplicate terminal Compensation: %v", e)
					}
					assertEA1CompensationSource(t, finished, definition, effects.originalRequests(), false)
					ea1CompensationSnapshotUnchanged(t, rt, finished)
					if child {
						ea1CompensationSnapshotUnchanged(t, rt, parent)
					}
					after, loadErr := rt.Load(t.Context(), finished.Run.ID)
					if loadErr != nil || after.Run.Revision != finished.Run.Revision || after.Run.Status != finished.Run.Status || !bytes.Equal(after.State, finished.State) {
						t.Fatal("terminal duplicate Cancel mutated original")
					}
					if child {
						afterParent, loadErr := rt.Load(t.Context(), parent.Run.ID)
						if loadErr != nil || afterParent.Run.Revision != parent.Run.Revision || afterParent.Run.Status != parent.Run.Status || !bytes.Equal(afterParent.State, parent.State) {
							t.Fatal("original cancellation mutated ancestor")
						}
					}
				} else {
					current, e := rt.Load(t.Context(), original.Run.ID)
					if e != nil {
						t.Fatal(e)
					}
					_, e = runner.Cancel(t.Context(), current.Run.ID, current.Run.Revision, "original cancellation")
					expected := workflow.ErrWorkflowTerminal
					if child {
						expected = kernel.ErrExecutionConflict
					}
					if !errors.Is(e, expected) || effects.calls.Load() != 1 {
						t.Fatalf("terminal source re-admitted: %v", e)
					}
					for _, want := range []kernel.Snapshot{current, winner} {
						ea1CompensationSnapshotUnchanged(t, rt, want)
						after, loadErr := rt.Load(t.Context(), want.Run.ID)
						if loadErr != nil || after.Run.Revision != want.Run.Revision || after.Run.Status != want.Run.Status || !bytes.Equal(after.State, want.State) {
							t.Fatal("repeated terminal rejection mutated self or ancestor")
						}
					}
				}
			})
		}
	}
}

// ea1CompensationDraft registers the real undo source and stops before Cancel.
func ea1CompensationDraft() workflow.DefinitionDraft {
	return workflow.DefinitionDraft{ID: "compensation-source", Revision: 1, Name: "original compensation",
		Policy: workflow.DefinitionPolicy{MaxCostUnits: 2, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectWrite},
		Nodes: []workflow.Node{
			{ID: "reserve", Type: workflow.NodeCompensation, Compensation: &workflow.CompensationNode{
				Do:   workflow.EffectCall{Class: workflow.EffectClassGeneric, Kind: "resource.reserve", Input: workflow.ValueSource{Kind: workflow.ValueLiteral, Value: json.RawMessage(`{"original":true}`)}, MaxCostUnits: 1},
				Undo: workflow.EffectCall{Class: workflow.EffectClassGeneric, Kind: "resource.release", Input: workflow.ValueSource{Kind: workflow.ValueNodeOutput, NodeID: "reserve"}},
			}},
			{ID: "wait", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "approval", Payload: json.RawMessage("{}")}},
			{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage("{}")}},
		}}
}

func assertEA1CompensationSource(t *testing.T, snapshot kernel.Snapshot, definition workflow.Definition, requests []workflow.EffectRequest, unknown bool) {
	t.Helper()
	view, err := workflow.ViewState(snapshot)
	if err != nil || len(view.Effects) != 2 || len(requests) != 2 || len(view.Compensations) != 1 ||
		view.Definition.ID != definition.ID || view.Definition.Hash != definition.Hash || view.Definition.Revision != definition.Revision ||
		view.Failure == nil || view.Failure.Status != kernel.RunStatusCancelled || view.Failure.Code != "workflow.cancelled" || view.Failure.Detail != "original cancellation" {
		t.Fatalf("original compensation source missing: %v", err)
	}
	for i, effect := range view.Effects {
		req := requests[i]
		if req.RunID != snapshot.Run.ID || req.Actor != snapshot.Run.Actor || req.Thread != snapshot.Run.Thread ||
			req.DefinitionID != definition.ID || req.DefinitionHash != definition.Hash || req.Attempt != 1 || req.Compensation != (i == 1) ||
			effect.ID != req.EffectID || effect.NodeID != req.NodeID || effect.Attempt != req.Attempt || effect.Kind != req.Kind ||
			effect.Class != req.Class || effect.Compensation != req.Compensation || !bytes.Equal(effect.Input, req.Input) {
			t.Fatalf("compensation source mismatch: %+v %+v", effect, req)
		}
		if unknown && i == 1 {
			if effect.Status != workflow.EffectPending || effect.ReceiptID != "" || effect.PhysicalNoEffect || effect.PhysicalNoEffectAttempt != 0 {
				t.Fatal("unknown undo fabricated settlement")
			}
		} else if effect.Status != workflow.EffectCompleted || effect.ReceiptID != ea1EntryReceiptID(req) {
			t.Fatal("original physical receipt source changed")
		}
	}
	compensation := view.Compensations[0]
	if compensation.EffectID != requests[1].EffectID || compensation.NodeID != requests[1].NodeID || !bytes.Equal(compensation.Input, requests[1].Input) {
		t.Fatal("undo was not bound to original registered compensation")
	}
	if unknown {
		if compensation.Status != workflow.CompensationRunning || compensation.ReceiptID != "" {
			t.Fatal("unknown compensation falsely completed")
		}
	} else if compensation.Status != workflow.CompensationCompleted || compensation.ReceiptID != ea1EntryReceiptID(requests[1]) {
		t.Fatal("compensation receipt mismatch")
	}
	if requests[0].EffectID == requests[1].EffectID || requests[0].Kind != "resource.reserve" || requests[1].Kind != "resource.release" ||
		!bytes.Equal(requests[0].Input, json.RawMessage(`{"original":true}`)) || !bytes.Equal(requests[1].Input, requests[0].Input) {
		t.Fatal("distinct reserve/undo original input lost")
	}
}

type ea1CompensationReceiptEffect struct{ ea1EntryReceiptEffect }

func (e *ea1CompensationReceiptEffect) Execute(ctx context.Context, req workflow.EffectRequest) (workflow.EffectResult, error) {
	result, err := e.ea1EntryReceiptEffect.Execute(ctx, req)
	result.Output = append(json.RawMessage(nil), req.Input...)
	return result, err
}

func ea1CompensationSnapshotUnchanged(t *testing.T, rt *kernel.Runtime, want kernel.Snapshot) {
	t.Helper()
	expected, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expected, ea1WaitSnapshotBytes(t, rt, want.Run.ID)) {
		t.Fatal("denied operation changed full original snapshot")
	}
}
