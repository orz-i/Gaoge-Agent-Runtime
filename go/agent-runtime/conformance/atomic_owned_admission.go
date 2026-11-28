package conformance

import (
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Verify the actual native transaction, not merely the final business Run.
// Every rejected attempt must leave the exact next epoch and grant identity
// available, and a genuine competing winner must have both facts together.
func runAtomicOwnedAdmission(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	for _, mode := range []string{"stale-revision", "foreign-event-head", "foreign-proof", "live-owner", "descendant-fence", "two-fresh-contenders", "physical-child"} {
		t.Run(mode, func(t *testing.T) {
			b := factory(t)
			ctx := t.Context()
			rt := retirementRuntime(t, b.Store)
			peer := retirementRuntime(t, b.Peer)
			request := kernel.CreateRequest{ID: "atomic-root", Kind: "agent", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "single native admission", State: []byte(`{}`), ExecutionRequired: true}
			var ancestors []kernel.Snapshot
			if mode == "physical-child" {
				for _, id := range []string{"grandparent", "parent"} {
					parent := request
					parent.ID = id
					s, e := rt.Create(ctx, parent)
					if e != nil {
						t.Fatal(e)
					}
					ancestors = append(ancestors, s)
					request.ParentRunID = id
				}
			}
			source, e := rt.Create(ctx, request)
			if e != nil {
				t.Fatal(e)
			}
			// Start from a known observed epoch, with no physical admission.
			previous, e := rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: source.Run.ID, OwnerID: "previous", LeaseDuration: time.Minute})
			if e != nil {
				t.Fatal(e)
			}
			if e = rt.ReleaseExecution(ctx, previous); e != nil {
				t.Fatal(e)
			}
			q := kernel.ExecutionAcquireRequest{RunID: source.Run.ID, OwnerID: "candidate", LeaseDuration: time.Minute}
			// Deterministic Host dispatch linkage also checks that a refused
			// transaction did not burn an otherwise unused Grant ID.
			dispatchID := "atomic-original"
			if mode == "physical-child" {
				dispatchID = ""
				foreign, e := peer.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: ancestors[0].Run.ID, OwnerID: "foreign-parent", LeaseDuration: time.Minute})
				if e != nil {
					t.Fatal(e)
				}
				bound, e := rt.WithExecution(foreign)
				if e != nil {
					t.Fatal(e)
				}
				proof := featurecontrol.Seal(source.Run.ID, source.Run.Revision, "agent_owned_physical_admission", kernel.PhysicalAdmissionSubject(kernel.ExecutionAcquireRequest{}, foreign, "start", ""), source.State, source)
				if _, v, e := bound.AdmitPhysicalExecution(ctx, source, kernel.ExecutionAcquireRequest{}, proof, "start", ""); !errors.Is(e, kernel.ErrExecutionConflict) || v != (kernel.PhysicalAdmissionResult{}) {
					t.Fatalf("child inherited a foreign/root lease: %v", e)
				}
				if e := peer.ReleaseExecution(ctx, foreign); e != nil {
					t.Fatal(e)
				}
			}
			admit := func(r *kernel.Runtime, v kernel.Snapshot, request kernel.ExecutionAcquireRequest, wrongProof bool) (*kernel.Runtime, kernel.PhysicalAdmissionResult, error) {
				subject := kernel.PhysicalAdmissionSubject(request, kernel.ExecutionLease{}, "start", dispatchID)
				if wrongProof {
					subject = "foreign-proof"
				}
				proof := featurecontrol.Seal(v.Run.ID, v.Run.Revision, "agent_owned_physical_admission", subject, v.State, v)
				return r.AdmitPhysicalExecution(ctx, v, request, proof, "start", dispatchID)
			}
			assertAdmission := func(r *kernel.Runtime, v kernel.PhysicalAdmissionResult, wantEpoch uint64) {
				t.Helper()
				if v.Lease.Epoch != wantEpoch || !v.Grant.MatchesExecution(v.Lease) {
					t.Fatalf("split admission or wrong epoch: %+v", v)
				}
				g, e := peer.LookupChildExecution(ctx, source.Run.ID)
				if e != nil || g != v.Grant {
					t.Fatalf("committed lease lacked exact grant: %v %+v", e, g)
				}
				if e = r.RequireExecution(ctx, source.Run.ID); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("admission granted execution before entry: %v", e)
				}
				outcome := kernel.PhysicalCallbackOutcome{HostCompleted: true, LeaseOwned: true}
				proof := featurecontrol.Seal(source.Run.ID, source.Run.Revision, "agent_physical_callback_return", g.ID, kernel.PhysicalCallbackReturnPayload(source, outcome), nil)
				if e = peer.RecordPhysicalCallbackReturn(ctx, g, source.Run.Revision, outcome, proof); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("admission fabricated an actual callback return: %v", e)
				}
				if _, e = r.CheckOwnedPhysicalEntry(ctx, source); e != nil {
					t.Fatalf("committed pair not executable: %v", e)
				}
				if e = r.RequireExecution(ctx, source.Run.ID); e != nil {
					t.Fatalf("entered runtime not native owner: %v", e)
				}
				if _, e = b.Peer.(kernel.PhysicalExecutionStore).CheckOwnedPhysicalEntry(ctx, g, v.Lease, source); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("repeated entry admitted: %v", e)
				}
				if e = peer.ReleaseExecution(ctx, v.Lease); !errors.Is(e, kernel.ErrChildExecutionBusy) {
					t.Fatalf("independent Release escaped Grant: %v", e)
				}
				if current, e := peer.Load(ctx, source.Run.ID); e != nil || kernel.ManagedDigest(current) != kernel.ManagedDigest(source) {
					t.Fatalf("admission changed business source: %v", e)
				}
				if mode == "physical-child" {
					for _, parent := range ancestors {
						if _, e := peer.Cancel(ctx, parent.Run.ID, parent.Run.Revision, "child active"); !errors.Is(e, kernel.ErrChildExecutionBusy) {
							t.Fatalf("leased child lost ancestor fence: %v", e)
						}
					}
					if e := peer.RecordPhysicalCallbackReturn(ctx, g, source.Run.Revision, outcome, proof); e != nil {
						t.Fatal(e)
					}
					b.AdvanceTo(v.Lease.ExpiresAt.Add(time.Millisecond))
					if e := FinishOwnedFixture(ctx, peer, g, source); e != nil {
						t.Fatal(e)
					}
					retired, e := peer.LoadOwnedRetirement(ctx, g.ID)
					if e != nil || !retired.Valid() || retired.Evidence.Grant != g || retired.Evidence.Completion.LeaseOwned != true || !retired.Evidence.Completion.MatchesReturnSnapshot(source) {
						t.Fatalf("child retirement lost exact original return: %v", e)
					}
					for _, custody := range []kernel.ExecutionCustody{kernel.CustodyRetired, kernel.CustodyPrepared, kernel.CustodyReturned} {
						if e := peer.VerifyExecutionCustody(ctx, source, custody); !errors.Is(e, kernel.ErrExecutionConflict) {
							t.Fatalf("physical child inherited root custody: %v", e)
						}
					}
					next, e := peer.AcquireExecution(ctx, q)
					if e != nil || next.Epoch != v.Lease.Epoch+1 {
						t.Fatalf("child retirement did not atomically release original lease: %v", e)
					}
				}
			}
			if mode == "two-fresh-contenders" {
				type result struct {
					r *kernel.Runtime
					v kernel.PhysicalAdmissionResult
					e error
				}
				start := make(chan struct{})
				results := make(chan result, 2)
				for i, r := range []*kernel.Runtime{rt, peer} {
					request := q
					if i == 1 {
						request.OwnerID = "other"
					}
					go func(r *kernel.Runtime, request kernel.ExecutionAcquireRequest) {
						<-start
						scoped, v, e := admit(r, source, request, false)
						results <- result{scoped, v, e}
					}(r, request)
				}
				close(start)
				wins := 0
				for range 2 {
					got := <-results
					if got.e == nil {
						wins++
						assertAdmission(got.r, got.v, previous.Epoch+1)
					} else if !errors.Is(got.e, kernel.ErrExecutionConflict) && !errors.Is(got.e, kernel.ErrChildExecutionBusy) && !errors.Is(got.e, kernel.ErrExecutionBusy) && !errors.Is(got.e, kernel.ErrConflict) {
						t.Fatalf("unexpected competing result: %v", got.e)
					}
				}
				if wins != 1 {
					t.Fatalf("atomic admission winners: %d", wins)
				}
				return
			}
			candidate := source
			wrongProof := false
			want := kernel.ErrExecutionConflict
			var competing kernel.ExecutionLease
			var childGrant kernel.ChildExecutionGrant
			var childProof *featurecontrol.Proof
			var childSource kernel.Snapshot
			switch mode {
			case "stale-revision":
				candidate.Run.Revision++
				want = kernel.ErrConflict
			case "foreign-event-head":
				candidate.EventHead++
			case "foreign-proof":
				wrongProof = true
			case "live-owner", "physical-child":
				competing, e = peer.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: source.Run.ID, OwnerID: "incumbent", LeaseDuration: time.Minute})
				if e != nil {
					t.Fatal(e)
				}
				previous = competing
			case "descendant-fence":
				child, e := peer.Create(ctx, kernel.CreateRequest{ID: "atomic-child", Kind: "agent", ParentRunID: source.Run.ID, Actor: source.Run.Actor, Thread: source.Run.Thread, Goal: "descendant fence", State: []byte(`{}`)})
				if e != nil {
					t.Fatal(e)
				}
				childGrant, e = AdmitChildFixture(ctx, peer, child.Run.ID, child.Run.Revision)
				if e != nil {
					t.Fatal(e)
				}
				childSource = child
				childProof = featurecontrol.Seal(child.Run.ID, child.Run.Revision, "agent_physical_settled", childGrant.ID, child.State, child)
				want = kernel.ErrChildExecutionBusy
			}
			scoped, v, e := admit(rt, candidate, q, wrongProof)
			if !errors.Is(e, want) || scoped != nil || v != (kernel.PhysicalAdmissionResult{}) {
				t.Fatalf("rejected admission: %v %+v", e, v)
			}
			if g, e := peer.LookupChildExecution(ctx, source.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatalf("refusal left root Grant: %+v %v", g, e)
			}
			if competing.LeaseID != "" {
				bound, e := peer.WithExecution(competing)
				if e != nil {
					t.Fatal(e)
				}
				if e = bound.RequireExecution(ctx, source.Run.ID); e != nil {
					t.Fatalf("refusal revoked incumbent: %v", e)
				}
				if e = peer.ReleaseExecution(ctx, competing); e != nil {
					t.Fatal(e)
				}
			}
			if childGrant.ID != "" {
				if e = peer.RecordPhysicalCallbackReturn(ctx, childGrant, childGrant.ExpectedRevision, kernel.PhysicalCallbackOutcome{HostCompleted: true}, featurecontrol.Seal(childSource.Run.ID, childSource.Run.Revision, "agent_physical_callback_return", childGrant.ID, kernel.PhysicalCallbackReturnPayload(childSource, kernel.PhysicalCallbackOutcome{HostCompleted: true}), nil)); e != nil {
					t.Fatal(e)
				}
				if e = peer.FinishChildExecution(ctx, childGrant, childProof); e != nil {
					t.Fatal(e)
				}
			}
			scoped, v, e = admit(rt, source, q, false)
			if e != nil {
				t.Fatalf("refusal burned lease/Grant identity: %v", e)
			}
			assertAdmission(scoped, v, previous.Epoch+1)
		})
	}
}
