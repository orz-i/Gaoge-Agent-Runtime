package harness

import (
	"context"
	"errors"
	"math"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type managedHostStore interface {
	kernel.ManagedRecoveryHostStore
	CloseManagedRecovery(context.Context, kernel.ManagedClosureProof) error
}

// bindManagedOriginalDispatch is called only AFTER actual native Root dispatch
// admission. The source is read back, then revalidated under the Host Turn lock.
func (r *Runner) bindManagedOriginalDispatch(ctx context.Context, inv Invocation, dispatch Item) (context.Context, error) {
	native, ok := r.agent.(*agent.Runner)
	if !ok || !native.SupportsManagedRecovery() || inv.ParentItemID != "" {
		return ctx, nil
	}
	phase, ok := ValidExecutionDispatchGrantAdmission(dispatch)
	if _, managed := kernel.ManagedDispatchOperation(phase); !ok || !managed || inv.Attempt != 1 {
		return ctx, nil
	}
	saved, err := r.store.GetItem(ctx, dispatch.ID)
	if err != nil {
		return ctx, err
	}
	if savedPhase, valid := VerifiedExecutionDispatchGrant(saved); !valid || savedPhase != phase {
		return ctx, ErrConflict
	}
	var approval *kernel.ManagedApprovalDecision
	if phase == "resolve-approval" {
		var valid bool
		approval, valid = originalDispatchApproval(saved)
		if !valid {
			return ctx, ErrConflict
		}
	}
	return r.bindManagedRoot(ctx, inv, phase, saved.ID, saved, approval)
}

// Both native commands and legacy outstanding dispatches seal the same
// immutable Host selection. Only the old path needs a Host execution Item.
func (r *Runner) bindManagedRoot(ctx context.Context, inv Invocation, phase, id string, dispatch Item, approval *kernel.ManagedApprovalDecision) (context.Context, error) {
	host, ok := r.store.(managedHostStore)
	if !ok || !r.runtime.UsesManagedRecoveryHost(host) {
		return ctx, kernel.ErrExecutionRequired
	}
	if inv.Attempt <= 0 || uint64(inv.Attempt) > math.MaxUint32 {
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
	ka, err := r.runtime.ManagedRecoveryAuthority(ctx)
	if err != nil {
		return ctx, err
	}
	ha, err := host.ManagedRecoveryAuthority(ctx)
	if err != nil {
		return ctx, err
	}
	b := kernel.ManagedHostBinding{KernelAuthority: ka, HostAuthority: ha, TurnID: turn.ID, InvocationID: inv.ID, Attempt: uint32(inv.Attempt), RunID: inv.ExecutionRefID, DispatchID: id, Phase: phase, Approval: approval}
	if b.DirectNativeCommand() {
		b.Source, err = nativeHostSource(session, turn, inv, config, b)
	} else {
		b.Source, err = ManagedHostSource(session, turn, inv, config, dispatch)
	}
	if err != nil {
		return ctx, err
	}
	return r.runtime.BindManagedDispatch(ctx, b)
}

// Native root commands use the Host's already frozen selection, with no new
// Host dispatch grant. The correlation is consumed only by the native Grant
// and its approval journal. Managed recovery seals these same Host facts.
func (r *Runner) withNativeRootCommand(ctx context.Context, native *agent.Runner, inv Invocation, phase string, approval *kernel.ManagedApprovalDecision, physical func(context.Context) (kernel.Snapshot, error)) (kernel.Snapshot, error) {
	id, err := r.runtime.NewID("nativecmd")
	if err != nil {
		return kernel.Snapshot{}, err
	}
	id = "nativecmd_" + kernel.ManagedDigest(struct{ Invocation, Run, Attempt string }{inv.ID, inv.ExecutionRefID, id})
	if native.SupportsManagedRecovery() && inv.Attempt == 1 {
		ctx, err = r.bindManagedRoot(ctx, inv, phase, id, Item{}, approval)
		if err != nil {
			return kernel.Snapshot{}, err
		}
	}
	op := plugin.RunResume
	if approval != nil {
		op = plugin.RunResolveApproval
	}
	snap, _, err := native.ObservePhysicalDispatch(ctx, inv.ExecutionRefID, op, id, physical)
	return snap, err
}

// Direct recovery is qualified by the native original Grant/retirement. A
// normally returned source is historical, not a second Host pending dispatch.
func (r *Runner) directManagedPending(ctx context.Context, native *agent.Runner, t kernel.ManagedTransfer) (bool, error) {
	if t.Resolved() {
		return t.Selection.Mode == kernel.ManagedCancel, nil
	}
	if t.Selection.Mode != "" {
		return true, nil
	}
	b := t.SafePoint.Host
	op := plugin.RunResume
	if b.Approval != nil {
		op = plugin.RunResolveApproval
	}
	kind, err := native.ReconcileOriginalPhysicalDispatch(ctx, b.RunID, op, b.DispatchID)
	return kind == agent.PhysicalCallUnknown, err
}

// recoverReturnedManagedDispatch is receipt-only. Refresh can close a CLOSED
// original RESUME without entering native admission or the execution path.
func (r *Runner) recoverReturnedManagedDispatch(ctx context.Context, native *agent.Runner, inv Invocation, dispatch Item) (bool, error) {
	if inv.ParentItemID != "" || inv.Attempt != 1 ||
		!native.SupportsManagedRecovery() {
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
	if !t.Resolved() || t.Selection.Mode != kernel.ManagedResume {
		return false, nil
	}
	proof, err := r.runtime.ManagedRecoveryClosure(ctx, t.SafePoint.ID)
	if err != nil || !proof.Matches(t.Selection) {
		return false, errors.Join(ErrConflict, err)
	}
	if err = host.CloseManagedRecovery(ctx, proof); err != nil {
		return false, err
	}
	return true, nil
}

// recoverManagedInvocation does not open a new physical dispatch. The Core
// entry checks the original source, reads Host authorization and commits native admission.
func (r *Runner) recoverManagedInvocation(ctx context.Context, inv Invocation) (kernel.Snapshot, bool, error) {
	native, ok := r.agent.(*agent.Runner)
	if !ok || !native.SupportsManagedRecovery() || inv.ParentItemID != "" {
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
	if b.DirectNativeCommand() {
		pending, e := r.directManagedPending(ctx, native, transfer)
		if e != nil || !pending {
			return kernel.Snapshot{}, e != nil, e
		}
	} else {
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
					res := transfer.Selection
					if !res.Valid() || transfer.Terminal == nil {
						return kernel.Snapshot{}, true, ErrConflict
					}
					fields, valid := authorityStringFields(receipts[item.ID][0].Payload, "boundary", "recoveryID", "mode", "selectionHash", "terminalHash")
					if !valid || fields["recoveryID"] != transfer.SafePoint.RecoveryID || fields["mode"] != string(res.Mode) || fields["selectionHash"] != kernel.ManagedDigest(res) || fields["terminalHash"] != kernel.ManagedDigest(*transfer.Terminal) {
						return kernel.Snapshot{}, true, ErrConflict
					}
					if transfer.Resolved() && res.Mode == kernel.ManagedResume {
						receipt, e := r.runtime.LoadOwnedRetirement(ctx, transfer.SuccessorGrant.ID)
						if e != nil || !receipt.MatchesManagedClosure(transfer, *transfer.Terminal) {
							return kernel.Snapshot{}, true, errors.Join(ErrConflict, e)
						}
						return kernel.Snapshot{}, false, nil
					}
					if transfer.Resolved() && res.Mode == kernel.ManagedCancel {
						if e := r.verifyManagedCancelledProjection(ctx, inv, b.DispatchID); e != nil {
							return kernel.Snapshot{}, true, e
						}
						return *transfer.Terminal, true, nil
					}
					return kernel.Snapshot{}, true, ErrConflict
				}
				// A legacy parked or selected native frontier cannot be settled by an
				// ordinary callback receipt. Host projection is not a native return.
				if transfer.LegacyState == "PARKED" || transfer.Selection.Mode != "" {
					return kernel.Snapshot{}, true, ErrConflict
				}
				return kernel.Snapshot{}, false, nil
			}
		}
		if original.ID == "" || len(receipts[b.DispatchID]) != 0 {
			return kernel.Snapshot{}, true, ErrConflict
		}
		if transfer.Selection.Mode == "" {
			if repaired, e := r.recoverOriginalPhysicalDispatch(ctx, inv); e != nil {
				return kernel.Snapshot{}, true, e
			} else if repaired {
				return kernel.Snapshot{}, false, nil
			}
		}
	}
	host, ok := r.store.(managedHostStore)
	if !ok || !r.runtime.UsesManagedRecoveryHost(host) {
		return kernel.Snapshot{}, true, kernel.ErrExecutionRequired
	}
	// An already committed native closure needs projection only, never a
	// Host reservation lookup or a second dispatch admission.
	if transfer.Resolved() {
		proof, proofErr := r.runtime.ManagedRecoveryClosure(ctx, transfer.SafePoint.ID)
		if proofErr != nil || !proof.Matches(transfer.Selection) {
			return kernel.Snapshot{}, true, errors.Join(ErrConflict, proofErr)
		}
		if transfer.Selection.Mode == kernel.ManagedCancel {
			if e := r.verifyManagedCancelledProjection(ctx, inv, b.DispatchID); e != nil {
				return kernel.Snapshot{}, true, e
			}
		}
		if !b.DirectNativeCommand() {
			if closeErr := host.CloseManagedRecovery(ctx, proof); closeErr != nil {
				return kernel.Snapshot{}, true, closeErr
			}
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
	if snap.Run.Status == kernel.RunStatusCancelled {
		if e := r.verifyManagedCancelledProjection(ctx, inv, b.DispatchID); e != nil {
			return snap, true, errors.Join(runErr, e)
		}
	}
	if !b.DirectNativeCommand() {
		if err = host.CloseManagedRecovery(ctx, proof); err != nil {
			return snap, true, errors.Join(runErr, err)
		}
	}
	if runErr != nil && !terminalRuntimeStatus(snap.Run.Status) {
		return snap, true, runErr
	}
	return snap, true, nil
}

// cancelManagedOriginal passes the authorized root cancellation to the same
// native transaction as recovery admission; it creates no Host election.
func (r *Runner) cancelManagedOriginal(ctx context.Context, turn Turn, inv Invocation, reason string) (Snapshot, bool, error) {
	native, ok := r.agent.(*agent.Runner)
	canceller, nativeCancel := r.cancellation.(*kernel.Runtime)
	host, bound := r.store.(managedHostStore)
	if !ok || !nativeCancel || canceller != r.runtime || !native.ObservesKernelRuntime(r.runtime) ||
		!bound || !r.runtime.UsesManagedRecoveryHost(host) ||
		!native.SupportsManagedRecovery() {
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
	if b.DirectNativeCommand() {
		pending, e := r.directManagedPending(ctx, native, t)
		if e != nil || !pending {
			return Snapshot{}, e != nil, e
		}
	} else {
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
	}
	if t.SafePoint.LifecycleVersion != kernel.ManagedLifecycleAgentObligations || t.Selection.Mode == kernel.ManagedResume {
		return Snapshot{}, false, nil
	}
	if err = r.verifyManagedCancellationScope(ctx, turn, inv, b.DispatchID); err != nil {
		return Snapshot{}, true, err
	}
	if !t.Resolved() {
		ctx, err = r.runtime.BindManagedDispatch(ctx, b)
		if err != nil {
			return Snapshot{}, true, err
		}
	}
	// No new Host cancellation fence or mode reservation. The already
	// authorized command competes in the original native execution transaction.
	snap, proof, err := native.CancelManagedContinuation(ctx, inv.ExecutionRefID, reason)
	if err != nil {
		return Snapshot{}, true, err
	}
	if !proof.Matches(proof.Transfer().Selection) || proof.Transfer().Selection.Mode != kernel.ManagedCancel || snap.Run.Status != kernel.RunStatusCancelled {
		return Snapshot{}, true, ErrConflict
	}
	if err = r.verifyManagedCancellationScope(ctx, turn, inv, b.DispatchID); err != nil {
		return Snapshot{}, true, err
	}
	if !b.DirectNativeCommand() {
		if err = host.CloseManagedRecovery(ctx, proof); err != nil {
			return Snapshot{}, true, err
		}
	}
	out, err := r.syncRuntimeSnapshotWithRetryAfterCancellation(ctx, turn, inv, snap)
	return out, true, err
}

func (r *Runner) verifyManagedCancelledProjection(ctx context.Context, inv Invocation, dispatchID string) error {
	turn, err := loadVerifiedTurn(ctx, r.store, inv.TurnID)
	if err != nil {
		return err
	}
	return r.verifyManagedCancellationScope(ctx, turn, inv, dispatchID)
}

// A root cancellation proves only the original native scope. Both direct
// cancellation and replay must establish that no unrelated Host or child
// effects need cleanup before claiming whole-turn cancellation.
func (r *Runner) verifyManagedCancellationScope(ctx context.Context, turn Turn, inv Invocation, dispatchID string) error {
	invocations, err := r.store.ListInvocations(ctx, turn.ID)
	if err != nil {
		return err
	}
	if len(invocations) != 1 || invocations[0].ID != inv.ID {
		return ErrRuntimeCancellationUnverified
	}
	children, err := r.relatedChildRunIDs(ctx, inv.ExecutionRefID)
	if err != nil {
		return err
	}
	delegated, err := r.delegatedRunIDs(ctx, turn.ID)
	if err != nil {
		return err
	}
	if len(children) != 0 || len(delegated) != 0 {
		return ErrRuntimeCancellationUnverified
	}
	items, err := r.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if err != nil {
		return err
	}
	receipts := executionDispatchReceiptIndex(items)
	for _, item := range items {
		if item.ParentItemID == ExecutionDispatchParentItemID && item.ID != dispatchID {
			if _, settled := VerifiedExecutionDispatchReceipt(item, receipts[item.ID]); !settled {
				return ErrConflict
			}
		}
	}
	if err = r.checkInteractionCancellation(ctx, turn, ""); err != nil {
		return err
	}
	// No write-capable waiting Interaction projection is owned by this
	// specialized parked root selection.
	interactions, err := r.store.ListInteractions(ctx, turn.ID)
	if err != nil {
		return err
	}
	for _, i := range interactions {
		if i.Status == InteractionWaiting {
			return ErrConflict
		}
	}
	return nil
}
