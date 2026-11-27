package harness

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func (s *MemoryStore) ManagedRecoveryAuthority(ctx context.Context) (kernel.ManagedAuthority, error) {
	if err := ctx.Err(); err != nil {
		return kernel.ManagedAuthority{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.managedAuthority.Valid() {
		return kernel.ManagedAuthority{}, ErrConflict
	}
	return s.managedAuthority, nil
}
func (s *MemoryStore) readManagedOriginalLocked(b kernel.ManagedHostBinding) error {
	t := s.turns[b.TurnID]
	i := s.invocations[b.InvocationID]
	items, err := s.globalPhysicalEvidenceLocked(b.TurnID)
	if err != nil {
		return err
	}
	if b.Approval != nil {
		for _, child := range s.itemChildren[stableID("hia", b.TurnID, b.Approval.CheckpointID)] {
			if child.TurnID != b.TurnID {
				items = append(items, child)
			}
		}
	}
	return ValidateManagedOriginalRows(s.managedAuthority, b, s.sessions[t.SessionID], t, i, s.configs[t.ConfigSnapshotID], s.itemIDs[b.DispatchID], items)
}
func (s *MemoryStore) VerifyManagedOriginalDispatch(ctx context.Context, b kernel.ManagedHostBinding, _ bool) error {
	if !b.Valid() {
		return ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readManagedOriginalLocked(b)
}

// planOriginalApprovalLocked reads all globally indexed children. An
// occupied canonical decision ID under a different parent cannot be adopted.
func (s *MemoryStore) planOriginalApprovalLocked(grant, receipt Item) (Item, bool, error) {
	parent, ok := OriginalApprovalRequestID(grant)
	if !ok {
		return Item{}, false, nil
	}
	decision, insert, err := PlanOriginalApprovalSettlement(grant, receipt, s.itemIDs[parent], s.itemChildren[parent])
	if err != nil {
		return Item{}, false, err
	}
	if insert {
		if _, exists := s.itemIDs[decision.ID]; exists {
			return Item{}, false, ErrConflict
		}
	}
	return decision, insert, nil
}
func (s *MemoryStore) appendSettlementItemLocked(item Item) {
	item = cloneItem(item)
	s.items[item.TurnID] = append(s.items[item.TurnID], item)
	s.itemIDs[item.ID] = item
	if item.ParentItemID != "" {
		s.itemChildren[item.ParentItemID] = append(s.itemChildren[item.ParentItemID], item)
	}
}
