package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// ManagedRecoveryStoreFixture supplies independent authoritative handles.
// AdvanceTo waits for real Store time on SQL; Memory may advance its own clock.
// Reopen closes every old SQL handle before reopening, not a process-local proof.
type ManagedRecoveryStoreFixture struct {
	Store     kernel.Store
	Peer      kernel.Store
	AdvanceTo func(time.Time)
	Reopen    func() kernel.Store
	// RetryableContention is an adapter-specific infrastructure observation.
	// It is not an authorization outcome and never satisfies a conflict check.
	RetryableContention func(error) bool
}
type managedStorePorts interface {
	kernel.Store
	kernel.ExecutionStore

	kernel.PhysicalExecutionStore

	kernel.ExecutionQuarantineStore
	kernel.ManagedRecoveryStore
}

func RunManagedRecoveryStoreSuite(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	t.Helper()
	t.Run("identity-retired-successor", func(t *testing.T) { runRetiredManagedSuccessor(t, factory) })
	t.Run("identity-collision-resume", func(t *testing.T) { runForeignGrantErasure(t, factory, kernel.ManagedResume) })
	t.Run("identity-collision-cancel", func(t *testing.T) { runForeignGrantErasure(t, factory, kernel.ManagedCancel) })
	t.Run("owner-and-claim", func(t *testing.T) { runManagedStoreLiveOwnerAndClaimCompetition(t, factory) })
	t.Run("parked-ancestor", func(t *testing.T) { runManagedStoreAncestorGate(t, factory) })
	t.Run("parked-gates", func(t *testing.T) { runManagedStoreParkedAuthorityGates(t, factory) })
	t.Run("selection-ack", func(t *testing.T) { runManagedStoreSelectionAndExactACK(t, factory) })
	t.Run("callback-entry", func(t *testing.T) { runManagedStoreCallbackEntry(t, factory) })
	t.Run("two-enable-claimants", func(t *testing.T) { runManagedStoreEnableCompetition(t, factory) })
	t.Run("resume-cancel-native-election", func(t *testing.T) { runManagedResumeCancelElection(t, factory) })
	t.Run("transaction-ack", func(t *testing.T) { runManagedStoreTransactionACK(t, factory) })
	t.Run("historical-closure", func(t *testing.T) { runManagedStoreFinishClosesHistoricalTransfer(t, factory) })
}

// These low-level tests use the existing internal Feature proof seam. They
// certify Store transactions, not the Agent's concrete lifecycle eligibility.
type managedStoreFixture struct {
	store           managedStorePorts
	backend         ManagedRecoveryStoreFixture
	runtime, scoped *kernel.Runtime
	source          kernel.Snapshot
	grant           kernel.ChildExecutionGrant
	lease           kernel.ExecutionLease
	point           kernel.ManagedSafePoint
	host            *managedStoreHost
}

// Force a real-time Feature timestamp with a non-microsecond remainder on all
// platforms. Store lease authority and backend AdvanceTo remain unchanged.
type managedStorePrecisionClock struct{}

func (managedStorePrecisionClock) Now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond).Add(123 * time.Nanosecond)
}

