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
}
type managedStorePorts interface {
	kernel.Store
	kernel.ExecutionStore
	kernel.ControlStore
	kernel.ChildExecutionStore
	kernel.OwnedPhysicalExecutionStore
	kernel.OwnedRetirementStore
	kernel.ExecutionQuarantineStore
	kernel.ManagedRecoveryStore
}

func RunManagedRecoveryStoreSuite(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	t.Helper()
	t.Run("owner-park-competition", func(t *testing.T) { runManagedStoreOwnerParkCompetition(t, factory) })
	t.Run("owner-and-leave", func(t *testing.T) { runManagedStoreLiveOwnerAndLeaveCompetition(t, factory) })
	t.Run("parked-ancestor", func(t *testing.T) { runManagedStoreAncestorGate(t, factory) })
	t.Run("parked-gates", func(t *testing.T) { runManagedStoreParkedAuthorityGates(t, factory) })
	t.Run("selection-ack", func(t *testing.T) { runManagedStoreSelectionAndExactACK(t, factory) })
	t.Run("two-enable-claimants", func(t *testing.T) { runManagedStoreEnableCompetition(t, factory) })
	t.Run("transaction-ack", func(t *testing.T) { runManagedStoreTransactionACK(t, factory) })
	t.Run("historical-closure", func(t *testing.T) { runManagedStoreT4ClosesHistoricalTransfer(t, factory) })
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
	g, err := bound.BeginOwnedPhysicalExecution(t.Context(), v.Run.ID, v.Run.Revision, featurecontrol.Seal(v.Run.ID, v.Run.Revision, "agent_owned_physical_admission", l.LeaseID, v.State, v.Checkpoint), "start")
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := bound.WithPhysicalGrant(t.Context(), g, featurecontrol.Seal(v.Run.ID, v.Run.Revision, "agent_physical_owner", g.ID, v.State, v.Checkpoint))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := s.ManagedRecoveryAuthority(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	host := kernel.ManagedHostBinding{KernelAuthority: authority, HostAuthority: kernel.ManagedAuthority{Namespace: strings.Repeat("a", 64), Generation: 1, Version: 1}, TurnID: "turn", InvocationID: "invocation", Attempt: 1, RunID: v.Run.ID, DispatchID: "original-dispatch", Phase: "start", Source: json.RawMessage(`{"frozen":"original"}`)}
	point := kernel.ManagedSafePoint{ID: "safe-1", Host: host, OriginalGrant: g, OriginalLease: l, Source: v, ModelInvocationID: "model-1", ModelRequestHash: strings.Repeat("b", 64), LifecycleVersion: "managed-context-v1", LifecycleSource: json.RawMessage(`{"returned":"source"}`)}
	point.RecoveryID = kernel.ManagedRecoveryID(point.ID, host)
	source, err := scoped.ApplyManagedModelIntent(t.Context(), v.Run.ID, v.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"pending":"model-1","dispatchAttempt":0}`), Events: []kernel.EventDraft{{Type: "agent.model_invocation.pending"}}}, point, featurecontrol.Seal(v.Run.ID, v.Run.Revision, "managed_intent_source", g.ID, v.State, point))
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
	if transfer.State != kernel.ManagedAvailable || transfer.SafePoint.Source.EventHead != source.EventHead || kernel.ManagedDigest(transfer.SafePoint.Source) != kernel.ManagedDigest(source) || loadErr != nil || kernel.ManagedDigest(source) != kernel.ManagedDigest(persisted) {
		pointJSON, pointJSONErr := json.Marshal(transfer.SafePoint.Source)
		sourceJSON, sourceJSONErr := json.Marshal(source)
		persistedJSON, persistedJSONErr := json.Marshal(persisted)
		t.Fatalf("T0 did not bind exact committed aggregate: available=%t eventHeadEqual=%t sourceDigestEqual=%t returnedMatchesLoaded=%t loadErr=%v JSONErrors=[%v,%v,%v]\npointSource=%s\nreturnedSource=%s\nloadedSource=%s",
			transfer.State == kernel.ManagedAvailable, transfer.SafePoint.Source.EventHead == source.EventHead,
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
func runManagedStoreLiveOwnerAndLeaveCompetition(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	for _, mode := range []string{"live", "renew", "leave", "claim", "eventhead"} {
		t.Run(mode, func(t *testing.T) {
			f := managedStoreSetup(t, factory)
			before, err := f.store.LoadManagedRecovery(t.Context(), f.source.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			q := kernel.ManagedParkRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, ExpectedRevision: f.source.Run.Revision}
			if mode == "eventhead" {
				changed := f.source
				changed.EventHead++
				if kernel.ManagedSourceMatches(f.point, changed) {
					t.Fatal("journal head mismatch admitted")
				}
				return
			}
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
			if mode == "leave" {
				left, e := f.store.LeaveManagedSafePoint(t.Context(), kernel.ManagedLeaveRequest{RunID: q.RunID, SuspensionID: q.SuspensionID, ExpectedRevision: q.ExpectedRevision, ExpectedTransferRevision: before.Revision, Generation: before.Generation, Lease: f.lease})
				if e != nil || left.State != kernel.ManagedInvalidated {
					t.Fatalf("Leave: %v", e)
				}
				before = left
				f.backend.AdvanceTo(f.lease.ExpiresAt.Add(100 * time.Millisecond))
			}
			if mode == "claim" {
				changed, e := f.scoped.Apply(t.Context(), f.source.Run.ID, f.source.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"claim":"original-owner"}`)})
				if e != nil {
					t.Fatal(e)
				}
				f.source = changed
				q.ExpectedRevision = changed.Run.Revision
				before, e = f.store.LoadManagedRecovery(t.Context(), f.source.Run.ID)
				if e != nil || before.State != kernel.ManagedInvalidated {
					t.Fatalf("ordinary claim did not invalidate: %v", e)
				}
				f.backend.AdvanceTo(f.lease.ExpiresAt.Add(100 * time.Millisecond))
			}
			if _, e := f.backend.Peer.(kernel.ManagedRecoveryStore).ParkManagedRecovery(t.Context(), q); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("PARK %s: %v", mode, e)
			}
			managedStoreSame(t, f, before)
		})
	}
}
func runManagedStoreParkedAuthorityGates(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	f := managedStoreSetup(t, factory)
	f.backend.AdvanceTo(f.lease.ExpiresAt.Add(100 * time.Millisecond))
	parked, e := f.store.ParkManagedRecovery(t.Context(), kernel.ManagedParkRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, ExpectedRevision: f.source.Run.Revision})
	if e != nil {
		t.Fatal(e)
	}
	check := func(name string, e error) {
		t.Helper()
		if !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("%s: %v", name, e)
		}
		managedStoreSame(t, f, parked)
	}
	_, e = f.backend.Peer.(kernel.ExecutionStore).RenewExecution(t.Context(), kernel.ExecutionRenewRequest{Lease: f.lease, LeaseDuration: time.Minute})
	check("renew", e)
	check("check", f.store.CheckExecution(t.Context(), f.source.Run.ID, &f.lease))
	check("release", f.store.ReleaseExecution(t.Context(), f.lease))
	_, e = f.scoped.Apply(t.Context(), f.source.Run.ID, f.source.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: f.source.State})
	check("apply", e)
	_, e = f.runtime.Cancel(t.Context(), f.source.Run.ID, f.source.Run.Revision, "cancel")
	check("control", e)
	_, e = f.store.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: f.source.Run.ID, OwnerID: "foreign", LeaseDuration: time.Minute})
	if !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("acquire: %v", e)
	}
	managedStoreSame(t, f, parked)
	check("finish", f.store.FinishChildExecution(t.Context(), f.grant))
	check("quarantine", f.store.QuarantineExecution(t.Context(), kernel.ExecutionQuarantine{RunID: f.source.Run.ID, Revision: f.source.Run.Revision, Generation: 1, Reason: "unknown"}))
}

