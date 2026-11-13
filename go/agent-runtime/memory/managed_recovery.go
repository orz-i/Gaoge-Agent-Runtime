package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Authority is initialized once with the Store, never by a recovery read.
func newMemoryManagedAuthority() kernel.ManagedAuthority {
	var namespace [32]byte
	if _, err := rand.Read(namespace[:]); err != nil {
		panic("memory: cannot initialize managed recovery authority: " + err.Error())
	}
	return kernel.ManagedAuthority{Namespace: hex.EncodeToString(namespace[:]), Generation: 1, Version: 1}
}

func (s *Store) ManagedRecoveryAuthority(ctx context.Context) (kernel.ManagedAuthority, error) {
	if s == nil || ctx == nil {
		return kernel.ManagedAuthority{}, kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return kernel.ManagedAuthority{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.managedAuthority, nil
}

func cloneMemoryManagedTransfer(v kernel.ManagedTransfer) kernel.ManagedTransfer {
	// All fields are fixed persistence types, with no user-provided marshalers.
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var result kernel.ManagedTransfer
	if err := json.Unmarshal(b, &result); err != nil {
		panic(err)
	}
	return result
}

func (s *Store) LoadManagedRecovery(ctx context.Context, runID string) (kernel.ManagedTransfer, error) {
	if s == nil || ctx == nil || runID == "" {
		return kernel.ManagedTransfer{}, kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return kernel.ManagedTransfer{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.managedTransfers[s.managedCurrent[runID]]
	if !ok {
		return kernel.ManagedTransfer{}, kernel.ErrNotFound
	}
	return cloneMemoryManagedTransfer(value), nil
}

var _ kernel.ManagedRecoveryStore = (*Store)(nil)

func (s *Store) managedGateLocked(runID string) error {
	return kernel.ManagedOrdinaryGate(s.managedTransfers[s.managedCurrent[runID]])
}

func (s *Store) saveManagedLocked(t kernel.ManagedTransfer) {
	s.managedTransfers[t.SafePoint.ID] = cloneMemoryManagedTransfer(t)
	s.managedCurrent[t.SafePoint.Host.RunID] = t.SafePoint.ID
}

func (s *Store) managedCurrentLocked(runID, suspensionID string) (kernel.ManagedTransfer, kernel.Snapshot, error) {
	snap, exists := s.records[runID]
	if !exists {
		return kernel.ManagedTransfer{}, kernel.Snapshot{}, kernel.ErrNotFound
	}
	t, ok := s.managedTransfers[suspensionID]
	if !ok || s.managedCurrent[runID] != suspensionID || t.SafePoint.Host.RunID != runID {
		return kernel.ManagedTransfer{}, kernel.Snapshot{}, kernel.ErrExecutionConflict
	}
	return cloneMemoryManagedTransfer(t), cloneSnapshot(snap), nil
}

func (s *Store) ParkManagedRecovery(ctx context.Context, q kernel.ManagedParkRequest) (kernel.ManagedTransfer, error) {
	if s == nil || ctx == nil || q.RunID == "" || q.SuspensionID == "" {
		return kernel.ManagedTransfer{}, kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return kernel.ManagedTransfer{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, snap, err := s.managedCurrentLocked(q.RunID, q.SuspensionID)
	if err != nil {
		return kernel.ManagedTransfer{}, err
	}
	g, ok := s.ownedGrantLocked(q.RunID)
	if !ok || !s.managedSoleGrantLocked(q.RunID, g.grant.ID) || g.settledRevision != 0 || g.completion.GrantID != "" || s.execution[q.RunID].quarantineRev != 0 {
		return kernel.ManagedTransfer{}, kernel.ErrExecutionConflict
	}
	next, err := kernel.ManagedParkTransfer(t, q, snap, s.execution[q.RunID].lease, g.grant, s.managedAuthority, s.currentExecutionTime())
	if err != nil {
		return kernel.ManagedTransfer{}, err
	}
	s.saveManagedLocked(next)
	return cloneMemoryManagedTransfer(next), nil
}

func (s *Store) LeaveManagedSafePoint(ctx context.Context, q kernel.ManagedLeaveRequest) (kernel.ManagedTransfer, error) {
	if s == nil || ctx == nil || q.RunID == "" || q.SuspensionID == "" {
		return kernel.ManagedTransfer{}, kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return kernel.ManagedTransfer{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, snap, err := s.managedCurrentLocked(q.RunID, q.SuspensionID)
	if err != nil {
		return kernel.ManagedTransfer{}, err
	}
	expected, _ := kernel.ManagedCurrentAuthority(t)
	g, ok := s.childGrants[expected.ID]
	if !ok || g.grant != expected || g.settledRevision != 0 || g.completion.GrantID != "" || s.execution[q.RunID].quarantineRev != 0 {
		return kernel.ManagedTransfer{}, kernel.ErrExecutionConflict
	}
	next, err := kernel.ManagedLeaveTransfer(t, q, snap, s.execution[q.RunID].lease, s.currentExecutionTime())
	if err != nil {
		return kernel.ManagedTransfer{}, err
	}
	s.saveManagedLocked(next)
	return cloneMemoryManagedTransfer(next), nil
}

func (s *Store) removeManagedGrantLocked(g memoryChildGrant) {
	delete(s.childGrants, g.grant.ID)
	for _, id := range g.ancestors {
		delete(s.childGrantOwners[id], g.grant.ID)
		if len(s.childGrantOwners[id]) == 0 {
			delete(s.childGrantOwners, id)
		}
	}
}

func (s *Store) EnableManagedRecovery(ctx context.Context, q kernel.ManagedEnableRequest) (kernel.ManagedTransfer, error) {
	if s == nil || ctx == nil || q.RunID == "" || q.SuspensionID == "" {
		return kernel.ManagedTransfer{}, kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return kernel.ManagedTransfer{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, snap, err := s.managedCurrentLocked(q.RunID, q.SuspensionID)
	if err != nil {
		return kernel.ManagedTransfer{}, err
	}
	// Shared exact ACK repair never returns a different claimant's live token.
	if kernel.ManagedEnableReplay(t, q, snap, s.execution[q.RunID].lease, s.currentExecutionTime()) {
		return cloneMemoryManagedTransfer(t), nil
	}
	expected, expectedLease := kernel.ManagedCurrentAuthority(t)
	old, ok := s.childGrants[expected.ID]
	state := s.execution[q.RunID]
	if !ok || !s.managedSoleGrantLocked(q.RunID, expected.ID) || old.grant != expected || old.settledRevision != 0 || old.completion.GrantID != "" || !expected.MatchesExecution(state.lease) || state.epoch != expectedLease.Epoch || state.quarantineRev != 0 || t.SafePoint.Host.KernelAuthority != s.managedAuthority {
		return kernel.ManagedTransfer{}, kernel.ErrExecutionConflict
	}
	if _, exists := s.childGrants[q.GrantID]; exists {
		return kernel.ManagedTransfer{}, kernel.ErrExecutionConflict
	}
	next, err := kernel.ManagedEnabledTransfer(t, q, snap, state.epoch, s.currentExecutionTime())
	if err != nil {
		return kernel.ManagedTransfer{}, err
	}
	s.removeManagedGrantLocked(old)
	state.epoch = next.SuccessorLease.Epoch
	state.lease = next.SuccessorLease
	s.execution[q.RunID] = state
	s.childGrants[next.SuccessorGrant.ID] = memoryChildGrant{grant: next.SuccessorGrant, ancestors: []string{q.RunID}}
	if s.childGrantOwners[q.RunID] == nil {
		s.childGrantOwners[q.RunID] = make(map[string]struct{})
	}
	s.childGrantOwners[q.RunID][next.SuccessorGrant.ID] = struct{}{}
	s.saveManagedLocked(next)
	return cloneMemoryManagedTransfer(next), nil
}

func (s *Store) CancelParkedManagedContinuation(ctx context.Context, q kernel.ManagedCancelRequest) (kernel.ManagedTransfer, error) {
	if s == nil || ctx == nil || q.RunID == "" || q.SuspensionID == "" {
		return kernel.ManagedTransfer{}, kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return kernel.ManagedTransfer{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, snap, err := s.managedCurrentLocked(q.RunID, q.SuspensionID)
	if err != nil {
		return kernel.ManagedTransfer{}, err
	}
	if kernel.ManagedCancelReplay(t, q, snap) {
		return cloneMemoryManagedTransfer(t), nil
	}
	expected, expectedLease := kernel.ManagedCurrentAuthority(t)
	old, ok := s.childGrants[expected.ID]
	state := s.execution[q.RunID]
	if !ok || !s.managedSoleGrantLocked(q.RunID, expected.ID) || old.grant != expected || old.settledRevision != 0 || old.completion.GrantID != "" || !expected.MatchesExecution(state.lease) || state.epoch != expectedLease.Epoch || state.epoch == math.MaxUint64 || state.quarantineRev != 0 || t.SafePoint.Host.KernelAuthority != s.managedAuthority {
		return kernel.ManagedTransfer{}, kernel.ErrExecutionConflict
	}
	next, m, err := kernel.ManagedCancellationMutation(t, q, snap, s.currentExecutionTime())
	if err != nil {
		return kernel.ManagedTransfer{}, err
	}
	// This exact reducer is the sole PARKED terminal path. Never pass an ordinary
	// mutation through a temporarily removed fence or fabricate callback return.
	terminal := snapshotFromRecord(m.Record)
	journal := appendEvents(s.events[q.RunID], m.Events, m.Record.Run.UpdatedAt)
	terminal.EventHead = int64(len(journal))
	s.records[q.RunID] = cloneSnapshot(terminal)
	s.events[q.RunID] = journal
	s.appendCommittedTransition(m.Record.Run, m.Events)
	state.epoch++
	state.lease = kernel.ExecutionLease{}
	s.execution[q.RunID] = state
	s.removeManagedGrantLocked(old)
	next.Terminal = &terminal
	s.saveManagedLocked(next)
	return cloneMemoryManagedTransfer(next), nil
}

func (s *Store) LoadManagedRecoveryByID(ctx context.Context, id string) (kernel.ManagedTransfer, error) {
	if s == nil || ctx == nil || id == "" {
		return kernel.ManagedTransfer{}, kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return kernel.ManagedTransfer{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.managedTransfers[id]
	if !ok {
		return kernel.ManagedTransfer{}, kernel.ErrNotFound
	}
	return cloneMemoryManagedTransfer(t), nil
}

func (s *Store) CompleteManagedRecovery(ctx context.Context, g kernel.ChildExecutionGrant, snapshot kernel.Snapshot, p kernel.ManagedCompletionProof) error {
	if s == nil || ctx == nil {
		return kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var matches []string
	for id, t := range s.managedTransfers {
		if t.SuccessorGrant == g && t.Selection.Valid() {
			matches = append(matches, id)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	current, ok := s.records[g.ChildRunID]
	original, hasGrant := s.childGrants[g.ID]
	if !ok || !hasGrant || original.grant != g || original.settledRevision != current.Run.Revision || kernel.ManagedDigest(current) != kernel.ManagedDigest(snapshot) || !p.Matches(g, current) {
		return kernel.ErrExecutionConflict
	}
	for _, id := range matches {
		t := s.managedTransfers[id]
		if t.Selection.Mode != kernel.ManagedResume || t.SafePoint.Host.KernelAuthority != s.managedAuthority || t.State == kernel.ManagedParked || t.State == kernel.ManagedCancelled {
			return kernel.ErrExecutionConflict
		}
		if t.State == kernel.ManagedClosed && (t.Terminal == nil || kernel.ManagedDigest(*t.Terminal) != kernel.ManagedDigest(current)) {
			return kernel.ErrExecutionConflict
		}
	}
	for _, id := range matches {
		t := s.managedTransfers[id]
		if t.State != kernel.ManagedClosed {
			t.State = kernel.ManagedClosed
			t.Revision++
			terminal := cloneSnapshot(current)
			t.Terminal = &terminal
			s.managedTransfers[id] = cloneMemoryManagedTransfer(t)
		}
	}
	return nil
}

// Never discard independently indexed descendant authority during transfer.
func (s *Store) managedSoleGrantLocked(runID, grantID string) bool {
	owners := s.childGrantOwners[runID]
	_, ok := owners[grantID]
	return ok && len(owners) == 1
}
