package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// RunEA1WorkflowStartBoundaries covers actual WR-S P0/P1/P5 and the direct
// parented and built-in subworkflow admission portion of WC-S P0. It does not certify
// lease revocation, root deadlines, or cross-Run shared-budget election.
func RunEA1WorkflowStartBoundaries(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store)) {
	t.Run("child/P0/builtin-subworkflow-source", func(t *testing.T) { ea1StartSubworkflowAdmission(t, factory) })
	for _, child := range []bool{false, true} {
		for _, race := range []string{"revision", "terminal"} {
			name := "root"
			if child {
				name = "child"
			}
			t.Run(name+"/P0/"+race, func(t *testing.T) {
				backing, peer := factory(t)
				store := &ea1P0BeginInterleave{Store: backing, ChildExecutionStore: backing.(kernel.ChildExecutionStore)}
				rt := ea1StartRuntime(t, store)
				observer := ea1StartRuntime(t, peer)
				req := ea1StartRequest(t)
				var parent kernel.Snapshot
				if child {
					var e error
					parent, e = rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: workflow.RunKind, Goal: "original parent", Actor: req.Actor, Thread: req.Thread, State: json.RawMessage("{}")})
					if e != nil {
						t.Fatal(e)
					}
					req.ParentRunID = parent.Run.ID
				}
				effects := &ea1EntryReceiptEffect{}
				runner := ea1StartRunner(t, rt, effects)
				bad := req
				bad.ID = "bad-definition"
				bad.Definition.Hash = "tampered"
				if _, e := runner.StartRun(t.Context(), bad); !errors.Is(e, workflow.ErrInvalidExecution) {
					t.Fatalf("invalid definition admitted: %v", e)
				}
				ea1StartAbsent(t, rt, bad.ID)
				if child {
					for _, vector := range []string{"tenant", "actor", "thread", "missing"} {
						bad = req
						bad.ID = "bad-" + vector
						switch vector {
						case "tenant":
							bad.Actor.TenantID = "foreign"
						case "actor":
							bad.Actor.ActorID = "foreign"
						case "thread":
							bad.Thread.ID = "foreign"
						case "missing":
							bad.ParentRunID = "missing"
						}
						_, e := runner.StartRun(t.Context(), bad)
						want := kernel.ErrExecutionConflict
						if vector == "missing" {
							want = kernel.ErrNotFound
						}
						if !errors.Is(e, want) {
							t.Fatalf("foreign %s source: %v", vector, e)
						}
						ea1StartAbsent(t, rt, bad.ID)
						ea1CompBarrierUnchanged(t, rt, parent)
					}
				}
				var created, winner kernel.Snapshot
				store.before = func(ctx context.Context) error {
					var e error
					created, e = observer.Load(ctx, req.ID)
					if e != nil {
						return e
					}
					if race == "revision" {
						winner, e = observer.Apply(ctx, req.ID, created.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: created.State})
					} else if child {
						winner, e = observer.Cancel(ctx, parent.Run.ID, parent.Run.Revision, "parent wins before admission")
					} else {
						winner, e = observer.Cancel(ctx, req.ID, created.Run.Revision, "self wins before admission")
					}
					return e
				}
				returned, e := runner.StartRun(t.Context(), req)
				if !errors.Is(e, kernel.ErrExecutionConflict) || returned.Run.ID != req.ID {
					t.Fatalf("Start admission result: %v %+v", e, returned.Run)
				}
				if effects.calls.Load() != 0 {
					t.Fatal("denied admission dispatched")
				}
				// Create committed before admission: the Run MUST remain, not disappear.
				ea1CompBarrierUnchanged(t, observer, winner)
				want := winner
				if child && race == "terminal" {
					want = created
					ea1CompBarrierUnchanged(t, observer, created)
				}
				persisted, e := observer.Load(t.Context(), req.ID)
				if e != nil || !reflect.DeepEqual(persisted, want) {
					t.Fatal("post-Create denial lost original Run")
				}
				ea1StartSource(t, persisted, req)
				if _, e = observer.LookupChildExecution(t.Context(), req.ID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("denied admission left grant: %v", e)
				}
				for _, vector := range []string{"same", "actor", "thread", "input", "definition"} {
					duplicate := req
					switch vector {
					case "actor":
						duplicate.Actor.ActorID = "other"
					case "thread":
						duplicate.Thread.ID = "other"
					case "input":
						duplicate.Input = json.RawMessage(`{"other":true}`)
					case "definition":
						draft := ea1StartDraft()
						draft.ID = "other"
						duplicate.Definition, e = workflow.CompileDefinition(draft)
						if e != nil {
							t.Fatal(e)
						}
					}
					_, e = runner.StartRun(t.Context(), duplicate)
					// Parent identity validation can reject a foreign duplicate before duplicate-ID lookup.
					validRejection := errors.Is(e, kernel.ErrAlreadyExists)
					// Memory checks duplicate ID first; SQL may validate the parent first.
					if child && (vector == "actor" || vector == "thread" || race == "terminal") {
						validRejection = validRejection || errors.Is(e, kernel.ErrExecutionConflict)
					}
					if !validRejection {
						t.Fatalf("duplicate %s: %v", vector, e)
					}
					ea1CompBarrierUnchanged(t, observer, persisted)
					if child {
						p, pe := observer.Load(t.Context(), parent.Run.ID)
						if pe != nil {
							t.Fatal(pe)
						}
						expectedParent := parent
						if race == "terminal" {
							expectedParent = winner
						}
						if !reflect.DeepEqual(p, expectedParent) {
							t.Fatal("duplicate changed parent")
						}
					}
				}
				if effects.calls.Load() != 0 {
					t.Fatal("duplicate sent physical effect")
				}
				if race == "revision" {
					finished, e := runner.Resume(t.Context(), req.ID, persisted.Run.Revision)
					if e != nil || finished.Run.Status != kernel.RunStatusCompleted || effects.calls.Load() != 1 {
						t.Fatalf("created Run cannot explicitly resume: %v", e)
					}
					ea1StartSource(t, finished, req)
				}
			})
		}
	}
	for _, phase := range []string{"P1", "P5", "P5-retry"} {
		t.Run("root/"+phase, func(t *testing.T) {
			backing, peer := factory(t)
			observer := ea1StartRuntime(t, peer)
			req := ea1StartRequest(t)
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			owned := backing
			if phase == "P1" {
				owned = &ea1WaitAdmissionBarrier{Store: backing, ChildExecutionStore: backing.(kernel.ChildExecutionStore), entered: entered, release: release}
			}
			effects := &ea1StartEffects{entered: entered, release: release, block: phase != "P1", retry: phase == "P5-retry"}
			owner := ea1StartRunner(t, ea1StartRuntime(t, owned), effects)
			competitor := ea1StartRunner(t, observer, effects)
			done := make(chan error, 1)
			go func() { _, e := owner.StartRun(context.Background(), req); done <- e }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("Start barrier not reached")
			}
			during, e := observer.Load(t.Context(), req.ID)
			if e != nil {
				t.Fatal(e)
			}
			ea1StartSource(t, during, req)
			grant, e := observer.LookupChildExecution(t.Context(), req.ID)
			if e != nil || grant.ExpectedRevision != 1 {
				t.Fatalf("original Start grant missing: %+v %v", grant, e)
			}
			count := len(effects.requests())
			if phase == "P1" && count != 0 || phase != "P1" && count != 1 {
				t.Fatal("wrong physical boundary")
			}
			for _, status := range []kernel.RunStatus{kernel.RunStatusRunning, kernel.RunStatusCancelled} {
				_, e = observer.Apply(t.Context(), req.ID, during.Run.Revision, kernel.Mutation{Status: status, State: during.State})
				if !errors.Is(e, kernel.ErrChildExecutionBusy) {
					t.Fatalf("competing Apply: %v", e)
				}
				ea1CompBarrierUnchanged(t, observer, during)
			}
			_, e = observer.Cancel(t.Context(), req.ID, during.Run.Revision, "competing terminal")
			if !errors.Is(e, kernel.ErrChildExecutionBusy) {
				t.Fatalf("kernel Cancel: %v", e)
			}
			_, e = competitor.Cancel(t.Context(), req.ID, during.Run.Revision, "competing compensation")
			if !errors.Is(e, kernel.ErrChildExecutionBusy) {
				t.Fatalf("workflow Cancel: %v", e)
			}
			_, e = competitor.Resume(t.Context(), req.ID, during.Run.Revision)
			if !errors.Is(e, kernel.ErrChildExecutionBusy) {
				t.Fatalf("competing Resume: %v", e)
			}
			_, e = competitor.StartRun(t.Context(), req)
			if !errors.Is(e, kernel.ErrAlreadyExists) {
				t.Fatalf("duplicate Start: %v", e)
			}
			if e = competitor.ReconcileChildPhysicalGrant(t.Context(), req.ID); !errors.Is(e, kernel.ErrChildExecutionBusy) {
				t.Fatalf("premature reconcile: %v", e)
			}
			if e = peer.(kernel.ChildExecutionStore).FinishChildExecution(t.Context(), grant); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("premature Finish: %v", e)
			}
			ea1CompBarrierUnchanged(t, observer, during)
			same, e := observer.LookupChildExecution(t.Context(), req.ID)
			if e != nil || same != grant || len(effects.requests()) != count {
				t.Fatal("competition changed grant or dispatched")
			}
			unblock()
			select {
			case e = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Start callback did not finish")
			}
			current, loadErr := observer.Load(t.Context(), req.ID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if phase == "P5-retry" {
				if !errors.Is(e, workflow.ErrEffectPending) {
					t.Fatalf("safe retry not scheduled: %v", e)
				}
				v, ve := workflow.ViewState(current)
				if ve != nil || len(v.Effects) != 1 || v.Effects[0].Attempt != 2 || v.Budget.Effects != 1 || v.Budget.CostUnitsUsed != 0 || v.Budget.CostUnitsReserved != 1 {
					t.Fatal("retry reservation not stable")
				}
				if _, e = observer.LookupChildExecution(t.Context(), req.ID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("safe attempt not retired: %v", e)
				}
				current, e = competitor.Resume(t.Context(), req.ID, current.Run.Revision)
			}
			if e != nil || current.Run.Status != kernel.RunStatusCompleted {
				t.Fatalf("original completion: %v status=%s", e, current.Run.Status)
			}
			ea1StartSource(t, current, req)
			requests := effects.requests()
			attempt := 1
			if phase == "P5-retry" {
				attempt = 2
			}
			if len(requests) != attempt {
				t.Fatal("wrong physical attempts")
			}
			last := requests[len(requests)-1]
			v, ve := workflow.ViewState(current)
			if ve != nil || len(v.Effects) != 1 {
				t.Fatal("missing original receipt")
			}
			receipt := v.Effects[0]
			if last.RunID != req.ID || last.Actor != req.Actor || last.Thread != req.Thread || last.DefinitionID != req.Definition.ID || last.DefinitionHash != req.Definition.Hash || last.Attempt != attempt || last.Compensation || last.Kind != "external.once" || last.NodeID != "effect" || string(last.Input) != `{"source":"original"}` || receipt.ID != last.EffectID || receipt.Attempt != last.Attempt || receipt.Class != last.Class || receipt.Kind != last.Kind || receipt.NodeID != last.NodeID || receipt.Compensation || !reflect.DeepEqual(receipt.Input, last.Input) || receipt.ReceiptID != ea1EntryReceiptID(last) || receipt.Status != workflow.EffectCompleted {
				t.Fatal("original full request/receipt not bound")
			}
			if attempt == 2 {
				first := requests[0]
				if first.OwnerNodeID != "effect" {
					t.Fatal("wrong original owner")
				}
				first.Attempt = 2
				first.OwnerNodeID = fmt.Sprintf("%s_attempt_2", first.EffectID)
				if !reflect.DeepEqual(first, last) {
					t.Fatal("retry changed immutable request source")
				}
			}
			if v.Budget.Effects != 1 || v.Budget.CostUnitsUsed != 1 || v.Budget.CostUnitsReserved != 0 {
				t.Fatal("budget not settled once")
			}
			if _, e = observer.LookupChildExecution(t.Context(), req.ID); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatalf("settled grant stranded: %v", e)
			}
			_, e = competitor.Resume(t.Context(), req.ID, current.Run.Revision)
			if !errors.Is(e, workflow.ErrWorkflowTerminal) {
				t.Fatalf("terminal resume: %v", e)
			}
			ea1CompBarrierUnchanged(t, observer, current)
		})
	}
}
func ea1StartDraft() workflow.DefinitionDraft {
	return workflow.DefinitionDraft{ID: "start-source", Revision: 1, Name: "original Start", Limits: workflow.Limits{MaxActivationsPerSegment: 8}, Policy: workflow.DefinitionPolicy{MaxCostUnits: 1, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectWrite}, Nodes: []workflow.Node{
		{ID: "effect", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{Kind: "external.once", Input: json.RawMessage(`{"source":"original"}`), MaxCostUnits: 1, Retry: workflow.RetryPolicy{MaxAttempts: 2, RetryableErrorCodes: []string{"safe-retry"}}}},
		{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage("{}")}},
	}}
}
func ea1StartRequest(t *testing.T) workflow.StartRequest {
	t.Helper()
	def, e := workflow.CompileDefinition(ea1StartDraft())
	if e != nil {
		t.Fatal(e)
	}
	return workflow.StartRequest{ID: "original", RequestID: "original-source", Goal: "original Start", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "original"}, Definition: def, Input: json.RawMessage("{}")}
}
func ea1StartRuntime(t *testing.T, s kernel.Store) *kernel.Runtime {
	t.Helper()
	r, e := kernel.New(kernel.Dependencies{Store: s})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func ea1StartRunner(t *testing.T, r *kernel.Runtime, e workflow.EffectExecutor) *workflow.Runner {
	t.Helper()
	runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: r, Effects: e})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}
