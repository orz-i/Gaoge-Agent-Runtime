package memory

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// A physical operation has one stable record for its entire lifetime. An
// active record owns its original Grant and live ancestor index. Once
// consumed, only its immutable identity or owned retirement fact remains.
// No identity may be reused, including after a managed transfer/cancel.
type memoryEffect struct {
	grant      kernel.ChildExecutionGrant
	ancestors  []string
	completion kernel.PhysicalCallbackCompletion
	retirement *kernel.OwnedRetirementReceipt
}

func (e memoryEffect) active() bool { return len(e.ancestors) != 0 }

// The owner map is an index of active operations, never a second authority.
func (s *Store) activeEffect(id string) (memoryEffect, bool) {
	e, ok := s.effects[id]
	return e, ok && e.active()
}

func (s *Store) consumeEffectLocked(e memoryEffect, retirement *kernel.OwnedRetirementReceipt) {
	id := e.grant.ID
	if retirement == nil {
		s.effects[id] = memoryEffect{grant: e.grant}
	} else {
		// The retirement receipt already contains the complete original identity,
		// completion and Run snapshot. Do not retain a duplicate live record.
		s.effects[id] = memoryEffect{retirement: retirement}
	}
	for _, owner := range e.ancestors {
		delete(s.effectOwners[owner], id)
		if len(s.effectOwners[owner]) == 0 {
			delete(s.effectOwners, owner)
		}
	}
}

func (store *Store) validateLiveRunOwners(ctx context.Context, run kernel.Run) ([]string, error) {
	return kernel.ValidateLiveRunOwners(ctx, run, func(_ context.Context, id string) (kernel.Snapshot, error) {
		if err := store.nativeEntryGateLocked(id); err != nil {
			return kernel.Snapshot{}, err
		}
		parent, readErr := store.snapshotLocked(id)
		if readErr != nil {
			return kernel.Snapshot{}, readErr
		}
		return parent, nil
	})
}

// FinishChildExecution requires the original immutable Grant identity. An
// unknown result never calls this port and therefore fences parent terminal
// control until explicit result reconciliation instead of inventing cleanup.
func (store *Store) FinishChildExecution(ctx context.Context, grant kernel.ChildExecutionGrant, permit kernel.ChildExecutionSettlement) error {
	if store == nil || ctx == nil || grant.ID == "" || grant.ChildRunID == "" {
		return kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.nativeEntryGateLocked(grant.ChildRunID); err != nil {
		return err
	}
	previous, ok := store.activeEffect(grant.ID)
	if !ok || previous.grant != grant {
		return kernel.ErrExecutionConflict
	}
	if err := store.validateEffectOwnersLocked(previous); err != nil {
		return err
	}
	return store.retireOwnedLocked(previous, permit)
}

func (store *Store) LookupChildExecution(ctx context.Context, childID string) (kernel.ChildExecutionGrant, error) {
	if store == nil || ctx == nil || childID == "" {
		return kernel.ChildExecutionGrant{}, kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return kernel.ChildExecutionGrant{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	var candidate kernel.ChildExecutionGrant
	for id := range store.effectOwners[childID] {
		value, ok := store.activeEffect(id)
		if !ok {
			return kernel.ChildExecutionGrant{}, kernel.ErrExecutionConflict
		}
		if value.grant.ChildRunID != childID {
			continue
		}
		if err := store.validateEffectOwnersLocked(value); err != nil {
			return kernel.ChildExecutionGrant{}, err
		}
		if candidate.ID != "" {
			return kernel.ChildExecutionGrant{}, kernel.ErrExecutionConflict
		}
		candidate = value.grant
	}
	if candidate.ID == "" {
		return kernel.ChildExecutionGrant{}, kernel.ErrNotFound
	}
	return candidate, nil
}