func managedStoreSetup(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) managedStoreFixture {
	t.Helper()
	backend := factory(t)
	s, ok := backend.Store.(managedStorePorts)
	if !ok || backend.Peer == nil || backend.AdvanceTo == nil || backend.Reopen == nil {
		t.Fatal("incomplete managed Store factory")
	}
	hostStore := &managedStoreHost{}
	rt, err := kernel.New(kernel.Dependencies{Store: s, ManagedRecoveryHost: hostStore, Clock: managedStorePrecisionClock{}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "owned-agent", Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, RequestID: "original-source", Goal: "original task", State: json.RawMessage(`{"original":true}`), ExecutionRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	l, err := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: v.Run.ID, OwnerID: "original", LeaseDuration: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := rt.WithExecution(l)
	if err != nil {
		t.Fatal(err)
	}
	dispatchID := "nativecmd_original-dispatch"
	scoped, g, err := admitBorrowed(t.Context(), bound, v, "start", dispatchID)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := s.ManagedRecoveryAuthority(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	host := kernel.ManagedHostBinding{KernelAuthority: authority, HostAuthority: kernel.ManagedAuthority{Namespace: strings.Repeat("a", 64), Generation: 1, Version: 1}, TurnID: "turn", InvocationID: "invocation", Attempt: 1, RunID: v.Run.ID, DispatchID: "nativecmd_original-dispatch", Phase: "start", Source: json.RawMessage(`{"frozen":"original"}`)}
	point := kernel.ManagedSafePoint{ID: "safe-1", Host: host, OriginalGrant: g, OriginalLease: l, Source: v, ModelInvocationID: "model-1", ModelRequestHash: strings.Repeat("b", 64), LifecycleVersion: kernel.ManagedLifecycleAgentObligations}
	point.LifecycleSource, err = json.Marshal(kernel.ManagedAgentObligations{Phase: "pending", SourceRevision: v.Run.Revision + 1, InvocationID: point.ModelInvocationID, RequestHash: point.ModelRequestHash, SamplingDigest: strings.Repeat("c", 64), Journal: json.RawMessage(`{"returned":"source"}`)})
	if err != nil {
		t.Fatal(err)
	}
	point.RecoveryID = kernel.ManagedRecoveryID(point.ID, host)
	source, err := scoped.ApplyManagedNativeTransition(t.Context(), v.Run.ID, v.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"pending":"model-1","dispatchAttempt":0}`), Events: []kernel.EventDraft{{Type: "agent.model_invocation.pending"}}}, point, featurecontrol.Seal(v.Run.ID, v.Run.Revision, "managed_intent_source", g.ID, v.State, point))
	if errors.Is(err, errManagedStoreACK) {
		source, err = rt.Load(t.Context(), v.Run.ID)
	}
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := s.LoadManagedRecovery(t.Context(), v.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, loadErr := rt.Load(t.Context(), v.Run.ID)
	if !transfer.Valid() || transfer.SafePoint.Source.EventHead != source.EventHead || kernel.ManagedDigest(transfer.SafePoint.Source) != kernel.ManagedDigest(source) || loadErr != nil || kernel.ManagedDigest(source) != kernel.ManagedDigest(persisted) {
		pointJSON, pointJSONErr := json.Marshal(transfer.SafePoint.Source)
		sourceJSON, sourceJSONErr := json.Marshal(source)
		persistedJSON, persistedJSONErr := json.Marshal(persisted)
		t.Fatalf("T0 did not bind exact committed aggregate: available=%t eventHeadEqual=%t sourceDigestEqual=%t returnedMatchesLoaded=%t loadErr=%v JSONErrors=[%v,%v,%v]\npointSource=%s\nreturnedSource=%s\nloadedSource=%s",
			transfer.Valid(), transfer.SafePoint.Source.EventHead == source.EventHead,
			kernel.ManagedDigest(transfer.SafePoint.Source) == kernel.ManagedDigest(source),
			loadErr == nil && kernel.ManagedDigest(source) == kernel.ManagedDigest(persisted), loadErr,
			pointJSONErr, sourceJSONErr, persistedJSONErr, pointJSON, sourceJSON, persistedJSON)
	}
	return managedStoreFixture{store: s, backend: backend, runtime: rt, scoped: scoped, source: source, grant: g, lease: l, point: transfer.SafePoint, host: hostStore}
}
func managedStoreSame(t *testing.T, f managedStoreFixture, before kernel.ManagedTransfer) {
	t.Helper()
	after, err := f.store.LoadManagedRecovery(t.Context(), f.source.Run.ID)
	if err != nil || kernel.ManagedDigest(after) != kernel.ManagedDigest(before) {
		t.Fatalf("transfer changed: %v", err)
	}
	source, err := f.runtime.Load(t.Context(), f.source.Run.ID)
	if err != nil || kernel.ManagedDigest(source) != kernel.ManagedDigest(f.source) {
		t.Fatalf("source changed: %v", err)
	}
	grant, err := f.store.LookupChildExecution(t.Context(), f.source.Run.ID)
	if err != nil || grant != f.grant {
		t.Fatalf("original grant changed: %v", err)
	}
}
func runManagedStoreLiveOwnerAndClaimCompetition(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	for _, mode := range []string{"live", "renew", "claim", "eventhead"} {
		t.Run(mode, func(t *testing.T) {
			f := managedStoreSetup(t, factory)
			before, err := f.store.LoadManagedRecovery(t.Context(), f.source.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "eventhead" {
				changed := f.source
				changed.EventHead++
				if kernel.ManagedSourceMatches(f.point, changed) {
					t.Fatal("journal head mismatch admitted")
				}
				return
			}
			f.host.binding = f.point.Host
			proof, err := f.runtime.AuthorizeManagedContinuation(t.Context(), before)
			if err != nil {
				t.Fatal(err)
			}
			admission := proof.OwnedAdmission(kernel.ExecutionAcquireRequest{RunID: f.source.Run.ID, OwnerID: "successor", LeaseDuration: time.Minute}, "successor-lease", "successor-grant")
			if mode == "renew" {
				f.backend.AdvanceTo(f.lease.ExpiresAt.Add(-time.Second))
				renewed, e := f.backend.Peer.(kernel.ExecutionStore).RenewExecution(t.Context(), kernel.ExecutionRenewRequest{Lease: f.lease, LeaseDuration: time.Minute})
				if e != nil {
					t.Fatal(e)
				}
				f.backend.AdvanceTo(f.lease.ExpiresAt.Add(100 * time.Millisecond))
				if !f.lease.ExpiresAt.Add(100 * time.Millisecond).Before(renewed.ExpiresAt) {
					t.Fatal("renewal setup invalid")
				}
			}
			if mode == "claim" {
				changed, e := f.scoped.Apply(t.Context(), f.source.Run.ID, f.source.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"claim":"original-owner"}`)})
				if e != nil {
					t.Fatal(e)
				}
				f.source = changed
				before, e = f.store.LoadManagedRecovery(t.Context(), f.source.Run.ID)
				if e != nil || !before.Valid() || kernel.ManagedSourceMatches(before.SafePoint, changed) {
					t.Fatalf("ordinary claim did not stale the source: %v", e)
				}
				f.backend.AdvanceTo(f.lease.ExpiresAt.Add(100 * time.Millisecond))
			}
			if _, e := f.backend.Peer.(managedStorePorts).BeginPhysicalExecution(t.Context(), admission); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("native admission %s: %v", mode, e)
			}
			managedStoreSame(t, f, before)
		})
	}
}
func runManagedStoreParkedAuthorityGates(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	f := managedStoreSetup(t, factory)
	before, e := f.store.LoadManagedRecovery(t.Context(), f.source.Run.ID)
	if e != nil {
		t.Fatal(e)
	}
	selection, candidate := managedStoreSelection(t, f, kernel.ManagedResume)
	if kernel.ManagedDigest(candidate) != kernel.ManagedDigest(before) {
		t.Fatal("preparation changed persisted authority")
	}
	managedStoreSame(t, f, before)
	enabled, e := enableManagedRecovery(t.Context(), f.store, managedEnableRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, OwnerID: "owner", LeaseID: "lease", GrantID: "successor", LeaseDuration: time.Minute, Selection: selection})
	if e != nil {
		t.Fatal(e)
	}
	oldLease, oldGrant := f.lease, f.grant
	f.grant, f.lease = enabled.SuccessorGrant, enabled.SuccessorLease
	check := func(name string, e error) {
		t.Helper()
		if !errors.Is(e, kernel.ErrExecutionConflict) && !errors.Is(e, kernel.ErrChildExecutionBusy) {
			t.Fatalf("%s: %v", name, e)
		}
		managedStoreSame(t, f, enabled)
	}
	_, e = f.backend.Peer.(kernel.ExecutionStore).RenewExecution(t.Context(), kernel.ExecutionRenewRequest{Lease: oldLease, LeaseDuration: time.Minute})
	check("old renew", e)
	check("old check", f.store.CheckExecution(t.Context(), f.source.Run.ID, &oldLease))
	check("old release", f.store.ReleaseExecution(t.Context(), oldLease))
	_, e = f.scoped.Apply(t.Context(), f.source.Run.ID, f.source.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: f.source.State})
	check("old apply", e)
	_, e = f.runtime.Cancel(t.Context(), f.source.Run.ID, f.source.Run.Revision, "cancel")
	check("control", e)
	_, e = f.store.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: f.source.Run.ID, OwnerID: "foreign", LeaseDuration: time.Minute})
	check("acquire", e)
	check("old finish", f.store.FinishChildExecution(t.Context(), oldGrant, kernel.ChildExecutionSettlement{}))
	check("quarantine", f.store.QuarantineExecution(t.Context(), kernel.ExecutionQuarantine{RunID: f.source.Run.ID, Revision: f.source.Run.Revision, Generation: 1, Reason: "unknown"}))
}

