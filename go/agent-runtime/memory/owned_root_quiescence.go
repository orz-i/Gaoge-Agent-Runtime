package memory

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.OwnedRootSettlementStore = (*Store)(nil)
var _ kernel.ReturnedRootSettlementStore = (*Store)(nil)

func (s *Store) VerifyReturnedRootSettlement(ctx context.Context, expected kernel.Snapshot) error {
	return s.verifyRootSettlement(ctx, expected, nil)
}

func (s *Store) VerifyBoundedToolControl(ctx context.Context, expected kernel.Snapshot) error {
	return s.verifyRootSettlement(ctx, expected, kernel.VerifyBoundedToolControlFacts)
}

func (s *Store) VerifyCompletedToolReturn(ctx context.Context, expected kernel.Snapshot) error {
	return s.verifyRootSettlement(ctx, expected, kernel.VerifyCompletedToolReturnFacts)
}

// VerifyOwnedRootSettlement is read-only under the same Store lock that
// serializes physical grants and Kernel control writes. No public Run metadata
// can substitute for the exact retired owner callback retirement.
func (s *Store) VerifyOwnedRootSettlement(ctx context.Context, expected kernel.Snapshot) error {
	return s.verifyRootSettlement(ctx, expected, kernel.VerifyOwnedRootSettlementFacts)
}

func (s *Store) verifyRootSettlement(ctx context.Context, expected kernel.Snapshot, check kernel.RootSettlementCheck) error {
	if s == nil || ctx == nil || expected.Run.ID == "" {
		return kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	current, exists := s.records[expected.Run.ID]
	if !exists {
		return kernel.ErrNotFound
	}
	if err := s.nativeEntryGateLocked(expected.Run.ID); err != nil {
		return err
	}
	native := s.execution[expected.Run.ID]
	if native.quarantineRev != 0 {
		return kernel.ErrExecutionConflict
	}
	lease := native.lease
	// Expiry is NOT callback completion. It is safe to ignore a lease-only
	// owner only because active grants and the exact last physical admission
	// are independently checked below under this same Run lock.
	if !lease.ExpiresAt.After(s.currentExecutionTime()) {
		lease = kernel.ExecutionLease{}
	}
	active := len(s.childGrantOwners[expected.Run.ID]) != 0
	for _, g := range s.childGrants {
		if g.grant.ChildRunID == expected.Run.ID {
			active = true
		}
	}
	receipts := make([]kernel.OwnedRetirementReceipt, 0)
	for id, r := range s.ownedRetirements {
		if r.Evidence.Grant.ChildRunID != expected.Run.ID {
			continue
		}
		if r.Evidence.Grant.ID != id {
			return kernel.ErrExecutionConflict
		}
		receipts = append(receipts, r)
	}
	if check == nil {
		var original memoryChildGrant
		for _, candidate := range s.childGrants {
			if candidate.grant.ChildRunID == expected.Run.ID {
				if original.grant.ID != "" {
					return kernel.ErrExecutionConflict
				}
				original = candidate
			}
		}
		return kernel.VerifyReturnedRootSettlementFacts(expected, current, native.epoch, native.physicalEpoch,
			native.lease, original.grant, original.completion, receipts, active)
	}
	return check(expected, current, native.epoch, native.physicalEpoch, lease, active, receipts)
}
