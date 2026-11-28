package memory

import (
	"context"
	"encoding/json"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func cloneOwnedRetirement(r kernel.OwnedRetirementReceipt) kernel.OwnedRetirementReceipt {
	b, _ := json.Marshal(r)
	var out kernel.OwnedRetirementReceipt
	_ = json.Unmarshal(b, &out)
	return out
}

// retireOwnedLocked commits the exact managed closure with ordinary retirement.
func (s *Store) retireOwnedLocked(previous memoryEffect, permit kernel.ChildExecutionSettlement) error {
	g := previous.grant
	current := s.runs[g.ChildRunID].Snapshot
	native := s.runs[g.ChildRunID].execution
	if native.epoch != g.ExecutionEpoch || native.physicalEpoch != g.ExecutionEpoch ||
		native.quarantineRev != 0 || len(previous.ancestors) != 1 || previous.ancestors[0] != g.ChildRunID ||
		len(s.effectOwners[g.ChildRunID]) != 1 {
		return kernel.ErrExecutionConflict
	}
	if _, ok := s.effectOwners[g.ChildRunID][g.ID]; !ok {
		return kernel.ErrExecutionConflict
	}
	var authority kernel.ManagedAuthority
	var transfer kernel.ManagedTransfer
	if g.AdmissionOperation == "managed_resume" {
		authority = s.managedAuthority
		transfer = s.managedTransfers[s.managedCurrent[g.ChildRunID]]
	}
	r, err := kernel.BuildOwnedRetirementReceipt(g, cloneSnapshot(current), native.lease, previous.completion, permit, authority, transfer)
	if err != nil {
		return err
	}
	retirement := cloneOwnedRetirement(r)
	s.consumeEffectLocked(previous, &retirement)
	if r.Evidence.Completion.LeaseOwned {
		native.lease = kernel.ExecutionLease{}
		s.setExecutionLocked(g.ChildRunID, native)
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
	effect := s.effects[id]
	if effect.retirement == nil {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrNotFound
	}
	r := *effect.retirement
	if !s.validRetirementLocked(r) || r.Evidence.Grant.ID != id {
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
	rows, err := s.retirementsLocked(runID)
	if err != nil {
		return kernel.OwnedRetirementReceipt{}, err
	}
	native := s.runs[runID].execution
	hasGrants := len(s.effectOwners[runID]) != 0
	if e := s.nativeEntryGateLocked(runID); e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	if native.quarantineRev != 0 {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrExecutionConflict
	}
	r, e := kernel.SelectOwnedRetirementForRun(runID, rows, s.runs[runID].Snapshot, native.epoch, native.lease, hasGrants)
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	return cloneOwnedRetirement(r), nil
}

// Historical receipts are views of the same consumed operation records.
func (s *Store) retirementsLocked(runID string) ([]kernel.OwnedRetirementReceipt, error) {
	rows := make([]kernel.OwnedRetirementReceipt, 0)
	for id, e := range s.effects {
		if e.retirement == nil || e.retirement.Evidence.Grant.ChildRunID != runID {
			continue
		}
		if e.retirement.Evidence.Grant.ID != id || !s.validRetirementLocked(*e.retirement) {
			return nil, kernel.ErrExecutionConflict
		}
		rows = append(rows, *e.retirement)
	}
	return rows, nil
}

func (s *Store) validRetirementLocked(r kernel.OwnedRetirementReceipt) bool {
	if !r.Valid() {
		return false
	}
	if len(r.ClosureRefs) == 0 {
		return true
	}
	t, ok := s.managedTransfers[r.ClosureRefs[0].SourceID]
	if !ok || t.SafePoint.Host.KernelAuthority != s.managedAuthority {
		return false
	}
	_, err := kernel.ObserveManagedRetirement(t, r)
	return err == nil
}