type managedStoreHost struct{ binding kernel.ManagedHostBinding }

func (h *managedStoreHost) ManagedRecoveryAuthority(context.Context) (kernel.ManagedAuthority, error) {
	return h.binding.HostAuthority, nil
}
func (h *managedStoreHost) VerifyManagedOriginalDispatch(_ context.Context, b kernel.ManagedHostBinding) error {
	if kernel.ManagedDigest(b) != kernel.ManagedDigest(h.binding) {
		return kernel.ErrExecutionConflict
	}
	return nil
}
func managedStoreSelection(t *testing.T, f managedStoreFixture, mode kernel.ManagedRecoveryMode) (kernel.ManagedAdmissionProof, kernel.ManagedTransfer) {
	t.Helper()
	f.backend.AdvanceTo(f.lease.ExpiresAt.Add(100 * time.Millisecond))
	parked, e := f.runtime.LoadManagedRecovery(t.Context(), f.source.Run.ID)
	if e != nil {
		t.Fatal(e)
	}
	transfer := parked
	f.host.binding = f.point.Host
	var proof kernel.ManagedAdmissionProof
	if mode == kernel.ManagedCancel {
		proof, e = f.runtime.AuthorizeManagedContinuation(t.Context(), parked, "original")
	} else {
		proof, e = f.runtime.AuthorizeManagedContinuation(t.Context(), parked)
	}
	if e != nil {
		t.Fatal(e)
	}
	return proof, transfer
}

type managedEnableRequest struct {
	RunID, SuspensionID, OwnerID, LeaseID, GrantID string
	LeaseDuration                                  time.Duration
	Selection                                      kernel.ManagedAdmissionProof
}

func enableManagedRecovery(ctx context.Context, s managedStorePorts, q managedEnableRequest) (kernel.ManagedTransfer, error) {
	p := q.Selection.OwnedAdmission(kernel.ExecutionAcquireRequest{RunID: q.RunID, OwnerID: q.OwnerID, LeaseDuration: q.LeaseDuration}, q.LeaseID, q.GrantID)
	v, e := s.BeginPhysicalExecution(ctx, p)
	if e != nil {
		return kernel.ManagedTransfer{}, e
	}
	t, e := s.LoadManagedRecoveryByID(ctx, q.SuspensionID)
	if e != nil || t.SuccessorGrant != v.Grant || t.SuccessorLease != v.Lease {
		return kernel.ManagedTransfer{}, errors.Join(kernel.ErrExecutionConflict, e)
	}
	return t, nil
}

// CancelManagedFixture submits the same native control as the Agent and
// reads the original history only after a successful commit.
func CancelManagedFixture(ctx context.Context, rt *kernel.Runtime, p kernel.ManagedAdmissionProof) (kernel.ManagedTransfer, error) {
	point := p.Point()
	if _, _, err := rt.ApplyControl(ctx, point.Host.RunID, point.Source.Run.Revision, p.CancellationGuard(), kernel.Mutation{}, nil); err != nil {
		return kernel.ManagedTransfer{}, err
	}
	return rt.LoadManagedRecoveryByID(ctx, point.ID)
}

func cancelManagedStoreFixture(ctx context.Context, store managedStorePorts, p kernel.ManagedAdmissionProof) (kernel.ManagedTransfer, error) {
	point := p.Point()
	if _, _, err := store.Commit(ctx, point.Host.RunID, point.Source.Run.Revision, kernel.StoreMutation{}, kernel.ControlCommit(p.CancellationGuard())); err != nil {
		return kernel.ManagedTransfer{}, err
	}
	return store.LoadManagedRecoveryByID(ctx, point.ID)
}

