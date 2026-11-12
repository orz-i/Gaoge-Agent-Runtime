package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// RunEA1WaitBarriers covers only Root/Child ResolveWait P1 and P3.
// A peer Store observes authoritative fencing independently of the caller.
func RunEA1WaitBarriers(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store)) {
	for _, child := range []bool{false, true} {
		for _, phase := range []string{"P1", "P3"} {
			name := "root"
			if child {
				name = "child"
			}
			t.Run(name+"/"+phase, func(t *testing.T) {
				backing, peer := factory(t)
				rt, err := kernel.New(kernel.Dependencies{Store: backing})
				if err != nil {
					t.Fatal(err)
				}
				observer, err := kernel.New(kernel.Dependencies{Store: peer})
				if err != nil {
					t.Fatal(err)
				}
				actor := kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}
				thread := kernel.ThreadRef{Kind: "conversation", ID: "original"}
				var parent kernel.Snapshot
				parentID := ""
				if child {
					parent, err = rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: workflow.RunKind, Actor: actor, Thread: thread, Goal: "parent", State: json.RawMessage("{}")})
					if err != nil {
						t.Fatal(err)
					}
					parentID = parent.Run.ID
				}
				def, err := workflow.CompileDefinition(workflow.DefinitionDraft{ID: "wait-barrier", Revision: 1, Name: "original ResolveWait", Nodes: []workflow.Node{
					{ID: "wait", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "approval", Payload: json.RawMessage("{}")}},
					{ID: "effect", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{Kind: "external.once", Input: json.RawMessage("{\"source\":\"original\"}")}},
					{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage("{}")}},
				}})
				if err != nil {
					t.Fatal(err)
				}
				effects := &ea1EntryReceiptEffect{}
				setup, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: effects})
				if err != nil {
					t.Fatal(err)
				}
				original, err := setup.StartRun(t.Context(), workflow.StartRequest{ID: "original", RequestID: "request", ParentRunID: parentID, Actor: actor, Thread: thread, Goal: "wait", Definition: def, Input: json.RawMessage("{}")})
				if err != nil || original.Run.Status != kernel.RunStatusWaitingInput || effects.calls.Load() != 0 {
					t.Fatalf("setup not waiting: %v", err)
				}
				entered := make(chan struct{}, 1)
				release := make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				var intercepted kernel.Store
				if phase == "P1" {
					intercepted = &ea1WaitAdmissionBarrier{Store: backing, ChildExecutionStore: backing.(kernel.ChildExecutionStore), entered: entered, release: release}
				} else {
					intercepted = &ea1BeforeMarkBarrier{Store: backing, ChildExecutionStore: backing.(kernel.ChildExecutionStore), entered: entered, release: release}
				}
				ownerRT, err := kernel.New(kernel.Dependencies{Store: intercepted})
				if err != nil {
					t.Fatal(err)
				}
				owner, err := workflow.NewRunner(workflow.Dependencies{Runtime: ownerRT, Effects: effects})
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					_, e := owner.ResolveWait(context.Background(), original.Run.ID, original.Run.Revision, json.RawMessage("{\"approved\":true}"))
					done <- e
				}()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("actual ResolveWait never reached barrier")
				}
				snapshot, err := observer.Load(t.Context(), original.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				grant, err := observer.LookupChildExecution(t.Context(), original.Run.ID)
				if err != nil || grant.ExpectedRevision != original.Run.Revision {
					t.Fatalf("original admission lost: %+v %v", grant, err)
				}
				if phase == "P1" {
					if effects.calls.Load() != 0 || !reflect.DeepEqual(snapshot, original) {
						t.Fatal("P1 dispatched or changed checkpoint before grant returned")
					}
				} else {
					view, e := workflow.ViewState(snapshot)
					requests := effects.originalRequests()
					if e != nil || snapshot.Run.Status != kernel.RunStatusCompleted || len(requests) != 1 || len(view.Effects) != 1 {
						t.Fatal("P3 missing durable receipt")
					}
					beforeView, beforeErr := workflow.ViewState(original)
					if beforeErr != nil || len(beforeView.Waits) != 1 || len(view.Waits) != 1 ||
						view.Waits[0].ID != beforeView.Waits[0].ID || view.Waits[0].Status != workflow.WaitResolved ||
						string(view.Waits[0].Response) != "{\"approved\":true}" {
						t.Fatal("P3 original Wait or response not preserved")
					}
					req := requests[0]
					receipt := view.Effects[0]
					if req.RunID != original.Run.ID || req.Actor != actor || req.Thread != thread || req.DefinitionID != def.ID || req.DefinitionHash != def.Hash || req.Attempt != 1 || req.Compensation ||
						receipt.ID != req.EffectID || receipt.Attempt != req.Attempt || receipt.NodeID != req.NodeID || receipt.Kind != req.Kind || receipt.Class != req.Class || receipt.Compensation || receipt.ReceiptID != ea1EntryReceiptID(req) || receipt.Status != workflow.EffectCompleted || !reflect.DeepEqual(receipt.Input, req.Input) {
						t.Fatal("P3 receipt not original full request")
					}
				}
				peerRunner, e := workflow.NewRunner(workflow.Dependencies{Runtime: observer, Effects: effects})
				if e != nil {
					t.Fatal(e)
				}
				if e = peerRunner.ReconcileChildPhysicalGrant(t.Context(), original.Run.ID); !errors.Is(e, kernel.ErrChildExecutionBusy) {
					t.Fatalf("premature reconcile: %v", e)
				}
				if e = peer.(kernel.ChildExecutionStore).FinishChildExecution(t.Context(), grant); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("premature finish: %v", e)
				}
				targets := []kernel.Snapshot{snapshot}
				if child {
					targets = append(targets, parent)
				}
				for _, want := range targets {
					expected := kernel.ErrChildExecutionBusy
					if phase == "P3" && want.Run.ID == original.Run.ID {
						expected = kernel.ErrTerminal
					}
					_, e = observer.Cancel(t.Context(), want.Run.ID, want.Run.Revision, "competing cancel")
					if !errors.Is(e, expected) {
						t.Fatalf("cancel not fenced: %v", e)
					}
					_, e = observer.Apply(t.Context(), want.Run.ID, want.Run.Revision, kernel.Mutation{Status: want.Run.Status, State: want.State, Checkpoint: want.Checkpoint})
					if !errors.Is(e, expected) {
						t.Fatalf("apply not fenced: %v", e)
					}
					after, loadErr := observer.Load(t.Context(), want.Run.ID)
					if loadErr != nil || !reflect.DeepEqual(after, want) {
						t.Fatal("rejected control mutated self/ancestor")
					}
				}
				same, e := observer.LookupChildExecution(t.Context(), original.Run.ID)
				if e != nil || same != grant {
					t.Fatal("peer mutated original grant")
				}
				unblock()
				select {
				case e = <-done:
					if e != nil {
						t.Fatal(e)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("original owner stalled")
				}
				if effects.calls.Load() != 1 {
					t.Fatal("physical replay")
				}
				if _, e = observer.LookupChildExecution(t.Context(), original.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("settled grant stranded: %v", e)
				}
			})
		}
	}
}

type ea1WaitAdmissionBarrier struct {
	kernel.Store
	kernel.ChildExecutionStore
	entered chan struct{}
	release chan struct{}
}

func (s *ea1WaitAdmissionBarrier) BeginChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	if err := s.ChildExecutionStore.BeginChildExecution(ctx, g); err != nil {
		return err
	}
	s.entered <- struct{}{}
	<-s.release
	return nil
}