type managedStoreHost struct{ reservation kernel.ManagedHostReservation }

func (h *managedStoreHost) ManagedRecoveryAuthority(context.Context) (kernel.ManagedAuthority, error) {
	return h.reservation.Binding.HostAuthority, nil
}
func (h *managedStoreHost) ReadManagedOriginalDispatch(_ context.Context, b kernel.ManagedHostBinding) (kernel.ManagedHostBinding, error) {
	if kernel.ManagedDigest(b) != kernel.ManagedDigest(h.reservation.Binding) {
		return kernel.ManagedHostBinding{}, kernel.ErrExecutionConflict
	}
	return b, nil
}
func (h *managedStoreHost) ReserveManagedRecovery(_ context.Context, p kernel.ManagedParkedProof) (kernel.ManagedHostReservation, error) {
	if !p.Matches(h.reservation.Binding) {
		return kernel.ManagedHostReservation{}, kernel.ErrExecutionConflict
	}
	return h.reservation, nil
}
func (h *managedStoreHost) ReadManagedRecovery(_ context.Context, b kernel.ManagedHostBinding, id string) (kernel.ManagedHostReservation, error) {
	if id != h.reservation.RecoveryID || kernel.ManagedDigest(b) != kernel.ManagedDigest(h.reservation.Binding) {
		return kernel.ManagedHostReservation{}, kernel.ErrExecutionConflict
	}
	return h.reservation, nil
}
func managedStoreSelection(t *testing.T, f managedStoreFixture, mode kernel.ManagedRecoveryMode) (kernel.ManagedSelectionProof, kernel.ManagedTransfer) {
	t.Helper()
	f.backend.AdvanceTo(f.lease.ExpiresAt.Add(100 * time.Millisecond))
	parked, e := f.runtime.ParkManagedContinuation(t.Context(), f.source.Run.ID)
	if e != nil {
		t.Fatal(e)
	}
	transfer := parked.Transfer()
	r := kernel.ManagedHostReservation{ID: "reservation", RecoveryID: f.point.RecoveryID, SuspensionID: f.point.ID, Binding: f.point.Host, Mode: mode, Revision: 1}
	if mode == kernel.ManagedCancel {
		r.Cancellation = kernel.ManagedCancellation{IntentID: "hcancel", Seq: 7, Payload: json.RawMessage(`{"target":"owned-agent","reason":"original"}`), Reason: "original"}
		r.Cancellation.PayloadDigest = kernel.ManagedDigest(r.Cancellation.Payload)
	}
	f.host.reservation = r
	proof, e := f.runtime.SelectManagedContinuation(t.Context(), parked)
	if e != nil {
		t.Fatal(e)
	}
	return proof, transfer
}
func runManagedStoreSelectionAndExactACK(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	for _, mode := range []kernel.ManagedRecoveryMode{kernel.ManagedResume, kernel.ManagedCancel} {
		t.Run(string(mode), func(t *testing.T) {
			f := managedStoreSetup(t, factory)
			proof, parked := managedStoreSelection(t, f, mode)
			enable := kernel.ManagedEnableRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, ClaimantID: "claimant", OwnerID: "owner", LeaseID: "lease", GrantID: "successor", ExpectedTransferRevision: parked.Revision, Generation: parked.Generation, LeaseDuration: time.Minute, Selection: proof}
			cancel := kernel.ManagedCancelRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, ExpectedTransferRevision: parked.Revision, Generation: parked.Generation, Selection: proof}
			if mode == kernel.ManagedCancel {
				if _, e := f.store.EnableManagedRecovery(t.Context(), enable); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("CANCEL authorized send: %v", e)
				}
				managedStoreSame(t, f, parked)
				parkedProof, e := f.runtime.ParkManagedContinuation(t.Context(), f.source.Run.ID)
				if e != nil {
					t.Fatal(e)
				}
				altered := proof.Reservation()
				altered.Cancellation.IntentID = "another-intent"
				altered.Cancellation.Seq++
				// Two individually authentic reads must still not overwrite the already
				// chosen immutable cancellation tuple on the Kernel side.
				f.host.reservation = altered
				different, e := f.runtime.SelectManagedContinuation(t.Context(), parkedProof)
				f.host.reservation = proof.Reservation()
				if e != nil {
					t.Fatal(e)
				}
				result, e := f.store.CancelParkedManagedContinuation(t.Context(), cancel)
				if e != nil || result.State != kernel.ManagedCancelled {
					t.Fatalf("cancel: %v", e)
				}
				again, e := f.store.CancelParkedManagedContinuation(t.Context(), cancel)
				if e != nil || kernel.ManagedDigest(again) != kernel.ManagedDigest(result) {
					t.Fatalf("cancel ACK: %v", e)
				}
				foreignCancel := cancel
				foreignCancel.Selection = different
				if _, e = f.store.CancelParkedManagedContinuation(t.Context(), foreignCancel); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("different cancellation overwrote selection: %v", e)
				}
				if kernel.ManagedDigest(result.Terminal.State) != kernel.ManagedDigest(f.source.State) || result.Terminal.EventHead != f.source.EventHead+1 {
					t.Fatal("cancel lost receipt or journal source")
				}
				managedStoreReopenEqual(t, f, result)
				return
			}
			if _, e := f.store.CancelParkedManagedContinuation(t.Context(), cancel); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("RESUME authorized cancel: %v", e)
			}
			managedStoreSame(t, f, parked)
			result, e := f.store.EnableManagedRecovery(t.Context(), enable)
			if e != nil {
				t.Fatal(e)
			}
			again, e := f.store.EnableManagedRecovery(t.Context(), enable)
			if e != nil || kernel.ManagedDigest(again) != kernel.ManagedDigest(result) {
				t.Fatalf("same-call ACK: %v", e)
			}
			foreign := enable
			foreign.ClaimantID = "foreign"
			if _, e = f.store.EnableManagedRecovery(t.Context(), foreign); !errors.Is(e, kernel.ErrExecutionConflict) {
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

func runManagedStoreT4ClosesHistoricalTransfer(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
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
	second := f.point
	second.ID = "safe-2"
	second.RecoveryID = kernel.ManagedRecoveryID(second.ID, second.Host)
	second.OriginalGrant = g
	second.OriginalLease = enabled.SuccessorLease
	second.ModelInvocationID = "model-2"
	next, e := scoped.ApplyManagedModelIntent(t.Context(), f.source.Run.ID, f.source.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"pending":"model-2","originalReceipt":"retained"}`), Events: []kernel.EventDraft{{Type: "agent.model_invocation.pending"}}}, second, featurecontrol.Seal(f.source.Run.ID, f.source.Run.Revision, "managed_intent_source", g.ID, f.source.State, second))
	if e != nil {
		t.Fatal(e)
	}
	current, e := f.store.LoadManagedRecovery(t.Context(), f.source.Run.ID)
	if e != nil || current.SafePoint.ID != second.ID {
		t.Fatalf("second safe point: %v", e)
	}
	if _, e = scoped.LeaveManagedSafePoint(t.Context(), f.source.Run.ID); e != nil {
		t.Fatal(e)
	}
	completion := featurecontrol.Seal(next.Run.ID, next.Run.Revision, "managed_successor_completed", g.ID, next.State, next)
	if e = scoped.CompleteManagedRecovery(t.Context(), g, next, completion); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("unmarked completion accepted: %v", e)
	}
	mark := featurecontrol.Seal(next.Run.ID, next.Run.Revision, "agent_owned_physical_settled", g.ID, next.State, next.Checkpoint)
	if e = scoped.MarkChildExecutionSettled(t.Context(), g, mark); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e = scoped.CompleteManagedRecovery(t.Context(), g, next, completion); e != nil {
			t.Fatal(e)
		}
	}
	original, e := f.store.LoadManagedRecoveryByID(t.Context(), f.point.ID)
	if e != nil || original.State != kernel.ManagedClosed || original.Terminal == nil || kernel.ManagedDigest(*original.Terminal) != kernel.ManagedDigest(next) {
		t.Fatalf("historical closure: %v", e)
	}
	current, e = f.store.LoadManagedRecovery(t.Context(), f.source.Run.ID)
	if e != nil || current.SafePoint.ID != second.ID || current.State != kernel.ManagedInvalidated {
		t.Fatalf("closure rewrote unrelated current pointer: %v", e)
	}
	if e = scoped.FinishChildExecution(t.Context(), g, mark); e != nil {
		t.Fatal(e)
	}
	persisted, e := f.runtime.Load(t.Context(), next.Run.ID)
	if e != nil || kernel.ManagedDigest(persisted) != kernel.ManagedDigest(next) {
		t.Fatal("T4 changed feature state")
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

func (s *managedStoreACK) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, r uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	value, e := s.managedStorePorts.ApplyExecution(ctx, l, r, m)
	if e == nil && s.mode == "t0" && m.ManagedSafePoint.Present() {
		s.hits++
		return kernel.Snapshot{}, errManagedStoreACK
	}
	return value, e
}
func (s *managedStoreACK) ParkManagedRecovery(ctx context.Context, q kernel.ManagedParkRequest) (kernel.ManagedTransfer, error) {
	v, e := s.managedStorePorts.ParkManagedRecovery(ctx, q)
	if e == nil && s.mode == "park" {
		s.hits++
		return kernel.ManagedTransfer{}, errManagedStoreACK
	}
	return v, e
}
func (s *managedStoreACK) EnableManagedRecovery(ctx context.Context, q kernel.ManagedEnableRequest) (kernel.ManagedTransfer, error) {
	v, e := s.managedStorePorts.EnableManagedRecovery(ctx, q)
	if e == nil && s.mode == "enable" {
		s.hits++
		return kernel.ManagedTransfer{}, errManagedStoreACK
	}
	return v, e
}
func (s *managedStoreACK) CancelParkedManagedContinuation(ctx context.Context, q kernel.ManagedCancelRequest) (kernel.ManagedTransfer, error) {
	v, e := s.managedStorePorts.CancelParkedManagedContinuation(ctx, q)
	if e == nil && s.mode == "cancel" {
		s.hits++
		return kernel.ManagedTransfer{}, errManagedStoreACK
	}
	return v, e
}
func runManagedStoreTransactionACK(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	for _, mode := range []string{"t0", "park", "enable", "cancel"} {
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
			if mode == "park" {
				f.backend.AdvanceTo(f.lease.ExpiresAt.Add(100 * time.Millisecond))
				fault.mode = mode
				q := kernel.ManagedParkRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, ExpectedRevision: f.source.Run.Revision}
				if _, e := fault.ParkManagedRecovery(t.Context(), q); !errors.Is(e, errManagedStoreACK) {
					t.Fatalf("PARK fault: %v", e)
				}
				first, e := peer.LoadManagedRecovery(t.Context(), q.RunID)
				if e != nil {
					t.Fatal(e)
				}
				again, e := peer.ParkManagedRecovery(t.Context(), q)
				if e != nil || kernel.ManagedDigest(first) != kernel.ManagedDigest(again) {
					t.Fatalf("PARK ACK duplicate changed transfer: %v", e)
				}
				if fault.hits != 1 {
					t.Fatal("PARK fault missed")
				}
				managedStoreReopenEqual(t, f, again)
				return
			}
			selectionMode := kernel.ManagedResume
			if mode == "cancel" {
				selectionMode = kernel.ManagedCancel
			}
			proof, parked := managedStoreSelection(t, f, selectionMode)
			fault.mode = mode
			var retry kernel.ManagedTransfer
			var e error
			if mode == "enable" {
				q := kernel.ManagedEnableRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, ClaimantID: "original-claim", OwnerID: "owner", LeaseID: "lease", GrantID: "successor", ExpectedTransferRevision: parked.Revision, Generation: parked.Generation, LeaseDuration: time.Minute, Selection: proof}
				if _, e = fault.EnableManagedRecovery(t.Context(), q); !errors.Is(e, errManagedStoreACK) {
					t.Fatalf("ENABLE fault: %v", e)
				}
				retry, e = peer.EnableManagedRecovery(t.Context(), q)
			} else {
				q := kernel.ManagedCancelRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, ExpectedTransferRevision: parked.Revision, Generation: parked.Generation, Selection: proof}
				if _, e = fault.CancelParkedManagedContinuation(t.Context(), q); !errors.Is(e, errManagedStoreACK) {
					t.Fatalf("CANCEL fault: %v", e)
				}
				retry, e = peer.CancelParkedManagedContinuation(t.Context(), q)
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
	proof, parked := managedStoreSelection(t, f, kernel.ManagedResume)
	base := kernel.ManagedEnableRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, ExpectedTransferRevision: parked.Revision, Generation: parked.Generation, LeaseDuration: time.Minute, Selection: proof}
	a, b := base, base
	a.ClaimantID = "a"
	a.OwnerID = "owner-a"
	a.LeaseID = "lease-a"
	a.GrantID = "grant-a"
	b.ClaimantID = "b"
	b.OwnerID = "owner-b"
	b.LeaseID = "lease-b"
	b.GrantID = "grant-b"
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	type answer struct {
		transfer kernel.ManagedTransfer
		err      error
	}
	results := make(chan answer, 2)
	attempt := func(store kernel.ManagedRecoveryStore, q kernel.ManagedEnableRequest) {
		ready <- struct{}{}
		<-start
		v, e := store.EnableManagedRecovery(t.Context(), q)
		results <- answer{v, e}
	}
	go attempt(f.store, a)
	go attempt(f.backend.Peer.(kernel.ManagedRecoveryStore), b)
	<-ready
	<-ready
	close(start)
	one, two := <-results, <-results
	if one.err != nil {
		one, two = two, one
	}
	if one.err != nil || !errors.Is(two.err, kernel.ErrExecutionConflict) || two.transfer.SuccessorLease.LeaseID != "" {
		t.Fatalf("ENABLE competitors: winner=%v loser=%v", one.err, two.err)
	}
	if one.transfer.SuccessorLease.Epoch != f.lease.Epoch+1 || len(one.transfer.Activations) != 1 {
		t.Fatal("competition advanced more than one epoch/activation")
	}
	original, e := f.runtime.Load(t.Context(), f.source.Run.ID)
	if e != nil || kernel.ManagedDigest(original) != kernel.ManagedDigest(f.source) {
		t.Fatal("ENABLE race changed source")
	}
	managedStoreReopenEqual(t, f, one.transfer)
}

func runManagedStoreAncestorGate(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	for _, mode := range []string{"park-first", "child-first"} {
		t.Run(mode, func(t *testing.T) {
			f := managedStoreSetup(t, factory)
			child, e := f.runtime.Create(t.Context(), kernel.CreateRequest{ID: "managed-child", Kind: "agent", Goal: "original managed child", Actor: f.source.Run.Actor, Thread: f.source.Run.Thread, ParentRunID: f.source.Run.ID, State: json.RawMessage(`{"child":"original"}`)})
			if e != nil {
				t.Fatal(e)
			}
			grant := kernel.ChildExecutionGrant{ID: "child-grant", ChildRunID: child.Run.ID, ExpectedRevision: child.Run.Revision}
			peer := f.backend.Peer.(managedStorePorts)
			original, e := f.store.LoadManagedRecovery(t.Context(), f.source.Run.ID)
			if e != nil {
				t.Fatal(e)
			}
			q := kernel.ManagedParkRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, ExpectedRevision: f.source.Run.Revision}
			if mode == "child-first" {
				if e = peer.BeginChildExecution(t.Context(), grant); e != nil {
					t.Fatal(e)
				}
				f.backend.AdvanceTo(f.lease.ExpiresAt.Add(100 * time.Millisecond))
				if _, e = f.store.ParkManagedRecovery(t.Context(), q); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("PARK discarded descendant authority: %v", e)
				}
				actual, e := peer.LookupChildExecution(t.Context(), child.Run.ID)
				if e != nil || actual != grant {
					t.Fatal("descendant index lost")
				}
			} else {
				f.backend.AdvanceTo(f.lease.ExpiresAt.Add(100 * time.Millisecond))
				original, e = f.store.ParkManagedRecovery(t.Context(), q)
				if e != nil {
					t.Fatal(e)
				}
				if e = peer.BeginChildExecution(t.Context(), grant); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("child passed parked ancestor: %v", e)
				}
				if _, e = peer.LookupChildExecution(t.Context(), child.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("rejected child acquired index: %v", e)
				}
			}
			actual, e := peer.Load(t.Context(), child.Run.ID)
			if e != nil || kernel.ManagedDigest(actual) != kernel.ManagedDigest(child) {
				t.Fatal("child aggregate mutated")
			}
			managedStoreSame(t, f, original)
		})
	}
}

type managedStoreRaceGate struct {
	managedStorePorts
	ownerArrived, parkArrived chan struct{}
	ownerRelease, parkRelease chan struct{}
	kind                      string
}

func (g *managedStoreRaceGate) RenewExecution(ctx context.Context, q kernel.ExecutionRenewRequest) (kernel.ExecutionLease, error) {
	if g.kind == "renew" {
		close(g.ownerArrived)
		<-g.ownerRelease
	}
	return g.managedStorePorts.RenewExecution(ctx, q)
}
func (g *managedStoreRaceGate) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	if g.kind == "claim" {
		close(g.ownerArrived)
		<-g.ownerRelease
	}
	return g.managedStorePorts.ApplyExecution(ctx, l, rev, m)
}
func (g *managedStoreRaceGate) ParkManagedRecovery(ctx context.Context, q kernel.ManagedParkRequest) (kernel.ManagedTransfer, error) {
	close(g.parkArrived)
	<-g.parkRelease
	return g.managedStorePorts.ParkManagedRecovery(ctx, q)
}
func runManagedStoreOwnerParkCompetition(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture) {
	for _, kind := range []string{"renew", "claim"} {
		for _, winner := range []string{"owner", "park"} {
			t.Run(kind+"-"+winner, func(t *testing.T) {
				// Install the wrapper before original admission, but do not intercept
				// setup. The original scoped Runtime remains bound across the T0 revision.
				var owner *managedStoreRaceGate
				f := managedStoreSetup(t, func(t *testing.T) ManagedRecoveryStoreFixture {
					backend := factory(t)
					owner = &managedStoreRaceGate{managedStorePorts: backend.Store.(managedStorePorts)}
					backend.Store = owner
					return backend
				})
				arrivedOwner, arrivedPark := make(chan struct{}), make(chan struct{})
				releaseOwner, releasePark := make(chan struct{}), make(chan struct{})
				owner.ownerArrived = arrivedOwner
				owner.ownerRelease = releaseOwner
				owner.kind = kind
				parker := &managedStoreRaceGate{managedStorePorts: f.backend.Peer.(managedStorePorts), parkArrived: arrivedPark, parkRelease: releasePark}
				scoped := f.scoped
				var e error
				type ownerResult struct {
					snapshot kernel.Snapshot
					lease    kernel.ExecutionLease
					err      error
				}
				ownerDone := make(chan ownerResult, 1)
				type parkResult struct {
					transfer kernel.ManagedTransfer
					err      error
				}
				parkDone := make(chan parkResult, 1)
				q := kernel.ManagedParkRequest{RunID: f.source.Run.ID, SuspensionID: f.point.ID, ExpectedRevision: f.source.Run.Revision}
				go func() {
					if kind == "renew" {
						v, e := owner.RenewExecution(t.Context(), kernel.ExecutionRenewRequest{Lease: f.lease, LeaseDuration: time.Minute})
						ownerDone <- ownerResult{lease: v, err: e}
					} else {
						v, e := scoped.Apply(t.Context(), f.source.Run.ID, f.source.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"claim":"original"}`)})
						ownerDone <- ownerResult{snapshot: v, err: e}
					}
				}()
				go func() { v, e := parker.ParkManagedRecovery(t.Context(), q); parkDone <- parkResult{v, e} }()
				// Both real requests have reached the Store boundary with the original
				// source. Release one transaction first; no scheduling sleep guesses.
				<-arrivedOwner
				<-arrivedPark
				if winner == "owner" {
					close(releaseOwner)
					first := <-ownerDone
					if first.err != nil {
						close(releasePark)
						<-parkDone
						t.Fatalf("owner winner: %v", first.err)
					}
					f.backend.AdvanceTo(f.lease.ExpiresAt.Add(100 * time.Millisecond))
					close(releasePark)
					second := <-parkDone
					if !errors.Is(second.err, kernel.ErrExecutionConflict) {
						t.Fatalf("PARK after owner winner: %v", second.err)
					}
					if kind == "claim" {
						f.source = first.snapshot
					} else if e = f.store.CheckExecution(t.Context(), f.source.Run.ID, &first.lease); e != nil {
						t.Fatalf("PARK revoked renewed lease: %v", e)
					}
					transfer, e := f.store.LoadManagedRecovery(t.Context(), f.source.Run.ID)
					if e != nil {
						t.Fatal(e)
					}
					want := kernel.ManagedAvailable
					if kind == "claim" {
						want = kernel.ManagedInvalidated
					}
					if transfer.State != want {
						t.Fatalf("owner winner state %s", transfer.State)
					}
					managedStoreSame(t, f, transfer)
				} else {
					f.backend.AdvanceTo(f.lease.ExpiresAt.Add(100 * time.Millisecond))
					close(releasePark)
					first := <-parkDone
					if first.err != nil {
						close(releaseOwner)
						<-ownerDone
						t.Fatalf("PARK winner: %v", first.err)
					}
					close(releaseOwner)
					second := <-ownerDone
					if !errors.Is(second.err, kernel.ErrExecutionConflict) {
						t.Fatalf("old owner after PARK: %v", second.err)
					}
					managedStoreSame(t, f, first.transfer)
				}
			})
		}
	}
}