func ea1StartAbsent(t *testing.T, rt *kernel.Runtime, id string) {
	t.Helper()
	if _, e := rt.Load(t.Context(), id); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("pre-Create denial persisted: %v", e)
	}
	if _, e := rt.LookupChildExecution(t.Context(), id); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("denied source grant: %v", e)
	}
}
func ea1StartSource(t *testing.T, s kernel.Snapshot, r workflow.StartRequest) {
	t.Helper()
	v, e := workflow.ViewState(s)
	if e != nil || v.Definition.ID != r.Definition.ID || v.Definition.Hash != r.Definition.Hash || v.Definition.Revision != r.Definition.Revision || s.Run.ID != r.ID || s.Run.ParentRunID != r.ParentRunID || s.Run.Actor != r.Actor || s.Run.Thread != r.Thread || s.Run.RequestID != r.RequestID || !reflect.DeepEqual(v.Input, r.Input) {
		t.Fatal("created original source changed")
	}
}

type ea1StartEffects struct {
	mu           sync.Mutex
	captured     []workflow.EffectRequest
	entered      chan struct{}
	release      chan struct{}
	block, retry bool
}

func (e *ea1StartEffects) requests() []workflow.EffectRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]workflow.EffectRequest(nil), e.captured...)
}
func (e *ea1StartEffects) Execute(_ context.Context, r workflow.EffectRequest) (workflow.EffectResult, error) {
	r.Input = append(json.RawMessage(nil), r.Input...)
	e.mu.Lock()
	e.captured = append(e.captured, r)
	e.mu.Unlock()
	if r.Attempt == 1 {
		if e.block {
			e.entered <- struct{}{}
			<-e.release
		}
		if e.retry {
			return workflow.EffectResult{Disposition: workflow.DispositionFailed, PhysicalNoEffect: true, ErrorCode: "safe-retry"}, nil
		}
	}
	return workflow.EffectResult{Disposition: workflow.DispositionCompleted, ReceiptID: ea1EntryReceiptID(r), Output: append(json.RawMessage(nil), r.Input...), CostUnits: r.MaxCostUnits}, nil
}

