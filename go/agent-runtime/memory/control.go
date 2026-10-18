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
	current, ok := store.records[runID]
	if !ok {
		return kernel.Snapshot{}, false, kernel.ErrNotFound
	}
	if current.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, false, kernel.ErrConflict
	}
	if err := kernel.ValidateControl(current, guard, mutation, store.currentExecutionTime()); err != nil {
		return kernel.Snapshot{}, false, err
	}
	authority := store.execution[runID]
	if authority.quarantineRev != 0 && mutation.Record.Run.Status == kernel.RunStatusRunning {
		// An unrelated validated control cannot age out a poisoned work
		// identity while the same Feature wait remains unresolved.
		return kernel.Snapshot{}, false, kernel.ErrExecutionQuarantined
	}
	if authority.epoch == math.MaxUint64 {
		return kernel.Snapshot{}, false, kernel.ErrExecutionConflict
	}
	// Invalidate even a still-live external worker before committing the
	// state and wakeup; Epoch=0 stays legacy/unclaimed.
	revoked := authority.epoch > 0
	if revoked {
		authority.epoch++
		authority.lease = kernel.ExecutionLease{}
		store.execution[runID] = authority
	}
	snapshot, err := store.applyLocked(runID, expectedRevision, mutation)
	return snapshot, revoked, err
}