func runManagedStoreSelectionAndExactACK(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	for _, mode := range []kernel.ManagedRecoveryMode{kernel.ManagedResume, kernel.ManagedCancel} {
		t.Run(string(mode), func(t *testing.T) {
			f := managedStoreSetup(t, factory)
			proof, parked := managedStoreSelection(t, f, mode)
			enable := managedEnableRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, OwnerID: "owner", LeaseID: "lease", GrantID: "successor", LeaseDuration: time.Minute, Selection: proof}
			cancel := proof
			if mode == kernel.ManagedCancel {
				if _, e := enableManagedRecovery(t.Context(), f.store, enable); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("CANCEL authorized send: %v", e)
				}
				managedStoreSame(t, f, parked)
				parkedProof, e := f.runtime.LoadManagedRecovery(t.Context(), f.source.Run.ID)
				if e != nil {
					t.Fatal(e)
				}
				different, e := f.runtime.AuthorizeManagedContinuation(t.Context(), parkedProof, "different reason")
				if e != nil {
					t.Fatal(e)
				}
				// A logical native child can be created after authorization
				// but before CANCEL's Store transaction. It is not a
				// borrowed Grant and need not have entered a callback yet.
				peer, e := kernel.New(kernel.Dependencies{Store: f.backend.Peer})
				if e != nil {
					t.Fatal(e)
				}
				req := kernel.CreateRequest{ID: "competing-logical-child", LogicalParentRunID: f.source.Run.ID, Kind: "agent", Actor: f.source.Run.Actor, Thread: f.source.Run.Thread, Goal: "competing child", State: json.RawMessage("{}")}
				child, e := peer.Create(t.Context(), req)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = cancelManagedStoreFixture(t.Context(), f.store, cancel); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("CANCEL bypassed native descendant: %v", e)
				}
				managedStoreSame(t, f, parked)
				if observed, e := peer.Load(t.Context(), child.Run.ID); e != nil || kernel.ManagedDigest(observed) != kernel.ManagedDigest(child) {
					t.Fatal("rejected CANCEL changed child", e)
				}
				if _, e = peer.Cancel(t.Context(), child.Run.ID, child.Run.Revision, "settle original child"); e != nil {
					t.Fatal(e)
				}
				result, e := cancelManagedStoreFixture(t.Context(), f.store, cancel)
				if e != nil || !result.Resolved() || result.Selection.Mode != kernel.ManagedCancel {
					t.Fatalf("cancel: %v", e)
				}
				req.ID = "late-logical-child"
				if _, e = peer.Create(t.Context(), req); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("child crossed committed ancestor CANCEL: %v", e)
				}
				again, e := cancelManagedStoreFixture(t.Context(), f.store, cancel)
				if e != nil || kernel.ManagedDigest(again) != kernel.ManagedDigest(result) {
					t.Fatalf("cancel ACK: %v", e)
				}
				foreignCancel := cancel
				foreignCancel = different
				if _, e = cancelManagedStoreFixture(t.Context(), f.store, foreignCancel); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("different cancellation overwrote selection: %v", e)
				}
				if kernel.ManagedDigest(result.Terminal.State) != kernel.ManagedDigest(f.source.State) || result.Terminal.EventHead != f.source.EventHead+1 {
					t.Fatal("cancel lost receipt or journal source")
				}
				managedStoreReopenEqual(t, f, result)
				return
			}
			if _, e := cancelManagedStoreFixture(t.Context(), f.store, cancel); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("RESUME authorized cancel: %v", e)
			}
			managedStoreSame(t, f, parked)
			result, e := enableManagedRecovery(t.Context(), f.store, enable)
			if e != nil {
				t.Fatal(e)
			}
			if _, e := enableManagedRecovery(t.Context(), f.store, enable); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("duplicate native admission: %v", e)
			}
			foreign := enable
			foreign.LeaseID = "foreign-lease"
			if _, e = enableManagedRecovery(t.Context(), f.store, foreign); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("foreign claimant got authority: %v", e)
			}
			historical, e := f.store.LoadManagedRecoveryByID(t.Context(), f.point.ID)
			if e != nil || kernel.ManagedDigest(historical) != kernel.ManagedDigest(result) {
				t.Fatal("historical transfer changed")
			}
			managedStoreReopenEqual(t, f, result)
		})
	}
}

