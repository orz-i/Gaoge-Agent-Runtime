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

// RunEA1CompensationBarriers covers ONLY WR-C/WC-C P1/P5. The P5 retry
// carries an explicit no-physical-effect result, never an ambiguous retry.
func RunEA1CompensationBarriers(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store)) {
	for _, child := range []bool{false, true} {
		for _, phase := range []string{"P1", "P5", "P5-retry"} {
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
				// Only the non-retry P5 child case exercises an actually due ancestor.
				// Both Runtime and Store use real clocks. StartRequest has no self deadline.
				var deadline *time.Time
				if child && phase == "P5" {
					due := time.Now().UTC().Add(2 * time.Second)
					deadline = &due
				}
				observer, err := kernel.New(kernel.Dependencies{Store: peer})
				if err != nil {
					t.Fatal(err)
				}
				actor := kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}
				thread := kernel.ThreadRef{Kind: "conversation", ID: "original"}
				parentID := ""
				if child {
					p, e := rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: workflow.RunKind, Actor: actor, Thread: thread, Goal: "original ancestor", State: json.RawMessage("{}"), DeadlineAt: deadline})
					if e != nil {
						t.Fatal(e)
					}
					parentID = p.Run.ID
				}
				def, err := workflow.CompileDefinition(workflow.DefinitionDraft{ID: "compensation-barriers", Revision: 1, Name: "original Cancel", Limits: workflow.Limits{MaxActivationsPerSegment: 8}, Policy: workflow.DefinitionPolicy{MaxCostUnits: 2, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectWrite}, Nodes: []workflow.Node{
					{ID: "reserve", Type: workflow.NodeCompensation, Compensation: &workflow.CompensationNode{
						Do:   workflow.EffectCall{Class: workflow.EffectClassGeneric, Kind: "resource.reserve", Input: workflow.ValueSource{Kind: workflow.ValueLiteral, Value: json.RawMessage(`{"original":true}`)}, MaxCostUnits: 1},
						Undo: workflow.EffectCall{Class: workflow.EffectClassGeneric, Kind: "resource.release", Input: workflow.ValueSource{Kind: workflow.ValueNodeOutput, NodeID: "reserve"}, MaxCostUnits: 1, Retry: workflow.RetryPolicy{MaxAttempts: 2, RetryableErrorCodes: []string{"safe-retry"}}}}},
					{ID: "wait", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "approval", Payload: json.RawMessage("{}")}},
					{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage("{}")}},
				}})
				if err != nil {
					t.Fatal(err)
				}
				entered := make(chan struct{}, 1)
				release := make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				effects := &ea1CompBarrierEffects{entered: entered, release: release, block: phase != "P1", retry: phase == "P5-retry"}
				setup, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: effects})
				if err != nil {
					t.Fatal(err)
				}
				original, err := setup.StartRun(t.Context(), workflow.StartRequest{ID: "original", RequestID: "original-source", ParentRunID: parentID, Actor: actor, Thread: thread, Goal: "reserve then cancel", Definition: def, Input: json.RawMessage("{}")})
				if err != nil || original.Run.Status != kernel.RunStatusWaitingInput || len(effects.requests()) != 1 {
					t.Fatalf("setup: %v", err)
				}
				owned := backing
				if phase == "P1" {
					owned = &ea1WaitAdmissionBarrier{Store: backing, ChildExecutionStore: backing.(kernel.ChildExecutionStore), entered: entered, release: release}
				}
				ownerRT, err := kernel.New(kernel.Dependencies{Store: owned})
				if err != nil {
					t.Fatal(err)
				}
				owner, err := workflow.NewRunner(workflow.Dependencies{Runtime: ownerRT, Effects: effects})
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					_, e := owner.Cancel(context.Background(), original.Run.ID, original.Run.Revision, "original cancellation")
					done <- e
				}()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("actual Cancel barrier not reached")
				}
				during, err := observer.Load(t.Context(), original.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				grant, err := observer.LookupChildExecution(t.Context(), original.Run.ID)
				if err != nil || grant.ExpectedRevision != original.Run.Revision {
					t.Fatalf("original admission: %+v %v", grant, err)
				}
				requests := effects.requests()
				if phase == "P1" {
					if len(requests) != 1 || !reflect.DeepEqual(during, original) {
						t.Fatal("P1 dispatched or mutated before admission returned")
					}
				} else {
					assertEA1CompBarrierBinding(t, during, def, requests, 1, false)
				}
				competing, err := workflow.NewRunner(workflow.Dependencies{Runtime: observer, Effects: effects})
				if err != nil {
					t.Fatal(err)
				}
				if _, e := competing.Cancel(t.Context(), during.Run.ID, during.Run.Revision, "competing compensation"); !errors.Is(e, kernel.ErrChildExecutionBusy) {
					t.Fatalf("competing Cancel: %v", e)
				}
				resumeRejection := kernel.ErrChildExecutionBusy
				if phase == "P1" {
					resumeRejection = workflow.ErrWaitPending
				}
				if _, e := competing.Resume(t.Context(), during.Run.ID, during.Run.Revision); !errors.Is(e, resumeRejection) {
					t.Fatalf("competing Resume: %v", e)
				}
				if phase == "P1" {
					if _, e := competing.ResolveWait(t.Context(), during.Run.ID, during.Run.Revision, json.RawMessage(`{"approved":true}`)); !errors.Is(e, kernel.ErrChildExecutionBusy) {
						t.Fatalf("competing actual Wait approval: %v", e)
					}
					ea1CompBarrierUnchanged(t, observer, during)
				}
				if e := competing.ReconcileChildPhysicalGrant(t.Context(), during.Run.ID); !errors.Is(e, kernel.ErrChildExecutionBusy) {
					t.Fatalf("premature reconcile: %v", e)
				}
				if e := peer.(kernel.ChildExecutionStore).FinishChildExecution(t.Context(), grant); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("premature Finish: %v", e)
				}
				ids := []string{during.Run.ID}
				if child {
					ids = append(ids, parentID)
				}
				for _, id := range ids {
					before, e := observer.Load(t.Context(), id)
					if e != nil {
						t.Fatal(e)
					}
					for _, status := range []kernel.RunStatus{kernel.RunStatusRunning, kernel.RunStatusCancelled} {
						_, e = observer.Apply(t.Context(), id, before.Run.Revision, kernel.Mutation{Status: status, State: before.State})
						if !errors.Is(e, kernel.ErrChildExecutionBusy) {
							t.Fatalf("self/ancestor Apply %s: %v", status, e)
						}
						ea1CompBarrierUnchanged(t, observer, before)
					}
					_, e = observer.Cancel(t.Context(), id, before.Run.Revision, "competing terminal")
					if !errors.Is(e, kernel.ErrChildExecutionBusy) {
						t.Fatalf("self/ancestor Cancel: %v", e)
					}
					ea1CompBarrierUnchanged(t, observer, before)
					if id == parentID && deadline != nil {
						// No simulated eligibility: wait until Store-side wall time is due too.
						if remaining := time.Until(*deadline); remaining > 0 {
							time.Sleep(remaining + 20*time.Millisecond)
						}
						if time.Now().Before(*deadline) {
							t.Fatal("deadline fixture not actually due")
						}
						_, applied, e := observer.Expire(t.Context(), id, before.Run.Revision)
						// A bounded tolerance accommodates a slightly lagging database clock.
						// ErrDeadline itself is never accepted as physical-fence evidence.
						tolerance := time.Now().Add(time.Second)
						for !applied && errors.Is(e, kernel.ErrDeadline) && time.Now().Before(tolerance) {
							ea1CompBarrierUnchanged(t, observer, before)
							time.Sleep(20 * time.Millisecond)
							_, applied, e = observer.Expire(t.Context(), id, before.Run.Revision)
						}
						if applied || !errors.Is(e, kernel.ErrChildExecutionBusy) {
							t.Fatalf("due ancestor deadline: %t %v", applied, e)
						}
						ea1CompBarrierUnchanged(t, observer, before)
					}
				}
				ea1CompBarrierUnchanged(t, observer, during)
				same, e := observer.LookupChildExecution(t.Context(), during.Run.ID)
				if e != nil || same != grant {
					t.Fatal("competition changed grant")
				}
				if len(effects.requests()) != len(requests) {
					t.Fatal("competition dispatched another callback")
				}
				unblock()
				select {
				case e = <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("Cancel did not return")
				}
				settled, loadErr := observer.Load(t.Context(), during.Run.ID)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				if phase == "P5-retry" {
					if !errors.Is(e, workflow.ErrEffectPending) || settled.Run.Status != kernel.RunStatusRunning {
						t.Fatalf("safe retry not scheduled: %v status=%s", e, settled.Run.Status)
					}
					if _, lookupErr := observer.LookupChildExecution(t.Context(), during.Run.ID); !errors.Is(lookupErr, kernel.ErrNotFound) {
						t.Fatalf("safe first attempt not retired: %v", lookupErr)
					}
					view, ve := workflow.ViewState(settled)
					if ve != nil || len(view.Effects) != 2 || view.Effects[1].Attempt != 2 || view.Budget.Effects != 2 || view.Budget.CostUnitsUsed != 1 || view.Budget.CostUnitsReserved != 1 {
						t.Fatal("retry lost original reservation/attempt")
					}
					// Retry uses its own non-expiring fixture, separate from deadline competition.
					settled, e = owner.Resume(t.Context(), settled.Run.ID, settled.Run.Revision)
					if e == nil || e.Error() != "workflow cancellation requested" || settled.Run.Status != kernel.RunStatusCancelled || settled.Run.ErrorCode != "workflow.cancelled" {
						t.Fatalf("legal retry must finish original cancellation without stranded grant: %v status=%s code=%s", e, settled.Run.Status, settled.Run.ErrorCode)
					}
				} else if e != nil {
					t.Fatal(e)
				}
				if settled.Run.Status != kernel.RunStatusCancelled {
					t.Fatalf("not cancelled: %s", settled.Run.Status)
				}
				attempt := 1
				if phase == "P5-retry" {
					attempt = 2
				}
				assertEA1CompBarrierBinding(t, settled, def, effects.requests(), attempt, true)
				if _, e = observer.LookupChildExecution(t.Context(), during.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("grant stranded: %v", e)
				}
				if _, e = competing.Cancel(t.Context(), settled.Run.ID, settled.Run.Revision, "duplicate"); !errors.Is(e, workflow.ErrWorkflowTerminal) {
					t.Fatalf("terminal Cancel: %v", e)
				}
				if _, e = competing.Resume(t.Context(), settled.Run.ID, settled.Run.Revision); !errors.Is(e, workflow.ErrWorkflowTerminal) {
					t.Fatalf("terminal Resume: %v", e)
				}
				ea1CompBarrierUnchanged(t, observer, settled)
				if len(effects.requests()) != attempt+1 {
					t.Fatal("unexpected physical replay")
				}
			})
		}
	}
}

