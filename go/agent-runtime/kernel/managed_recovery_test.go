package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func managedReducerFixture() (ManagedTransfer, Snapshot, ExecutionLease, ChildExecutionGrant, ManagedAuthority, time.Time) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	a := ManagedAuthority{Namespace: strings.Repeat("01", 32), Generation: 1, Version: 1}
	h := ManagedAuthority{Namespace: strings.Repeat("02", 32), Generation: 1, Version: 1}
	s := Snapshot{Run: Run{ID: "root", Kind: "agent", Status: RunStatusRunning, Revision: 3}, State: json.RawMessage(`{"pending":"original"}`), EventHead: 4}
	l := ExecutionLease{RunID: "root", OwnerID: "old", LeaseID: "old-lease", Epoch: 1, ExpiresAt: now.Add(-time.Second)}
	g := ChildExecutionGrant{ID: "old-grant", ChildRunID: "root", ExpectedRevision: 1, ExecutionOwnerID: l.OwnerID, ExecutionLeaseID: l.LeaseID, ExecutionEpoch: l.Epoch, AdmissionSourceHash: strings.Repeat("a", 64), AdmissionOperation: "start"}
	b := ManagedHostBinding{KernelAuthority: a, HostAuthority: h, TurnID: "turn", InvocationID: "inv", Attempt: 1, RunID: "root", DispatchID: "original-dispatch", Phase: "start", Source: json.RawMessage(`{"actor":"original"}`)}
	p := ManagedSafePoint{ID: "safe", Host: b, Source: s, OriginalGrant: g, OriginalLease: l, ModelInvocationID: "model-2", ModelRequestHash: strings.Repeat("b", 64), LifecycleVersion: "managed-context-v1", LifecycleSource: json.RawMessage(`{"actual":"returned"}`)}
	p.RecoveryID = ManagedRecoveryID(p.ID, b)
	return ManagedInitialTransfer(p), s, l, g, a, now
}
func TestManagedRecoveryReducersPreserveSourceAndLiveOwner(t *testing.T) {
	original, s, l, g, a, now := managedReducerFixture()
	q := ManagedParkRequest{RunID: s.Run.ID, SuspensionID: original.SafePoint.ID, ExpectedRevision: s.Run.Revision}
	live := l
	live.ExpiresAt = now.Add(time.Second)
	if _, e := ManagedParkTransfer(original, q, s, live, g, a, now); e != ErrExecutionConflict {
		t.Fatalf("live owner: %v", e)
	}
	changed := s
	changed.EventHead++
	if _, e := ManagedParkTransfer(original, q, changed, l, g, a, now); e != ErrExecutionConflict {
		t.Fatalf("journal mismatch: %v", e)
	}
	foreign := a
	foreign.Generation++
	if _, e := ManagedParkTransfer(original, q, s, l, g, foreign, now); e != ErrExecutionConflict {
		t.Fatalf("authority mismatch: %v", e)
	}
	parked, e := ManagedParkTransfer(original, q, s, l, g, a, now)
	if e != nil || parked.State != ManagedParked || parked.Revision != 2 || parked.Generation != 2 || ManagedDigest(parked.SafePoint) != ManagedDigest(original.SafePoint) {
		t.Fatalf("park: %+v %v", parked, e)
	}
	if ManagedOrdinaryGate(parked) != ErrExecutionConflict {
		t.Fatal("park permits ordinary mutation")
	}
	invalid := ManagedInvalidateMutation(original)
	if _, e := ManagedParkTransfer(invalid, q, s, l, g, a, now); e != ErrExecutionConflict {
		t.Fatalf("old claim won but park succeeded: %v", e)
	}
}
func TestManagedRecoveryModeAndExactACKIdentity(t *testing.T) {
	initial, s, l, g, a, now := managedReducerFixture()
	p, e := ManagedParkTransfer(initial, ManagedParkRequest{RunID: s.Run.ID, SuspensionID: "safe", ExpectedRevision: 3}, s, l, g, a, now)
	if e != nil {
		t.Fatal(e)
	}
	r := ManagedHostReservation{ID: "reservation", RecoveryID: p.SafePoint.RecoveryID, SuspensionID: "safe", Binding: p.SafePoint.Host, Mode: ManagedResume, Revision: 1}
	proof := ManagedSelectionProof{reservation: r, transfer: p, valid: true}
	q := ManagedEnableRequest{RunID: s.Run.ID, SuspensionID: "safe", ClaimantID: "claim-1", OwnerID: "next", LeaseID: "next-lease", GrantID: "next-grant", ExpectedTransferRevision: p.Revision, Generation: p.Generation, LeaseDuration: time.Minute, Selection: proof}
	enabled, e := ManagedEnabledTransfer(p, q, s, l.Epoch, now)
	if e != nil {
		t.Fatal(e)
	}
	if !ManagedEnableReplay(enabled, q, s, enabled.SuccessorLease, now) {
		t.Fatal("same call ACK not recoverable")
	}
	other := q
	other.ClaimantID = "claim-2"
	if ManagedEnableReplay(enabled, other, s, enabled.SuccessorLease, now) {
		t.Fatal("foreign caller adopted lease")
	}
	c := ManagedCancellation{IntentID: "original-cancel", Seq: 7, Payload: json.RawMessage(`{"runID":"root","boundary":"cancel_scope_pending"}`)}
	c.PayloadDigest = ManagedDigest(c.Payload)
	cancelR := r
	cancelR.Mode = ManagedCancel
	cancelR.Cancellation = c
	cancelP := ManagedSelectionProof{reservation: cancelR, transfer: p, valid: true}
	q.Selection = cancelP
	if _, e = ManagedEnabledTransfer(p, q, s, l.Epoch, now); e != ErrExecutionConflict {
		t.Fatalf("CANCEL enabled physical dispatch: %v", e)
	}
	cq := ManagedCancelRequest{RunID: s.Run.ID, SuspensionID: "safe", ExpectedTransferRevision: p.Revision, Generation: p.Generation, Selection: cancelP}
	cancelled, m, e := ManagedCancellationMutation(p, cq, s, now)
	if e != nil {
		t.Fatal(e)
	}
	if m.Record.Run.Status != RunStatusCancelled || ManagedDigest(m.Record.State) != ManagedDigest(s.State) || cancelled.Terminal.EventHead != s.EventHead+1 {
		t.Fatal("cancel changed original evidence")
	}
	if !ManagedCancelReplay(cancelled, cq, *cancelled.Terminal) {
		t.Fatal("cancel ACK replay rejected")
	}
	forged := cancelP
	forged.reservation.Cancellation.Seq++
	cq.Selection = forged
	if ManagedCancelReplay(cancelled, cq, *cancelled.Terminal) {
		t.Fatal("foreign cancellation receipt adopted")
	}
	cq.Selection = proof
	if _, _, e = ManagedCancellationMutation(p, cq, s, now); e != ErrExecutionConflict {
		t.Fatalf("RESUME became cancellation: %v", e)
	}
}
func TestManagedSafePointLeaveIsBeforeCallbackPermission(t *testing.T) {
	p, s, l, _, _, now := managedReducerFixture()
	l.ExpiresAt = now.Add(time.Minute)
	q := ManagedLeaveRequest{RunID: s.Run.ID, SuspensionID: p.SafePoint.ID, ExpectedRevision: s.Run.Revision, ExpectedTransferRevision: p.Revision, Generation: p.Generation, Lease: l}
	next, e := ManagedLeaveTransfer(p, q, s, l, now)
	if e != nil || next.State != ManagedInvalidated {
		t.Fatalf("leave %v", e)
	}
	if _, e = ManagedLeaveTransfer(next, q, s, l, now); e != ErrExecutionConflict {
		t.Fatal("ambiguous ACK was converted into new permission")
	}
}

