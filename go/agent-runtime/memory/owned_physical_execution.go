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
	if err := s.managedGateLocked(g.ChildRunID); err != nil {
		return err
	}
	if _, retired := s.ownedRetirements[g.ID]; retired {
		return kernel.ErrExecutionConflict
	}
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
	if err := s.managedGateLocked(g.ChildRunID); err != nil {
		return kernel.Snapshot{}, err
	}
	current, ok := s.records[g.ChildRunID]
	if !ok {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	original, ok := s.childGrants[g.ID]
	if !ok || original.grant != g || !g.IsOwned() || current.Run.Kind != "agent" || current.Run.ParentRunID != "" || current.Run.Status != kernel.RunStatusRunning || original.settledRevision != 0 || original.completion.GrantID != g.ID || original.completion.HostCompleted == p.IsModelEvidence() {
		return kernel.Snapshot{}, kernel.ErrExecutionConflict
	}
	// Recovery never clears/replaces an epoch. A foreign/replaced native identity
	// cannot be adopted merely because a stale physical row survived.
	if !g.MatchesExecution(s.execution[g.ChildRunID].lease) {
		return kernel.Snapshot{}, kernel.ErrExecutionConflict
	}
	if current.Run.Revision != revision {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	if !p.Matches(g, current, m) {
		return kernel.Snapshot{}, kernel.ErrExecutionConflict
	}
	m.PhysicalPermit = p.PhysicalPermit(g, current, m)
	return s.applyLocked(g.ChildRunID, revision, m)
}

// RecordOwnedPhysicalCallbackReturn changes only the original physical scope,
// never the Run projection, wakeup revision, checkpoint or lease epoch.
func (s *Store) RecordOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, o kernel.OwnedPhysicalCallbackOutcome, p kernel.OwnedPhysicalCallbackReturn) error {
	if s == nil || ctx == nil {
		return kernel.ErrInvalidInput
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.managedGateLocked(g.ChildRunID); err != nil {
		return err
	}
	original, ok := s.childGrants[g.ID]
	current, exists := s.records[g.ChildRunID]
	if !ok || !exists || original.grant != g || !g.IsOwned() || current.Run.Revision != rev || !g.MatchesExecution(s.execution[g.ChildRunID].lease) || !p.Matches(g, current, o) {
		return kernel.ErrExecutionConflict
	}
	completion := p.Completion(g, current, o)
	if original.completion.GrantID != "" {
		if original.completion != completion {
			return kernel.ErrExecutionConflict
		}
		return nil
	}
	if original.settledRevision != 0 {
		return kernel.ErrExecutionConflict
	}
	original.completion = completion
	s.childGrants[g.ID] = original
	return nil
}
func (s *Store) LookupOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.OwnedPhysicalCallbackCompletion, error) {
	if s == nil || ctx == nil || !g.IsOwned() {
		return kernel.OwnedPhysicalCallbackCompletion{}, kernel.ErrInvalidInput
	}
	if e := ctx.Err(); e != nil {
		return kernel.OwnedPhysicalCallbackCompletion{}, e
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	original, ok := s.childGrants[g.ID]
	if !ok {
		return kernel.OwnedPhysicalCallbackCompletion{}, kernel.ErrNotFound
	}
	if original.grant != g {
		return kernel.OwnedPhysicalCallbackCompletion{}, kernel.ErrExecutionConflict
	}
	if original.completion.GrantID == "" {
		return kernel.OwnedPhysicalCallbackCompletion{}, kernel.ErrNotFound
	}
	return original.completion, nil
}
