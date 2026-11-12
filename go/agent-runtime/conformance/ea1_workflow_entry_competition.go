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

// RunEA1WorkflowEntryCompetition covers only WC-R-P1/P5 and WR-W-P5.
// These public Workflow entries own a physical Grant and per-Run budget,
// not Agent ExecutionLease or cross-Run SharedBudget objects.
func RunEA1WorkflowEntryCompetition(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store)) {
	for _, entry := range []string{"resume-child", "wait-root"} {
		for _, phase := range []string{"P1", "P5", "P5-retry"} {
			if entry == "wait-root" && phase == "P1" {
				continue
			}
			t.Run(entry+"/"+phase, func(t *testing.T) {
				backing, peer := factory(t)
				rt, e := kernel.New(kernel.Dependencies{Store: backing})
				if e != nil {
					t.Fatal(e)
				}
				observer, e := kernel.New(kernel.Dependencies{Store: peer})
				if e != nil {
					t.Fatal(e)
				}
				actor := kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}
				thread := kernel.ThreadRef{Kind: "conversation", ID: "original"}
				child := entry == "resume-child"
				parentID := ""
				var parent kernel.Snapshot
				var deadline *time.Time
				if child && phase == "P5" {
					d := time.Now().UTC().Add(2 * time.Second)
					deadline = &d
				}
				if child {
					parent, e = rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: workflow.RunKind, Actor: actor, Thread: thread, Goal: "original ancestor", State: json.RawMessage("{}"), DeadlineAt: deadline})
					if e != nil {
						t.Fatal(e)
					}
					parentID = parent.Run.ID
				}
				first := workflow.Node{ID: "wait", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "approval", Payload: json.RawMessage("{}")}}
				limits := workflow.Limits{}
				if child {
					first = workflow.Node{ID: "first", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{Kind: "external.first", Input: json.RawMessage("{\"source\":\"first\"}"), MaxCostUnits: 1}}
					limits.MaxActivationsPerSegment = 8
				}
				def, e := workflow.CompileDefinition(workflow.DefinitionDraft{ID: "entry-competition", Revision: 1, Name: "original public entry", Limits: limits, Policy: workflow.DefinitionPolicy{MaxCostUnits: 2, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectWrite}, Nodes: []workflow.Node{
					first, {ID: "target", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{Kind: "external.target", Input: json.RawMessage("{\"source\":\"target\"}"), MaxCostUnits: 1, Retry: workflow.RetryPolicy{MaxAttempts: 2, RetryableErrorCodes: []string{"safe-retry"}}}},
					{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage("{}")}},
				}})
				if e != nil {
					t.Fatal(e)
				}
				entered := make(chan struct{}, 1)
				release := make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				effects := &ea1EntryCompetitionEffects{entered: entered, release: release, retry: phase == "P5-retry"}
				setup, e := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: effects})
				if e != nil {
					t.Fatal(e)
				}
				original, e := setup.StartRun(t.Context(), workflow.StartRequest{ID: "original", RequestID: "request", ParentRunID: parentID, Actor: actor, Thread: thread, Goal: "entry", Definition: def, Input: json.RawMessage("{}")})
				if child {
					if !errors.Is(e, workflow.ErrSegmentYielded) || len(effects.requests()) != 1 {
						t.Fatalf("pre-Resume yield: %v", e)
					}
				} else if e != nil || original.Run.Status != kernel.RunStatusWaitingInput || len(effects.requests()) != 0 {
					t.Fatalf("pre-Wait setup: %v", e)
				}
				effects.block = phase != "P1"
				owned := backing
				if phase == "P1" {
					owned = &ea1WaitAdmissionBarrier{Store: backing, ChildExecutionStore: backing.(kernel.ChildExecutionStore), entered: entered, release: release}
				}
				ownerRT, e := kernel.New(kernel.Dependencies{Store: owned})
				if e != nil {
					t.Fatal(e)
				}
				owner, e := workflow.NewRunner(workflow.Dependencies{Runtime: ownerRT, Effects: effects})
				if e != nil {
					t.Fatal(e)
				}
				competing, e := workflow.NewRunner(workflow.Dependencies{Runtime: observer, Effects: effects})
				if e != nil {
					t.Fatal(e)
				}
				done := make(chan error, 1)
				go func() {
					var err error
					if child {
						_, err = owner.Resume(context.Background(), original.Run.ID, original.Run.Revision)
					} else {
						_, err = owner.ResolveWait(context.Background(), original.Run.ID, original.Run.Revision, json.RawMessage("{\"approved\":true}"))
					}
					done <- err
				}()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("actual public entry did not reach fixed barrier")
				}
				during, e := observer.Load(t.Context(), original.Run.ID)
				if e != nil {
					t.Fatal(e)
				}
				if !child {
					ea1EntryCompetitionWaitOrigin(t, during, original)
				}
				grant, e := observer.LookupChildExecution(t.Context(), original.Run.ID)
				if e != nil || grant.ExpectedRevision != original.Run.Revision {
					t.Fatal("original admission missing")
				}
				requests := effects.requests()
				expectedCalls := 1
				if child {
					expectedCalls = 2
				}
				if phase == "P1" {
					expectedCalls--
				}
				if len(requests) != expectedCalls {
					t.Fatal("unexpected physical send count")
				}
				if phase == "P1" {
					ea1CompBarrierUnchanged(t, observer, original)
				} else {
					assertEA1EntryCompetitionSource(t, during, def, requests, false, child)
				}
				if _, e = competing.Resume(t.Context(), during.Run.ID, during.Run.Revision); !errors.Is(e, kernel.ErrChildExecutionBusy) {
					t.Fatalf("competing Resume: %v", e)
				}
				ea1CompBarrierUnchanged(t, observer, during)
				if _, e = competing.Cancel(t.Context(), during.Run.ID, during.Run.Revision, "competitor"); !errors.Is(e, kernel.ErrChildExecutionBusy) {
					t.Fatalf("competing Cancel: %v", e)
				}
				ea1CompBarrierUnchanged(t, observer, during)
				if !child {
					if _, e = competing.ResolveWait(t.Context(), during.Run.ID, during.Run.Revision, json.RawMessage("{}")); !errors.Is(e, workflow.ErrWaitPending) {
						t.Fatalf("repeated resolved Wait: %v", e)
					}
					ea1CompBarrierUnchanged(t, observer, during)
				}
				if e = competing.ReconcileChildPhysicalGrant(t.Context(), during.Run.ID); !errors.Is(e, kernel.ErrChildExecutionBusy) {
					t.Fatalf("premature reconciliation: %v", e)
				}
				if e = peer.(kernel.ChildExecutionStore).FinishChildExecution(t.Context(), grant); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("premature Finish: %v", e)
				}
				targets := []kernel.Snapshot{during}
				if child {
					targets = append(targets, parent)
				}
				for _, before := range targets {
					for _, status := range []kernel.RunStatus{kernel.RunStatusRunning, kernel.RunStatusCancelled} {
						_, e = observer.Apply(t.Context(), before.Run.ID, before.Run.Revision, kernel.Mutation{Status: status, State: before.State})
						if !errors.Is(e, kernel.ErrChildExecutionBusy) {
							t.Fatalf("self/ancestor Apply: %v", e)
						}
						ea1CompBarrierUnchanged(t, observer, before)
					}
					_, e = observer.Cancel(t.Context(), before.Run.ID, before.Run.Revision, "control")
					if !errors.Is(e, kernel.ErrChildExecutionBusy) {
						t.Fatalf("self/ancestor Cancel: %v", e)
					}
					ea1CompBarrierUnchanged(t, observer, before)
				}
				if deadline != nil {
					if delay := time.Until(*deadline); delay > 0 {
						time.Sleep(delay)
					}
					tolerance := time.Now().Add(time.Second)
					for {
						_, applied, err := observer.Expire(t.Context(), parent.Run.ID, parent.Run.Revision)
						ea1CompBarrierUnchanged(t, observer, parent)
						if errors.Is(err, kernel.ErrChildExecutionBusy) && !applied {
							break
						}
						if applied || !errors.Is(err, kernel.ErrDeadline) || time.Now().After(tolerance) {
							t.Fatalf("actual ancestor deadline: %t %v", applied, err)
						}
						time.Sleep(20 * time.Millisecond)
					}
				}
				same, e := observer.LookupChildExecution(t.Context(), during.Run.ID)
				if e != nil || same != grant {
					t.Fatal("competing controls altered original grant")
				}
				ea1CompBarrierUnchanged(t, observer, during)
				if len(effects.requests()) != expectedCalls {
					t.Fatal("competition replayed physical callback")
				}
				unblock()
				select {
				case e = <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("owner stalled")
				}
				settled, loadErr := observer.Load(t.Context(), during.Run.ID)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				if phase == "P5-retry" {
					if !errors.Is(e, workflow.ErrEffectPending) {
						t.Fatalf("safe retry scheduling: %v", e)
					}
					if _, lookupErr := observer.LookupChildExecution(t.Context(), settled.Run.ID); !errors.Is(lookupErr, kernel.ErrNotFound) {
						t.Fatalf("safe attempt grant not retired: %v", lookupErr)
					}
					settled, e = competing.Resume(t.Context(), settled.Run.ID, settled.Run.Revision)
				}
				if e != nil || settled.Run.Status != kernel.RunStatusCompleted {
					t.Fatalf("original completion: %v status=%s", e, settled.Run.Status)
				}
				assertEA1EntryCompetitionSource(t, settled, def, effects.requests(), true, child)
				if !child {
					ea1EntryCompetitionWaitOrigin(t, settled, original)
				}
				if _, e = observer.LookupChildExecution(t.Context(), settled.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("settled grant remains: %v", e)
				}
				calls := len(effects.requests())
				if _, e = competing.Resume(t.Context(), settled.Run.ID, settled.Run.Revision); !errors.Is(e, workflow.ErrWorkflowTerminal) {
					t.Fatalf("terminal Resume: %v", e)
				}
				if _, e = competing.Cancel(t.Context(), settled.Run.ID, settled.Run.Revision, "again"); !errors.Is(e, workflow.ErrWorkflowTerminal) {
					t.Fatalf("terminal Cancel: %v", e)
				}
				ea1CompBarrierUnchanged(t, observer, settled)
				if len(effects.requests()) != calls {
					t.Fatal("terminal replay")
				}
			})
		}
	}
}