func TestManagedReservationCloneAndPersistencePreserveModeZero(t *testing.T) {
	transfer, _, _, _, _, _ := managedReducerFixture()
	resume := ManagedHostReservation{ID: "reservation", RecoveryID: transfer.SafePoint.RecoveryID, SuspensionID: transfer.SafePoint.ID, Binding: transfer.SafePoint.Host, Mode: ManagedResume, Revision: 1}
	if !resume.Valid() {
		t.Fatal("invalid source fixture")
	}
	cloned := managedClone(resume)
	if !cloned.Valid() || !reflect.DeepEqual(cloned, resume) {
		t.Fatal("clone changed zero cancellation")
	}
	encoded, err := json.Marshal(resume)
	if err != nil {
		t.Fatal(err)
	}
	var restored ManagedHostReservation
	if err = json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.Valid() || !reflect.DeepEqual(restored, resume) {
		t.Fatalf("persistent roundtrip changed mode source: %s", encoded)
	}
	empty := resume
	empty.Cancellation.Payload = json.RawMessage{}
	if empty.Valid() {
		t.Fatal("nonnil empty cancellation accepted")
	}
	if _, e := json.Marshal(empty); e == nil {
		t.Fatal("empty cancellation erased on persistent write")
	}
	explicit := resume
	explicit.Cancellation.Payload = json.RawMessage("null")
	explicitClone := managedClone(explicit)
	if explicitClone.Valid() || !reflect.DeepEqual(explicit, explicitClone) {
		t.Fatal("explicit null was erased into RESUME authority")
	}
	raw, err := json.Marshal(explicit)
	if err != nil {
		t.Fatal(err)
	}
	var restoredNull ManagedHostReservation
	if err = json.Unmarshal(raw, &restoredNull); err != nil {
		t.Fatal(err)
	}
	if restoredNull.Valid() {
		t.Fatal("persistent explicit null accepted")
	}
	cloned.Binding.Source[0] = 'x'
	if reflect.DeepEqual(cloned.Binding.Source, resume.Binding.Source) {
		t.Fatal("mutable source alias")
	}
	cancel := resume
	cancel.Mode = ManagedCancel
	cancel.Cancellation = ManagedCancellation{IntentID: "original", Seq: 8, Payload: json.RawMessage(`{"target":"root"}`)}
	cancel.Cancellation.PayloadDigest = ManagedDigest(cancel.Cancellation.Payload)
	raw, err = json.Marshal(cancel)
	if err != nil {
		t.Fatal(err)
	}
	var restoredCancel ManagedHostReservation
	if err = json.Unmarshal(raw, &restoredCancel); err != nil {
		t.Fatal(err)
	}
	if !restoredCancel.Valid() || !reflect.DeepEqual(cancel, restoredCancel) {
		t.Fatal("CANCEL source lost in codec")
	}
}