func ea1CompBarrierUnchanged(t *testing.T, rt *kernel.Runtime, before kernel.Snapshot) {
	t.Helper()
	after, e := rt.Load(t.Context(), before.Run.ID)
	if e != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("rejected control mutated full snapshot")
	}
}
func assertEA1CompBarrierBinding(t *testing.T, s kernel.Snapshot, def workflow.Definition, requests []workflow.EffectRequest, attempt int, completed bool) {
	t.Helper()
	v, e := workflow.ViewState(s)
	if e != nil || len(v.Effects) != 2 || len(v.Compensations) != 1 || len(requests) != attempt+1 || v.Definition.ID != def.ID || v.Definition.Hash != def.Hash || v.Definition.Revision != def.Revision {
		t.Fatal("original compensation structure/source lost")
	}
	req := requests[len(requests)-1]
	eff := v.Effects[1]
	comp := v.Compensations[0]
	if req.RunID != s.Run.ID || req.Actor != s.Run.Actor || req.Thread != s.Run.Thread || req.DefinitionID != def.ID || req.DefinitionHash != def.Hash || req.Attempt != attempt || !req.Compensation || req.Kind != "resource.release" || req.NodeID != "reserve" || string(req.Input) != `{"original":true}` || eff.ID != req.EffectID || eff.Attempt != req.Attempt || eff.NodeID != req.NodeID || eff.Kind != req.Kind || eff.Class != req.Class || !eff.Compensation || !reflect.DeepEqual(eff.Input, req.Input) || comp.EffectID != eff.ID {
		t.Fatal("original undo full request/attempt binding lost")
	}
	if attempt == 2 {
		first := requests[1]
		if first.OwnerNodeID != first.NodeID || req.OwnerNodeID != req.EffectID+"_attempt_2" {
			t.Fatal("retry lost exact attempt-specific relation owner")
		}
		first.Attempt = 2
		first.OwnerNodeID = req.EffectID + "_attempt_2"
		if !reflect.DeepEqual(first, req) {
			t.Fatal("retry changed original request beyond attempt")
		}
	}
	if v.Budget.Effects != 2 {
		t.Fatal("duplicate effect reservation")
	}
	if completed {
		if eff.Status != workflow.EffectCompleted || eff.ReceiptID != ea1EntryReceiptID(req) || comp.Status != workflow.CompensationCompleted || comp.ReceiptID != eff.ReceiptID || v.Budget.CostUnitsUsed != 2 || v.Budget.CostUnitsReserved != 0 {
			t.Fatal("receipt/budget not settled exactly once")
		}
	} else if v.Budget.CostUnitsUsed != 1 || v.Budget.CostUnitsReserved != 1 {
		t.Fatal("inflight original budget not reserved once")
	}
}

type ea1CompBarrierEffects struct {
	mu           sync.Mutex
	captured     []workflow.EffectRequest
	entered      chan struct{}
	release      chan struct{}
	block, retry bool
}

func (e *ea1CompBarrierEffects) requests() []workflow.EffectRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]workflow.EffectRequest(nil), e.captured...)
}
func (e *ea1CompBarrierEffects) Execute(_ context.Context, r workflow.EffectRequest) (workflow.EffectResult, error) {
	r.Input = append(json.RawMessage(nil), r.Input...)
	e.mu.Lock()
	e.captured = append(e.captured, r)
	e.mu.Unlock()
	if r.Compensation && r.Attempt == 1 {
		if e.block {
			e.entered <- struct{}{}
			<-e.release
		}
		if e.retry {
			return workflow.EffectResult{Disposition: workflow.DispositionFailed, PhysicalNoEffect: true, ErrorCode: "safe-retry", ErrorDetail: "test-only confirmed no dispatch"}, nil
		}
	}
	return workflow.EffectResult{Disposition: workflow.DispositionCompleted, ReceiptID: ea1EntryReceiptID(r), Output: append(json.RawMessage(nil), r.Input...), CostUnits: r.MaxCostUnits}, nil
}
