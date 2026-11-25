package memory

import "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"

// Called only under Store.mu, before ANY successor owner/revision mutation.
// All validation precedes both writes; an ambiguous historical frontier stays
// unchanged and cannot be hidden behind a later callback's retirement.
type memoryRetirementSuccessor struct {
	receipt kernel.OwnedRetirementReceipt
	release bool
}

func (s *Store) planRetirementSuccessorLocked(runID string) (memoryRetirementSuccessor, error) {
	var original kernel.OwnedRetirementReceipt
	for id, row := range s.ownedRetirements {
		if row.Evidence.Grant.ChildRunID != runID || row.Phase == "ACKNOWLEDGED" {
			continue
		}
		if row.Evidence.Grant.ID != id || original.Version != 0 {
			return memoryRetirementSuccessor{}, kernel.ErrOwnedRetirementPending
		}
		original = row
	}
	if original.Version == 0 {
		return memoryRetirementSuccessor{}, nil
	}
	native := s.execution[runID]
	if err := s.managedGateLocked(runID); err != nil {
		return memoryRetirementSuccessor{}, err
	}
	if native.quarantineRev != 0 {
		return memoryRetirementSuccessor{}, kernel.ErrOwnedRetirementPending
	}
	active := len(s.childGrantOwners[runID]) != 0
	for _, g := range s.childGrants {
		if g.grant.ChildRunID == runID {
			active = true
		}
	}
	next, release, err := kernel.ReduceOwnedRetirementSuccessor(original, s.records[runID], native.epoch, native.lease, active)
	if err != nil {
		return memoryRetirementSuccessor{}, err
	}
	return memoryRetirementSuccessor{receipt: next, release: release}, nil
}

func (s *Store) commitRetirementSuccessorLocked(runID string, plan memoryRetirementSuccessor) {
	if plan.receipt.Version == 0 {
		return
	}
	if plan.release {
		native := s.execution[runID]
		native.lease = kernel.ExecutionLease{}
		s.execution[runID] = native
	}
	s.ownedRetirements[plan.receipt.Evidence.Grant.ID] = cloneOwnedRetirement(plan.receipt)
}

func (s *Store) settleRetirementSuccessorLocked(runID string) (bool, error) {
	plan, err := s.planRetirementSuccessorLocked(runID)
	if err != nil {
		return false, err
	}
	s.commitRetirementSuccessorLocked(runID, plan)
	return plan.release, nil
}
