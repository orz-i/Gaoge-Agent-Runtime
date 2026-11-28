package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	return s.observeManagedLocked(value)
}

var _ kernel.ManagedRecoveryStore = (*Store)(nil)

func (s *Store) nativeEntryGateLocked(runID string) error {
	native := s.runs[runID].execution
	pending := false
	for id := range s.effectOwners[runID] {
		g, ok := s.activeEffect(id)
		if !ok {
			return kernel.ErrExecutionConflict
		}
		if g.grant.ChildRunID != runID || !g.grant.IsOwned() {
			continue
		}
		if g.grant.ExecutionEpoch != native.epoch || native.physicalEpoch > native.epoch {
			return kernel.ErrExecutionConflict
		}
		if native.physicalEpoch < native.epoch {
			if pending || native.quarantineRev != 0 || !g.grant.MatchesExecution(native.lease) {
				return kernel.ErrExecutionConflict
			}
			pending = true
		}
	}
	if pending {
		return errors.Join(kernel.ErrExecutionConflict, kernel.ErrChildExecutionBusy)
	}
	return nil
}

func (s *Store) saveManagedLocked(t kernel.ManagedTransfer) {
	s.managedTransfers[t.SafePoint.ID] = cloneMemoryManagedTransfer(t)
	s.managedCurrent[t.SafePoint.Host.RunID] = t.SafePoint.ID
}

func (s *Store) managedCurrentLocked(runID string) (kernel.ManagedTransfer, kernel.Snapshot, error) {
	snap, exists := s.snapshotLocked(runID)
	if !exists {
		return kernel.ManagedTransfer{}, kernel.Snapshot{}, kernel.ErrNotFound
	}
	t, ok := s.managedTransfers[s.managedCurrent[runID]]
	if !ok || t.SafePoint.Host.RunID != runID {
		return kernel.ManagedTransfer{}, kernel.Snapshot{}, kernel.ErrExecutionConflict
	}
	return cloneMemoryManagedTransfer(t), cloneSnapshot(snap), nil
}

func (s *Store) CommitManagedCancellation(ctx context.Context, q kernel.ManagedCancelRequest) (kernel.ManagedTransfer, error) {
	if s == nil || ctx == nil || q.RunID == "" || q.SuspensionID == "" {
		return kernel.ManagedTransfer{}, kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return kernel.ManagedTransfer{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, snap, err := s.managedCurrentLocked(q.RunID)
	if err != nil {
		return kernel.ManagedTransfer{}, err
	}
	if kernel.ManagedCancelReplay(t, q, snap) {
		return cloneMemoryManagedTransfer(t), nil
	}
	expected := kernel.ManagedCurrentGrant(t)
	old, ok := s.activeEffect(expected.ID)
	state := s.runs[q.RunID].execution
	if !ok || !s.managedSoleGrantLocked(q.RunID, expected.ID) || old.grant != expected || old.settledRevision != 0 || old.completion.GrantID != "" || !expected.MatchesExecution(state.lease) || state.epoch != expected.ExecutionEpoch || state.epoch == math.MaxUint64 || state.quarantineRev != 0 || t.SafePoint.Host.KernelAuthority != s.managedAuthority {
		return kernel.ManagedTransfer{}, kernel.ErrExecutionConflict
	}
	t, err = kernel.PrepareManagedAdmission(t, q.Selection, snap, state.lease, old.grant, s.managedAuthority, state.physicalEpoch, s.currentExecutionTime())
	if err != nil {
		return kernel.ManagedTransfer{}, err
	}
	// The original physical Grant index does not include independently
	// owned logical children. Check their actual native ancestry under mu.
	for id, child := range s.runs {
		if (child.Run.Status == kernel.RunStatusCompleted || child.Run.Status == kernel.RunStatusFailed || child.Run.Status == kernel.RunStatusCancelled) &&
			len(s.effectOwners[id]) == 0 && s.runs[id].execution.quarantineRev == 0 {
			continue
		}
		for parent, depth := kernel.RunParentID(child.Run), 0; parent != "" && depth < 64; depth++ {
			if parent == q.RunID {
				return kernel.ManagedTransfer{}, kernel.ErrExecutionConflict
			}
			parent = kernel.RunParentID(s.runs[parent].Snapshot.Run)
		}
	}
	next, m, err := kernel.ManagedCancellationMutation(t, q, snap, s.currentExecutionTime())
	if err != nil {
		return kernel.ManagedTransfer{}, err
	}
	ledger, err := kernel.ManagedCancellationLedger(ctx, t, s.ledgers)
	if err != nil {
		return kernel.ManagedTransfer{}, err
	}
	// The original managed selection is qualified above. Commit the same
	// Run/accounting/journal facts as every other native transition.
	terminal := s.commitRunLocked(snapshotFromRecord(m.Record), m.Events, ledger)
	state.epoch++
	state.lease = kernel.ExecutionLease{}
	s.setExecutionLocked(q.RunID, state)
	s.consumeEffectLocked(old, nil)
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
	return s.observeManagedLocked(t)
}

// Never discard independently indexed descendant authority during transfer.
func (s *Store) managedSoleGrantLocked(runID, grantID string) bool {
	owners := s.effectOwners[runID]
	_, ok := owners[grantID]
	return ok && len(owners) == 1
}

func (s *Store) observeManagedLocked(t kernel.ManagedTransfer) (kernel.ManagedTransfer, error) {
	if t.SuccessorGrant.ID != "" && t.Terminal == nil {
		if receipt := s.effects[t.SuccessorGrant.ID].retirement; receipt != nil {
			closed, e := kernel.ObserveManagedRetirement(t, *receipt)
			if e != nil {
				return closed, e
			}
			t = closed
		}
	}
	return cloneMemoryManagedTransfer(t), nil
}