// The built-in executor, not a direct parented StartRequest, creates this child
// from an exact published reference and the parent's durable Effect intent.
func ea1StartSubworkflowAdmission(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store)) {
	t.Helper()
	backing, peer := factory(t)
	observer := ea1StartRuntime(t, peer)
	wrapped := &ea1StartChildAdmission{Store: backing, ChildExecutionStore: backing.(kernel.ChildExecutionStore)}
	rt := ea1StartRuntime(t, wrapped)
	registry, e := workflow.NewDefinitionRegistry(workflow.NewMemoryDefinitionStore(), nil)
	if e != nil {
		t.Fatal(e)
	}
	req := ea1StartRequest(t)
	published, _, _, e := registry.Publish(t.Context(), workflow.PublishDefinitionRequest{Scope: workflow.DefinitionScope{Kind: workflow.DefinitionScopeActor, TenantID: req.Actor.TenantID, ActorID: req.Actor.ActorID}, IdempotencyKey: "child-source", PublishedBy: req.Actor.ActorID, Draft: ea1StartDraft()})
	if e != nil {
		t.Fatal(e)
	}
	ref := workflow.DefinitionReference{ID: published.Definition.ID, Revision: published.Definition.Revision, Hash: published.Definition.Hash}
	parentDef, e := workflow.CompileDefinition(workflow.DefinitionDraft{ID: "builtin-parent", Revision: 1, Name: "built-in source", Limits: workflow.Limits{MaxActivationsPerSegment: 8}, Policy: workflow.DefinitionPolicy{MaxCostUnits: 1, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectWrite}, Nodes: []workflow.Node{
		{ID: "child", Type: workflow.NodeSubworkflow, Subworkflow: &workflow.SubworkflowNode{Definition: ref, Input: workflow.ValueSource{Kind: workflow.ValueWorkflowInput}}},
		{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage("{}")}},
	}})
	if e != nil {
		t.Fatal(e)
	}
	req.Definition = parentDef
	req.Input = json.RawMessage(`{"source":"nested-original"}`)
	var original, winner kernel.Snapshot
	var admitted bool
	wrapped.beforeChild = func(ctx context.Context, g kernel.ChildExecutionGrant) error {
		child, err := observer.Load(ctx, g.ChildRunID)
		if err != nil {
			return err
		}
		if child.Run.ParentRunID == "" {
			return nil
		}
		if admitted {
			t.Fatal("nested admission repeated")
		}
		admitted = true
		original = child
		parent, err := observer.Load(ctx, req.ID)
		if err != nil {
			return err
		}
		pv, err := workflow.ViewState(parent)
		if err != nil || len(pv.Effects) != 1 {
			t.Fatal("missing original parent Effect intent")
		}
		effect := pv.Effects[0]
		cv, err := workflow.ViewState(child)
		if err != nil || effect.Class != workflow.EffectClassSubworkflow || effect.Definition == nil || *effect.Definition != ref || effect.Attempt != 1 || effect.NodeID != "child" || child.Run.ID != effect.ID+"-child" || child.Run.RequestID != effect.ID || child.Run.ParentRunID != parent.Run.ID || child.Run.Actor != req.Actor || child.Run.Thread != req.Thread || child.Run.Goal != "subworkflow:"+ref.ID || cv.Definition.ID != ref.ID || cv.Definition.Hash != ref.Hash || cv.Definition.Revision != ref.Revision || cv.NestedDepth != 1 || !reflect.DeepEqual(cv.Input, req.Input) || !reflect.DeepEqual(effect.Input, req.Input) {
			t.Fatal("built-in child not bound to original published source/Effect")
		}
		winner, err = observer.Apply(ctx, child.Run.ID, child.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: child.State})
		return err
	}
	external := &ea1EntryReceiptEffect{}
	runner, e := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Registry: registry, Effects: external})
	if e != nil {
		t.Fatal(e)
	}
	_, e = runner.StartRun(t.Context(), req)
	if !admitted || !errors.Is(wrapped.denied, kernel.ErrExecutionConflict) || e == nil || external.calls.Load() != 0 {
		t.Fatalf("built-in stale child admission did not fail before dispatch: parent=%v admission=%v calls=%d", e, wrapped.denied, external.calls.Load())
	}
	if original.Run.ID == "" || winner.Run.Revision != original.Run.Revision+1 {
		t.Fatal("child Create/admission boundary missing")
	}
	ea1CompBarrierUnchanged(t, observer, winner)
	if _, e = observer.LookupChildExecution(t.Context(), winner.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("rejected built-in child left grant: %v", e)
	}
}

type ea1StartChildAdmission struct {
	kernel.Store
	kernel.ChildExecutionStore
	beforeChild func(context.Context, kernel.ChildExecutionGrant) error
	denied      error
}

func (s *ea1StartChildAdmission) BeginChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	if s.beforeChild != nil {
		if e := s.beforeChild(ctx, g); e != nil {
			return e
		}
	}
	err := s.ChildExecutionStore.BeginChildExecution(ctx, g)
	if err != nil {
		s.denied = err
	}
	return err
}
