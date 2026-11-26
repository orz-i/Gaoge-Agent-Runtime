package memory

import (
	"context"
	"encoding/json"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.OwnedRetirementStore = (*Store)(nil)

func cloneOwnedRetirement(r kernel.OwnedRetirementReceipt) kernel.OwnedRetirementReceipt {
	b, _ := json.Marshal(r)
	var out kernel.OwnedRetirementReceipt
	_ = json.Unmarshal(b, &out)
	return out
}

// retireOwnedLocked commits the exact managed closure with ordinary retirement.
func (s *Store) retireOwnedLocked(previous memoryChildGrant, permit kernel.ChildExecutionSettlement) error {
	g := previous.grant
	current := s.records[g.ChildRunID]
	native := s.execution[g.ChildRunID]
	if native.epoch != g.ExecutionEpoch || native.physicalEpoch != g.ExecutionEpoch ||
		native.quarantineRev != 0 || len(previous.ancestors) != 1 || previous.ancestors[0] != g.ChildRunID ||
		len(s.childGrantOwners[g.ChildRunID]) != 1 {
		return kernel.ErrExecutionConflict
	}
	if _, ok := s.childGrantOwners[g.ChildRunID][g.ID]; !ok {
		return kernel.ErrExecutionConflict
	}
	if _, exists := s.ownedRetirements[g.ID]; exists {
		return kernel.ErrExecutionConflict
	}
	closures, err := kernel.CloseManagedRetirement(g, current, s.managedAuthority, s.managedTransfers[s.managedCurrent[g.ChildRunID]])
	if err != nil {
		return err
	}
	r, err := kernel.BuildOwnedRetirementReceipt(g, cloneSnapshot(current), s.execution[g.ChildRunID].lease, previous.completion, permit, closures)
	if err != nil {
		return err
	}
	if s.ownedRetirements == nil {
		s.ownedRetirements = make(map[string]kernel.OwnedRetirementReceipt)
	}
	s.ownedRetirements[g.ID] = cloneOwnedRetirement(r)
	if r.Evidence.Completion.LeaseOwned {
		native.lease = kernel.ExecutionLease{}
		s.execution[g.ChildRunID] = native
	}
	return nil
}
func (s *Store) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	if s == nil || ctx == nil || id == "" {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrInvalidInput
	}
	if e := ctx.Err(); e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.ownedRetirements[id]
	if !ok {
		return r, kernel.ErrNotFound
	}
	if !r.Valid() || r.Evidence.Grant.ID != id {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrExecutionConflict
	}
	return cloneOwnedRetirement(r), nil
}
func (s *Store) LookupOwnedRetirementForRun(ctx context.Context, runID string) (kernel.OwnedRetirementReceipt, error) {
	if s == nil || ctx == nil || runID == "" {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrInvalidInput
	}
	if e := ctx.Err(); e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := make([]kernel.OwnedRetirementReceipt, 0)
	for id, r := range s.ownedRetirements {
		if r.Evidence.Grant.ChildRunID != runID {
			continue
		}
		if r.Evidence.Grant.ID != id {
			return kernel.OwnedRetirementReceipt{}, kernel.ErrExecutionConflict
		}
		rows = append(rows, r)
	}
	native := s.execution[runID]
	hasGrants := len(s.childGrantOwners[runID]) != 0
	for _, g := range s.childGrants {
		if g.grant.ChildRunID == runID {
			hasGrants = true
		}
	}
	if e := s.nativeEntryGateLocked(runID); e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	if native.quarantineRev != 0 {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrExecutionConflict
	}
	r, e := kernel.SelectOwnedRetirementForRun(runID, rows, s.records[runID], native.epoch, native.lease, hasGrants)
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	return cloneOwnedRetirement(r), nil
}
