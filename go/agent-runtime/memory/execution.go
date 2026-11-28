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
	snap, ok := store.snapshotLocked(request.RunID)
	if !ok {
		return kernel.ExecutionLease{}, kernel.ErrNotFound
	}
	if _, exists := store.ownedGrantLocked(request.RunID); exists {
		return kernel.ExecutionLease{}, kernel.ErrChildExecutionBusy
	}
	state := store.runs[request.RunID].execution
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
	store.setExecutionLocked(request.RunID, state)
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
	state := store.runs[req.Lease.RunID].execution
	if g, ok := store.ownedGrantLocked(req.Lease.RunID); ok && !g.grant.MatchesExecution(req.Lease) {
		return kernel.ExecutionLease{}, kernel.ErrExecutionConflict
	}
	if !activeMemoryLease(state, req.Lease, now) {
		return kernel.ExecutionLease{}, kernel.ErrExecutionConflict
	}
	state.lease.ExpiresAt = now.Add(req.LeaseDuration)
	store.setExecutionLocked(req.Lease.RunID, state)
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
	state := store.runs[lease.RunID].execution
	if !activeMemoryLease(state, lease, store.currentExecutionTime()) {
		return kernel.ErrExecutionConflict
	}
	state.lease = kernel.ExecutionLease{} // Preserve epoch; old token stays fenced.
	store.setExecutionLocked(lease.RunID, state)
	return nil
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
	snap, ok := store.snapshotLocked(runID)
	if !ok {
		return kernel.ErrNotFound
	}
	if snap.Run.Status == kernel.RunStatusCompleted || snap.Run.Status == kernel.RunStatusFailed || snap.Run.Status == kernel.RunStatusCancelled {
		return kernel.ErrTerminal
	}
	state := store.runs[runID].execution
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