func assertEA1EntryCompetitionSource(t *testing.T, s kernel.Snapshot, def workflow.Definition, requests []workflow.EffectRequest, completed, child bool) {
	t.Helper()
	view, e := workflow.ViewState(s)
	count := 1
	used := int64(0)
	if child {
		count = 2
		used = 1
	}
	if e != nil || len(view.Effects) != count || view.Definition.Hash != def.Hash || view.Definition.ID != def.ID {
		t.Fatal("original definition/effect structure changed")
	}
	last := requests[len(requests)-1]
	wantCalls := count
	if last.Attempt == 2 {
		wantCalls++
	}
	if len(requests) != wantCalls {
		t.Fatal("extra physical dispatch or missing original attempt")
	}
	if child && (requests[0].Kind != "external.first" || requests[0].Attempt != 1 || string(requests[0].Input) != "{\"source\":\"first\"}") {
		t.Fatal("original first Effect source changed")
	}
	for _, effect := range view.Effects {
		var request workflow.EffectRequest
		for _, r := range requests {
			if r.EffectID == effect.ID {
				request = r
			}
		}
		if request.EffectID == "" || request.RunID != s.Run.ID || request.Actor != s.Run.Actor || request.Thread != s.Run.Thread || request.DefinitionID != def.ID || request.DefinitionHash != def.Hash || request.Compensation ||
			effect.ID != request.EffectID || effect.NodeID != request.NodeID || effect.Kind != request.Kind || effect.Class != request.Class || effect.Attempt != request.Attempt || effect.Compensation || !reflect.DeepEqual(effect.Input, request.Input) {
			t.Fatal("original complete request binding changed")
		}
		if effect.ID != last.EffectID || completed {
			if effect.Status != workflow.EffectCompleted || effect.ReceiptID != ea1EntryReceiptID(request) {
				t.Fatal("exact original receipt absent")
			}
		} else if effect.Status != workflow.EffectPending || effect.ReceiptID != "" {
			t.Fatal("inflight callback fabricated receipt")
		}
	}
	if last.NodeID != "target" || last.Kind != "external.target" || string(last.Input) != "{\"source\":\"target\"}" {
		t.Fatal("target source changed")
	}
	if last.Attempt == 2 {
		prior := requests[len(requests)-2]
		if prior.Attempt != 1 || prior.OwnerNodeID != prior.NodeID || last.OwnerNodeID != last.EffectID+"_attempt_2" {
			t.Fatal("retry owner identity changed")
		}
		prior.Attempt = 2
		prior.OwnerNodeID = last.EffectID + "_attempt_2"
		if !reflect.DeepEqual(prior, last) {
			t.Fatal("retry changed original request")
		}
	} else if last.Attempt != 1 {
		t.Fatal("unexpected attempt")
	}
	if view.Budget.Effects != count {
		t.Fatal("duplicate effect reservation")
	}
	if completed {
		if view.Budget.CostUnitsUsed != int64(count) || view.Budget.CostUnitsReserved != 0 {
			t.Fatal("budget not settled once")
		}
	} else if view.Budget.CostUnitsUsed != used || view.Budget.CostUnitsReserved != 1 {
		t.Fatal("original budget reservation lost")
	}
	if !child {
		if len(view.Waits) != 1 || view.Waits[0].Status != workflow.WaitResolved || string(view.Waits[0].Response) != "{\"approved\":true}" {
			t.Fatal("resolved Wait source lost")
		}
	}
}

