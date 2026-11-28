package memory

import (
	"context"
	"math"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func validMemoryQuarantine(value kernel.ExecutionQuarantine) bool {
	return value.RunID != "" && strings.TrimSpace(value.RunID) == value.RunID &&
		value.Revision > 0 && value.Reason != "" && len(value.Reason) <= 64 &&
		strings.TrimSpace(value.Reason) == value.Reason
}

// QuarantineExecution atomically revokes the current lease, without advancing
// the Feature's opaque Run state or revision. One diagnosis belongs to the
// exact current revision/generation, not all future progress of this Run.
func (store *Store) QuarantineExecution(ctx context.Context, value kernel.ExecutionQuarantine) error {
	if store == nil || ctx == nil || !validMemoryQuarantine(value) {
		return kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.nativeEntryGateLocked(value.RunID); err != nil {
		return err
	}
	snapshot, readErr := store.snapshotLocked(value.RunID)
	if readErr != nil {
		return readErr
	}
	if snapshot.Run.Revision != value.Revision {
		return kernel.ErrConflict
	}
	if snapshot.Run.Status != kernel.RunStatusRunning {
		return kernel.ErrExecutionRequired
	}
	if _, exists := store.ownedGrantLocked(value.RunID); exists {
		return kernel.ErrChildExecutionBusy
	}
	state := store.runs[value.RunID].execution
	if state.epoch == 0 {
		return kernel.ErrExecutionRequired
	}
	if state.quarantineRev != 0 {
		if state.quarantineGen == value.Generation && state.quarantineCode == value.Reason {
			return nil
		}
		return kernel.ErrConflict
	}
	if state.epoch == math.MaxUint64 {
		return kernel.ErrExecutionConflict
	}
	state = store.runs[value.RunID].execution
	state.epoch++
	state.lease = kernel.ExecutionLease{}
	state.quarantineRev, state.quarantineGen, state.quarantineCode =
		value.Revision, value.Generation, value.Reason
	store.setExecutionLocked(value.RunID, state)
	return nil
}

// RepairExecutionQuarantine is trusted-internal. The embedding host MUST
// authorize the operator and verify any unknown Provider effects first.
func (store *Store) RepairExecutionQuarantine(ctx context.Context, value kernel.ExecutionQuarantine) error {
	if store == nil || ctx == nil || !validMemoryQuarantine(value) {
		return kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.nativeEntryGateLocked(value.RunID); err != nil {
		return err
	}
	snapshot, readErr := store.snapshotLocked(value.RunID)
	if readErr != nil {
		return readErr
	}
	state := store.runs[value.RunID].execution
	if snapshot.Run.Revision != value.Revision || snapshot.Run.Status != kernel.RunStatusRunning ||
		state.quarantineRev != value.Revision || state.quarantineGen != value.Generation ||
		state.quarantineCode != value.Reason {
		return kernel.ErrConflict
	}
	state.quarantineRev, state.quarantineGen, state.quarantineCode = 0, 0, ""
	store.setExecutionLocked(value.RunID, state)
	return nil
}
