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
	if s == nil || ctx == nil || kernel.ValidateOwnedPhysicalAdmissionRequest(g, l) != nil {
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
	if _, err := s.settleRetirementSuccessorLocked(g.ChildRunID); err != nil {
		return err
	}
	native := s.execution[g.ChildRunID]
	hasGrants := len(s.childGrantOwners[g.ChildRunID]) != 0
	for _, old := range s.childGrants {
		if old.grant.ID == g.ID || old.grant.ChildRunID == g.ChildRunID {
			hasGrants = true
		}
	}
	if err := kernel.ValidateOwnedPhysicalAdmission(g, current, l, native.lease, s.currentExecutionTime(), native.quarantineRev != 0, hasGrants, p); err != nil {
		return err
	}
	s.childGrants[g.ID] = memoryChildGrant{grant: g, ancestors: []string{g.ChildRunID}}
	s.childGrantOwners[g.ChildRunID] = map[string]struct{}{g.ID: {}}
	native.physicalEpoch = g.ExecutionEpoch
	s.execution[g.ChildRunID] = native
	return nil
}
func (s *Store) loadOwnedPhysicalGrantLocked(g kernel.ChildExecutionGrant) (memoryChildGrant, error) {
	original, ok := s.childGrants[g.ID]
	if !ok {
		for _, candidate := range s.childGrants {
			if candidate.grant.ChildRunID == g.ChildRunID {
				return memoryChildGrant{}, kernel.ErrExecutionConflict
			}
		}
		return memoryChildGrant{}, kernel.ErrNotFound
	}
	if original.grant != g || len(original.ancestors) != 1 || original.ancestors[0] != g.ChildRunID {
		return memoryChildGrant{}, kernel.ErrExecutionConflict
	}
	return original, nil
}

func (s *Store) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, revision uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	if s == nil || ctx == nil || kernel.ValidateOwnedPhysicalRecoveryRequest(g, revision, m) != nil {
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
	original, err := s.loadOwnedPhysicalGrantLocked(g)
	if err != nil {
		return kernel.Snapshot{}, kernel.ErrExecutionConflict
	}
	if err := kernel.ValidateOwnedPhysicalRecovery(g, current, s.execution[g.ChildRunID].lease, revision, m, p, original.completion, original.settledRevision); err != nil {
		return kernel.Snapshot{}, err
	}
	m.PhysicalPermit = p.PhysicalPermit(g, current, m)
	return s.applyLocked(g.ChildRunID, revision, m)
}

// RecordOwnedPhysicalCallbackReturn changes only the original physical scope,
// never the Run projection, wakeup revision, checkpoint or lease epoch.
func (s *Store) RecordOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, o kernel.OwnedPhysicalCallbackOutcome, p kernel.OwnedPhysicalCallbackReturn) error {
	if s == nil || ctx == nil || !g.IsOwned() || rev == 0 {
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
	original, err := s.loadOwnedPhysicalGrantLocked(g)
	current, exists := s.records[g.ChildRunID]
	if err != nil || !exists {
		return kernel.ErrExecutionConflict
	}
	completion, write, err := kernel.ReduceOwnedPhysicalCallbackReturn(g, current, s.execution[g.ChildRunID].lease, rev, o, p, original.completion, original.settledRevision)
	if err != nil || !write {
		return err
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
	original, err := s.loadOwnedPhysicalGrantLocked(g)
	if err != nil {
		return kernel.OwnedPhysicalCallbackCompletion{}, err
	}
	if err := kernel.ValidateOwnedPhysicalCompletion(g, original.completion); err != nil {
		return kernel.OwnedPhysicalCallbackCompletion{}, err
	}
	return original.completion, nil
}
