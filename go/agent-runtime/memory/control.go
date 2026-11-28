package memory

import (
	"context"
	"math"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.ControlStore = (*Store)(nil)

// ApplyControl is the one trusted gate/termination write path. Feature-owned
// approval/allowance validators run before calling this Store capability;
// these structural guards are rechecked under the same lock as CAS.
func (store *Store) ApplyControl(ctx context.Context, runID string, expectedRevision uint64,
	guard kernel.ControlGuard, mutation kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	if store == nil || ctx == nil || runID == "" || expectedRevision == 0 {
		return kernel.Snapshot{}, false, kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return kernel.Snapshot{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	p, managed, err := guard.ManagedCancellation(runID, expectedRevision, mutation)
	if err != nil {
		return kernel.Snapshot{}, false, err
	}
	if !managed {
		if err = store.nativeEntryGateLocked(runID); err != nil {
			return kernel.Snapshot{}, false, err
		}
	}
	current, ok := store.snapshotLocked(runID)
	if !ok {
		return kernel.Snapshot{}, false, kernel.ErrNotFound
	}
	authority := store.runs[runID].execution
	var transfer kernel.ManagedTransfer
	var predecessor memoryEffect
	var consumed *kernel.ChildExecutionGrant
	if managed {
		t, exists := store.managedTransfers[store.managedCurrent[runID]]
		if !exists || t.SafePoint.Host.RunID != runID || t.SafePoint.Host.KernelAuthority != store.managedAuthority {
			return kernel.Snapshot{}, false, kernel.ErrExecutionConflict
		}
		if kernel.ManagedCancelReplay(t, p, current) {
			return cloneSnapshot(current), false, nil
		}
		g := kernel.ManagedCurrentGrant(t)
		predecessor, ok = store.activeEffect(g.ID)
		if !ok || !store.managedSoleGrantLocked(runID, g.ID) || predecessor.grant != g || predecessor.completion.GrantID != "" ||
			!g.MatchesExecution(authority.lease) || authority.epoch != g.ExecutionEpoch || authority.epoch == math.MaxUint64 || authority.quarantineRev != 0 {
			return kernel.Snapshot{}, false, kernel.ErrExecutionConflict
		}
		if e := store.managedCancellationQuiescentLocked(runID); e != nil {
			return kernel.Snapshot{}, false, e
		}
		transfer, mutation, err = kernel.PlanManagedCancellation(ctx, t, p, current, authority.lease, g, store.managedAuthority, authority.physicalEpoch, store.currentExecutionTime(), store.ledgers)
		if err != nil {
			return kernel.Snapshot{}, false, err
		}
		consumed = &g
	}
	if current.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, false, kernel.ErrConflict
	}
	if guard.Kind == kernel.ControlApproval && current.Run.ParentRunID != "" {
		if _, err := store.validateLiveRunOwners(ctx, current.Run); err != nil {
			return kernel.Snapshot{}, false, err
		}
	}
	if err := kernel.ValidateControl(current, guard, mutation, store.currentExecutionTime()); err != nil {
		return kernel.Snapshot{}, false, err
	}
	if authority.quarantineRev != 0 && mutation.Record.Run.Status == kernel.RunStatusRunning {
		// An unrelated validated control cannot age out a poisoned work
		// identity while the same Feature wait remains unresolved.
		return kernel.Snapshot{}, false, kernel.ErrExecutionQuarantined
	}
	// A child physical Grant requires an original, per-revision permit
	// for control writes as well as ordinary Apply. Check BEFORE revoking
	// a Run Execution epoch so a rejected Cancel is truly no-mutation.
	ownedApproval := false
	for id := range store.effectOwners[runID] {
		original, ok := store.activeEffect(id)
		if !ok {
			return kernel.Snapshot{}, false, kernel.ErrExecutionConflict
		}
		if consumed != nil && original.grant == *consumed {
			continue
		}
		if original.grant.IsOwned() {
			lease, hasLease := mutation.PhysicalPermit.ExecutionLease()
			if guard.Kind != kernel.ControlApproval || !hasLease || !original.grant.MatchesExecution(lease) || !activeMemoryLease(authority, lease, store.currentExecutionTime()) || !mutation.PhysicalPermit.Matches(original.grant, current) {
				return kernel.Snapshot{}, false, kernel.ErrChildExecutionBusy
			}
			ownedApproval = true
		}
		if original.grant.ChildRunID == runID {
			if !mutation.PhysicalPermit.Matches(original.grant, current) {
				return kernel.Snapshot{}, false, kernel.ErrChildExecutionBusy
			}
		} else {
			return kernel.Snapshot{}, false, kernel.ErrChildExecutionBusy
		}
	}
	if authority.epoch == math.MaxUint64 {
		return kernel.Snapshot{}, false, kernel.ErrExecutionConflict
	}
	// Commit state, accounting, wakeup and revocation under the same mutex.
	// A rejected mutation must not invalidate a still-live worker.
	// Epoch=0 stays legacy/unclaimed.
	revoked := authority.epoch > 0 && !ownedApproval
	snapshot, err := store.applyLocked(ctx, runID, expectedRevision, mutation, true, consumed)
	if err != nil {
		return kernel.Snapshot{}, false, err
	}
	if consumed != nil {
		store.consumeEffectLocked(predecessor, nil)
		transfer.Terminal = &snapshot
		store.saveManagedLocked(transfer)
	}
	if revoked {
		authority.epoch++
		authority.lease = kernel.ExecutionLease{}
		store.setExecutionLocked(runID, authority)
	}
	return snapshot, revoked, nil
}
