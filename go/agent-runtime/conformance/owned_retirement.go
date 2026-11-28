package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// RunOwnedRetirementStoreSuite uses actual Store transactions and internal
// Feature proofs to certify the Store boundary, not arbitrary callback truth.
// Actual Agent callback lifetime/racing Reconcile is covered separately.
func RunOwnedRetirementStoreSuite(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	t.Run("atomic-fresh-admission", func(t *testing.T) { runAtomicOwnedAdmission(t, factory) })
	t.Run("identity-retired-ID-reuse", func(t *testing.T) { runRetiredGrantIDReuse(t, factory) })
	t.Run("identity-owned-borrowed-race", func(t *testing.T) { runGrantIdentityRace(t, factory) })
	t.Run("identity-rejected-admission-rolls-back", func(t *testing.T) { runGrantIdentityRollback(t, factory) })
	for _, owned := range []bool{true, false} {
		t.Run(map[bool]string{true: "owned", false: "borrowed"}[owned], func(t *testing.T) { runRetirementACKWindows(t, factory, owned) })
	}
	for _, variant := range []string{"foreign-grant", "foreign-owner", "foreign-lease", "foreign-epoch", "wrong-revision", "descendant"} {
		t.Run("finish-refused/"+variant, func(t *testing.T) { runRetirementRejectedFinish(t, factory, variant) })
	}
	for _, successor := range []string{"acquire", "release", "leased-apply", "cancel", "descendant-grant", "quarantine"} {
		t.Run("successor-closure/"+successor, func(t *testing.T) { runRetirementSuccessorClosure(t, factory, successor) })
	}
	for _, kind := range []string{"active", "unknown-callback", "legacy-mark"} {
		t.Run("no-release/"+kind, func(t *testing.T) { runRetirementNoRelease(t, factory, kind) })
	}
	t.Run("expired-return", func(t *testing.T) {
		f := retirementSetup(t, factory)
		retirementCallback(t, f, true, true)
		f.backend.AdvanceTo(f.lease.ExpiresAt.Add(time.Millisecond))
		retirementFinish(t, f)
	})
	t.Run("competing-finish", func(t *testing.T) { runRetirementCompetition(t, factory) })
	t.Run("actual-Agent-two-retired-generations", func(t *testing.T) { runRetirementTwoGenerations(t, factory) })
	t.Run("actual-Agent-finish-death-public-runID-recovery", func(t *testing.T) { runRetirementActualAgentRecovery(t, factory) })
	t.Run("finish-lost-ACK-runID-reopen", func(t *testing.T) { runRetirementRunIDRecovery(t, factory) })
	t.Run("managed-closure-with-retirement", func(t *testing.T) { runRetirementManaged(t, factory) })
}

type retirementFixture struct {
	backend   ManagedRecoveryStoreFixture
	rt, bound *kernel.Runtime
	store     managedStorePorts
	source    kernel.Snapshot
	lease     kernel.ExecutionLease
	grant     kernel.ChildExecutionGrant
}

func admitBorrowed(ctx context.Context, rt *kernel.Runtime, s kernel.Snapshot, operation, dispatchID string) (kernel.ChildExecutionGrant, error) {
	l, _ := rt.BoundExecutionLease()
	q := kernel.ExecutionAcquireRequest{}
	proof := featurecontrol.Seal(s.Run.ID, s.Run.Revision, "agent_owned_physical_admission", kernel.OwnedPhysicalAdmissionSubject(q, l, operation, dispatchID), s.State, s)
	scoped, v, e := rt.AdmitOwnedPhysicalExecution(ctx, s, q, proof, operation, dispatchID)
	if e == nil {
		_, e = scoped.CheckOwnedPhysicalEntry(ctx, s)
	}
	return v.Grant, e
}