type managedConfiguredKernel struct {
	Store
	ManagedRecoveryStore
	transfer ManagedTransfer
	enable   ManagedEnableRequest
}

func (s *managedConfiguredKernel) ManagedRecoveryAuthority(context.Context) (ManagedAuthority, error) {
	return s.transfer.SafePoint.Host.KernelAuthority, nil
}
func (s *managedConfiguredKernel) LoadManagedRecovery(context.Context, string) (ManagedTransfer, error) {
	return managedClone(s.transfer), nil
}
func (s *managedConfiguredKernel) EnableManagedRecovery(_ context.Context, q ManagedEnableRequest) (ManagedTransfer, error) {
	s.enable = q
	return managedClone(s.transfer), nil
}

type managedConfiguredHost struct {
	binding     ManagedHostBinding
	reservation ManagedHostReservation
	calls       int
}

func (s *managedConfiguredHost) ManagedRecoveryAuthority(context.Context) (ManagedAuthority, error) {
	s.calls++
	return s.binding.HostAuthority, nil
}
func (s *managedConfiguredHost) ReadManagedOriginalDispatch(_ context.Context, b ManagedHostBinding) (ManagedHostBinding, error) {
	s.calls++
	if !reflect.DeepEqual(b, s.binding) {
		return ManagedHostBinding{}, ErrExecutionConflict
	}
	return managedClone(s.binding), nil
}
func (s *managedConfiguredHost) ReserveManagedRecovery(_ context.Context, p ManagedParkedProof) (ManagedHostReservation, error) {
	s.calls++
	if !p.Matches(s.binding) {
		return ManagedHostReservation{}, ErrExecutionConflict
	}
	return managedClone(s.reservation), nil
}
func (s *managedConfiguredHost) ReadManagedRecovery(_ context.Context, b ManagedHostBinding, id string) (ManagedHostReservation, error) {
	s.calls++
	if !reflect.DeepEqual(b, s.binding) || id != s.reservation.RecoveryID {
		return ManagedHostReservation{}, ErrExecutionConflict
	}
	return managedClone(s.reservation), nil
}

