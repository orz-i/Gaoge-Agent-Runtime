package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Captures the original opaque CAS solely to test Store-side stale-vs-foreign
// classification after a real successful recovery, bypassing no authority.
type ea1OwnedRecoveryCapture struct {
	kernel.Store
	kernel.ExecutionStore
	kernel.ChildExecutionStore
	kernel.OwnedPhysicalExecutionStore
	mutation kernel.StoreMutation
	permit   kernel.OwnedPhysicalRecovery
}

func (s *ea1OwnedRecoveryCapture) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	s.mutation = m
	s.permit = p
	return s.OwnedPhysicalExecutionStore.ApplyOwnedPhysicalRecovery(ctx, g, rev, m, p)
}

// RunEA1OwnedCallbackReturn exercises the actual first-party opaque ports,
// never seeded SQL grants. This is a Store boundary test, not fabricated
// evidence that an arbitrary Host callback returned.
func RunEA1OwnedCallbackReturn(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store)) {
	t.Helper()
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown-host", true: "known-host"}[known], func(t *testing.T) {
			first, peer := factory(t)
			rt, e := kernel.New(kernel.Dependencies{Store: first})
			if e != nil {
				t.Fatal(e)
			}
			captured := &ea1OwnedRecoveryCapture{Store: peer, ExecutionStore: peer.(kernel.ExecutionStore), ChildExecutionStore: peer.(kernel.ChildExecutionStore), OwnedPhysicalExecutionStore: peer.(kernel.OwnedPhysicalExecutionStore)}
			observer, e := kernel.New(kernel.Dependencies{Store: captured})
			if e != nil {
				t.Fatal(e)
			}
			initial, e := rt.Create(t.Context(), kernel.CreateRequest{ID: "callback-root", Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "thread", ID: "original"}, RequestID: "original-request", Goal: "callback receipt", State: json.RawMessage(`{"original":true}`), ExecutionRequired: true})
			if e != nil {
				t.Fatal(e)
			}
			lease, e := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: initial.Run.ID, OwnerID: "original", LeaseDuration: time.Second})
			if e != nil {
				t.Fatal(e)
			}
			bound, e := rt.WithExecution(lease)
			if e != nil {
				t.Fatal(e)
			}
			admit := featurecontrol.Seal(initial.Run.ID, initial.Run.Revision, "agent_owned_physical_admission", lease.LeaseID, initial.State, initial.Checkpoint)
			g, e := bound.BeginOwnedPhysicalExecution(t.Context(), initial.Run.ID, initial.Run.Revision, admit, "start")
			if e != nil {
				t.Fatal(e)
			}
			// Wait on the authoritative Store clock, not a client-side TTL guess.
			checker := peer.(kernel.ExecutionStore)
			until := time.Now().Add(3 * time.Second)
			for {
				e = checker.CheckExecution(t.Context(), initial.Run.ID, &lease)
				if errors.Is(e, kernel.ErrExecutionConflict) {
					break
				}
				if e != nil || time.Now().After(until) {
					t.Fatalf("original lease did not expire: %v", e)
				}
				time.Sleep(5 * time.Millisecond)
			}
			outcome := kernel.OwnedPhysicalCallbackOutcome{HostCompleted: known, LeaseOwned: true}
			proof := featurecontrol.Seal(initial.Run.ID, initial.Run.Revision, "agent_owned_callback_return", g.ID, kernel.OwnedPhysicalCallbackReturnPayload(initial, outcome), nil)
			if e = rt.RecordOwnedPhysicalCallbackReturn(t.Context(), g, initial.Run.Revision, outcome, proof); e != nil {
				t.Fatal(e)
			}
			before, e := observer.LookupOwnedPhysicalCallbackReturn(t.Context(), g)
			if e != nil || before.GrantID != g.ID || before.ReturnRevision != initial.Run.Revision || before.HostCompleted != known || !before.LeaseOwned || len(before.ReturnSourceHash) != 64 {
				t.Fatalf("persisted completion: %+v %v", before, e)
			}
			if e = observer.RecordOwnedPhysicalCallbackReturn(t.Context(), g, initial.Run.Revision, outcome, proof); e != nil {
				t.Fatalf("same completion retry: %v", e)
			}
			changed := outcome
			changed.HostCompleted = !known
			forged := featurecontrol.Seal(initial.Run.ID, initial.Run.Revision, "agent_owned_callback_return", g.ID, kernel.OwnedPhysicalCallbackReturnPayload(initial, changed), nil)
			if e = observer.RecordOwnedPhysicalCallbackReturn(t.Context(), g, initial.Run.Revision, changed, forged); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("completion changed: %v", e)
			}
			actual, e := observer.Load(t.Context(), initial.Run.ID)
			if e != nil || !reflect.DeepEqual(actual, initial) {
				t.Fatalf("completion changed Run projection: before=%#v after=%#v err=%v", initial, actual, e)
			}
			m := kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"original":true,"receipt":"verified"}`)}
			source := featurecontrol.Seal(initial.Run.ID, initial.Run.Revision, "agent_owned_receipt_recovery_source", g.ID, initial.State, initial.Checkpoint)
			target := featurecontrol.Seal(initial.Run.ID, initial.Run.Revision, "agent_owned_receipt_recovery_target", g.ID, m.State, m.Checkpoint)
			repaired, e := observer.ApplyOwnedPhysicalRecovery(t.Context(), g, initial.Run.Revision, m, source, target)
			if !known {
				if !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("unknown Host authorized receipt CAS: %v", e)
				}
				mark := featurecontrol.Seal(initial.Run.ID, initial.Run.Revision, "agent_owned_physical_recovered", g.ID, initial.State, initial.Checkpoint)
				if e = observer.MarkOwnedPhysicalRecovered(t.Context(), g, mark); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("unknown Host authorized Mark: %v", e)
				}
				// Purpose isolation: only verified model evidence may be persisted under
				// a recorded but unknown Host callback. This Store test seals synthetic
				// state; Agent tests own the exact provider/Invocation receipt validator.
				evidenceSource := featurecontrol.Seal(initial.Run.ID, initial.Run.Revision, "agent_owned_model_receipt_evidence_source", g.ID, initial.State, initial.Checkpoint)
				evidenceTarget := featurecontrol.Seal(initial.Run.ID, initial.Run.Revision, "agent_owned_model_receipt_evidence_target", g.ID, m.State, m.Checkpoint)
				forbidden := m
				forbidden.Events = []kernel.EventDraft{{Type: "forged.wakeup", Wakeup: true}}
				if _, e = observer.ApplyOwnedModelReceiptEvidence(t.Context(), g, initial.Run.Revision, forbidden, evidenceSource, evidenceTarget); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("evidence purpose emitted wakeup: %v", e)
				}
				evidence, e := observer.ApplyOwnedModelReceiptEvidence(t.Context(), g, initial.Run.Revision, m, evidenceSource, evidenceTarget)
				if e != nil || evidence.EventHead != initial.EventHead || evidence.Run.Revision != initial.Run.Revision+1 {
					t.Fatalf("evidence-only CAS: %+v %v", evidence, e)
				}
				persisted, e := observer.LookupOwnedPhysicalCallbackReturn(t.Context(), g)
				if e != nil || persisted != before {
					t.Fatal("evidence CAS replaced callback outcome")
				}
				mark = featurecontrol.Seal(evidence.Run.ID, evidence.Run.Revision, "agent_owned_physical_recovered", g.ID, evidence.State, evidence.Checkpoint)
				if e = observer.MarkOwnedPhysicalRecovered(t.Context(), g, mark); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("known model evidence retired unknown Host: %v", e)
				}
				if _, e = observer.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: initial.Run.ID, OwnerID: "forbidden", LeaseDuration: time.Second}); !errors.Is(e, kernel.ErrChildExecutionBusy) {
					t.Fatalf("evidence enabled send: %v", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			// The exact original authority losing only its revision is a normal
			// CAS conflict; a changed grant is never eligible for that retry class.
			if _, e = captured.OwnedPhysicalExecutionStore.ApplyOwnedPhysicalRecovery(t.Context(), g, initial.Run.Revision, captured.mutation, captured.permit); !errors.Is(e, kernel.ErrConflict) {
				t.Fatalf("pure stale receipt CAS: %v", e)
			}
			foreign := g
			foreign.ExecutionEpoch++
			if _, e = captured.OwnedPhysicalExecutionStore.ApplyOwnedPhysicalRecovery(t.Context(), foreign, initial.Run.Revision, captured.mutation, captured.permit); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("foreign receipt CAS became retryable: %v", e)
			}
			unchanged, e := observer.Load(t.Context(), g.ChildRunID)
			if e != nil || !reflect.DeepEqual(unchanged, repaired) {
				t.Fatal("rejected CAS changed snapshot")
			}
			// A crash/fault between the receipt CAS and Mark leaves the original
			// completion intact; a new Runtime must seal the CURRENT receipt state.
			stale := featurecontrol.Seal(initial.Run.ID, initial.Run.Revision, "agent_owned_physical_recovered", g.ID, initial.State, initial.Checkpoint)
			if e = observer.MarkOwnedPhysicalRecovered(t.Context(), g, stale); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("stale Mark accepted: %v", e)
			}
			completion, e := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), g)
			if e != nil || completion != before {
				t.Fatal("receipt CAS rewrote callback completion")
			}
			recovered := featurecontrol.Seal(repaired.Run.ID, repaired.Run.Revision, "agent_owned_physical_recovered", g.ID, repaired.State, repaired.Checkpoint)
			if e = rt.MarkOwnedPhysicalRecovered(t.Context(), g, recovered); e != nil {
				t.Fatal(e)
			}
			if e = observer.MarkOwnedPhysicalRecovered(t.Context(), g, recovered); e != nil {
				t.Fatalf("Mark lost ACK retry: %v", e)
			}
			if e = rt.FinishChildExecution(t.Context(), g, recovered); e != nil {
				t.Fatal(e)
			}
			if _, e = observer.LookupOwnedPhysicalCallbackReturn(t.Context(), g); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatalf("retired completion exists: %v", e)
			}
			if e = observer.RecordOwnedPhysicalCallbackReturn(context.WithoutCancel(t.Context()), g, initial.Run.Revision, outcome, proof); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("Finish allowed reconstruction: %v", e)
			}
			final, loadErr := observer.Load(t.Context(), initial.Run.ID)
			if loadErr != nil || !reflect.DeepEqual(final, repaired) {
				t.Fatal("rejected reconstruction mutated Run")
			}
			if _, e = observer.LookupChildExecution(t.Context(), initial.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatalf("reconstruction restored grant: %v", e)
			}
			if _, e = observer.RenewExecution(t.Context(), kernel.ExecutionRenewRequest{Lease: lease, LeaseDuration: time.Second}); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("reconstruction revived lease: %v", e)
			}

		})
	}
}
