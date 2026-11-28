package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type memoryExecutionState struct {
	epoch          uint64
	physicalEpoch  uint64 // Last actual owned callback admission, never lease churn.
	lease          kernel.ExecutionLease
	quarantineRev  uint64
	quarantineGen  uint64
	quarantineCode string
}

var _ kernel.ExecutionStore = (*Store)(nil)
var _ kernel.ExecutionQuarantineStore = (*Store)(nil)

// NewStoreWithExecutionClock supplies a deterministic Store-owned clock for
// fault tests. The default NewStore uses the host monotonic wall-clock source.
func NewStoreWithExecutionClock(clock kernel.Clock) *Store {
	s := NewStore()
	if clock != nil {
		s.executionClock = clock.Now
	}
	return s
}
func (store *Store) currentExecutionTime() time.Time {
	if store.executionClock == nil {
		return time.Now().UTC()
	}
	return store.executionClock().UTC()
}
func newMemoryLeaseID() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}
func validMemoryExecutionRequest(r kernel.ExecutionAcquireRequest) bool {
	return strings.TrimSpace(r.RunID) != "" && strings.TrimSpace(r.OwnerID) != "" &&
		r.LeaseDuration >= time.Millisecond && r.LeaseDuration <= 24*time.Hour
}
func validMemoryLease(l kernel.ExecutionLease) bool {
	return l.RunID != "" && l.OwnerID != "" && l.LeaseID != "" && l.Epoch != 0 && !l.ExpiresAt.IsZero()
}
func activeMemoryLease(state memoryExecutionState, claimed kernel.ExecutionLease, now time.Time) bool {
	return state.epoch > 0 && state.lease.RunID == claimed.RunID &&
		state.lease.OwnerID == claimed.OwnerID && state.lease.LeaseID == claimed.LeaseID &&
		state.lease.Epoch == claimed.Epoch &&
		now.Before(state.lease.ExpiresAt)
}
func (store *Store) AcquireExecution(ctx context.Context, request kernel.ExecutionAcquireRequest) (kernel.ExecutionLease, error) {
	if store == nil || ctx == nil || !validMemoryExecutionRequest(request) {
		return kernel.ExecutionLease{}, kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return kernel.ExecutionLease{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.nativeEntryGateLocked(request.RunID); err != nil {
		return kernel.ExecutionLease{}, kernel.ErrChildExecutionBusy
	}
	snap, ok := store.records[request.RunID]
	if !ok {
		return kernel.ExecutionLease{}, kernel.ErrNotFound
	}
	if _, exists := store.ownedGrantLocked(request.RunID); exists {
		return kernel.ExecutionLease{}, kernel.ErrChildExecutionBusy
	}
	state := store.execution[request.RunID]
	current := state.lease
	current.Epoch = state.epoch
	id, err := newMemoryLeaseID()
	if err != nil {
		return kernel.ExecutionLease{}, err
	}
	lease, err := kernel.PlanExecutionAcquire(snap.Run, current, store.currentExecutionTime(), request, id, state.quarantineRev != 0)
	if err != nil {
		return kernel.ExecutionLease{}, err
	}
	state.epoch, state.lease = lease.Epoch, lease
	store.execution[request.RunID] = state
	return lease, nil
}
func (store *Store) RenewExecution(ctx context.Context, req kernel.ExecutionRenewRequest) (kernel.ExecutionLease, error) {
	if store == nil || ctx == nil || !validMemoryLease(req.Lease) || req.LeaseDuration < time.Millisecond || req.LeaseDuration > 24*time.Hour {
		return kernel.ExecutionLease{}, kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return kernel.ExecutionLease{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.nativeEntryGateLocked(req.Lease.RunID); err != nil {
		return kernel.ExecutionLease{}, err
	}
	now := store.currentExecutionTime()
	state := store.execution[req.Lease.RunID]
	if g, ok := store.ownedGrantLocked(req.Lease.RunID); ok && !g.grant.MatchesExecution(req.Lease) {
		return kernel.ExecutionLease{}, kernel.ErrExecutionConflict
	}
	if !activeMemoryLease(state, req.Lease, now) {
		return kernel.ExecutionLease{}, kernel.ErrExecutionConflict
	}
	state.lease.ExpiresAt = now.Add(req.LeaseDuration)
	store.execution[req.Lease.RunID] = state
	return state.lease, nil
}
func (store *Store) ReleaseExecution(ctx context.Context, lease kernel.ExecutionLease) error {
	if store == nil || ctx == nil || !validMemoryLease(lease) {
		return kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.nativeEntryGateLocked(lease.RunID); err != nil {
		return err
	}
	if _, exists := store.ownedGrantLocked(lease.RunID); exists {
		return kernel.ErrChildExecutionBusy
	}
	state := store.execution[lease.RunID]
	if !activeMemoryLease(state, lease, store.currentExecutionTime()) {
		return kernel.ErrExecutionConflict
	}
	state.lease = kernel.ExecutionLease{} // Preserve epoch; old token stays fenced.
	store.execution[lease.RunID] = state
	return nil
}
func (store *Store) ApplyExecution(ctx context.Context, lease kernel.ExecutionLease, expectedRevision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	if store == nil || ctx == nil || !validMemoryLease(lease) || expectedRevision == 0 || mutation.Record.Run.ID != lease.RunID || mutation.Record.Run.Revision != expectedRevision+1 {
		return kernel.Snapshot{}, kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return kernel.Snapshot{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.nativeEntryGateLocked(lease.RunID); err != nil {
		return kernel.Snapshot{}, err
	}
	state := store.execution[lease.RunID]
	if g, ok := store.ownedGrantLocked(lease.RunID); ok && !g.grant.MatchesExecution(lease) {
		return kernel.Snapshot{}, kernel.ErrExecutionConflict
	}
	if state.quarantineRev != 0 {
		return kernel.Snapshot{}, kernel.ErrExecutionQuarantined
	}
	if !activeMemoryLease(state, lease, store.currentExecutionTime()) {
		return kernel.Snapshot{}, kernel.ErrExecutionConflict
	}
	current, ok := store.records[lease.RunID]
	if !ok {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	if mutation.Ledger != nil && current.Run.ParentRunID != "" {
		if _, err := store.validateLiveRunOwners(ctx, current.Run); err != nil {
			return kernel.Snapshot{}, err
		}
	}
	if current.Run.Status != kernel.RunStatusRunning {
		// Even the incumbent owner cannot lift a manual approval/budget gate.
		// Only a Feature-validated ApplyControl can release it.
		return kernel.Snapshot{}, kernel.ErrExecutionRequired
	}
	return store.applyLocked(ctx, lease.RunID, expectedRevision, mutation, false)
}

// CheckExecution obtains the Store-owned clock under the same mutex as epoch
// changes. An informational client ExpiresAt cannot revive a stale token.
func (store *Store) CheckExecution(ctx context.Context, runID string, lease *kernel.ExecutionLease) error {
	if store == nil || ctx == nil || strings.TrimSpace(runID) == "" {
		return kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if err := store.nativeEntryGateLocked(runID); err != nil {
		return err
	}
	snap, ok := store.records[runID]
	if !ok {
		return kernel.ErrNotFound
	}
	if snap.Run.Status == kernel.RunStatusCompleted || snap.Run.Status == kernel.RunStatusFailed || snap.Run.Status == kernel.RunStatusCancelled {
		return kernel.ErrTerminal
	}
	state := store.execution[runID]
	if state.quarantineRev != 0 {
		return kernel.ErrExecutionQuarantined
	}
	if lease == nil {
		if state.epoch > 0 {
			return kernel.ErrExecutionRequired
		}
		return nil
	}
	if g, ok := store.ownedGrantLocked(runID); ok && !g.grant.MatchesExecution(*lease) {
		return kernel.ErrExecutionConflict
	}
	if !validMemoryLease(*lease) || lease.RunID != runID {
		return kernel.ErrExecutionConflict
	}
	if snap.Run.Status != kernel.RunStatusRunning {
		return kernel.ErrExecutionRequired
	}
	if !activeMemoryLease(state, *lease, store.currentExecutionTime()) {
		return kernel.ErrExecutionConflict
	}
	return nil
}
