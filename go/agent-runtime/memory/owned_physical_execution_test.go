package memory_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

type ownedPhysicalClock struct{ now time.Time }

func (c *ownedPhysicalClock) Now() time.Time { return c.now }
func ownedPhysicalFixture(t *testing.T) (*memory.Store, *kernel.Runtime, *kernel.Runtime, kernel.Snapshot, kernel.ExecutionLease, *ownedPhysicalClock) {
	t.Helper()
	clock := &ownedPhysicalClock{time.Now().UTC()}
	s := memory.NewStoreWithExecutionClock(clock)
	rt, e := kernel.New(kernel.Dependencies{Store: s, Clock: clock})
	if e != nil {
		t.Fatal(e)
	}
	v, e := rt.Create(t.Context(), kernel.CreateRequest{ID: "owned-agent", Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, RequestID: "original-source", Goal: "original task", State: json.RawMessage(`{"original":true}`), ExecutionRequired: true})
	if e != nil {
		t.Fatal(e)
	}
	l, e := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: v.Run.ID, OwnerID: "original", LeaseDuration: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	bound, e := rt.WithExecution(l)
	if e != nil {
		t.Fatal(e)
	}
	return s, rt, bound, v, l, clock
}
func ownedPhysicalAdmit(t *testing.T, bound *kernel.Runtime, v kernel.Snapshot, l kernel.ExecutionLease) (kernel.ChildExecutionGrant, *kernel.Runtime) {
	t.Helper()
	proof := featurecontrol.Seal(v.Run.ID, v.Run.Revision, "agent_owned_physical_admission", l.LeaseID, v.State, v.Checkpoint)
	g, e := bound.BeginOwnedPhysicalExecution(t.Context(), v.Run.ID, v.Run.Revision, proof, "start")
	if e != nil {
		t.Fatal(e)
	}
	scoped, e := bound.WithPhysicalGrant(t.Context(), g, featurecontrol.Seal(v.Run.ID, v.Run.Revision, "agent_physical_owner", g.ID, v.State, v.Checkpoint))
	if e != nil {
		t.Fatal(e)
	}
	return g, scoped
}
func TestOwnedPhysicalMemoryAdmissionBindingAndTTL(t *testing.T) {
	s, rt, bound, v, l, clock := ownedPhysicalFixture(t)
	if _, e := bound.BeginOwnedPhysicalExecution(t.Context(), v.Run.ID, v.Run.Revision, nil, "start"); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("nil proof: %v", e)
	}
	g, scoped := ownedPhysicalAdmit(t, bound, v, l)
	if !g.MatchesExecution(l) || g.AdmissionOperation != "start" || g.AdmissionSourceHash == "" {
		t.Fatal("missing immutable native binding")
	}
	if e := s.BeginChildExecution(t.Context(), g); !errors.Is(e, kernel.ErrInvalidInput) {
		t.Fatalf("owned passed old admission: %v", e)
	}
	before, e := rt.Load(t.Context(), v.Run.ID)
	if e != nil {
		t.Fatal(e)
	}
	mutation := kernel.Mutation{Status: kernel.RunStatusRunning, State: before.State}
	if _, e = rt.Apply(t.Context(), v.Run.ID, v.Run.Revision, mutation); !errors.Is(e, kernel.ErrExecutionRequired) {
		t.Fatalf("unbound Apply: %v", e)
	}
	if _, e = bound.Apply(t.Context(), v.Run.ID, v.Run.Revision, mutation); !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("native lease without physical permit: %v", e)
	}
	if _, e = rt.Cancel(t.Context(), v.Run.ID, v.Run.Revision, "foreign control"); !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("control: %v", e)
	}
	if e = rt.ReleaseExecution(t.Context(), l); !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("release: %v", e)
	}
	if e = s.QuarantineExecution(t.Context(), kernel.ExecutionQuarantine{RunID: v.Run.ID, Revision: v.Run.Revision, Generation: 1, Reason: "unknown"}); !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("quarantine revoked epoch: %v", e)
	}
	after, e := rt.Load(t.Context(), v.Run.ID)
	if e != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("rejected operation mutated snapshot")
	}
	if e = s.CheckExecution(t.Context(), v.Run.ID, &l); e != nil {
		t.Fatal("original lease revoked")
	}
	advanced, e := scoped.Apply(t.Context(), v.Run.ID, v.Run.Revision, mutation)
	if e != nil {
		t.Fatal(e)
	}
	clock.now = clock.now.Add(2 * time.Minute)
	if _, e = rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: v.Run.ID, OwnerID: "takeover", LeaseDuration: time.Minute}); !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("TTL retired scope: %v", e)
	}
	if _, e = rt.RenewExecution(t.Context(), kernel.ExecutionRenewRequest{Lease: l, LeaseDuration: time.Minute}); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("expired renewal: %v", e)
	}
	if _, e = scoped.Apply(t.Context(), v.Run.ID, advanced.Run.Revision, mutation); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("expired original wrote: %v", e)
	}
	proof := featurecontrol.Seal(v.Run.ID, advanced.Run.Revision, "agent_owned_physical_settled", g.ID, advanced.State, advanced.Checkpoint)
	if e = scoped.MarkChildExecutionSettled(t.Context(), g, proof); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("expired original minted completion: %v", e)
	}
}
func TestOwnedPhysicalMemoryReceiptOnlyRecovery(t *testing.T) {
	s, rt, bound, v, l, clock := ownedPhysicalFixture(t)
	g, _ := ownedPhysicalAdmit(t, bound, v, l)
	clock.now = clock.now.Add(2 * time.Minute)
	m := kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"original":true,"receipt":"confirmed"}`)}
	source := featurecontrol.Seal(v.Run.ID, v.Run.Revision, "agent_owned_receipt_recovery_source", g.ID, v.State, v.Checkpoint)
	target := featurecontrol.Seal(v.Run.ID, v.Run.Revision, "agent_owned_receipt_recovery_target", g.ID, m.State, m.Checkpoint)
	if _, e := rt.ApplyOwnedPhysicalRecovery(t.Context(), g, v.Run.Revision, m, nil, target); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("unbound source recovery: %v", e)
	}
	forged := g
	forged.ExecutionEpoch++
	if _, e := rt.ApplyOwnedPhysicalRecovery(t.Context(), forged, v.Run.Revision, m, source, target); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("foreign epoch recovery: %v", e)
	}
	if _, e := rt.ApplyOwnedPhysicalRecovery(t.Context(), g, v.Run.Revision, m, source, target); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("receipt recovery before callback return: %v", e)
	}
	outcome := kernel.OwnedPhysicalCallbackOutcome{HostCompleted: true, LeaseOwned: true}
	callbackProof := featurecontrol.Seal(v.Run.ID, v.Run.Revision, "agent_owned_callback_return", g.ID, kernel.OwnedPhysicalCallbackReturnPayload(v, outcome), nil)
	if e := rt.RecordOwnedPhysicalCallbackReturn(t.Context(), g, v.Run.Revision, outcome, callbackProof); e != nil {
		t.Fatal(e)
	}
	recovered, e := rt.ApplyOwnedPhysicalRecovery(t.Context(), g, v.Run.Revision, m, source, target)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CheckExecution(t.Context(), v.Run.ID, &l); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("receipt granted send authority: %v", e)
	}
	recoveredProof := featurecontrol.Seal(v.Run.ID, recovered.Run.Revision, "agent_owned_physical_recovered", g.ID, recovered.State, recovered.Checkpoint)
	if e = rt.MarkOwnedPhysicalRecovered(t.Context(), g, recoveredProof); e != nil {
		t.Fatal(e)
	}
	if e = rt.MarkOwnedPhysicalRecovered(t.Context(), g, recoveredProof); e != nil {
		t.Fatalf("repeat recovered Mark: %v", e)
	}
	if e = rt.FinishChildExecution(t.Context(), g, recoveredProof); e != nil {
		t.Fatal(e)
	}
	if _, e = rt.LookupChildExecution(t.Context(), v.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("recovered scope remains: %v", e)
	}
	next, e := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: v.Run.ID, OwnerID: "next", LeaseDuration: time.Minute})
	if e != nil || next.Epoch <= l.Epoch {
		t.Fatalf("new admission after retirement: %v", e)
	}
}
func TestOwnedPhysicalMemoryMarkedRecoveryAfterExpiry(t *testing.T) {
	_, rt, bound, v, l, clock := ownedPhysicalFixture(t)
	g, scoped := ownedPhysicalAdmit(t, bound, v, l)
	proof := featurecontrol.Seal(v.Run.ID, v.Run.Revision, "agent_owned_physical_settled", g.ID, v.State, v.Checkpoint)
	if e := scoped.MarkChildExecutionSettled(t.Context(), g, proof); e != nil {
		t.Fatal(e)
	}
	clock.now = clock.now.Add(2 * time.Minute)
	if e := scoped.MarkChildExecutionSettled(t.Context(), g, proof); e != nil {
		t.Fatalf("repeat original Mark after ACK loss: %v", e)
	}
	forged := g
	forged.AdmissionOperation = "other"
	if e := rt.FinishChildExecution(t.Context(), forged, proof); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("wrong operation released: %v", e)
	}
	if e := rt.FinishChildExecution(t.Context(), g, proof); e != nil {
		t.Fatal(e)
	}
}
func TestOwnedPhysicalMemoryApprovalPreservesLease(t *testing.T) {
	s, rt, bound, v, l, clock := ownedPhysicalFixture(t)
	cp := &kernel.Checkpoint{CreatedAt: clock.now, ID: "approval", Kind: "agent.tool_approval", Status: kernel.CheckpointPending, Payload: json.RawMessage(`{"call":"original"}`)}
	waiting, e := bound.Apply(t.Context(), v.Run.ID, v.Run.Revision, kernel.Mutation{Status: kernel.RunStatusWaitingInput, State: v.State, Checkpoint: cp})
	if e != nil {
		t.Fatal(e)
	}
	if e = rt.ReleaseExecution(t.Context(), l); e != nil {
		t.Fatal(e)
	}
	l, e = rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: v.Run.ID, OwnerID: "approval-owner", LeaseDuration: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	bound, e = rt.WithExecution(l)
	if e != nil {
		t.Fatal(e)
	}
	g, scoped := ownedPhysicalAdmit(t, bound, waiting, l)
	if e = s.CheckExecution(t.Context(), v.Run.ID, &l); !errors.Is(e, kernel.ErrExecutionRequired) {
		t.Fatalf("waiting authority admitted model: %v", e)
	}
	resolved := *cp
	resolved.Status = kernel.CheckpointResolved
	resolvedAt := clock.now
	resolved.ResolvedAt = &resolvedAt
	resolved.Response = json.RawMessage(`{"decision":"approve"}`)
	m := kernel.Mutation{Status: kernel.RunStatusRunning, State: v.State, Checkpoint: &resolved}
	proof := featurecontrol.Seal(v.Run.ID, waiting.Run.Revision, string(kernel.ControlApproval), cp.ID, m.State, m.Checkpoint)
	next, revoked, e := scoped.ApplyControl(t.Context(), v.Run.ID, waiting.Run.Revision, kernel.ControlGuard{Kind: kernel.ControlApproval, CheckpointID: cp.ID}, m, proof)
	if e != nil || revoked || next.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("owned approval: revoked=%t err=%v", revoked, e)
	}
	if e = s.CheckExecution(t.Context(), v.Run.ID, &l); e != nil {
		t.Fatalf("approval revoked native owner: %v", e)
	}
	current, e := rt.LookupChildExecution(t.Context(), v.Run.ID)
	if e != nil || current != g {
		t.Fatal("approval replaced immutable scope")
	}
}

func TestOwnedPhysicalCallbackReturnMemory(t *testing.T) {
	for _, hostComplete := range []bool{false, true} {
		t.Run(map[bool]string{false: "host-unknown", true: "host-complete"}[hostComplete], func(t *testing.T) {
			_, rt, bound, v, l, clock := ownedPhysicalFixture(t)
			g, _ := ownedPhysicalAdmit(t, bound, v, l)
			clock.now = clock.now.Add(2 * time.Minute)
			outcome := kernel.OwnedPhysicalCallbackOutcome{HostCompleted: hostComplete, LeaseOwned: true}
			proof := featurecontrol.Seal(v.Run.ID, v.Run.Revision, "agent_owned_callback_return", g.ID, kernel.OwnedPhysicalCallbackReturnPayload(v, outcome), nil)
			if e := rt.RecordOwnedPhysicalCallbackReturn(t.Context(), g, v.Run.Revision, outcome, proof); e != nil {
				t.Fatal(e)
			}
			completion, e := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), g)
			if e != nil || completion.GrantID != g.ID || completion.ReturnRevision != v.Run.Revision || completion.HostCompleted != hostComplete || !completion.LeaseOwned || len(completion.ReturnSourceHash) != 64 {
				t.Fatalf("completion: %+v %v", completion, e)
			}
			if e = rt.RecordOwnedPhysicalCallbackReturn(t.Context(), g, v.Run.Revision, outcome, proof); e != nil {
				t.Fatal(e)
			}
			flipped := outcome
			flipped.HostCompleted = !hostComplete
			flippedProof := featurecontrol.Seal(v.Run.ID, v.Run.Revision, "agent_owned_callback_return", g.ID, kernel.OwnedPhysicalCallbackReturnPayload(v, flipped), nil)
			if e = rt.RecordOwnedPhysicalCallbackReturn(t.Context(), g, v.Run.Revision, flipped, flippedProof); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("contradictory completion: %v", e)
			}
			flipped = outcome
			flipped.LeaseOwned = false
			if e = rt.RecordOwnedPhysicalCallbackReturn(t.Context(), g, v.Run.Revision, flipped, proof); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("unsealed ownership: %v", e)
			}
			current, e := rt.Load(t.Context(), v.Run.ID)
			if e != nil || !reflect.DeepEqual(current, v) {
				t.Fatal("callback fact changed Run")
			}
			recoveredProof := featurecontrol.Seal(v.Run.ID, v.Run.Revision, "agent_owned_physical_recovered", g.ID, v.State, v.Checkpoint)
			e = rt.MarkOwnedPhysicalRecovered(t.Context(), g, recoveredProof)
			if !hostComplete {
				if !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("unknown Host released: %v", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			// Record and Finish serialize on the same authority. Either order must
			// leave no resurrected scope, even when the original ACK was lost.
			results := make(chan error, 2)
			go func() {
				results <- rt.RecordOwnedPhysicalCallbackReturn(t.Context(), g, v.Run.Revision, outcome, proof)
			}()
			go func() { results <- rt.FinishChildExecution(t.Context(), g, recoveredProof) }()
			for range 2 {
				if e = <-results; e != nil && !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatal(e)
				}
			}
			if _, e = rt.LookupOwnedPhysicalCallbackReturn(t.Context(), g); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatalf("retired completion remains: %v", e)
			}
			if e = rt.RecordOwnedPhysicalCallbackReturn(t.Context(), g, v.Run.Revision, outcome, proof); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("retired scope rebuilt: %v", e)
			}
		})
	}
}