func retirementSetup(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) retirementFixture {
	t.Helper()
	b := factory(t)
	s, ok := b.Store.(managedStorePorts)
	if !ok || b.Peer == nil || b.Reopen == nil || b.AdvanceTo == nil {
		t.Fatal("retirement requires complete native Store/peer/reopen/clock")
	}
	rt, e := kernel.New(kernel.Dependencies{Store: s})
	if e != nil {
		t.Fatal(e)
	}
	v, e := rt.Create(t.Context(), kernel.CreateRequest{ID: "retirement-root", Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "original"}, Thread: kernel.ThreadRef{Kind: "thread", ID: "source"}, RequestID: "original-request", Goal: "retirement boundary", State: json.RawMessage(`{"originalReceipt":{"id":"receipt","content":"full"},"budget":{"used":3}}`), ExecutionRequired: true})
	if e != nil {
		t.Fatal(e)
	}
	l, e := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: v.Run.ID, OwnerID: "original-owner", LeaseDuration: 10 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	bound, e := rt.WithExecution(l)
	if e != nil {
		t.Fatal(e)
	}
	g, e := admitBorrowed(t.Context(), bound, v, "start", "")
	if e != nil {
		t.Fatal(e)
	}
	v, e = rt.Load(t.Context(), v.Run.ID)
	if e != nil {
		t.Fatal(e)
	}
	return retirementFixture{b, rt, bound, s, v, l, g}
}
func retirementMarkProof(f retirementFixture) *featurecontrol.Proof {
	return featurecontrol.Seal(f.source.Run.ID, f.source.Run.Revision, "agent_owned_physical_settled", f.grant.ID, f.source.State, f.source)
}
func retirementCallback(t *testing.T, f retirementFixture, known, owned bool) {
	t.Helper()
	o := kernel.PhysicalCallbackOutcome{HostCompleted: known, LeaseOwned: owned}
	p := featurecontrol.Seal(f.source.Run.ID, f.source.Run.Revision, "agent_owned_callback_return", f.grant.ID, kernel.PhysicalCallbackReturnPayload(f.source, o), nil)
	if e := f.rt.RecordPhysicalCallbackReturn(t.Context(), f.grant, f.source.Run.Revision, o, p); e != nil {
		t.Fatal(e)
	}
}

// FinishOwnedFixture is used only by SQL conformance corruption tests.
func FinishOwnedFixture(ctx context.Context, rt *kernel.Runtime, g kernel.ChildExecutionGrant, s kernel.Snapshot) error {
	return rt.FinishChildExecution(ctx, g, featurecontrol.Seal(s.Run.ID, s.Run.Revision, "agent_owned_physical_settled", g.ID, s.State, s))
}
func retirementFinish(t *testing.T, f retirementFixture) kernel.OwnedRetirementReceipt {
	t.Helper()
	if e := f.bound.FinishChildExecution(t.Context(), f.grant, retirementMarkProof(f)); e != nil {
		t.Fatal(e)
	}
	r, e := f.rt.LoadOwnedRetirement(t.Context(), f.grant.ID)
	if e != nil || !r.Valid() || r.Evidence.Grant != f.grant || kernel.ManagedDigest(r.Evidence.Snapshot) != kernel.ManagedDigest(f.source) {
		t.Fatalf("retirement did not retain original full evidence: %+v %v", r, e)
	}
	return r
}
func retirementSame(t *testing.T, rt *kernel.Runtime, source kernel.Snapshot, r kernel.OwnedRetirementReceipt) {
	t.Helper()
	v, e := rt.Load(t.Context(), source.Run.ID)
	if e != nil || kernel.ManagedDigest(v) != kernel.ManagedDigest(source) {
		t.Fatalf("ACK changed full source/journal: %v", e)
	}
	got, e := rt.LoadOwnedRetirement(t.Context(), r.Evidence.Grant.ID)
	if e != nil || kernel.ManagedDigest(got) != kernel.ManagedDigest(r) {
		t.Fatalf("rejected/repeated ACK changed retirement: %v", e)
	}
}

// Each wrapper returns an error only AFTER its original transaction committed.
// Reopen then checks the durable result instead of replaying a physical action.
type retirementLostACK struct {
	managedStorePorts
	mode string
	hits int
}

var errRetirementLostACK = errors.New("retirement fixture committed then lost ACK")

