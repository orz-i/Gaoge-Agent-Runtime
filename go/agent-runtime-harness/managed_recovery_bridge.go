package harness

import (
	"context"
	"errors"
	"math"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type managedHostStore interface {
	kernel.ManagedRecoveryHostStore
	CloseManagedRecovery(context.Context, kernel.ManagedClosureProof) error
}

// bindManagedOriginalDispatch is called only AFTER actual native Root dispatch
// admission. The source is read back, then revalidated under the Host Turn lock.
func (r *Runner) bindManagedOriginalDispatch(ctx context.Context, inv Invocation, dispatch Item) (context.Context, error) {
	native, ok := r.agent.(*agent.Runner)
	if !ok || (!native.SupportsManagedContextRecovery() && !native.SupportsSummaryNativeRecovery()) || inv.ParentItemID != "" {
		return ctx, nil
	}
	phase, ok := ValidExecutionDispatchGrantAdmission(dispatch)
	if _, managed := kernel.ManagedDispatchOperation(phase); !ok || !managed || inv.Attempt != 1 {
		return ctx, nil
	}
	host, ok := r.store.(managedHostStore)
	if !ok || !r.runtime.UsesManagedRecoveryHost(host) {
		return ctx, kernel.ErrExecutionRequired
	}
	if inv.Attempt <= 0 || uint64(inv.Attempt) > math.MaxUint32 {
		return ctx, ErrConflict
	}
	saved, err := r.store.GetItem(ctx, dispatch.ID)
	if err != nil {
		return ctx, err
	}
	if savedPhase, valid := VerifiedExecutionDispatchGrant(saved); !valid || savedPhase != phase {
		return ctx, ErrConflict
	}
	turn, err := r.store.GetTurn(ctx, inv.TurnID)
	if err != nil {
		return ctx, err
	}
	session, err := r.store.GetSession(ctx, turn.SessionID)
	if err != nil {
		return ctx, err
	}
	config, err := r.store.GetConfigSnapshot(ctx, turn.ConfigSnapshotID)
	if err != nil {
		return ctx, err
	}
	source, err := ManagedHostSource(session, turn, inv, config, saved)
	if err != nil {
		return ctx, err
	}
	ka, err := r.runtime.ManagedRecoveryAuthority(ctx)
	if err != nil {
		return ctx, err
	}
	ha, err := host.ManagedRecoveryAuthority(ctx)
	if err != nil {
		return ctx, err
	}
	b := kernel.ManagedHostBinding{KernelAuthority: ka, HostAuthority: ha, TurnID: turn.ID, InvocationID: inv.ID, Attempt: uint32(inv.Attempt), RunID: inv.ExecutionRefID, DispatchID: saved.ID, Phase: phase, Source: source}
	if phase == "resolve-approval" {
		var valid bool
		b.Approval, valid = originalDispatchApproval(saved)
		if !valid {
			return ctx, ErrConflict
		}
	}
	return r.runtime.BindManagedDispatch(ctx, b)
}

// closeOriginalManagedDispatch retains the original transfer's separate
// reservation and mode proof, but consumes the same native retirement before
// Host closure. It never manufactures an ordinary callback return.
func (r *Runner) closeOriginalManagedDispatch(ctx context.Context, native *agent.Runner, host managedHostStore, proof kernel.ManagedClosureProof) error {
	if err := native.ReconcileManagedClosure(ctx, proof); err != nil {
		return err
	}
	return host.CloseManagedRecovery(ctx, proof)
}

// recoverReturnedManagedDispatch is receipt-only. Refresh can close a CLOSED
// original RESUME without entering the managed PARK/ENABLE or execution path.
func (r *Runner) recoverReturnedManagedDispatch(ctx context.Context, native *agent.Runner, inv Invocation, dispatch Item) (bool, error) {
	if inv.ParentItemID != "" || inv.Attempt != 1 ||
		(!native.SupportsManagedContextRecovery() && !native.SupportsSummaryNativeRecovery()) {
		return false, nil
	}
	phase, ok := VerifiedExecutionDispatchGrant(dispatch)
	if _, managed := kernel.ManagedDispatchOperation(phase); !ok || !managed {
		return false, nil
	}
	t, err := r.runtime.LoadManagedRecovery(ctx, inv.ExecutionRefID)
	if kernel.ManagedRecoveryAbsent(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	b := t.SafePoint.Host
	// A historical transfer is not evidence for a newer pending dispatch.
	if b.DispatchID != dispatch.ID {
		return false, nil
	}
	if b.Phase != phase || b.InvocationID != inv.ID || b.TurnID != inv.TurnID || b.RunID != inv.ExecutionRefID || uint64(b.Attempt) != uint64(inv.Attempt) {
		return false, ErrConflict
	}
	host, ok := r.store.(managedHostStore)
	if !ok || !r.runtime.UsesManagedRecoveryHost(host) {
		return false, kernel.ErrExecutionRequired
	}
	res, err := host.ReadManagedRecovery(ctx, b, "")
	if errors.Is(err, kernel.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if res.Mode != kernel.ManagedResume {
		return false, nil
	}
	closed, err := r.runtime.LoadManagedRecoveryByID(ctx, res.SuspensionID)
	if err != nil {
		return false, err
	}
	if closed.State != kernel.ManagedClosed {
		return false, nil
	}
	proof, err := r.runtime.ManagedRecoveryClosure(ctx, res.SuspensionID)
	if err != nil || !proof.Matches(res) {
		return false, errors.Join(ErrConflict, err)
	}
	if err = r.closeOriginalManagedDispatch(ctx, native, host, proof); err != nil {
		return false, err
	}
	return true, nil
}

// recoverManagedInvocation does not open a new physical dispatch. The Core
// entry performs Kernel PARK, immutable Host reservation and unique ENABLE.
func (r *Runner) recoverManagedInvocation(ctx context.Context, inv Invocation) (kernel.Snapshot, bool, error) {
	native, ok := r.agent.(*agent.Runner)
	if !ok || (!native.SupportsManagedContextRecovery() && !native.SupportsSummaryNativeRecovery()) || inv.ParentItemID != "" {
		return kernel.Snapshot{}, false, nil
	}
	if err := r.requireInvocationRootModelOutputCeiling(ctx, inv); err != nil {
		return kernel.Snapshot{}, true, err
	}
	transfer, err := r.runtime.LoadManagedRecovery(ctx, inv.ExecutionRefID)
	if kernel.ManagedRecoveryAbsent(err) {
		return kernel.Snapshot{}, false, nil
	}
	if err != nil {
		return kernel.Snapshot{}, true, err
	}
	b := transfer.SafePoint.Host
	if b.InvocationID != inv.ID || b.TurnID != inv.TurnID || b.RunID != inv.ExecutionRefID || uint64(b.Attempt) != uint64(inv.Attempt) {
		return kernel.Snapshot{}, true, ErrConflict
	}
	// A normally returned dispatch may have a historical safe point.
	// It needs ordinary Resume, not recovery of the retired original.
	items, itemsErr := r.loadGlobalPhysicalEvidence(ctx, inv.TurnID)
	if itemsErr != nil {
		return kernel.Snapshot{}, true, itemsErr
	}
	receipts := executionDispatchReceiptIndex(items)
	var original Item
	for _, item := range items {
		if item.ID != b.DispatchID {
			continue
		}
		original = item
		if phase, ok := VerifiedExecutionDispatchGrant(item); !ok || phase != b.Phase || item.InvocationID != inv.ID || item.RunID != inv.ExecutionRefID || item.TurnID != inv.TurnID {
			return kernel.Snapshot{}, true, ErrConflict
		}
		if boundary, settled := VerifiedExecutionDispatchReceipt(item, receipts[item.ID]); settled {
			if boundary == "managed_dispatch_transferred" || boundary == "managed_dispatch_cancelled_before_next_effect" {
				host, ok := r.store.(managedHostStore)
				if !ok || !r.runtime.UsesManagedRecoveryHost(host) {
					return kernel.Snapshot{}, true, kernel.ErrExecutionRequired
				}
				res, e := host.ReadManagedRecovery(ctx, b, "")
				if e != nil {
					return kernel.Snapshot{}, true, e
				}
				closed, e := r.runtime.LoadManagedRecoveryByID(ctx, res.SuspensionID)
				if e != nil || !SameManagedReservation(closed.Selection, res) || closed.Terminal == nil {
					return kernel.Snapshot{}, true, errors.Join(ErrConflict, e)
				}
				fields, valid := authorityStringFields(receipts[item.ID][0].Payload, "boundary", "recoveryID", "mode", "selectionHash", "terminalHash")
				if !valid || fields["recoveryID"] != res.RecoveryID || fields["mode"] != string(res.Mode) || fields["selectionHash"] != kernel.ManagedDigest(res) || fields["terminalHash"] != kernel.ManagedDigest(*closed.Terminal) {
					return kernel.Snapshot{}, true, ErrConflict
				}
				if closed.State == kernel.ManagedClosed {
					receipt, e := r.runtime.LoadOwnedRetirement(ctx, closed.SuccessorGrant.ID)
					if e != nil || receipt.Phase != "ACKNOWLEDGED" || receipt.Evidence.Grant != closed.SuccessorGrant ||
						kernel.ManagedDigest(receipt.Evidence.Snapshot) != kernel.ManagedDigest(*closed.Terminal) {
						return kernel.Snapshot{}, true, errors.Join(ErrConflict, e)
					}
					for _, t := range receipt.Closures {
						if kernel.ManagedDigest(t) == kernel.ManagedDigest(closed) {
							return kernel.Snapshot{}, false, nil
						}
					}
				} else if closed.State == kernel.ManagedCancelled && res.Mode == kernel.ManagedCancel {
					return *closed.Terminal, true, nil
				}
				return kernel.Snapshot{}, true, ErrConflict
			}
			host, ok := r.store.(managedHostStore)
			if !ok || !r.runtime.UsesManagedRecoveryHost(host) {
				return kernel.Snapshot{}, true, kernel.ErrExecutionRequired
			}
			if _, e := host.ReadManagedRecovery(ctx, b, ""); !errors.Is(e, kernel.ErrNotFound) {
				return kernel.Snapshot{}, true, errors.Join(ErrConflict, e)
			}
			return kernel.Snapshot{}, false, nil
		}
	}
	if original.ID == "" || len(receipts[b.DispatchID]) != 0 {
		return kernel.Snapshot{}, true, ErrConflict
	}
	if transfer.Selection.ID == "" {
		if repaired, e := r.recoverOriginalPhysicalDispatch(ctx, inv); e != nil {
			return kernel.Snapshot{}, true, e
		} else if repaired {
			return kernel.Snapshot{}, false, nil
		}
	}
	host, ok := r.store.(managedHostStore)
	if !ok || !r.runtime.UsesManagedRecoveryHost(host) {
		return kernel.Snapshot{}, true, kernel.ErrExecutionRequired
	}
	// Actual source-bound storage check precedes any PARK mutation.
	if _, err = host.ReadManagedOriginalDispatch(ctx, b); err != nil {
		// Exact already-closed reservation may need only Host projection repair.
		reservation, readErr := host.ReadManagedRecovery(ctx, b, "")
		if readErr != nil {
			return kernel.Snapshot{}, true, errors.Join(err, readErr)
		}
		proof, proofErr := r.runtime.ManagedRecoveryClosure(ctx, reservation.SuspensionID)
		if proofErr != nil || !proof.Matches(reservation) {
			return kernel.Snapshot{}, true, errors.Join(ErrConflict, proofErr)
		}
		// Host closure and native retirement are separate durable facts.
		// A replay closes the exact original successor, never another callback.
		if closeErr := r.closeOriginalManagedDispatch(ctx, native, host, proof); closeErr != nil {
			return kernel.Snapshot{}, true, closeErr
		}
		return proof.Snapshot(), true, nil
	}
	boundCtx, err := r.runtime.BindManagedDispatch(ctx, b)
	if err != nil {
		return kernel.Snapshot{}, true, err
	}
	snap, proof, runErr := native.RecoverManagedContinuation(boundCtx, inv.ExecutionRefID)
	// A real returned successor may be waiting for its original child
	// receipt. Do not close the original Host dispatch until retirement.
	if !proof.Matches(proof.Transfer().Selection) {
		return snap, true, runErr
	}
	// A known terminal failure is an original result, not an uncertain
	// callback. Close only with the same durable snapshot and retirement;
	// return nonterminal errors only after closing the known physical dispatch.
	if kernel.ManagedDigest(snap) != kernel.ManagedDigest(proof.Snapshot()) {
		return snap, true, errors.Join(ErrConflict, runErr)
	}
	if err = r.closeOriginalManagedDispatch(ctx, native, host, proof); err != nil {
		return snap, true, errors.Join(runErr, err)
	}
	if runErr != nil && !terminalRuntimeStatus(snap.Run.Status) {
		return snap, true, runErr
	}
	return snap, true, nil
}

// cancelManagedOriginal is the public root cancellation entrance for an
// unresolved exact managed physical dispatch. PARK arbitrates the expired
// Kernel owner;
// the normal immutable cancellation intent arbitrates the Host RESUME/CANCEL
// choice. Do not use the ordinary callback-free Start exception.
func (r *Runner) cancelManagedOriginal(ctx context.Context, turn Turn, inv Invocation) (Snapshot, bool, error) {
	native, ok := r.agent.(*agent.Runner)
	canceller, nativeCancel := r.cancellation.(*kernel.Runtime)
	host, bound := r.store.(managedHostStore)
	if !ok || !nativeCancel || canceller != r.runtime || !native.ObservesKernelRuntime(r.runtime) ||
		!bound || !r.runtime.UsesManagedRecoveryHost(host) ||
		(!native.SupportsManagedContextRecovery() && !native.SupportsSummaryNativeRecovery()) {
		return Snapshot{}, false, nil
	}
	t, err := r.runtime.LoadManagedRecovery(ctx, inv.ExecutionRefID)
	if kernel.ManagedRecoveryAbsent(err) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, true, err
	}
	b := t.SafePoint.Host
	if b.TurnID != turn.ID || b.InvocationID != inv.ID || b.RunID != inv.ExecutionRefID || uint64(b.Attempt) != uint64(inv.Attempt) {
		return Snapshot{}, true, ErrConflict
	}
	items, err := r.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if err != nil {
		return Snapshot{}, true, err
	}
	receipts := executionDispatchReceiptIndex(items)
	for _, item := range items {
		if item.ID == b.DispatchID {
			if _, settled := VerifiedExecutionDispatchReceipt(item, receipts[item.ID]); settled {
				return Snapshot{}, false, nil
			}
		}
	}
	if t.SafePoint.LifecycleVersion != kernel.ManagedLifecycleAgentObligations || t.Selection.Mode == kernel.ManagedResume {
		return Snapshot{}, false, nil
	}
	if _, err = host.ReadManagedOriginalDispatch(ctx, b); err != nil {
		return Snapshot{}, true, err
	}
	// The parked root has no unresolved native children. This entry cannot
	// close a whole-turn cancellation on behalf of unrelated Host effects.
	// Historical child executions must first use their ordinary cleanup.
	invocations, err := r.store.ListInvocations(ctx, turn.ID)
	if err != nil {
		return Snapshot{}, true, err
	}
	if len(invocations) != 1 || invocations[0].ID != inv.ID {
		return Snapshot{}, true, ErrRuntimeCancellationUnverified
	}
	children, err := r.relatedChildRunIDs(ctx, inv.ExecutionRefID)
	if err != nil {
		return Snapshot{}, true, err
	}
	delegated, err := r.delegatedRunIDs(ctx, turn.ID)
	if err != nil {
		return Snapshot{}, true, err
	}
	if len(children) != 0 || len(delegated) != 0 {
		return Snapshot{}, true, ErrRuntimeCancellationUnverified
	}
	for _, item := range items {
		if item.ParentItemID == ExecutionDispatchParentItemID && item.ID != b.DispatchID {
			if _, settled := VerifiedExecutionDispatchReceipt(item, receipts[item.ID]); !settled {
				return Snapshot{}, true, ErrConflict
			}
		}
	}
	if err = r.checkInteractionCancellation(ctx, turn, ""); err != nil {
		return Snapshot{}, true, err
	}
	// No write-capable waiting Interaction projection is owned by this
	// specialized parked root selection.
	interactions, err := r.store.ListInteractions(ctx, turn.ID)
	if err != nil {
		return Snapshot{}, true, err
	}
	for _, i := range interactions {
		if i.Status == InteractionWaiting {
			return Snapshot{}, true, ErrConflict
		}
	}
	ctx, err = r.runtime.BindManagedDispatch(ctx, b)
	if err != nil {
		return Snapshot{}, true, err
	}
	if t.State != kernel.ManagedCancelled {
		if _, err = native.ParkManagedForCancellation(ctx, inv.ExecutionRefID); err != nil {
			return Snapshot{}, true, err
		}
	}
	// Recover the original elected intent rather than creating a new one
	// after a lost intent/selection/cancellation ACK.
	items, err = r.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if err != nil {
		return Snapshot{}, true, err
	}
	cancelReceipts := cancellationFenceReceipts(items)
	var elected Item
	for _, item := range items {
		if item.ParentItemID != CancellationFenceParentItemID || VerifiedCancellationFenceReceipt(item, cancelReceipts[item.ID]) {
			continue
		}
		source, valid := cancellationProtocol(item)
		if elected.ID != "" || !valid || source != "native" || item.RunID != inv.ExecutionRefID || item.TurnID != turn.ID ||
			!ValidCancellationFenceIntentAdmission(item) || !CancellationEffectsSettled(item, items) {
			return Snapshot{}, true, ErrConflict
		}
		elected = item
	}
	if elected.ID == "" {
		if t.State == kernel.ManagedCancelled {
			return Snapshot{}, true, ErrConflict
		}
		elected, _, err = r.beginCancellationFence(ctx, turn, inv.ExecutionRefID, inv)
		if err != nil {
			return Snapshot{}, true, err
		}
	}
	snap, proof, err := native.CancelManagedContinuation(ctx, inv.ExecutionRefID)
	if err != nil {
		return Snapshot{}, true, err
	}
	if !proof.Matches(proof.Transfer().Selection) || proof.Transfer().Selection.Mode != kernel.ManagedCancel ||
		proof.Transfer().Selection.Cancellation.IntentID != elected.ID || snap.Run.Status != kernel.RunStatusCancelled {
		return Snapshot{}, true, ErrConflict
	}
	if err = host.CloseManagedRecovery(ctx, proof); err != nil {
		return Snapshot{}, true, err
	}
	out, err := r.syncRuntimeSnapshotWithRetryAfterCancellation(ctx, turn, inv, snap)
	return out, true, err
}