// The exact ENABLED successor consumes its entry qualification in the
// native entry transaction. Reject a foreign or repeated attempt, including
// a call whose initial successful ACK was discarded by the caller.
func runManagedStoreCallbackEntry(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	f := managedStoreSetup(t, factory)
	selection, _ := managedStoreSelection(t, f, kernel.ManagedResume)
	scoped, admitted, e := f.runtime.AdmitManagedContinuation(t.Context(), selection, "entry-owner", 3*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	g, l := admitted.Grant, admitted.Lease
	timed, e := f.store.LoadManagedRecoveryByID(t.Context(), f.point.ID)
	if e != nil || timed.SuccessorGrant != g || timed.SuccessorLease != l {
		t.Fatalf("lost original admission: %v", e)
	}
	port := f.backend.Peer.(kernel.PhysicalExecutionStore)
	f.grant = g
	foreign := f.source
	foreign.EventHead++
	if _, e = port.CheckOwnedPhysicalEntry(t.Context(), g, l, foreign); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("changed source admitted: %v", e)
	}
	managedStoreSame(t, f, timed)

	if _, e = scoped.Apply(t.Context(), f.source.Run.ID, f.source.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"unsafe":"before-entry"}`)}); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("enabled grant bypassed entry: %v", e)
	}
	managedStoreSame(t, f, timed)
	// Two handles presenting the exact same original bearer still obtain
	// at most one callback-entry ACK. No second in-memory owner flag can
	// compensate for an unconsumed Store qualification.
	type entryResult struct {
		window kernel.OwnedPhysicalEntryWindow
		err    error
	}
	start := make(chan struct{})
	results := make(chan entryResult, 2)
	for _, contender := range []kernel.PhysicalExecutionStore{f.store, port} {
		go func() {
			<-start
			w, e := contender.CheckOwnedPhysicalEntry(t.Context(), g, l, f.source)
			results <- entryResult{w, e}
		}()
	}
	close(start)
	winners := 0
	for range 2 {
		result := <-results
		if result.err == nil && result.window.LeaseRemaining > 0 {
			winners++
		} else if !errors.Is(result.err, kernel.ErrExecutionConflict) && (f.backend.RetryableContention == nil || !f.backend.RetryableContention(result.err)) {
			t.Fatalf("unexpected entry result: %+v %v", result.window, result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("entry winners=%d", winners)
	}
	left, e := f.store.LoadManagedRecovery(t.Context(), f.source.Run.ID)
	if e != nil || kernel.ManagedDigest(left) != kernel.ManagedDigest(timed) {
		t.Fatalf("native entry rewrote recovery evidence: %v %+v", e, left)
	}
	// No replay permission, even though the exact same source and lease
	// remain current. This includes loss of the original entry ACK.
	if _, e = port.CheckOwnedPhysicalEntry(t.Context(), g, l, f.source); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("entry ACK replay granted another callback: %v", e)
	}
	f.backend.AdvanceTo(l.ExpiresAt.Add(100 * time.Millisecond))
	if _, e = port.CheckOwnedPhysicalEntry(t.Context(), g, l, f.source); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("expired entered successor admitted: %v", e)
	}
	proof, e := f.runtime.AuthorizeManagedContinuation(t.Context(), left)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.runtime.AdmitManagedContinuation(t.Context(), proof, "another-owner", time.Minute); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("entered successor re-admitted: %v", e)
	}
	managedStoreSame(t, f, left)
	if _, e = f.store.LookupPhysicalCallbackReturn(t.Context(), g); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("entry fabricated callback return: %v", e)
	}
	if _, e = f.store.LoadOwnedRetirement(t.Context(), g.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("entry fabricated retirement: %v", e)
	}
}

func runManagedStoreFinishClosesHistoricalTransfer(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	f := managedStoreSetup(t, factory)
	selection, _ := managedStoreSelection(t, f, kernel.ManagedResume)
	scoped, admitted, e := f.runtime.AdmitManagedContinuation(t.Context(), selection, "owner", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	g := admitted.Grant
	enabled, e := f.store.LoadManagedRecoveryByID(t.Context(), f.point.ID)
	if e != nil || enabled.SuccessorGrant != g || enabled.SuccessorLease != admitted.Lease {
		t.Fatalf("lost original admission: %v", e)
	}
	if _, e = scoped.CheckOwnedPhysicalEntry(t.Context(), f.source); e != nil {
		t.Fatal(e)
	}
	mutation := kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"pending":"model-2","originalReceipt":"retained"}`), Events: []kernel.EventDraft{{Type: "agent.model_invocation.pending"}}}
	// The successor owns effects within the original transfer. Even with
	// the internal proof seam it cannot mint a second original dispatch.
	forged := f.point
	forged.ID = "forged-safe"
	forged.RecoveryID = kernel.ManagedRecoveryID(forged.ID, forged.Host)
	forged.OriginalGrant, forged.OriginalLease = g, enabled.SuccessorLease
	if _, e = scoped.ApplyManagedNativeTransition(t.Context(), f.source.Run.ID, f.source.Run.Revision, mutation, forged, featurecontrol.Seal(f.source.Run.ID, f.source.Run.Revision, "managed_intent_source", g.ID, f.source.State, forged)); !errors.Is(e, kernel.ErrInvalidInput) && !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("successor minted another original safe point: %v", e)
	}
	next, e := scoped.Apply(t.Context(), f.source.Run.ID, f.source.Run.Revision, mutation)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.store.FinishChildExecution(t.Context(), g, kernel.ChildExecutionSettlement{}); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("unmarked finish: %v", e)
	}
	retired := retirementFixture{backend: f.backend, rt: f.runtime, bound: scoped, store: f.store, source: next, lease: enabled.SuccessorLease, grant: g}
	retirementCallback(t, retired, true, true)
	before, e := f.store.LoadManagedRecoveryByID(t.Context(), f.point.ID)
	if e != nil || before.Resolved() || before.Selection.Mode != kernel.ManagedResume || before.Terminal != nil {
		t.Fatalf("mark prematurely closed transfer: %v", e)
	}
	receipt := retirementFinish(t, retired)
	original, e := f.store.LoadManagedRecoveryByID(t.Context(), f.point.ID)
	if e != nil || !original.Resolved() || original.Selection.Mode != kernel.ManagedResume || original.Terminal == nil || kernel.ManagedDigest(*original.Terminal) != kernel.ManagedDigest(next) || !receipt.MatchesManagedClosure(original, next) {
		t.Fatalf("Finish did not atomically close exact transfer: %v", e)
	}

	// An actual later independently admitted dispatch can publish its own
	// point without replacing or reactivating the earlier closure.
	lease, e := f.runtime.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: next.Run.ID, OwnerID: "later", LeaseDuration: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	bound, e := f.runtime.WithExecution(lease)
	if e != nil {
		t.Fatal(e)
	}
	dispatchID := "nativecmd_later-dispatch"
	scoped, later, e := admitBorrowed(t.Context(), bound, next, "resume", dispatchID)
	if e != nil {
		t.Fatal(e)
	}
	second := f.point
	second.ID = "safe-2"
	second.Host.Phase, second.Host.DispatchID = "resume", dispatchID
	second.RecoveryID = kernel.ManagedRecoveryID(second.ID, second.Host)
	second.OriginalGrant, second.OriginalLease = later, lease
	second.ModelInvocationID = "model-2"
	obligations, ok := f.point.AgentObligations()
	if !ok {
		t.Fatal("missing original obligations")
	}
	obligations.SourceRevision, obligations.InvocationID = next.Run.Revision+1, second.ModelInvocationID
	second.LifecycleSource, e = json.Marshal(obligations)
	if e != nil {
		t.Fatal(e)
	}
	fresh, e := scoped.ApplyManagedNativeTransition(t.Context(), next.Run.ID, next.Run.Revision, mutation, second, featurecontrol.Seal(next.Run.ID, next.Run.Revision, "managed_intent_source", later.ID, next.State, second))
	if e != nil {
		t.Fatal(e)
	}
	current, e := f.store.LoadManagedRecovery(t.Context(), next.Run.ID)
	if e != nil || current.SafePoint.ID != second.ID || !current.Valid() || !kernel.ManagedSourceMatches(current.SafePoint, fresh) {
		t.Fatalf("later independent dispatch did not own its point: %v", e)
	}
	historical, e := f.store.LoadManagedRecoveryByID(t.Context(), f.point.ID)
	if e != nil || kernel.ManagedDigest(historical) != kernel.ManagedDigest(original) {
		t.Fatalf("new dispatch rewrote historical closure: %v", e)
	}
	if _, e = f.runtime.AuthorizeManagedContinuation(t.Context(), original); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("historical point parked current dispatch: %v", e)
	}
	persisted, e := f.runtime.Load(t.Context(), fresh.Run.ID)
	if e != nil || kernel.ManagedDigest(persisted) != kernel.ManagedDigest(fresh) {
		t.Fatal("historical closure changed current feature state")
	}
}