func (s *retirementLostACK) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	e := s.managedStorePorts.FinishChildExecution(ctx, g, p)
	if e == nil && s.mode == "finish" {
		s.hits++
		return errRetirementLostACK
	}
	return e
}
func retirementRuntime(t *testing.T, s kernel.Store) *kernel.Runtime {
	t.Helper()
	rt, e := kernel.New(kernel.Dependencies{Store: s})
	if e != nil {
		t.Fatal(e)
	}
	return rt
}
func runRetirementACKWindows(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture, owned bool) {
	f := retirementSetup(t, factory)
	retirementCallback(t, f, true, owned)
	fault := &retirementLostACK{managedStorePorts: f.store, mode: "finish"}
	rt := retirementRuntime(t, fault)
	peer := retirementRuntime(t, f.backend.Peer)
	if e := rt.FinishChildExecution(t.Context(), f.grant, retirementMarkProof(f)); !errors.Is(e, errRetirementLostACK) || fault.hits != 1 {
		t.Fatalf("Finish commit response cut not reached: %v", e)
	}
	r, e := peer.LoadOwnedRetirement(t.Context(), f.grant.ID)
	if e != nil || r.Evidence.Completion.LeaseOwned != owned || !r.Valid() {
		t.Fatalf("peer retirement: %+v %v", r, e)
	}
	if e = f.store.FinishChildExecution(t.Context(), f.grant, kernel.ChildExecutionSettlement{}); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("generic missing Finish became ACK: %v", e)
	}
	freshStore := f.backend.Reopen()
	fresh := retirementRuntime(t, freshStore)
	acknowledged, e := fresh.LoadOwnedRetirement(t.Context(), f.grant.ID)
	if e != nil || !acknowledged.Valid() || acknowledged.Identity() != r.Identity() {
		t.Fatalf("missing final receipt: %v", e)
	}
	retirementSame(t, fresh, f.source, acknowledged)
	check := freshStore.(kernel.ExecutionStore)
	if e = check.CheckExecution(t.Context(), f.source.Run.ID, &f.lease); owned && !errors.Is(e, kernel.ErrExecutionConflict) || !owned && e != nil {
		t.Fatalf("native ownership disposition: %v", e)
	}
	if !owned {
		f.backend.AdvanceTo(f.lease.ExpiresAt.Add(time.Millisecond))
		if e = check.CheckExecution(t.Context(), f.source.Run.ID, &f.lease); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("borrowed lease extended past original expiry: %v", e)
		}
	}
	// After old ACK, a legitimate later epoch/grant must remain untouched by
	// exact old ACK confirmation. It is not authority to acknowledge a first ACK.
	next, e := fresh.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: f.source.Run.ID, OwnerID: "later", LeaseDuration: time.Minute})
	if e != nil || next.Epoch != f.lease.Epoch+1 {
		t.Fatalf("new owner: %+v %v", next, e)
	}
	nextRT, e := fresh.WithExecution(next)
	if e != nil {
		t.Fatal(e)
	}
	changed, e := nextRT.Apply(t.Context(), f.source.Run.ID, f.source.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"later":"new owner"}`), Events: []kernel.EventDraft{{Type: "later.changed"}}})
	if e != nil {
		t.Fatal(e)
	}
	nextGrant, e := admitBorrowed(t.Context(), nextRT, changed, "resume", "")
	if e != nil {
		t.Fatal(e)
	}
	if old, e := fresh.LoadOwnedRetirement(t.Context(), f.grant.ID); e != nil || kernel.ManagedDigest(old) != kernel.ManagedDigest(r) {
		t.Fatalf("original receipt changed: %v", e)
	}
	retirementSame(t, fresh, changed, acknowledged)
	current, e := fresh.LookupChildExecution(t.Context(), changed.Run.ID)
	if e != nil || current != nextGrant {
		t.Fatal("old ACK changed replacement grant")
	}
	if e = check.CheckExecution(t.Context(), changed.Run.ID, &next); e != nil {
		t.Fatal("old ACK released new native owner")
	}
	retirementSame(t, fresh, changed, acknowledged)
}
func runRetirementRejectedFinish(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture, variant string) {
	f := retirementSetup(t, factory)
	retirementCallback(t, f, true, true)
	g := f.grant
	switch variant {
	case "foreign-grant":
		g.ID = "foreign"
	case "foreign-owner":
		g.ExecutionOwnerID = "foreign"
	case "foreign-lease":
		g.ExecutionLeaseID = "foreign"
	case "foreign-epoch":
		g.ExecutionEpoch++
	case "wrong-revision":
		g.ExpectedRevision++
	case "descendant":
		child, e := f.rt.Create(t.Context(), kernel.CreateRequest{ID: "retirement-child", Kind: "agent", ParentRunID: f.source.Run.ID, Actor: f.source.Run.Actor, Thread: f.source.Run.Thread, Goal: "original child", State: json.RawMessage(`{}`)})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.rt.BeginChildExecution(t.Context(), child.Run.ID, child.Run.Revision); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.rt.FinishChildExecution(t.Context(), g, retirementMarkProof(f)); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("invalid finish accepted: %v", e)
	}
	if _, e := f.rt.LoadOwnedRetirement(t.Context(), f.grant.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("fabricated receipt: %v", e)
	}
	if got, e := f.rt.LookupChildExecution(t.Context(), f.source.Run.ID); e != nil || got != f.grant {
		t.Fatalf("original grant changed: %v", e)
	}
	if e := f.store.CheckExecution(t.Context(), f.source.Run.ID, &f.lease); e != nil {
		t.Fatalf("lease released: %v", e)
	}
}

func runRetirementNoRelease(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture, kind string) {
	f := retirementSetup(t, factory)
	if kind == "unknown-callback" {
		retirementCallback(t, f, false, true)
		p := featurecontrol.Seal(f.source.Run.ID, f.source.Run.Revision, "agent_owned_physical_settled", f.grant.ID, f.source.State, f.source)
		if e := f.rt.FinishChildExecution(t.Context(), f.grant, p); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("unknown callback authorized recovery Mark: %v", e)
		}

	}
	if kind == "legacy-mark" {
		if e := f.bound.RecordPhysicalCallbackReturn(t.Context(), f.grant, f.source.Run.Revision, kernel.PhysicalCallbackOutcome{HostCompleted: true}, retirementMarkProof(f)); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("owned mark accepted: %v", e)
		}
	}
	if e := f.rt.FinishChildExecution(t.Context(), f.grant, retirementMarkProof(f)); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("unmarked/unknown scope finished: %v", e)
	}
	if _, e := f.rt.LoadOwnedRetirement(t.Context(), f.grant.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("unknown receipt exists: %v", e)
	}
	if g, e := f.rt.LookupChildExecution(t.Context(), f.source.Run.ID); e != nil || g != f.grant {
		t.Fatal("unknown changed original grant")
	}
	if e := f.store.CheckExecution(t.Context(), f.source.Run.ID, &f.lease); e != nil {
		t.Fatal("no-release boundary cleared lease")
	}
}
func runRetirementCompetition(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	f := retirementSetup(t, factory)
	retirementCallback(t, f, true, true)
	peer, ok := f.backend.Peer.(managedStorePorts)
	if !ok {
		t.Fatal("peer missing native ports")
	}
	// Both independent requests reach the transaction boundary before release.
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, s := range []managedStorePorts{f.store, peer} {
		wg.Add(1)
		go func(s managedStorePorts) {
			defer wg.Done()
			ready <- struct{}{}
			<-release
			results <- retirementRuntime(t, s).FinishChildExecution(t.Context(), f.grant, retirementMarkProof(f))
		}(s)
	}
	<-ready
	<-ready
	close(release)
	wg.Wait()
	close(results)
	wins := 0
	for e := range results {
		if e == nil {
			wins++
		} else if !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("Finish competitor error: %v", e)
		}
	}
	if wins != 1 {
		t.Fatalf("Finish winners %d", wins)
	}
	r, e := f.rt.LoadOwnedRetirement(t.Context(), f.grant.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !r.Valid() {
		t.Fatal("winning Finish lacks final receipt")
	}
	if e = f.store.CheckExecution(t.Context(), f.source.Run.ID, &f.lease); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("winning Finish retained owner: %v", e)
	}
	retirementSame(t, f.rt, f.source, r)
}
func runRetirementManaged(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	f := managedStoreSetup(t, factory)
	selection, _ := managedStoreSelection(t, f, kernel.ManagedResume)
	enabled, e := enableManagedRecovery(t.Context(), f.store, managedEnableRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, OwnerID: "owner", LeaseID: "lease", GrantID: "successor", LeaseDuration: time.Minute, Selection: selection})
	if e != nil {
		t.Fatal(e)
	}
	bound, e := f.runtime.WithExecution(enabled.SuccessorLease)
	if e != nil {
		t.Fatal(e)
	}
	g := enabled.SuccessorGrant
	scoped, e := bound.WithPhysicalGrant(t.Context(), g, featurecontrol.Seal(f.source.Run.ID, f.source.Run.Revision, "agent_physical_owner", g.ID, f.source.State, f.source.Checkpoint))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = scoped.CheckOwnedPhysicalEntry(t.Context(), f.source); e != nil {
		t.Fatal(e)
	}
	own := retirementFixture{backend: f.backend, rt: f.runtime, bound: scoped, store: f.store, source: f.source, lease: enabled.SuccessorLease, grant: g}
	if e = f.store.FinishChildExecution(t.Context(), g, kernel.ChildExecutionSettlement{}); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("unmarked Finish closed managed grant: %v", e)
	}
	retirementCallback(t, own, true, true)
	before, e := f.store.LoadManagedRecoveryByID(t.Context(), f.point.ID)
	if e != nil || before.Resolved() || before.Selection.Mode != kernel.ManagedResume || before.Terminal != nil {
		t.Fatalf("premature closure: %v", e)
	}
	// Lose the actual native Finish ACK: the next reader must see both the
	// exact managed closure and the original retirement, never one alone.
	fault := &retirementLostACK{managedStorePorts: f.store, mode: "finish"}
	if e = retirementRuntime(t, fault).FinishChildExecution(t.Context(), g, retirementMarkProof(own)); !errors.Is(e, errRetirementLostACK) {
		t.Fatalf("managed finish ACK cut: %v", e)
	}
	r, e := f.runtime.LoadOwnedRetirement(t.Context(), g.ID)
	after, ce := f.store.LoadManagedRecoveryByID(t.Context(), f.point.ID)
	if e != nil || ce != nil || len(r.Closures) != 1 || r.Closures[0].SafePoint.ID != f.point.ID || !r.Closures[0].Resolved() || r.Closures[0].Selection.Mode != kernel.ManagedResume || kernel.ManagedDigest(r.Closures[0]) != kernel.ManagedDigest(after) {
		t.Fatalf("retirement omitted original closure: %v %v", e, ce)
	}
	if _, e = f.store.LookupChildExecution(t.Context(), g.ChildRunID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("retired grant survived closure: %v", e)
	}
	fresh := retirementRuntime(t, f.backend.Reopen())
	retired, e := fresh.LoadOwnedRetirement(t.Context(), g.ID)
	if e != nil || len(retired.Closures) != 1 || kernel.ManagedDigest(retired.Closures) != kernel.ManagedDigest(r.Closures) {
		t.Fatal("reopen lost managed lineage")
	}
	retirementSame(t, fresh, f.source, retired)
}

// Reopen after the real Finish transaction committed but before its response.
// Recovery starts from public run ID, not a retained grant-ID lookup.
func runRetirementRunIDRecovery(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	f := retirementSetup(t, factory)
	retirementCallback(t, f, true, true)
	fault := &retirementLostACK{managedStorePorts: f.store, mode: "finish"}
	rt := retirementRuntime(t, fault)
	if e := rt.FinishChildExecution(t.Context(), f.grant, retirementMarkProof(f)); !errors.Is(e, errRetirementLostACK) || fault.hits != 1 {
		t.Fatalf("real Finish ACK loss: %v", e)
	}
	fresh := retirementRuntime(t, f.backend.Reopen())
	selected, e := fresh.LookupOwnedRetirementForRun(t.Context(), f.source.Run.ID)
	if e != nil || !selected.Valid() || kernel.ManagedDigest(selected.Evidence.Snapshot) != kernel.ManagedDigest(f.source) || selected.Evidence.Grant != f.grant {
		t.Fatalf("run-only durable recovery: %v", e)
	}
	acknowledged, e := fresh.LoadOwnedRetirement(t.Context(), selected.Evidence.Grant.ID)
	if e != nil || kernel.ManagedDigest(acknowledged) != kernel.ManagedDigest(selected) {
		t.Fatalf("read changed receipt: %v", e)
	}
	retirementSame(t, fresh, f.source, acknowledged)
}

func runRetiredGrantIDReuse(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	f := retirementSetup(t, factory)
	retirementCallback(t, f, true, true)
	r := retirementFinish(t, f)
	ctx := t.Context()
	rt, e := kernel.New(kernel.Dependencies{Store: f.backend.Peer, IDs: fixedGrantIDs{f.grant.ID}})
	if e != nil {
		t.Fatal(e)
	}
	l, e := rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: f.source.Run.ID, OwnerID: "new-owner", LeaseDuration: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	b, e := rt.WithExecution(l)
	if e != nil {
		t.Fatal(e)
	}
	v, e := rt.Load(ctx, f.source.Run.ID)
	if e != nil {
		t.Fatal(e)
	}
	g, e := admitBorrowed(ctx, b, v, "start", "")
	if !errors.Is(e, kernel.ErrExecutionConflict) || g != (kernel.ChildExecutionGrant{}) {
		t.Fatalf("retired ID was admitted: %+v %v", g, e)
	}
	if _, e = rt.LookupChildExecution(ctx, v.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("rejected admission left an active grant: %v", e)
	}
	retained, e := f.backend.Reopen().(kernel.OwnedRetirementStore).LoadOwnedRetirement(ctx, f.grant.ID)
	if e != nil || retained.Identity() != r.Identity() || !retained.Valid() {
		t.Fatalf("original retirement changed: %v", e)
	}
}

// Competing transactions hold unrelated Run locks and use the same ID, but
// only one identity may become authoritative. Borrowed replicas are legal
// within the winner's grant, not a second identity.
func runGrantIdentityRace(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	b := factory(t)
	ctx := t.Context()
	id := "same-physical-identity"
	rootRT, e := kernel.New(kernel.Dependencies{Store: b.Store, IDs: fixedGrantIDs{id}})
	if e != nil {
		t.Fatal(e)
	}
	childRT, e := kernel.New(kernel.Dependencies{Store: b.Peer, IDs: fixedGrantIDs{id}})
	if e != nil {
		t.Fatal(e)
	}
	create := func(rt *kernel.Runtime, id, parent, tenant string, native bool) kernel.Snapshot {
		t.Helper()
		s, e := rt.Create(ctx, kernel.CreateRequest{ID: id, ParentRunID: parent, Kind: "agent", Actor: kernel.ActorRef{TenantID: tenant, ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "identity race", State: []byte("{}"), ExecutionRequired: native})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	root := create(rootRT, "owned-root", "", "owned", true)
	parent := create(childRT, "borrowed-parent", "", "borrowed", false)
	child := create(childRT, "borrowed-child", parent.Run.ID, "borrowed", false)
	lease, e := rootRT.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: root.Run.ID, OwnerID: "owner", LeaseDuration: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	bound, e := rootRT.WithExecution(lease)
	if e != nil {
		t.Fatal(e)
	}
	type result struct {
		grant kernel.ChildExecutionGrant
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	go func() {
		<-start
		g, e := admitBorrowed(ctx, bound, root, "start", "")
		results <- result{g, e}
	}()
	go func() {
		<-start
		g, e := childRT.BeginChildExecution(ctx, child.Run.ID, child.Run.Revision)
		results <- result{g, e}
	}()
	close(start)
	var winner kernel.ChildExecutionGrant
	var wins int
	for range 2 {
		r := <-results
		if r.err == nil {
			winner = r.grant
			wins++
		} else if !errors.Is(r.err, kernel.ErrExecutionConflict) {
			t.Fatalf("unexpected contender error: %v", r.err)
		}
	}
	if wins != 1 || winner.ID != id {
		t.Fatalf("identity race admitted %d grants: %+v", wins, winner)
	}
	for _, runID := range []string{root.Run.ID, child.Run.ID} {
		g, e := rootRT.LookupChildExecution(ctx, runID)
		if runID == winner.ChildRunID {
			if e != nil || g != winner {
				t.Fatalf("winner lost original identity: %+v %v", g, e)
			}
		} else if !errors.Is(e, kernel.ErrNotFound) {
			t.Fatalf("loser left fence: %+v %v", g, e)
		}
	}
	if winner.IsOwned() {
		o := kernel.PhysicalCallbackOutcome{HostCompleted: true, LeaseOwned: true}
		proof := featurecontrol.Seal(root.Run.ID, root.Run.Revision, "agent_owned_callback_return", winner.ID, kernel.PhysicalCallbackReturnPayload(root, o), nil)
		if e = rootRT.RecordPhysicalCallbackReturn(ctx, winner, root.Run.Revision, o, proof); e != nil {
			t.Fatal(e)
		}
		mark := featurecontrol.Seal(root.Run.ID, root.Run.Revision, "agent_owned_physical_settled", winner.ID, root.State, root)
		if e = bound.FinishChildExecution(ctx, winner, mark); e != nil {
			t.Fatal(e)
		}
	} else {
		mark := featurecontrol.Seal(child.Run.ID, child.Run.Revision, "agent_child_physical_settled", winner.ID, child.State, child.Checkpoint)
		if e = childRT.RecordPhysicalCallbackReturn(ctx, winner, child.Run.Revision, kernel.PhysicalCallbackOutcome{HostCompleted: true}, mark); e != nil {
			t.Fatal(e)
		}
		if e = childRT.FinishChildExecution(ctx, winner, mark); e != nil {
			t.Fatal(e)
		}
	}
	// A new wrapper/pool must retain the identity after all fence rows vanish.
	fresh, e := kernel.New(kernel.Dependencies{Store: b.Reopen(), IDs: fixedGrantIDs{id}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = fresh.BeginChildExecution(ctx, child.Run.ID, child.Run.Revision); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("consumed ID re-admitted as borrowed: %v", e)
	}
	freshBound, e := fresh.WithExecution(lease)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = admitBorrowed(ctx, freshBound, root, "start", ""); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("consumed ID re-admitted as owned: %v", e)
	}
}

// A reservation is committed only with its fence, not with a rejected
// attempt. A busy child may not burn a fresh ID for an unrelated Run.
func runGrantIdentityRollback(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	b := factory(t)
	ctx := t.Context()
	first, e := kernel.New(kernel.Dependencies{Store: b.Store, IDs: fixedGrantIDs{"first-id"}})
	if e != nil {
		t.Fatal(e)
	}
	second, e := kernel.New(kernel.Dependencies{Store: b.Peer, IDs: fixedGrantIDs{"rolled-back-id"}})
	if e != nil {
		t.Fatal(e)
	}
	create := func(id string) kernel.Snapshot {
		t.Helper()
		s, e := first.Create(ctx, kernel.CreateRequest{ID: id, Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "identity rollback", State: []byte("{}")})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	a, other := create("busy-run"), create("unrelated-run")
	original, e := first.BeginChildExecution(ctx, a.Run.ID, a.Run.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = second.BeginChildExecution(ctx, a.Run.ID, a.Run.Revision); !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("busy child error: %v", e)
	}
	if got, e := first.LookupChildExecution(ctx, a.Run.ID); e != nil || got != original {
		t.Fatalf("rejected attempt changed original grant: %+v %v", got, e)
	}
	if got, e := second.BeginChildExecution(ctx, other.Run.ID, other.Run.Revision); e != nil || got.ID != "rolled-back-id" {
		t.Fatalf("failed attempt permanently reserved ID: %+v %v", got, e)
	}
}
