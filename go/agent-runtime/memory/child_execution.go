package memory

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.ChildExecutionStore = (*Store)(nil)

type memoryChildGrant struct {
	grant           kernel.ChildExecutionGrant
	ancestors       []string
	settledRevision uint64
}

func memoryTerminalStatus(status kernel.RunStatus) bool {
	switch status {
	case kernel.RunStatusCompleted, kernel.RunStatusFailed, kernel.RunStatusCancelled:
		return true
	case kernel.RunStatusRunning, kernel.RunStatusWaitingInput, kernel.RunStatusPausedBudget:
		return false
	default:
		return true
	}
}

// childAncestorsLocked checks the immutable whole physical ancestry under the
// same mutex as parent mutations. A copied relation row cannot lend a same-ID
// child the authority to dispatch effects through a foreign parent.
func (store *Store) childAncestorsLocked(child kernel.Run) ([]string, error) {
	seen := map[string]struct{}{child.ID: {}}
	ancestors := make([]string, 0, 4)
	for id := child.ParentRunID; id != ""; {
		if _, cycle := seen[id]; cycle || len(ancestors) >= 64 {
			return nil, kernel.ErrInvalidInput
		}
		seen[id] = struct{}{}
		parent, ok := store.records[id]
		if !ok {
			return nil, kernel.ErrNotFound
		}
		if parent.Run.Actor != child.Actor || parent.Run.Thread != child.Thread ||
			memoryTerminalStatus(parent.Run.Status) {
			return nil, kernel.ErrExecutionConflict
		}
		ancestors = append(ancestors, id)
		id = parent.Run.ParentRunID
	}
	if len(ancestors) == 0 {
		return nil, kernel.ErrInvalidInput
	}
	return ancestors, nil
}

// BeginChildExecution is a persistent physical admission serialized with
// parent Cancel/Fail/Complete, including native and unleased Run transitions.
func (store *Store) BeginChildExecution(ctx context.Context, grant kernel.ChildExecutionGrant) error {
	if store == nil || ctx == nil || grant.ID == "" || grant.ChildRunID == "" || grant.ExpectedRevision == 0 || grant.IsOwned() {
		return kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	child, ok := store.records[grant.ChildRunID]
	if !ok {
		return kernel.ErrNotFound
	}
	if child.Run.Revision != grant.ExpectedRevision ||
		(child.Run.Status != kernel.RunStatusRunning && child.Run.Status != kernel.RunStatusWaitingInput) {
		return kernel.ErrExecutionConflict
	}
	var ancestors []string
	if child.Run.Kind != "workflow" && child.Run.Kind != "agent" {
		return kernel.ErrExecutionRequired
	}
	if child.Run.ParentRunID != "" {
		var err error
		ancestors, err = store.childAncestorsLocked(child.Run)
		if err != nil {
			return err
		}
	}
	// Every physical execution fences its OWN Run and ALL ancestors.
	// The first-party Feature receives a per-revision scoped mutation permit,
	// while unbound Kernel.Cancel/Apply/Control must fail until settlement.
	ancestors = append(ancestors, child.Run.ID)
	if _, exists := store.childGrants[grant.ID]; exists {
		return kernel.ErrChildExecutionBusy
	}
	for _, previous := range store.childGrants {
		if previous.grant.ChildRunID == grant.ChildRunID {
			return kernel.ErrChildExecutionBusy
		}
	}
	store.childGrants[grant.ID] = memoryChildGrant{grant: grant, ancestors: ancestors}
	for _, id := range ancestors {
		if store.childGrantOwners[id] == nil {
			store.childGrantOwners[id] = make(map[string]struct{})
		}
		store.childGrantOwners[id][grant.ID] = struct{}{}
	}
	return nil
}

// MarkChildExecutionSettled requires an exact persisted child revision and
// original Grant. The callback-completion marker cannot be inferred from a
// clean-looking Run while the original physical callback is still active.
func (store *Store) MarkChildExecutionSettled(ctx context.Context, grant kernel.ChildExecutionGrant, permit kernel.ChildExecutionSettlement) error {
	if store == nil || ctx == nil || grant.ID == "" || grant.ChildRunID == "" {
		return kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	original, ok := store.childGrants[grant.ID]
	if !ok || original.grant != grant {
		return kernel.ErrExecutionConflict
	}
	child, ok := store.records[grant.ChildRunID]
	if !ok || !permit.Matches(grant, child) ||
		(original.settledRevision != 0 && original.settledRevision != child.Run.Revision) {
		return kernel.ErrExecutionConflict
	}
	if grant.IsOwned() && original.settledRevision == 0 && !permit.IsRecovery() {
		lease, ok := permit.ExecutionLease()
		if !ok || !grant.MatchesExecution(lease) || !activeMemoryLease(store.execution[grant.ChildRunID], lease, store.currentExecutionTime()) {
			return kernel.ErrExecutionConflict
		}
	}
	if grant.IsOwned() && !grant.MatchesExecution(store.execution[grant.ChildRunID].lease) {
		return kernel.ErrExecutionConflict
	}
	original.settledRevision = child.Run.Revision
	store.childGrants[grant.ID] = original
	return nil
}

// FinishChildExecution requires the original immutable Grant identity. An
// unknown result never calls this port and therefore fences parent terminal
// control until explicit result reconciliation instead of inventing cleanup.
func (store *Store) FinishChildExecution(ctx context.Context, grant kernel.ChildExecutionGrant) error {
	if store == nil || ctx == nil || grant.ID == "" || grant.ChildRunID == "" {
		return kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	previous, ok := store.childGrants[grant.ID]
	if !ok || previous.grant != grant || previous.settledRevision == 0 ||
		store.records[grant.ChildRunID].Run.Revision != previous.settledRevision {
		return kernel.ErrExecutionConflict
	}
	delete(store.childGrants, grant.ID)
	for _, id := range previous.ancestors {
		delete(store.childGrantOwners[id], grant.ID)
		if len(store.childGrantOwners[id]) == 0 {
			delete(store.childGrantOwners, id)
		}
	}
	return nil
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
	for _, value := range store.childGrants {
		if value.grant.ChildRunID != childID {
			continue
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