func managedStoreReopenEqual(t *testing.T, f managedStoreFixture, want kernel.ManagedTransfer) {
	t.Helper()
	reopened := f.backend.Reopen()
	store, ok := reopened.(kernel.ManagedRecoveryStore)
	if !ok {
		t.Fatal("reopen lost managed capability")
	}
	got, e := store.LoadManagedRecoveryByID(t.Context(), want.SafePoint.ID)
	if e != nil || kernel.ManagedDigest(got) != kernel.ManagedDigest(want) {
		t.Fatalf("reopen transfer/source/mode changed: %v", e)
	}
	actual, e := reopened.Load(t.Context(), f.source.Run.ID)
	if e != nil {
		t.Fatal(e)
	}
	expected := f.source
	if want.Terminal != nil {
		expected = *want.Terminal
	}
	if kernel.ManagedDigest(actual) != kernel.ManagedDigest(expected) {
		t.Fatal("reopen full aggregate changed")
	}
	authority, e := store.ManagedRecoveryAuthority(t.Context())
	if e != nil || authority != want.SafePoint.Host.KernelAuthority {
		t.Fatal("reopen authority changed")
	}
}

var errManagedStoreACK = errors.New("managed-store-commit-ack-lost")

type managedStoreACK struct {
	managedStorePorts
	mode string
	hits int
}

func (s *managedStoreACK) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Lease != nil {
		value, _, e := s.managedStorePorts.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
		if e == nil && s.mode == "t0" && commitMutation.ManagedSafePoint.Present() {
			s.hits++
			return kernel.Snapshot{}, false, errManagedStoreACK
		}
		return value, false, e
	}
	if commitAuth.Control != nil {
		g := *commitAuth.Control

		v, revoke, e := s.managedStorePorts.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
		if _, managed, _ := g.ManagedCancellation(commitID, commitRev, commitMutation); e == nil && s.mode == "cancel" && managed {
			s.hits++
			return kernel.Snapshot{}, false, errManagedStoreACK
		}
		return v, revoke, e
	}
	return s.managedStorePorts.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
}

func (s *managedStoreACK) BeginPhysicalExecution(ctx context.Context, p kernel.PhysicalAdmission) (kernel.PhysicalAdmissionResult, error) {
	v, e := s.managedStorePorts.BeginPhysicalExecution(ctx, p)
	if e == nil && s.mode == "enable" && p.IsManagedRecovery() {
		s.hits++
		return v, p.CommitOutcome(errManagedStoreACK)
	}
	return v, e
}

func runManagedStoreTransactionACK(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	for _, mode := range []string{"t0", "enable", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			var fault *managedStoreACK
			f := managedStoreSetup(t, func(t *testing.T) ManagedRecoveryStoreFixture {
				backend := factory(t)
				fault = &managedStoreACK{managedStorePorts: backend.Store.(managedStorePorts)}
				if mode == "t0" {
					fault.mode = mode
				}
				backend.Store = fault
				return backend
			})
			peer := f.backend.Peer.(managedStorePorts)
			if mode == "t0" {
				if fault.hits != 1 {
					t.Fatal("T0 fault missed actual native pending commit")
				}
				saved, e := peer.LoadManagedRecovery(t.Context(), f.source.Run.ID)
				if e != nil {
					t.Fatal(e)
				}
				managedStoreReopenEqual(t, f, saved)
				return
			}
			selectionMode := kernel.ManagedResume
			if mode == "cancel" {
				selectionMode = kernel.ManagedCancel
			}
			proof, _ := managedStoreSelection(t, f, selectionMode)
			fault.mode = mode
			var retry kernel.ManagedTransfer
			var e error
			if mode == "enable" {
				_, admitted, admitErr := f.runtime.AdmitManagedContinuation(t.Context(), proof, "owner", time.Minute)
				if admitErr != nil {
					t.Fatalf("native admission ACK recovery: %v", admitErr)
				}
				retry, e = peer.LoadManagedRecovery(t.Context(), f.source.Run.ID)
				if e != nil || retry.SuccessorGrant != admitted.Grant {
					t.Fatalf("native commit not observed: %v", e)
				}

			} else {
				q := proof
				if _, e = cancelManagedStoreFixture(t.Context(), fault, q); !errors.Is(e, errManagedStoreACK) {
					t.Fatalf("CANCEL fault: %v", e)
				}
				retry, e = cancelManagedStoreFixture(t.Context(), peer, q)
			}
			if e != nil || fault.hits != 1 {
				t.Fatalf("exact original recovery failed/hit=%d: %v", fault.hits, e)
			}
			managedStoreReopenEqual(t, f, retry)
		})
	}
}

