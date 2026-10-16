package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"math"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type memoryExecutionState struct {
	epoch uint64
	lease kernel.ExecutionLease
}

var _ kernel.ExecutionStore = (*Store)(nil)

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
	snap, ok := store.records[request.RunID]
	if !ok {
		return kernel.ExecutionLease{}, kernel.ErrNotFound
	}
	switch snap.Run.Status {
	case kernel.RunStatusCompleted, kernel.RunStatusFailed, kernel.RunStatusCancelled:
		return kernel.ExecutionLease{}, kernel.ErrTerminal
	case kernel.RunStatusRunning:
	case kernel.RunStatusWaitingInput, kernel.RunStatusPausedBudget:
		// Never grant execution for a pending approval/budget gate.
		return kernel.ExecutionLease{}, kernel.ErrExecutionConflict
	default:
		// Unknown statuses are not executable.
		return kernel.ExecutionLease{}, kernel.ErrExecutionConflict
	}
	now := store.currentExecutionTime()
	state := store.execution[request.RunID]
	if state.epoch > 0 && now.Before(state.lease.ExpiresAt) {
		return kernel.ExecutionLease{}, kernel.ErrExecutionConflict
	}
	if state.epoch == math.MaxUint64 {
		return kernel.ExecutionLease{}, kernel.ErrExecutionConflict
	}
	id, err := newMemoryLeaseID()
	if err != nil {
		return kernel.ExecutionLease{}, err
	}
	lease := kernel.ExecutionLease{
		RunID: request.RunID, OwnerID: strings.TrimSpace(request.OwnerID), LeaseID: id,
		Epoch: state.epoch + 1, ExpiresAt: now.Add(request.LeaseDuration),
	}
	store.execution[request.RunID] = memoryExecutionState{epoch: lease.Epoch, lease: lease}
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
	now := store.currentExecutionTime()
	state := store.execution[req.Lease.RunID]
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
	if !activeMemoryLease(store.execution[lease.RunID], lease, store.currentExecutionTime()) {
		return kernel.Snapshot{}, kernel.ErrExecutionConflict
	}
	current, ok := store.records[lease.RunID]
	if !ok {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	if current.Run.Status != kernel.RunStatusRunning {
		// Even the incumbent owner cannot lift a manual approval/budget gate.
		// Only a Feature-validated ApplyControl can release it.
		return kernel.Snapshot{}, kernel.ErrExecutionRequired
	}
	return store.applyLocked(lease.RunID, expectedRevision, mutation)
}
