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
func (s *MemoryStore) readManagedOriginalLocked(b kernel.ManagedHostBinding, recovery bool) error {
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
	if err = ValidateManagedOriginalRows(s.managedAuthority, b, s.sessions[t.SessionID], t, i, s.configs[t.ConfigSnapshotID], s.itemIDs[b.DispatchID], items); err != nil {
		return err
	}
	if recovery {
		return VerifyManagedRecoveryAdmission(b, t, i, items)
	}
	return nil
}
func (s *MemoryStore) VerifyManagedOriginalDispatch(ctx context.Context, b kernel.ManagedHostBinding, recovery bool) error {
	if !b.Valid() {
		return ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readManagedOriginalLocked(b, recovery)
}

// CloseManagedRecovery projects only the native committed result. Existing
// canonical receipts are the idempotency record, not a second closure row.
func (s *MemoryStore) CloseManagedRecovery(ctx context.Context, p kernel.ManagedClosureProof) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t := p.Transfer()
	b, r := t.SafePoint.Host, t.Selection
	if !p.Matches(r) {
		return ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.HostAuthority != s.managedAuthority {
		return ErrConflict
	}
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
	turn := s.turns[b.TurnID]
	if err = ValidateManagedOriginalRows(s.managedAuthority, b, s.sessions[turn.SessionID], turn, s.invocations[b.InvocationID], s.configs[turn.ConfigSnapshotID], s.itemIDs[b.DispatchID], nil); err != nil {
		return err
	}
	if replay, err := ManagedClosureReplay(p, items); err != nil || replay {
		return err
	}
	if err = s.readManagedOriginalLocked(b, false); err != nil {
		return err
	}
	if err = ManagedClosureAuthorization(p, items); err != nil {
		return err
	}
	additions, err := ManagedClosureItems(p, r)
	if err != nil {
		return err
	}
	grant := s.itemIDs[b.DispatchID]
	additions[0].Seq = uint64(len(s.items[b.TurnID]) + 1)
	decision, insert, err := s.planOriginalApprovalLocked(grant, additions[0])
	if err != nil {
		return err
	}
	if insert {
		additions = append([]Item{decision}, additions...)
	}
	for _, item := range additions {
		if _, exists := s.itemIDs[item.ID]; exists || len(s.itemChildren[item.ParentItemID]) != 0 {
			return ErrConflict
		}
	}
	for _, item := range additions {
		item.Seq = uint64(len(s.items[item.TurnID]) + 1)
		s.appendSettlementItemLocked(item)
	}
	return nil
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
