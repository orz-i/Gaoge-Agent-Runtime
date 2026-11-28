package memory

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.OwnedPhysicalExecutionStore = (*Store)(nil)

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
func (s *Store) BeginOwnedPhysicalExecution(ctx context.Context, p kernel.OwnedPhysicalAdmission) (kernel.OwnedPhysicalAdmissionResult, error) {
	var zero kernel.OwnedPhysicalAdmissionResult
	if s == nil || ctx == nil {
		return zero, kernel.ErrInvalidInput
	}
	g := p.Candidate().Grant
	if !g.Identity().Valid() {
		return kernel.OwnedPhysicalAdmissionResult{}, kernel.ErrExecutionConflict
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
	if _, e := s.validateLiveRunOwners(ctx, current.Run); e != nil {
		return zero, e
	}
	if _, occupied := s.effects[g.ID]; occupied {
		return zero, kernel.ErrExecutionConflict
	}
	native := s.runs[g.ChildRunID].execution
	persisted := native.lease
	persisted.Epoch = native.epoch
	var v kernel.OwnedPhysicalAdmissionResult
	var next kernel.ManagedTransfer
	var old memoryEffect
	var e error
	if p.IsManagedRecovery() {
		var found bool
		old, found = s.ownedGrantLocked(g.ChildRunID)
		if !found || !s.managedSoleGrantLocked(g.ChildRunID, old.grant.ID) || old.settledRevision != 0 || old.completion.GrantID != "" || native.quarantineRev != 0 {
			return zero, kernel.ErrExecutionConflict
		}
		t := s.managedTransfers[s.managedCurrent[g.ChildRunID]]
		v, next, e = kernel.ResolveManagedPhysicalAdmission(ctx, t, p, current, persisted, old.grant, s.managedAuthority, native.physicalEpoch, s.currentExecutionTime(), s.ledgers)
	} else {
		hasGrants := len(s.effectOwners[g.ChildRunID]) != 0
		v, e = kernel.ResolveOwnedPhysicalAdmission(current, persisted, s.currentExecutionTime(), native.quarantineRev != 0, hasGrants, p)
	}
	if e != nil {
		return zero, e
	}
	g = v.Grant
	entryEpoch, e := kernel.PrepareOwnedEntryEpoch(g, native.epoch, native.physicalEpoch)
	if e != nil {
		return zero, e
	}
	if p.IsManagedRecovery() {
		s.consumeEffectLocked(old, nil)
		s.saveManagedLocked(next)
	}
	s.effects[g.ID] = memoryEffect{grant: g, ancestors: []string{g.ChildRunID}}
	s.effectOwners[g.ChildRunID] = map[string]struct{}{g.ID: {}}
	if p.AcquiresLease() {
		native.epoch, native.lease = v.Lease.Epoch, v.Lease
	}
	native.physicalEpoch = entryEpoch
	s.setExecutionLocked(g.ChildRunID, native)
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
	if s == nil || ctx == nil || kernel.ValidateOwnedPhysicalAdmissionRequest(g, l) != nil {
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
		original.settledRevision, original.completion, transfer)
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
	if err := kernel.ValidateOwnedPhysicalRecovery(g, current, s.runs[g.ChildRunID].execution.lease, revision, m, p, original.completion, original.settledRevision); err != nil {
		return kernel.Snapshot{}, err
	}
	m.PhysicalPermit = p.PhysicalPermit(g, current, m)
	return s.applyLocked(ctx, g.ChildRunID, revision, m, false)
}

// RecordOwnedPhysicalCallbackReturn changes only the original physical scope,
// never the Run projection, wakeup revision, checkpoint or lease epoch.
func (s *Store) RecordOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, o kernel.OwnedPhysicalCallbackOutcome, p kernel.OwnedPhysicalCallbackReturn) error {
	if s == nil || ctx == nil || !g.IsOwned() || rev == 0 {
		return kernel.ErrInvalidInput
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	original, err := s.loadOwnedPhysicalGrantLocked(g)
	current, exists := s.snapshotLocked(g.ChildRunID)
	if err != nil || !exists {
		return kernel.ErrExecutionConflict
	}
	native := s.runs[g.ChildRunID].execution
	if native.epoch != g.ExecutionEpoch {
		return kernel.ErrExecutionConflict
	}
	completion, write, err := kernel.ReduceOwnedPhysicalCallbackReturn(g, current, native.lease, rev, o, p, original.completion, original.settledRevision, native.physicalEpoch)
	if err != nil || !write {
		return err
	}
	original.completion = completion
	s.effects[g.ID] = original
	native.physicalEpoch = g.ExecutionEpoch
	s.setExecutionLocked(g.ChildRunID, native)
	return nil
}
func (s *Store) LookupOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.OwnedPhysicalCallbackCompletion, error) {
	if s == nil || ctx == nil || !g.IsOwned() {
		return kernel.OwnedPhysicalCallbackCompletion{}, kernel.ErrInvalidInput
	}
	if e := ctx.Err(); e != nil {
		return kernel.OwnedPhysicalCallbackCompletion{}, e
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	original, err := s.loadOwnedPhysicalGrantLocked(g)
	if err != nil {
		return kernel.OwnedPhysicalCallbackCompletion{}, err
	}
	if _, exists := s.snapshotLocked(g.ChildRunID); !exists {
		return kernel.OwnedPhysicalCallbackCompletion{}, kernel.ErrExecutionConflict
	}
	if err := kernel.ValidateOwnedPhysicalCompletion(g, original.completion); err != nil {
		return kernel.OwnedPhysicalCallbackCompletion{}, err
	}
	return original.completion, nil
}
