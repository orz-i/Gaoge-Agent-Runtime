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

// RunEA1WorkflowResumeCompensationP3 runs the actual public Resume and Cancel
// entries, not StartRun substitutes, at receipt-persisted/before-Mark barriers.
func RunEA1WorkflowResumeCompensationP3(t *testing.T, factory func(*testing.T) kernel.Store) {
	for _, entry := range []string{"resume", "compensation"} {
		for _, child := range []bool{false, true} {
			label := "root"
			if child {
				label = "child"
			}
			t.Run(entry+"/"+label, func(t *testing.T) {
				backing := factory(t)
				physical := backing.(kernel.ChildExecutionStore)
				initial, err := kernel.New(kernel.Dependencies{Store: backing})
				if err != nil {
					t.Fatal(err)
				}
				actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
				thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
				parentID := ""
				var parent kernel.Snapshot
				if child {
					parent, err = initial.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: workflow.RunKind, Actor: actor, Thread: thread, Goal: "parent", State: json.RawMessage(`{}`)})
					if err != nil {
						t.Fatal(err)
					}
					parentID = parent.Run.ID
				}
				effect := &ea1P3Effect{}
				setup, err := workflow.NewRunner(workflow.Dependencies{Runtime: initial, Effects: effect})
				if err != nil {
					t.Fatal(err)
				}
				nodes := []workflow.Node{
					{ID: "first", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{Kind: "external.first", Input: json.RawMessage(`{}`)}},
					{ID: "second", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{Kind: "external.second", Input: json.RawMessage(`{}`)}},
					{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`{}`)}},
				}
				draft := workflow.DefinitionDraft{ID: "p3-entry", Revision: 1, Name: "P3 original public entry", Nodes: nodes, Limits: workflow.Limits{MaxActivationsPerSegment: 8}}
				if entry == "compensation" {
					draft.Limits = workflow.Limits{}
					draft.Policy = workflow.DefinitionPolicy{MaxCostUnits: 2, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectWrite}
					draft.Nodes = []workflow.Node{
						{ID: "reserve", Type: workflow.NodeCompensation, Compensation: &workflow.CompensationNode{
							Do:   workflow.EffectCall{Class: workflow.EffectClassGeneric, Kind: "resource.reserve", Input: workflow.ValueSource{Kind: workflow.ValueLiteral, Value: json.RawMessage(`{}`)}, MaxCostUnits: 1},
							Undo: workflow.EffectCall{Class: workflow.EffectClassGeneric, Kind: "resource.release", Input: workflow.ValueSource{Kind: workflow.ValueNodeOutput, NodeID: "reserve"}},
						}},
						{ID: "wait", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "approval", Payload: json.RawMessage(`{}`)}},
						{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`{}`)}},
					}
				}
				definition, err := workflow.CompileDefinition(draft)
				if err != nil {
					t.Fatal(err)
				}
				before, err := setup.StartRun(t.Context(), workflow.StartRequest{ID: "original", RequestID: "request", ParentRunID: parentID, Actor: actor, Thread: thread, Goal: "test", Definition: definition, Input: json.RawMessage(`{}`)})
				if entry == "resume" && !errors.Is(err, workflow.ErrSegmentYielded) {
					t.Fatalf("expected pre-resume yield: %v", err)
				}
				if entry == "compensation" && (err != nil || before.Run.Status != kernel.RunStatusWaitingInput) {
					t.Fatalf("expected pre-Cancel wait: %s %v", before.Run.Status, err)
				}
				if effect.calls.Load() != 1 {
					t.Fatalf("setup physical calls=%d", effect.calls.Load())
				}
				if _, err = initial.LookupChildExecution(t.Context(), before.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("setup grant not retired: %v", err)
				}
				barrier := &ea1BeforeMarkBarrier{Store: backing, ChildExecutionStore: physical, entered: make(chan struct{}, 1), release: make(chan struct{})}
				defer barrier.unblock()
				rt, err := kernel.New(kernel.Dependencies{Store: barrier})
				if err != nil {
					t.Fatal(err)
				}
				runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: effect})
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					var e error
					if entry == "resume" {
						_, e = runner.Resume(context.Background(), before.Run.ID, before.Run.Revision)
					} else {
						_, e = runner.Cancel(context.Background(), before.Run.ID, before.Run.Revision, "original cancellation")
					}
					done <- e
				}()
				select {
				case <-barrier.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("public entry did not reach P3")
				}
				snapshot, err := rt.Load(t.Context(), before.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				view, err := workflow.ViewState(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				if entry == "resume" {
					if snapshot.Run.Status != kernel.RunStatusCompleted || len(view.Effects) != 2 || view.Effects[1].ReceiptID == "" {
						t.Fatalf("resume receipt not persisted: %+v", view)
					}
				} else {
					if snapshot.Run.Status != kernel.RunStatusCancelled || len(view.Compensations) != 1 || view.Compensations[0].Status != workflow.CompensationCompleted || view.Compensations[0].ReceiptID == "" {
						t.Fatalf("undo receipt not persisted: %+v", view)
					}
				}
				grant, err := rt.LookupChildExecution(t.Context(), snapshot.Run.ID)
				if err != nil || grant.ExpectedRevision != before.Run.Revision {
					t.Fatalf("wrong original Grant: %+v %v", grant, err)
				}
				if err = runner.ReconcileChildPhysicalGrant(t.Context(), snapshot.Run.ID); !errors.Is(err, kernel.ErrChildExecutionBusy) {
					t.Fatalf("premature reconcile: %v", err)
				}
				if err = physical.FinishChildExecution(t.Context(), grant); !errors.Is(err, kernel.ErrExecutionConflict) {
					t.Fatalf("premature Finish: %v", err)
				}
				if child {
					_, err = rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "competing parent")
					if !errors.Is(err, kernel.ErrChildExecutionBusy) {
						t.Fatalf("parent fence absent: %v", err)
					}
					unchanged, e := rt.Load(t.Context(), parent.Run.ID)
					if e != nil || unchanged.Run.Revision != parent.Run.Revision {
						t.Fatal("parent mutated")
					}
				}
				same, err := rt.LookupChildExecution(t.Context(), snapshot.Run.ID)
				if err != nil || same != grant {
					t.Fatal("original grant mutated")
				}
				if effect.calls.Load() != 2 {
					t.Fatalf("physical calls at P3=%d", effect.calls.Load())
				}
				barrier.unblock()
				select {
				case err = <-done:
					if err != nil {
						t.Fatalf("original entry failed: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("entry stalled")
				}
				if _, err = rt.LookupChildExecution(t.Context(), snapshot.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("settled Grant remains: %v", err)
				}
				if effect.calls.Load() != 2 {
					t.Fatal("physical replay")
				}
			})
		}
	}
}

