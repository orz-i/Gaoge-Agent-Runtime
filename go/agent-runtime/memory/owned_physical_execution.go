package memory

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.OwnedPhysicalExecutionStore = (*Store)(nil)

// ownedGrantLocked is always called under the Store's authoritative mutex.
func (s *Store) ownedGrantLocked(runID string) (memoryChildGrant, bool) {
	for _, g := range s.childGrants {
		if g.grant.ChildRunID == runID && g.grant.IsOwned() {
			return g, true
		}
	}
	return memoryChildGrant{}, false
}
func (s *Store) BeginOwnedPhysicalExecution(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, p kernel.OwnedPhysicalAdmission) error {
	if s == nil || ctx == nil {
		return kernel.ErrInvalidInput
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.records[g.ChildRunID]
	if !ok {
		return kernel.ErrNotFound
	}
	if !p.Matches(g, current, l) || !activeMemoryLease(s.execution[g.ChildRunID], l, s.currentExecutionTime()) || s.execution[g.ChildRunID].quarantineRev != 0 {
		return kernel.ErrExecutionConflict
	}
	if len(s.childGrantOwners[g.ChildRunID]) != 0 {
		return kernel.ErrChildExecutionBusy
	}
	for _, old := range s.childGrants {
		if old.grant.ID == g.ID || old.grant.ChildRunID == g.ChildRunID {
			return kernel.ErrChildExecutionBusy
		}
	}
	s.childGrants[g.ID] = memoryChildGrant{grant: g, ancestors: []string{g.ChildRunID}}
	s.childGrantOwners[g.ChildRunID] = map[string]struct{}{g.ID: {}}
	return nil
}
func (s *Store) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, revision uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	if s == nil || ctx == nil {
		return kernel.Snapshot{}, kernel.ErrInvalidInput
	}
	if e := ctx.Err(); e != nil {
		return kernel.Snapshot{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.records[g.ChildRunID]
	if !ok {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	original, ok := s.childGrants[g.ID]
	if !ok || original.grant != g || !g.IsOwned() || original.settledRevision != 0 || current.Run.Revision != revision || !p.Matches(g, current, m) {
		return kernel.Snapshot{}, kernel.ErrExecutionConflict
	}
	// Recovery never clears/replaces an epoch. A foreign/replaced native identity
	// cannot be adopted merely because a stale physical row survived.
	if !g.MatchesExecution(s.execution[g.ChildRunID].lease) {
		return kernel.Snapshot{}, kernel.ErrExecutionConflict
	}
	m.PhysicalPermit = p.PhysicalPermit(g, current, m)
	return s.applyLocked(g.ChildRunID, revision, m)
}
