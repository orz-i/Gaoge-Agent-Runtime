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
	return ValidateManagedOriginalRows(s.managedAuthority, b, s.sessions[t.SessionID], t, i, s.configs[t.ConfigSnapshotID], s.itemIDs[b.DispatchID], items)
}
func (s *MemoryStore) ReadManagedOriginalDispatch(ctx context.Context, b kernel.ManagedHostBinding) (kernel.ManagedHostBinding, error) {
	if err := ctx.Err(); err != nil {
		return kernel.ManagedHostBinding{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.readManagedOriginalLocked(b); err != nil {
		return kernel.ManagedHostBinding{}, err
	}
	b.Source = append([]byte(nil), b.Source...)
	return b, nil
}
func (s *MemoryStore) ReserveManagedRecovery(ctx context.Context, p kernel.ManagedParkedProof) (kernel.ManagedHostReservation, error) {
	if err := ctx.Err(); err != nil {
		return kernel.ManagedHostReservation{}, err
	}
	b := p.Transfer().SafePoint.Host
	if !p.Matches(b) {
		return kernel.ManagedHostReservation{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.readManagedOriginalLocked(b); err != nil {
		return kernel.ManagedHostReservation{}, err
	}
	items := append([]Item(nil), s.items[b.TurnID]...)
	for _, item := range s.items[b.TurnID] {
		if item.ParentItemID == CancellationFenceParentItemID {
			for _, child := range s.itemChildren[item.ID] {
				if child.TurnID != b.TurnID {
					items = append(items, child)
				}
			}
		}
	}
	candidate, err := SelectManagedReservation(p, b, s.turns[b.TurnID], s.invocations[b.InvocationID], items)
	if err != nil {
		return kernel.ManagedHostReservation{}, err
	}
	if old, ok := s.managedReservations[b.DispatchID]; ok {
		if !SameManagedReservation(old, candidate) {
			return kernel.ManagedHostReservation{}, ErrConflict
		}
		return cloneManagedReservation(old), nil
	}
	s.managedReservations[b.DispatchID] = cloneManagedReservation(candidate)
	return cloneManagedReservation(candidate), nil
}
func (s *MemoryStore) ReadManagedRecovery(ctx context.Context, b kernel.ManagedHostBinding, id string) (kernel.ManagedHostReservation, error) {
	if err := ctx.Err(); err != nil {
		return kernel.ManagedHostReservation{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.managedReservations[b.DispatchID]
	if !ok {
		return kernel.ManagedHostReservation{}, kernel.ErrNotFound
	}
	if err := s.readManagedRecoverySourceLocked(b); err != nil {
		return kernel.ManagedHostReservation{}, err
	}
	if !r.Valid() || (id != "" && r.RecoveryID != id) || kernel.ManagedDigest(r.Binding) != kernel.ManagedDigest(b) {
		return kernel.ManagedHostReservation{}, ErrConflict
	}
	if err := ValidateManagedReservationRows(r, s.items[b.TurnID], s.managedClosures[b.DispatchID] != ""); err != nil {
		return kernel.ManagedHostReservation{}, err
	}
	return cloneManagedReservation(r), nil
}
func (s *MemoryStore) managedReservedTurnLocked(turnID string) bool {
	for _, r := range s.managedReservations {
		if r.Binding.TurnID == turnID && s.managedClosures[r.Binding.DispatchID] == "" {
			return true
		}
	}
	return false
}

func (s *MemoryStore) managedReservedCompletionLocked(item Item) bool {
	for _, r := range s.managedReservations {
		if s.managedClosures[r.Binding.DispatchID] != "" {
			continue
		}
		if item.ParentItemID == r.Binding.DispatchID || (r.Mode == kernel.ManagedCancel && item.ParentItemID == r.Cancellation.IntentID) {
			return true
		}
	}
	return false
}

// CloseManagedRecovery is the sole write path for transferred dispatch facts.
func (s *MemoryStore) CloseManagedRecovery(ctx context.Context, p kernel.ManagedClosureProof) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t := p.Transfer()
	b := t.SafePoint.Host
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.managedReservations[b.DispatchID]
	if !ok || !p.Matches(r) || b.HostAuthority != s.managedAuthority {
		return ErrConflict
	}
	digest := kernel.ManagedDigest(struct {
		Transfer kernel.ManagedTransfer
		Snapshot kernel.Snapshot
	}{t, p.Snapshot()})
	if old, closed := s.managedClosures[b.DispatchID]; closed {
		if old != digest {
			return ErrConflict
		}
		return nil
	}
	if err := s.readManagedOriginalLocked(b); err != nil {
		return err
	}
	additions, err := ManagedClosureItems(p, r)
	if err != nil {
		return err
	}
	for _, item := range additions {
		if _, exists := s.itemIDs[item.ID]; exists || len(s.itemChildren[item.ParentItemID]) != 0 {
			return ErrConflict
		}
	}
	for _, item := range additions {
		item.Seq = uint64(len(s.items[item.TurnID]) + 1)
		s.items[item.TurnID] = append(s.items[item.TurnID], cloneItem(item))
		s.itemIDs[item.ID] = cloneItem(item)
		s.itemChildren[item.ParentItemID] = append(s.itemChildren[item.ParentItemID], cloneItem(item))
	}
	s.managedClosures[b.DispatchID] = digest
	return nil
}

func (s *MemoryStore) readManagedRecoverySourceLocked(b kernel.ManagedHostBinding) error {
	if s.managedClosures[b.DispatchID] == "" {
		return s.readManagedOriginalLocked(b)
	}
	t := s.turns[b.TurnID]
	return ValidateManagedOriginalRows(s.managedAuthority, b, s.sessions[t.SessionID], t, s.invocations[b.InvocationID], s.configs[t.ConfigSnapshotID], s.itemIDs[b.DispatchID], nil)
}