func runManagedStoreEnableCompetition(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	f := managedStoreSetup(t, factory)
	proof, _ := managedStoreSelection(t, f, kernel.ManagedResume)
	base := managedEnableRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, LeaseDuration: time.Minute, Selection: proof}
	a, b := base, base

	a.OwnerID = "owner-a"
	a.LeaseID = "lease-a"
	a.GrantID = "grant-a"

	b.OwnerID = "owner-b"
	b.LeaseID = "lease-b"
	b.GrantID = "grant-b"
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	type answer struct {
		transfer kernel.ManagedTransfer
		err      error
		request  managedEnableRequest
	}
	results := make(chan answer, 2)
	attempt := func(store managedStorePorts, q managedEnableRequest) {
		ready <- struct{}{}
		<-start
		v, e := enableManagedRecovery(t.Context(), store, q)
		results <- answer{v, e, q}
	}
	go attempt(f.store, a)
	go attempt(f.backend.Peer.(managedStorePorts), b)
	<-ready
	<-ready
	close(start)
	one, two := <-results, <-results
	if one.err != nil {
		one, two = two, one
	}
	if one.err != nil || two.transfer.SuccessorLease.LeaseID != "" {
		t.Fatalf("ENABLE competitors: winner=%v loser=%v", one.err, two.err)
	}
	if f.backend.RetryableContention != nil && f.backend.RetryableContention(two.err) {
		// The winner has returned from COMMIT. The busy contender never
		// acquired authority. Re-read its original selection through the
		// actual same Store transaction before requiring business conflict.
		loser, err := enableManagedRecovery(t.Context(), f.store, two.request)
		if loser.SuccessorLease.LeaseID != "" || !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("busy loser acquired authority after winner: %+v %v", loser, err)
		}
	} else if !errors.Is(two.err, kernel.ErrExecutionConflict) {
		t.Fatalf("ENABLE competition returned unclassified loser: %v", two.err)
	}
	if one.transfer.SuccessorLease.Epoch != f.lease.Epoch+1 || !one.transfer.Valid() {
		t.Fatal("competition advanced more than one epoch/activation")
	}
	original, e := f.runtime.Load(t.Context(), f.source.Run.ID)
	if e != nil || kernel.ManagedDigest(original) != kernel.ManagedDigest(f.source) {
		t.Fatal("ENABLE race changed source")
	}
	managedStoreReopenEqual(t, f, one.transfer)
}

// Both opposing commands hold an authenticated read of the same original
// candidate. Neither preparation can elect a mode. The native commit wins.
func runManagedResumeCancelElection(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	for _, winner := range []kernel.ManagedRecoveryMode{kernel.ManagedResume, kernel.ManagedCancel} {
		t.Run(string(winner), func(t *testing.T) {
			f := managedStoreSetup(t, factory)
			resume, source := managedStoreSelection(t, f, kernel.ManagedResume)
			cancel, other := managedStoreSelection(t, f, kernel.ManagedCancel)
			if kernel.ManagedDigest(source) != kernel.ManagedDigest(other) {
				t.Fatal("Host read elected authority")
			}
			managedStoreSame(t, f, source)
			q := managedEnableRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, OwnerID: "owner", LeaseID: "lease", GrantID: "successor", LeaseDuration: time.Minute, Selection: resume}
			c := cancel
			var result kernel.ManagedTransfer
			var e error
			peer := f.backend.Peer.(managedStorePorts)
			if winner == kernel.ManagedResume {
				result, e = enableManagedRecovery(t.Context(), f.store, q)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = cancelManagedStoreFixture(t.Context(), peer, c); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("losing cancel mutated original: %v", e)
				}
				if result.Resolved() || result.Selection.Mode != kernel.ManagedResume || result.SuccessorLease.Epoch != f.lease.Epoch+1 {
					t.Fatal("invalid resume winner")
				}
				f.grant = result.SuccessorGrant
				managedStoreSame(t, f, result)
			} else {
				result, e = cancelManagedStoreFixture(t.Context(), f.store, c)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = enableManagedRecovery(t.Context(), peer, q); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("losing resume acquired authority: %v", e)
				}
				if !result.Resolved() || result.Selection.Mode != kernel.ManagedCancel || result.Terminal == nil {
					t.Fatal("invalid cancel winner")
				}
				if _, e = f.store.LookupChildExecution(t.Context(), f.source.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatal("cancel retained grant", e)
				}
			}
			managedStoreReopenEqual(t, f, result)
		})
	}
}