type managedForbiddenIDSource struct{ calls int }

func (s *managedForbiddenIDSource) NewID(string) (string, error) {
	s.calls++
	return "", errors.New("configured entropy called before Leave")
}

func TestManagedRecoveryUsesOnlyConstructionBoundHostAndInternalEntropy(t *testing.T) {
	initial, s, l, g, a, now := managedReducerFixture()
	parked, e := ManagedParkTransfer(initial, ManagedParkRequest{RunID: s.Run.ID, SuspensionID: initial.SafePoint.ID, ExpectedRevision: s.Run.Revision}, s, l, g, a, now)
	if e != nil {
		t.Fatal(e)
	}
	host := &managedConfiguredHost{binding: parked.SafePoint.Host, reservation: ManagedHostReservation{ID: "reserved", RecoveryID: parked.SafePoint.RecoveryID, SuspensionID: parked.SafePoint.ID, Binding: parked.SafePoint.Host, Mode: ManagedResume, Revision: 1}}
	counterfeit := &managedConfiguredHost{binding: host.binding, reservation: host.reservation}
	counterfeit.reservation.Mode = ManagedCancel
	store := &managedConfiguredKernel{transfer: parked}
	ids := &managedForbiddenIDSource{}
	rt, e := New(Dependencies{Store: store, IDs: ids, ManagedRecoveryHost: host})
	if e != nil {
		t.Fatal(e)
	}
	if !rt.UsesManagedRecoveryHost(host) || rt.UsesManagedRecoveryHost(counterfeit) {
		t.Fatal("runtime accepted foreign per-call port")
	}
	bound, e := rt.BindManagedDispatch(t.Context(), host.binding)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := rt.ManagedDispatch(bound, s.Run.ID); !ok {
		t.Fatal("actual bound dispatch lost")
	}
	// A deliberately reconstructed Runtime is a different trusted configuration,
	// and may not borrow the prior Runtime's bound actual-call context.
	other, e := New(Dependencies{Store: store, ManagedRecoveryHost: counterfeit})
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := other.ManagedDispatch(bound, s.Run.ID); ok {
		t.Fatal("different construction borrowed original binding")
	}
	proof := ManagedParkedProof{transfer: parked, authority: a, valid: true}
	selected, e := rt.SelectManagedContinuation(t.Context(), proof)
	if e != nil {
		t.Fatal(e)
	}
	if selected.Reservation().Mode != ManagedResume || counterfeit.calls != 0 {
		t.Fatal("unconfigured Host minted selection")
	}
	_, e = rt.EnableManagedContinuation(t.Context(), selected, "actual-claimant", "next-owner", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if ids.calls != 0 || store.enable.LeaseID == "" || store.enable.GrantID == "" || store.enable.LeaseID == store.enable.GrantID {
		t.Fatal("pre-Leave entropy was configurable or unbound")
	}
	unconfigured, e := New(Dependencies{Store: store})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = unconfigured.SelectManagedContinuation(t.Context(), proof); e != ErrExecutionConflict {
		t.Fatalf("unconfigured proof issuer: %v", e)
	}
}
