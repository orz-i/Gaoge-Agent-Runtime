package memory

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.OwnedRootSettlementStore = (*Store)(nil)

// VerifyOwnedRootSettlement is read-only under the same Store lock that
// serializes physical grants and Kernel control writes. No public Run metadata
// can substitute for the exact acknowledged owner callback retirement.
func (s *Store) VerifyOwnedRootSettlement(ctx context.Context, expected kernel.Snapshot) error {
	return s.verifyRootSettlement(ctx, expected, false)
}

func (s *Store) verifyRootSettlement(ctx context.Context, expected kernel.Snapshot, prepared bool) error {
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
	if err := s.managedGateLocked(expected.Run.ID); err != nil {
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
	if prepared {
		return kernel.VerifyPreparedRootSettlementFacts(expected, current, native.epoch, native.physicalEpoch, lease, active, receipts)
	}
	return kernel.VerifyOwnedRootSettlementFacts(expected, current, native.epoch, native.physicalEpoch, lease, active, receipts)
}