type ea1EntryCompetitionEffects struct {
	mu           sync.Mutex
	captured     []workflow.EffectRequest
	entered      chan struct{}
	release      chan struct{}
	block, retry bool
}

func (e *ea1EntryCompetitionEffects) requests() []workflow.EffectRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]workflow.EffectRequest(nil), e.captured...)
}
func (e *ea1EntryCompetitionEffects) Execute(_ context.Context, r workflow.EffectRequest) (workflow.EffectResult, error) {
	r.Input = append(json.RawMessage(nil), r.Input...)
	e.mu.Lock()
	e.captured = append(e.captured, r)
	e.mu.Unlock()
	if r.Kind == "external.target" && r.Attempt == 1 {
		if e.block {
			e.entered <- struct{}{}
			<-e.release
		}
		if e.retry {
			return workflow.EffectResult{Disposition: workflow.DispositionFailed, PhysicalNoEffect: true, ErrorCode: "safe-retry", ErrorDetail: "confirmed no dispatch"}, nil
		}
	}
	return workflow.EffectResult{Disposition: workflow.DispositionCompleted, ReceiptID: ea1EntryReceiptID(r), Output: append(json.RawMessage(nil), r.Input...), CostUnits: r.MaxCostUnits}, nil
}

func ea1EntryCompetitionWaitOrigin(t *testing.T, current, original kernel.Snapshot) {
	t.Helper()
	before, beforeErr := workflow.ViewState(original)
	after, afterErr := workflow.ViewState(current)
	if beforeErr != nil || afterErr != nil || len(before.Waits) != 1 || len(after.Waits) != 1 ||
		before.Waits[0].Status != workflow.WaitPending || after.Waits[0].Status != workflow.WaitResolved ||
		before.Waits[0].ID != after.Waits[0].ID || string(after.Waits[0].Response) != "{\"approved\":true}" {
		t.Fatal("original Wait identity or response changed")
	}
}
