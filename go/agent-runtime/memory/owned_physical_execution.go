package memory

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.PhysicalExecutionStore = (*Store)(nil)

// ownedGrantLocked is always called under the Store's authoritative mutex.
func (s *Store) ownedGrantLocked(runID string) (memoryEffect, bool) {
	for id := range s.effectOwners[runID] {
		g, ok := s.activeEffect(id)
		if ok && g.grant.ChildRunID == runID && g.grant.IsOwned() {
			return g, true
		}
	}
	return memoryEffect{}, false
}
func (s *Store) BeginPhysicalExecution(ctx context.Context, p kernel.PhysicalAdmission) (kernel.PhysicalAdmissionResult, error) {
	var zero kernel.PhysicalAdmissionResult
	if s == nil || ctx == nil {
		return zero, kernel.ErrInvalidInput
	}
	g := p.Candidate().Grant
	if !g.Identity().Valid() {
		return kernel.PhysicalAdmissionResult{}, kernel.ErrExecutionConflict
	}
	if e := ctx.Err(); e != nil {
		return zero, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.snapshotLocked(g.ChildRunID)
	if !ok {
		return zero, kernel.ErrNotFound
	}
	ancestors, e := s.validateLiveRunOwners(ctx, current.Run)
	if e != nil {
		return zero, e
	}
	if _, occupied := s.effects[g.ID]; occupied {
		return zero, kernel.ErrExecutionConflict
	}
	native := s.runs[g.ChildRunID].execution
	persisted := native.lease
	persisted.Epoch = native.epoch
	var v kernel.PhysicalAdmissionResult
	var next kernel.ManagedTransfer
	var old memoryEffect
	if !g.IsOwned() {
		if e = s.nativeEntryGateLocked(g.ChildRunID); e != nil {
			return zero, e
		}
	}
	if p.IsManagedRecovery() {
		var found bool
		old, found = s.ownedGrantLocked(g.ChildRunID)
		if !found || !s.managedSoleGrantLocked(g.ChildRunID, old.grant.ID) || old.completion.GrantID != "" || native.quarantineRev != 0 {
			return zero, kernel.ErrExecutionConflict
		}
		t := s.managedTransfers[s.managedCurrent[g.ChildRunID]]
		v, next, e = kernel.ResolveManagedPhysicalAdmission(ctx, t, p, current, persisted, old.grant, s.managedAuthority, native.physicalEpoch, s.currentExecutionTime(), s.ledgers)
	} else {
		hasGrants := len(s.effectOwners[g.ChildRunID]) != 0
		if !g.IsOwned() {
			hasGrants = false
			for id := range s.effectOwners[g.ChildRunID] {
				previous, ok := s.activeEffect(id)
				if !ok || previous.grant.ChildRunID == g.ChildRunID {
					hasGrants = true
					break
				}
			}
		}
		v, e = kernel.ResolvePhysicalAdmission(current, persisted, s.currentExecutionTime(), native.quarantineRev != 0, hasGrants, p)
	}
	if e != nil {
		return zero, e
	}
	g = v.Grant
	if g.IsOwned() {
		entryEpoch, e := kernel.PrepareOwnedEntryEpoch(g, native.epoch, native.physicalEpoch)
		if e != nil {
			return zero, e
		}
		if p.IsManagedRecovery() {
			s.consumeEffectLocked(old, nil)
			s.saveManagedLocked(next)
		}
		if p.AcquiresLease() {
			native.epoch, native.lease = v.Lease.Epoch, v.Lease
		}
		native.physicalEpoch = entryEpoch
		s.setExecutionLocked(g.ChildRunID, native)
	}
	ancestors = append(ancestors, g.ChildRunID)
	s.effects[g.ID] = memoryEffect{grant: g, ancestors: ancestors}
	for _, id := range ancestors {
		if s.effectOwners[id] == nil {
			s.effectOwners[id] = make(map[string]struct{})
		}
		s.effectOwners[id][g.ID] = struct{}{}
	}
	return v, nil
}
func (s *Store) loadOwnedPhysicalGrantLocked(g kernel.ChildExecutionGrant) (memoryEffect, error) {
	original, ok := s.activeEffect(g.ID)
	if !ok {
		for id := range s.effectOwners[g.ChildRunID] {
			candidate, ok := s.activeEffect(id)
			if !ok || candidate.grant.ChildRunID == g.ChildRunID {
				return memoryEffect{}, kernel.ErrExecutionConflict
			}
		}
		return memoryEffect{}, kernel.ErrNotFound
	}
	if original.grant != g || len(original.ancestors) != 1 || original.ancestors[0] != g.ChildRunID {
		return memoryEffect{}, kernel.ErrExecutionConflict
	}
	return original, nil
}

func (s *Store) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, expected kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	if s == nil || ctx == nil || kernel.ValidatePhysicalAdmissionRequest(g, l) != nil {
		return kernel.OwnedPhysicalEntryWindow{}, kernel.ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return kernel.OwnedPhysicalEntryWindow{}, err
	}
	current, ok := s.snapshotLocked(g.ChildRunID)
	if !ok {
		return kernel.OwnedPhysicalEntryWindow{}, kernel.ErrNotFound
	}
	if _, e := s.validateLiveRunOwners(ctx, current.Run); e != nil {
		return kernel.OwnedPhysicalEntryWindow{}, e
	}
	original, err := s.loadOwnedPhysicalGrantLocked(g)
	if err != nil {
		return kernel.OwnedPhysicalEntryWindow{}, err
	}
	owners := s.effectOwners[g.ChildRunID]
	if _, ok := owners[g.ID]; !ok || len(owners) != 1 {
		return kernel.OwnedPhysicalEntryWindow{}, kernel.ErrExecutionConflict
	}
	native := s.runs[g.ChildRunID].execution
	var transfer kernel.ManagedTransfer
	if g.AdmissionOperation == "managed_resume" {
		transfer = s.managedTransfers[s.managedCurrent[g.ChildRunID]]
	}
	window, err := kernel.ValidateOwnedPhysicalEntry(g, expected, current, l, native.lease, s.currentExecutionTime(), native.quarantineRev != 0, native.physicalEpoch,
		0, original.completion, transfer)
	if err == nil {
		if g.AdmissionOperation == "managed_resume" && transfer.SafePoint.Host.KernelAuthority != s.managedAuthority {
			return kernel.OwnedPhysicalEntryWindow{}, kernel.ErrExecutionConflict
		}
		native.physicalEpoch = g.ExecutionEpoch
		s.setExecutionLocked(g.ChildRunID, native)
	}
	return window, err
}

