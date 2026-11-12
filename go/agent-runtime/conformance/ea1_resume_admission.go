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

// RunEA1ResumeAdmissionSuite covers only WR-R-P0/WC-R-P0. A peer Store
// wins an authoritative revision/terminal transaction before physical admission.
func RunEA1ResumeAdmissionSuite(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store)) {
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
				effects := &ea1EntryReceiptEffect{}
				runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: effects})
				if err != nil {
					t.Fatal(err)
				}
				definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{ID: "resume-admission", Revision: 1, Name: "original Resume admission", Limits: workflow.Limits{MaxActivationsPerSegment: 8}, Nodes: []workflow.Node{
					{ID: "first", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{Kind: "first", Input: json.RawMessage(`{"source":1}`)}},
					{ID: "second", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{Kind: "second", Input: json.RawMessage(`{"source":2}`)}},
					{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage("{}")}},
				}})
				if err != nil {
					t.Fatal(err)
				}
				original, err := runner.StartRun(t.Context(), workflow.StartRequest{ID: "original", RequestID: "request", ParentRunID: parentID, Actor: actor, Thread: thread, Goal: "resume", Definition: definition, Input: json.RawMessage("{}")})
				if !errors.Is(err, workflow.ErrSegmentYielded) || effects.calls.Load() != 1 {
					t.Fatalf("first segment setup: %v calls=%d", err, effects.calls.Load())
				}
				for _, revision := range []uint64{0, original.Run.Revision - 1, original.Run.Revision + 1} {
					if _, e := runner.Resume(t.Context(), original.Run.ID, revision); !errors.Is(e, kernel.ErrConflict) {
						t.Fatalf("invalid revision accepted: %d %v", revision, e)
					}
				}
				if _, e := runner.Resume(t.Context(), "missing", original.Run.Revision); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("foreign/missing origin adopted: %v", e)
				}
				unchanged, e := rt.Load(t.Context(), original.Run.ID)
				if e != nil || unchanged.Run.Revision != original.Run.Revision || !bytes.Equal(unchanged.State, original.State) || effects.calls.Load() != 1 {
					t.Fatal("preflight mutated original dispatch state")
				}
				var winner kernel.Snapshot
				store.before = func(ctx context.Context) error {
					var e error
					if race == "revision" {
						winner, e = peerRT.Apply(ctx, original.Run.ID, original.Run.Revision, kernel.Mutation{Status: original.Run.Status, State: original.State, Checkpoint: original.Checkpoint})
					} else if child {
						winner, e = peerRT.Cancel(ctx, parent.Run.ID, parent.Run.Revision, "terminal ancestor wins admission")
					} else {
						winner, e = peerRT.Cancel(ctx, original.Run.ID, original.Run.Revision, "terminal root wins admission")
					}
					return e
				}
				_, err = runner.Resume(t.Context(), original.Run.ID, original.Run.Revision)
				if !errors.Is(err, kernel.ErrExecutionConflict) {
					t.Fatalf("stale original admission error=%v", err)
				}
				if effects.calls.Load() != 1 {
					t.Fatal("losing admission sent second physical Effect")
				}
				if _, err = rt.LookupChildExecution(t.Context(), original.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("losing admission stranded Grant: %v", err)
				}
				got, err := rt.Load(t.Context(), winner.Run.ID)
				if err != nil || got.Run.Revision != winner.Run.Revision || got.Run.Status != winner.Run.Status || !bytes.Equal(got.State, winner.State) {
					t.Fatal("losing admission changed winning transaction")
				}
				if race == "revision" {
					finished, e := runner.Resume(t.Context(), original.Run.ID, winner.Run.Revision)
					if e != nil || finished.Run.Status != kernel.RunStatusCompleted || effects.calls.Load() != 2 {
						t.Fatalf("original winner cannot continue: %v calls=%d", e, effects.calls.Load())
					}
					if _, e = runner.Resume(t.Context(), finished.Run.ID, finished.Run.Revision); !errors.Is(e, workflow.ErrWorkflowTerminal) {
						t.Fatalf("duplicate terminal Resume: %v", e)
					}
					view, e := workflow.ViewState(finished)
					if e != nil || len(view.Effects) != 2 {
						t.Fatal("missing original receipts")
					}
					for _, req := range effects.originalRequests() {
						if req.RunID != original.Run.ID || req.Actor != actor || req.Thread != thread || req.DefinitionHash != definition.Hash || req.Attempt != 1 {
							t.Fatal("winning resume changed original identity")
						}
						matches := 0
						for _, receipt := range view.Effects {
							if receipt.ID == req.EffectID {
								matches++
								if receipt.ReceiptID != ea1EntryReceiptID(req) || !bytes.Equal(receipt.Input, req.Input) {
									t.Fatal("receipt source mismatch")
								}
							}
						}
						if matches != 1 {
							t.Fatal("missing/duplicate receipt")
						}
					}
				} else {
					current, e := rt.Load(t.Context(), original.Run.ID)
					if e != nil {
						t.Fatal(e)
					}
					_, e = runner.Resume(t.Context(), current.Run.ID, current.Run.Revision)
					expected := workflow.ErrWorkflowTerminal
					if child {
						expected = kernel.ErrExecutionConflict
					}
					if !errors.Is(e, expected) || effects.calls.Load() != 1 {
						t.Fatalf("terminal source re-admitted: %v", e)
					}
				}
			})
		}
	}
}
