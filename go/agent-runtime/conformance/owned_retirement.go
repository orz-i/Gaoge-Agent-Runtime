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
	for _, owned := range []bool{true, false} {
		t.Run(map[bool]string{true: "owned", false: "borrowed"}[owned], func(t *testing.T) { runRetirementACKWindows(t, factory, owned) })
	}
	for _, variant := range []string{"nil-proof", "wrong-source", "foreign-grant", "foreign-owner", "foreign-lease", "foreign-epoch", "wrong-revision", "wrong-eventhead", "new-source", "new-epoch", "replacement-grant", "descendant-grant"} {
		t.Run("first-ack/"+variant, func(t *testing.T) { runRetirementRejectedACK(t, factory, variant) })
	}
	for _, kind := range []string{"active", "unknown-callback", "legacy-mark"} {
		t.Run("no-release/"+kind, func(t *testing.T) { runRetirementNoRelease(t, factory, kind) })
	}
	t.Run("competing-finish-and-ack", func(t *testing.T) { runRetirementCompetition(t, factory) })
	t.Run("actual-Agent-finish-death-public-runID-recovery", func(t *testing.T) { runRetirementActualAgentRecovery(t, factory) })
	t.Run("finish-lost-ACK-runID-reopen", func(t *testing.T) { runRetirementRunIDRecovery(t, factory) })
	t.Run("managed-T4-before-retirement", func(t *testing.T) { runRetirementManaged(t, factory) })
}

