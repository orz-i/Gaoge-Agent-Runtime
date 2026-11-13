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
	if !ok || !native.SupportsManagedContextRecovery() || inv.ParentItemID != "" || inv.ExecutionClass != ExecutionAgent {
		return ctx, nil
	}
	phase, ok := ValidExecutionDispatchGrantAdmission(dispatch)
	if !ok || phase != "start" {
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
	return r.runtime.BindManagedDispatch(ctx, b)
}

// recoverManagedInvocation does not open a new physical dispatch. The Core
// entry performs Kernel PARK, immutable Host reservation and unique ENABLE.
func (r *Runner) recoverManagedInvocation(ctx context.Context, inv Invocation) (kernel.Snapshot, bool, error) {
	native, ok := r.agent.(*agent.Runner)
	if !ok || !native.SupportsManagedContextRecovery() || inv.ParentItemID != "" || inv.ExecutionClass != ExecutionAgent {
		return kernel.Snapshot{}, false, nil
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
	// A normal already-returned Start may also have historical INVALIDATED
	// safe points. It needs ordinary Resume, not the interrupted-start path.
	items, itemsErr := r.loadGlobalPhysicalEvidence(ctx, inv.TurnID)
	if itemsErr != nil {
		return kernel.Snapshot{}, true, itemsErr
	}
	for _, item := range items {
		if item.ID != b.DispatchID {
			continue
		}
		if boundary, settled := VerifiedExecutionDispatchReceipt(item, items); settled &&
			boundary != "managed_dispatch_transferred" && boundary != "managed_dispatch_cancelled_before_next_effect" {
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
		if closeErr := host.CloseManagedRecovery(ctx, proof); closeErr != nil {
			return kernel.Snapshot{}, true, closeErr
		}
		return proof.Snapshot(), true, nil
	}
	snap, proof, err := native.RecoverManagedContinuation(ctx, inv.ExecutionRefID)
	if err != nil {
		return snap, true, err
	}
	if err = host.CloseManagedRecovery(ctx, proof); err != nil {
		return snap, true, err
	}
	return snap, true, nil
}