func (s *Store) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, revision uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	if s == nil || ctx == nil || kernel.ValidateOwnedPhysicalRecoveryRequest(g, revision, m) != nil {
		return kernel.Snapshot{}, kernel.ErrInvalidInput
	}
	if e := ctx.Err(); e != nil {
		return kernel.Snapshot{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.nativeEntryGateLocked(g.ChildRunID); err != nil {
		return kernel.Snapshot{}, err
	}
	current, ok := s.snapshotLocked(g.ChildRunID)
	if !ok {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	original, err := s.loadOwnedPhysicalGrantLocked(g)
	if err != nil {
		return kernel.Snapshot{}, kernel.ErrExecutionConflict
	}
	if err := kernel.ValidateOwnedPhysicalRecovery(g, current, s.runs[g.ChildRunID].execution.lease, revision, m, p, original.completion, 0); err != nil {
		return kernel.Snapshot{}, err
	}
	m.PhysicalPermit = p.PhysicalPermit(g, current, m)
	return s.applyLocked(ctx, g.ChildRunID, revision, m, false)
}

// RecordPhysicalCallbackReturn changes only the original physical scope,
// never the Run projection, wakeup revision, checkpoint or lease epoch.
func (s *Store) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, o kernel.PhysicalCallbackOutcome, p kernel.PhysicalCallbackReturn) error {
	if s == nil || ctx == nil || !g.Identity().Valid() || g.ExpectedRevision == 0 || rev == 0 {
		return kernel.ErrInvalidInput
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	original, ok := s.activeEffect(g.ID)
	current, exists := s.snapshotLocked(g.ChildRunID)
	if !ok || !exists || original.grant != g {
		return kernel.ErrExecutionConflict
	}
	native := s.runs[g.ChildRunID].execution
	if g.IsOwned() {
		if len(original.ancestors) != 1 || original.ancestors[0] != g.ChildRunID || native.epoch != g.ExecutionEpoch {
			return kernel.ErrExecutionConflict
		}
	} else if err := s.nativeEntryGateLocked(g.ChildRunID); err != nil {
		return err
	}

	completion, write, err := kernel.ReducePhysicalCallbackReturn(g, current, native.lease, rev, o, p, original.completion, 0, native.physicalEpoch)
	if err != nil || !write {
		return err
	}
	original.completion = completion
	s.effects[g.ID] = original
	if g.IsOwned() {
		native.physicalEpoch = g.ExecutionEpoch
		s.setExecutionLocked(g.ChildRunID, native)
	}
	return nil
}
func (s *Store) LookupPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.PhysicalCallbackCompletion, error) {
	if s == nil || ctx == nil || !g.Identity().Valid() || g.ExpectedRevision == 0 {
		return kernel.PhysicalCallbackCompletion{}, kernel.ErrInvalidInput
	}
	if e := ctx.Err(); e != nil {
		return kernel.PhysicalCallbackCompletion{}, e
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var original memoryEffect
	if g.IsOwned() {
		var err error
		original, err = s.loadOwnedPhysicalGrantLocked(g)
		if err != nil {
			return kernel.PhysicalCallbackCompletion{}, err
		}
	} else {
		var ok bool
		original, ok = s.activeEffect(g.ID)
		if !ok {
			return kernel.PhysicalCallbackCompletion{}, kernel.ErrNotFound
		}
		if original.grant != g {
			return kernel.PhysicalCallbackCompletion{}, kernel.ErrExecutionConflict
		}
	}
	if _, exists := s.snapshotLocked(g.ChildRunID); !exists {
		return kernel.PhysicalCallbackCompletion{}, kernel.ErrExecutionConflict
	}
	if err := kernel.ValidatePhysicalCompletion(g, original.completion); err != nil {
		return kernel.PhysicalCallbackCompletion{}, err
	}
	return original.completion, nil
}