type ea1P0BeginInterleave struct {
	kernel.Store
	kernel.ChildExecutionStore
	before func(context.Context) error
}

func (s *ea1P0BeginInterleave) BeginChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	if s.before != nil {
		f := s.before
		s.before = nil
		if err := f(ctx); err != nil {
			return err
		}
	}
	return s.ChildExecutionStore.BeginChildExecution(ctx, g)
}

type ea1WaitEffect struct{ calls atomic.Int32 }

func (e *ea1WaitEffect) Execute(context.Context, workflow.EffectRequest) (workflow.EffectResult, error) {
	e.calls.Add(1)
	return workflow.EffectResult{Disposition: workflow.DispositionCompleted, ReceiptID: "receipt", Output: json.RawMessage(`{}`)}, nil
}

// A second authoritative worker resolves the Root checkpoint between initial
// Load and Begin. Stale public ResolveWait must have zero physical side effects,
// no stranded Grant and preserve the winning checkpoint and revision.
func RunEA1RootWaitP0(t *testing.T, factory func(*testing.T) kernel.Store) {
	backing := factory(t)
	store := &ea1P0BeginInterleave{Store: backing, ChildExecutionStore: backing.(kernel.ChildExecutionStore)}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	effect := &ea1WaitEffect{}
	runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: effect, DeferResumption: true})
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.CompileDefinition(workflow.DefinitionDraft{ID: "root-wait", Revision: 1, Name: "Root Wait P0", Nodes: []workflow.Node{
		{ID: "wait", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "approval", Payload: json.RawMessage(`{}`)}},
		{ID: "effect", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{Kind: "external", Input: json.RawMessage(`{}`)}},
		{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`{}`)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	original, err := runner.StartRun(t.Context(), workflow.StartRequest{ID: "root", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "wait", Definition: def, Input: json.RawMessage(`{}`)})
	if err != nil || original.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("wait setup: %v", err)
	}
	for _, rev := range []uint64{0, original.Run.Revision - 1, original.Run.Revision + 1} {
		_, e := runner.ResolveWait(t.Context(), original.Run.ID, rev, json.RawMessage(`{}`))
		if !errors.Is(e, kernel.ErrConflict) {
			t.Fatalf("bad revision accepted %d: %v", rev, e)
		}
	}
	if _, e := runner.ResolveWait(t.Context(), original.Run.ID, original.Run.Revision, json.RawMessage(`invalid`)); !errors.Is(e, workflow.ErrInvalidExecution) {
		t.Fatalf("bad response accepted: %v", e)
	}
	var winner kernel.Snapshot
	store.before = func(ctx context.Context) error {
		var e error
		winner, e = runner.ResolveWait(ctx, original.Run.ID, original.Run.Revision, json.RawMessage(`{"winner":true}`))
		return e
	}
	_, err = runner.ResolveWait(t.Context(), original.Run.ID, original.Run.Revision, json.RawMessage(`{"stale":true}`))
	if !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("stale pre-admission must conflict: %v", err)
	}
	after, err := rt.Load(t.Context(), original.Run.ID)
	if err != nil || winner.Run.Revision <= original.Run.Revision || after.Run.Revision != winner.Run.Revision || string(after.State) != string(winner.State) {
		t.Fatalf("loser mutated winner: %v", err)
	}
	if effect.calls.Load() != 0 {
		t.Fatal("pre-admission path dispatched")
	}
	if _, err = rt.LookupChildExecution(t.Context(), original.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("stranded Grant: %v", err)
	}
	_, err = runner.ResolveWait(t.Context(), original.Run.ID, after.Run.Revision, json.RawMessage(`{}`))
	if !errors.Is(err, workflow.ErrWaitPending) {
		t.Fatalf("duplicate checkpoint accepted: %v", err)
	}
	resumed, err := runner.Resume(t.Context(), original.Run.ID, after.Run.Revision)
	if err != nil || resumed.Run.Status != kernel.RunStatusCompleted || effect.calls.Load() != 1 {
		t.Fatalf("winning continuation failed: %s %v calls=%d", resumed.Run.Status, err, effect.calls.Load())
	}
}