func runManagedStoreAncestorGate(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	for _, winner := range []string{"admission", "child"} {
		t.Run(winner, func(t *testing.T) {
			f := managedStoreSetup(t, factory)
			child, e := f.runtime.Create(t.Context(), kernel.CreateRequest{ID: "managed-child", Kind: "agent", Goal: "original managed child", Actor: f.source.Run.Actor, Thread: f.source.Run.Thread, ParentRunID: f.source.Run.ID, State: json.RawMessage(`{"child":"original"}`)})
			if e != nil {
				t.Fatal(e)
			}
			grant := kernel.ChildExecutionGrant{ID: "child-grant", ChildRunID: child.Run.ID, ExpectedRevision: child.Run.Revision}
			peer := f.backend.Peer.(managedStorePorts)
			childRuntime, e := kernel.New(kernel.Dependencies{Store: peer, IDs: fixedGrantIDs{grant.ID}})
			if e != nil {
				t.Fatal(e)
			}
			selection, candidate := managedStoreSelection(t, f, kernel.ManagedResume)
			managedStoreSame(t, f, candidate)
			q := managedEnableRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, OwnerID: "owner", LeaseID: "lease", GrantID: "successor", LeaseDuration: time.Minute, Selection: selection}
			if winner == "child" {
				if _, e = AdmitChildFixture(t.Context(), childRuntime, child.Run.ID, child.Run.Revision); e != nil {
					t.Fatal(e)
				}
				if _, e = enableManagedRecovery(t.Context(), f.store, q); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("admission discarded descendant authority: %v", e)
				}
				if got, e := peer.LookupChildExecution(t.Context(), child.Run.ID); e != nil || got != grant {
					t.Fatal("descendant index lost", e)
				}
				managedStoreSame(t, f, candidate)
			} else {
				admitted, e := enableManagedRecovery(t.Context(), f.store, q)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = AdmitChildFixture(t.Context(), childRuntime, child.Run.ID, child.Run.Revision); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("child passed admitted ancestor: %v", e)
				}
				if _, e = peer.LookupChildExecution(t.Context(), child.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatal("rejected child acquired index", e)
				}
				f.grant = admitted.SuccessorGrant
				managedStoreSame(t, f, admitted)
			}
			actual, e := peer.Load(t.Context(), child.Run.ID)
			if e != nil || kernel.ManagedDigest(actual) != kernel.ManagedDigest(child) {
				t.Fatal("child aggregate mutated")
			}
		})
	}
}

// Independent audit: a fault-injected ID source, never seeded SQL rows.
type fixedGrantIDs struct{ grant string }

func (s fixedGrantIDs) NewID(prefix string) (string, error) {
	if prefix == "ownedgrant" || prefix == "childgrant" {
		return s.grant, nil
	}
	return prefix + "-identity-fixture", nil
}

func runForeignGrantErasure(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture, mode kernel.ManagedRecoveryMode) {
	f := managedStoreSetup(t, factory)
	selection, parked := managedStoreSelection(t, f, mode)
	ctx := t.Context()
	rt, e := kernel.New(kernel.Dependencies{Store: f.backend.Peer, IDs: fixedGrantIDs{f.grant.ID}})
	if e != nil {
		t.Fatal(e)
	}
	v, e := rt.Create(ctx, kernel.CreateRequest{ID: "foreign-root", Kind: "agent", Actor: kernel.ActorRef{TenantID: "foreign-tenant", ActorID: "foreign-actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "foreign-thread"}, Goal: "foreign physical callback", State: []byte(`{}`), ExecutionRequired: true})
	if e != nil {
		t.Fatal(e)
	}
	l, e := rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: v.Run.ID, OwnerID: "foreign-worker", LeaseDuration: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	b, e := rt.WithExecution(l)
	if e != nil {
		t.Fatal(e)
	}
	_, other, e := admitBorrowed(ctx, b, v, "start", "")
	if !errors.Is(e, kernel.ErrInvalidInput) || other != (kernel.ChildExecutionGrant{}) {
		t.Fatalf("foreign deterministic dispatch identity admitted: %+v %v", other, e)
	}
	if g, e := rt.LookupChildExecution(ctx, f.source.Run.ID); e != nil || g != f.grant {
		t.Fatalf("collision changed original grant: %+v %v", g, e)
	}
	if _, e = rt.LookupChildExecution(ctx, v.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("rejected admission created foreign fence: %v", e)
	}
	switch mode {
	case kernel.ManagedResume:
		// Also exercise the Store's actual transfer identity gate, not
		// only the native ID namespace check at ordinary admission.
		q := managedEnableRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, OwnerID: "owner", LeaseID: "lease", GrantID: f.grant.ID, LeaseDuration: time.Minute, Selection: selection}
		if _, err := enableManagedRecovery(ctx, f.store, q); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("managed successor reused original dispatch Grant: %v", err)
		}
		managedStoreSame(t, f, parked)
		q.GrantID = "identity-valid-successor"
		out, err := enableManagedRecovery(ctx, f.store, q)
		if err != nil || out.Resolved() || out.Selection.Mode != kernel.ManagedResume {
			t.Fatalf("valid transfer failed: %v", err)
		}
	case kernel.ManagedCancel:
		out, err := cancelManagedStoreFixture(ctx, f.store, selection)
		if err != nil || !out.Resolved() || out.Selection.Mode != kernel.ManagedCancel {
			t.Fatalf("valid cancellation failed: %v", err)
		}
	}
	// Neither successful transfer nor cancellation releases the original ID.
	if _, _, e = admitBorrowed(ctx, b, v, "start", ""); !errors.Is(e, kernel.ErrInvalidInput) {
		t.Fatalf("managed consumption released original identity: %v", e)
	}
	if _, e = rt.LookupChildExecution(ctx, v.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("managed transfer changed unrelated Run fence: %v", e)
	}
	if current, e := rt.Load(ctx, v.Run.ID); e != nil || kernel.ManagedDigest(current) != kernel.ManagedDigest(v) {
		t.Fatalf("managed transfer changed unrelated Run: %v", e)
	}
}

func runRetiredManagedSuccessor(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	b := factory(t)
	reuse := func(*testing.T) ManagedRecoveryStoreFixture { return b }
	r := retirementSetup(t, reuse)
	retirementCallback(t, r, true, true)
	retirementFinish(t, r)
	f := managedStoreSetup(t, reuse)
	p, parked := managedStoreSelection(t, f, kernel.ManagedResume)
	enabled, e := enableManagedRecovery(t.Context(), f.store, managedEnableRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, OwnerID: "new-owner", LeaseID: "new-lease", GrantID: r.grant.ID, LeaseDuration: time.Minute, Selection: p})
	if !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("Managed Enable recreated retired identity: %+v %v", enabled, e)
	}
	managedStoreSame(t, f, parked)
}