type retirementFixture struct {
	backend   ManagedRecoveryStoreFixture
	rt, bound *kernel.Runtime
	store     managedStorePorts
	source    kernel.Snapshot
	lease     kernel.ExecutionLease
	grant     kernel.ChildExecutionGrant
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
	g, e := bound.BeginOwnedPhysicalExecution(t.Context(), v.Run.ID, v.Run.Revision, featurecontrol.Seal(v.Run.ID, v.Run.Revision, "agent_owned_physical_admission", l.LeaseID, v.State, v.Checkpoint), "start")
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
	return featurecontrol.Seal(f.source.Run.ID, f.source.Run.Revision, "agent_owned_physical_settled", f.grant.ID, f.source.State, f.source.Checkpoint)
}
func retirementACKProof(r kernel.OwnedRetirementReceipt) *featurecontrol.Proof {
	return featurecontrol.Seal(r.Evidence.Grant.ChildRunID, r.Evidence.Snapshot.Run.Revision, "agent_owned_retirement_ack", r.Evidence.Grant.ID, kernel.OwnedRetirementAcknowledgementPayload(r), nil)
}
func retirementCallback(t *testing.T, f retirementFixture, known, owned bool) {
	t.Helper()
	o := kernel.OwnedPhysicalCallbackOutcome{HostCompleted: known, LeaseOwned: owned}
	p := featurecontrol.Seal(f.source.Run.ID, f.source.Run.Revision, "agent_owned_callback_return", f.grant.ID, kernel.OwnedPhysicalCallbackReturnPayload(f.source, o), nil)
	if e := f.rt.RecordOwnedPhysicalCallbackReturn(t.Context(), f.grant, f.source.Run.Revision, o, p); e != nil {
		t.Fatal(e)
	}
}
func retirementMark(t *testing.T, f retirementFixture) {
	t.Helper()
	if e := f.bound.MarkChildExecutionSettled(t.Context(), f.grant, retirementMarkProof(f)); e != nil {
		t.Fatal(e)
	}
}
func retirementFinish(t *testing.T, f retirementFixture) kernel.OwnedRetirementReceipt {
	t.Helper()
	if e := f.bound.FinishChildExecution(t.Context(), f.grant, retirementMarkProof(f)); e != nil {
		t.Fatal(e)
	}
	r, e := f.rt.LoadOwnedRetirement(t.Context(), f.grant.ID)
	if e != nil || !r.Valid() || r.Phase != "RETIRED" || r.Evidence.Grant != f.grant || kernel.ManagedDigest(r.Evidence.Snapshot) != kernel.ManagedDigest(f.source) {
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

func (s *retirementLostACK) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	e := s.managedStorePorts.MarkChildExecutionSettled(ctx, g, p)
	if e == nil && s.mode == "mark" {
		s.hits++
		return errRetirementLostACK
	}
	return e
}
func (s *retirementLostACK) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	e := s.managedStorePorts.FinishChildExecution(ctx, g)
	if e == nil && s.mode == "finish" {
		s.hits++
		return errRetirementLostACK
	}
	return e
}
func (s *retirementLostACK) AcknowledgeOwnedRetirement(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.OwnedRetirementAcknowledgement) (kernel.OwnedRetirementAckResult, error) {
	v, e := s.managedStorePorts.AcknowledgeOwnedRetirement(ctx, g, p)
	if e == nil && s.mode == "ack" {
		s.hits++
		return kernel.OwnedRetirementAckResult{}, errRetirementLostACK
	}
	return v, e
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
	fault := &retirementLostACK{managedStorePorts: f.store, mode: "mark"}
	rt := retirementRuntime(t, fault)
	bound, e := rt.WithExecution(f.lease)
	if e != nil {
		t.Fatal(e)
	}
	if e = bound.MarkChildExecutionSettled(t.Context(), f.grant, retirementMarkProof(f)); !errors.Is(e, errRetirementLostACK) || fault.hits != 1 {
		t.Fatalf("Mark commit ACK cut not reached: %v", e)
	}
	peer := retirementRuntime(t, f.backend.Peer)
	// Actual peer Finish consumes the genuine marker, without waiting for TTL.
	fault.mode = "finish"
	fault.hits = 0
	if e = bound.FinishChildExecution(t.Context(), f.grant, retirementMarkProof(f)); !errors.Is(e, errRetirementLostACK) || fault.hits != 1 {
		t.Fatalf("Finish commit ACK cut not reached: %v", e)
	}
	r, e := peer.LoadOwnedRetirement(t.Context(), f.grant.ID)
	if e != nil || r.Evidence.Completion.LeaseOwned != owned || !r.Valid() {
		t.Fatalf("peer retirement: %+v %v", r, e)
	}
	if e = f.store.FinishChildExecution(t.Context(), f.grant); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("generic missing Finish became ACK: %v", e)
	}
	fault.mode = "ack"
	fault.hits = 0
	if _, e = rt.AcknowledgeOwnedRetirement(t.Context(), f.grant, retirementACKProof(r)); !errors.Is(e, errRetirementLostACK) || fault.hits != 1 {
		t.Fatalf("ACK commit cut not reached: %v", e)
	}
	freshStore := f.backend.Reopen()
	fresh := retirementRuntime(t, freshStore)
	acknowledged, e := fresh.LoadOwnedRetirement(t.Context(), f.grant.ID)
	if e != nil || acknowledged.Phase != "ACKNOWLEDGED" {
		t.Fatalf("missing ACK after reopen: %v", e)
	}
	result, e := fresh.AcknowledgeOwnedRetirement(t.Context(), f.grant, retirementACKProof(r))
	want := "preserved_borrowed"
	if owned {
		want = "released_owned"
	}
	if e != nil || !result.PreviouslyAcknowledged || result.Disposition != want || result.ReceiptID != f.grant.ID || result.SettledSourceDigest != kernel.ManagedDigest(f.source) {
		t.Fatalf("historical ACK: %+v %v", result, e)
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
	nextGrant, e := nextRT.BeginOwnedPhysicalExecution(t.Context(), changed.Run.ID, changed.Run.Revision, featurecontrol.Seal(changed.Run.ID, changed.Run.Revision, "agent_owned_physical_admission", next.LeaseID, changed.State, changed.Checkpoint), "resume")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = fresh.AcknowledgeOwnedRetirement(t.Context(), f.grant, retirementACKProof(r)); e != nil {
		t.Fatalf("old acknowledged confirmation rejected: %v", e)
	}
	retirementSame(t, fresh, changed, acknowledged)
	current, e := fresh.LookupChildExecution(t.Context(), changed.Run.ID)
	if e != nil || current != nextGrant {
		t.Fatal("old ACK changed replacement grant")
	}
	if e = check.CheckExecution(t.Context(), changed.Run.ID, &next); e != nil {
		t.Fatal("old ACK released new native owner")
	}
	bad := r
	bad.Evidence.Snapshot.EventHead++
	if _, e = fresh.AcknowledgeOwnedRetirement(t.Context(), f.grant, retirementACKProof(bad)); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("altered old source accepted: %v", e)
	}
	retirementSame(t, fresh, changed, acknowledged)
}
func runRetirementRejectedACK(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture, variant string) {
	f := retirementSetup(t, factory)
	retirementCallback(t, f, true, true)
	retirementMark(t, f)
	r := retirementFinish(t, f)
	source := f.source
	g := f.grant
	selected, e := f.rt.LookupOwnedRetirementForRun(t.Context(), f.source.Run.ID)
	if e != nil || kernel.ManagedDigest(selected) != kernel.ManagedDigest(r) {
		t.Fatalf("original runID selection: %v", e)
	}
	proof := retirementACKProof(selected)
	var replacement kernel.ChildExecutionGrant
	var childSource kernel.Snapshot
	switch variant {
	case "nil-proof":
		proof = nil
	case "wrong-source":
		wrong := r
		wrong.Evidence.Snapshot.State = json.RawMessage(`{"foreign":true}`)
		proof = retirementACKProof(wrong)
	case "wrong-revision":
		wrong := r
		wrong.Evidence.Snapshot.Run.Revision++
		proof = retirementACKProof(wrong)
	case "wrong-eventhead":
		wrong := r
		wrong.Evidence.Snapshot.EventHead++
		proof = retirementACKProof(wrong)
	case "foreign-grant":
		g.ID = "foreign-grant"
	case "foreign-owner":
		g.ExecutionOwnerID = "foreign-owner"
	case "foreign-lease":
		g.ExecutionLeaseID = "foreign-lease"
	case "foreign-epoch":
		g.ExecutionEpoch++
	case "new-source":
		var e error
		source, e = f.bound.Apply(t.Context(), source.Run.ID, source.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"differentSource":true}`)})
		if e != nil {
			t.Fatal(e)
		}
	case "new-epoch":
		if e := f.rt.ReleaseExecution(t.Context(), f.lease); e != nil {
			t.Fatal(e)
		}
		l, e := f.rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: source.Run.ID, OwnerID: "new-owner", LeaseDuration: time.Minute})
		if e != nil {
			t.Fatal(e)
		}
		f.lease = l
	case "descendant-grant":
		child, e := f.rt.Create(t.Context(), kernel.CreateRequest{ID: "retirement-descendant", Kind: "agent", ParentRunID: source.Run.ID, Actor: source.Run.Actor, Thread: source.Run.Thread, Goal: "original descendant", State: json.RawMessage(`{"original":true}`)})
		if e != nil {
			t.Fatal(e)
		}
		replacement, e = f.rt.BeginChildExecution(t.Context(), child.Run.ID, child.Run.Revision)
		if e != nil {
			t.Fatal(e)
		}
		childSource, e = f.rt.Load(t.Context(), child.Run.ID)
		if e != nil {
			t.Fatal(e)
		}
	case "replacement-grant":
		var e error
		replacement, e = f.bound.BeginOwnedPhysicalExecution(t.Context(), source.Run.ID, source.Run.Revision, featurecontrol.Seal(source.Run.ID, source.Run.Revision, "agent_owned_physical_admission", f.lease.LeaseID, source.State, source.Checkpoint), "resume")
		if e != nil {
			t.Fatal(e)
		}
	}
	if variant == "new-source" || variant == "new-epoch" || variant == "replacement-grant" || variant == "descendant-grant" {
		if _, e := f.rt.LookupOwnedRetirementForRun(t.Context(), source.Run.ID); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("changed source still selected: %v", e)
		}
	}
	if _, e := f.rt.AcknowledgeOwnedRetirement(t.Context(), g, proof); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("first ACK %s: %v", variant, e)
	}
	retirementSame(t, f.rt, source, r)
	if e := f.store.CheckExecution(t.Context(), source.Run.ID, &f.lease); e != nil {
		t.Fatalf("rejected ACK modified native owner: %v", e)
	}
	if replacement.ID != "" {
		same, e := f.rt.LookupChildExecution(t.Context(), replacement.ChildRunID)
		if e != nil || same != replacement {
			t.Fatal("rejected ACK changed replacement")
		}
	}
	if childSource.Run.ID != "" {
		child, e := f.rt.Load(t.Context(), childSource.Run.ID)
		if e != nil || kernel.ManagedDigest(child) != kernel.ManagedDigest(childSource) {
			t.Fatal("ACK changed descendant source")
		}
	}
	if _, e := f.store.AcknowledgeOwnedRetirement(t.Context(), f.grant, kernel.OwnedRetirementAcknowledgement{}); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("direct zero permit accepted: %v", e)
	}
	retirementSame(t, f.rt, source, r)
}
func runRetirementNoRelease(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture, kind string) {
	f := retirementSetup(t, factory)
	if kind == "unknown-callback" {
		retirementCallback(t, f, false, true)
		p := featurecontrol.Seal(f.source.Run.ID, f.source.Run.Revision, "agent_owned_physical_recovered", f.grant.ID, f.source.State, f.source.Checkpoint)
		if e := f.rt.MarkOwnedPhysicalRecovered(t.Context(), f.grant, p); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("unknown callback authorized recovery Mark: %v", e)
		}

	}
	if kind == "legacy-mark" {
		retirementMark(t, f)
		r := retirementFinish(t, f)
		if r.Evidence.MarkOrigin != "LEGACY_UNKNOWN" || r.Evidence.Completion.GrantID != "" {
			t.Fatal("legacy marker invented ownership")
		}
		if _, e := f.rt.AcknowledgeOwnedRetirement(t.Context(), f.grant, retirementACKProof(r)); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("legacy authorized release: %v", e)
		}
		retirementSame(t, f.rt, f.source, r)
	} else {
		if e := f.store.FinishChildExecution(t.Context(), f.grant); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("unmarked/unknown scope finished: %v", e)
		}
		if _, e := f.rt.LoadOwnedRetirement(t.Context(), f.grant.ID); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatalf("unmarked receipt exists: %v", e)
		}
		if _, e := f.store.AcknowledgeOwnedRetirement(t.Context(), f.grant, kernel.OwnedRetirementAcknowledgement{}); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("missing receipt authorized release: %v", e)
		}
		same, e := f.rt.LookupChildExecution(t.Context(), f.source.Run.ID)
		if e != nil || same != f.grant {
			t.Fatal("unknown changed original grant")
		}
	}
	if e := f.store.CheckExecution(t.Context(), f.source.Run.ID, &f.lease); e != nil {
		t.Fatal("no-release boundary cleared lease")
	}
}
func runRetirementCompetition(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	f := retirementSetup(t, factory)
	retirementCallback(t, f, true, true)
	retirementMark(t, f)
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
			results <- s.FinishChildExecution(t.Context(), f.grant)
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
	ready = make(chan struct{}, 2)
	release = make(chan struct{})
	acks := make(chan kernel.OwnedRetirementAckResult, 2)
	results = make(chan error, 2)
	for _, s := range []managedStorePorts{f.store, peer} {
		wg.Add(1)
		go func(s managedStorePorts) {
			defer wg.Done()
			rt, err := kernel.New(kernel.Dependencies{Store: s})
			if err != nil {
				results <- err
				ready <- struct{}{}
				return
			}
			ready <- struct{}{}
			<-release
			v, err := rt.AcknowledgeOwnedRetirement(t.Context(), f.grant, retirementACKProof(r))
			acks <- v
			results <- err
		}(s)
	}
	<-ready
	<-ready
	close(release)
	wg.Wait()
	close(results)
	close(acks)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := 0
	for v := range acks {
		if !v.PreviouslyAcknowledged {
			first++
		}
		if v.Disposition != "released_owned" {
			t.Fatal("wrong ACK disposition")
		}
	}
	if first != 1 {
		t.Fatalf("first ACK writers %d", first)
	}
	acknowledged, e := f.rt.LoadOwnedRetirement(t.Context(), f.grant.ID)
	if e != nil || acknowledged.Phase != "ACKNOWLEDGED" {
		t.Fatal("ACK receipt missing")
	}
	retirementSame(t, f.rt, f.source, acknowledged)
}
func runRetirementManaged(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	f := managedStoreSetup(t, factory)
	selection, parked := managedStoreSelection(t, f, kernel.ManagedResume)
	enabled, e := f.store.EnableManagedRecovery(t.Context(), kernel.ManagedEnableRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, ClaimantID: "claimant", OwnerID: "owner", LeaseID: "lease", GrantID: "successor", ExpectedTransferRevision: parked.Revision, Generation: parked.Generation, LeaseDuration: time.Minute, Selection: selection})
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
	if _, e = scoped.LeaveManagedSafePoint(t.Context(), f.source.Run.ID); e != nil {
		t.Fatal(e)
	}
	own := retirementFixture{backend: f.backend, rt: f.runtime, bound: scoped, store: f.store, source: f.source, lease: enabled.SuccessorLease, grant: g}
	retirementCallback(t, own, true, true)
	retirementMark(t, own)
	before, e := f.store.LoadManagedRecoveryByID(t.Context(), f.point.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.store.FinishChildExecution(t.Context(), g); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("Finish bypassed unfinished T4: %v", e)
	}
	after, e := f.store.LoadManagedRecoveryByID(t.Context(), f.point.ID)
	if e != nil || kernel.ManagedDigest(before) != kernel.ManagedDigest(after) {
		t.Fatal("Finish performed T4")
	}
	completion := featurecontrol.Seal(f.source.Run.ID, f.source.Run.Revision, "managed_successor_completed", g.ID, f.source.State, f.source)
	if e = scoped.CompleteManagedRecovery(t.Context(), g, f.source, completion); e != nil {
		t.Fatal(e)
	}
	r := retirementFinish(t, own)
	if len(r.Closures) != 1 || r.Closures[0].SafePoint.ID != f.point.ID || r.Closures[0].State != kernel.ManagedClosed {
		t.Fatal("retirement omitted actual original T4")
	}
	if _, e = f.runtime.AcknowledgeOwnedRetirement(t.Context(), g, retirementACKProof(r)); e != nil {
		t.Fatal(e)
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
	retirementMark(t, f)
	fault := &retirementLostACK{managedStorePorts: f.store, mode: "finish"}
	rt := retirementRuntime(t, fault)
	if e := rt.FinishChildExecution(t.Context(), f.grant, retirementMarkProof(f)); !errors.Is(e, errRetirementLostACK) || fault.hits != 1 {
		t.Fatalf("real Finish ACK loss: %v", e)
	}
	fresh := retirementRuntime(t, f.backend.Reopen())
	selected, e := fresh.LookupOwnedRetirementForRun(t.Context(), f.source.Run.ID)
	if e != nil || selected.Phase != "RETIRED" || kernel.ManagedDigest(selected.Evidence.Snapshot) != kernel.ManagedDigest(f.source) || selected.Evidence.Grant != f.grant {
		t.Fatalf("run-only durable recovery: %v", e)
	}
	result, e := fresh.AcknowledgeOwnedRetirement(t.Context(), selected.Evidence.Grant, retirementACKProof(selected))
	if e != nil || result.PreviouslyAcknowledged || result.Disposition != "released_owned" {
		t.Fatalf("recovered first ACK: %+v %v", result, e)
	}
	if _, e = fresh.LookupOwnedRetirementForRun(t.Context(), f.source.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("historical ACK selected by runID: %v", e)
	}
	acknowledged, e := fresh.LoadOwnedRetirement(t.Context(), selected.Evidence.Grant.ID)
	if e != nil || acknowledged.Phase != "ACKNOWLEDGED" {
		t.Fatalf("durable ACK: %v", e)
	}
	retirementSame(t, fresh, f.source, acknowledged)
}
