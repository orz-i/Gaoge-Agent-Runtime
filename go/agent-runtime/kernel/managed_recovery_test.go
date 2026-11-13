package kernel

import (
	"encoding/json"
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
