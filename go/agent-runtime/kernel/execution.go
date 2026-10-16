package kernel

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ErrExecutionConflict means the caller does not hold the current,
// unexpired authoritative Run execution lease. A queue claim or a matching
// Run revision alone never authorizes a model/Tool execution commit.
var (
	ErrExecutionConflict = errors.New("kernel execution lease is not current")
	ErrExecutionRequired = errors.New("kernel run requires fenced execution")
	// ErrExecutionBusy is a retryable Store write-lock error, NOT proof that
	// the Run's current owner/revision changed. It must never be ACKed as stale.
	ErrExecutionBusy = errors.New("kernel execution Store temporarily busy")
)

// ExecutionLease represents an exclusive, Store-authoritative execution
// segment. Epoch advances on takeover even when Run.Revision is unchanged.
// The opaque LeaseID is a separate claim identity, not a queue lease.
type ExecutionLease struct {
	RunID     string
	OwnerID   string
	LeaseID   string
	Epoch     uint64
	ExpiresAt time.Time
}

// ExecutionAcquireRequest is a request for an owner-exclusive Run segment.
// Store adapters MUST obtain current time from their own authoritative clock,
// not trust a caller-supplied Now.
type ExecutionAcquireRequest struct {
	RunID         string
	OwnerID       string
	LeaseDuration time.Duration
}

// ExecutionRenewRequest keeps the same epoch and lease identity, extending
// the expiration if and only if the segment remains active.
type ExecutionRenewRequest struct {
	Lease         ExecutionLease
	LeaseDuration time.Duration
}

// ExecutionStore is an opt-in Stage 2 Store port. It is NOT folded into
// Store so existing unleased six-state applications remain compatible.
// Once a Run is claimed, Store.Apply MUST reject unfenced updates for that
// Run, even after expiry/release, until a later explicit control port is
// implemented. The fenced Apply checks epoch, owner, lease ID, expiry and
// Run revision within the SAME authoritative transaction.
type ExecutionStore interface {
	AcquireExecution(context.Context, ExecutionAcquireRequest) (ExecutionLease, error)
	RenewExecution(context.Context, ExecutionRenewRequest) (ExecutionLease, error)
	ReleaseExecution(context.Context, ExecutionLease) error
	ApplyExecution(context.Context, ExecutionLease, uint64, StoreMutation) (Snapshot, error)
}

// WithExecution returns a scoped Runtime bound to one exact lease.
// It uses the same immutable Kernel dependencies, but normal Runtime.Apply
// automatically routes to ExecutionStore.ApplyExecution.
// A plain Runtime can still Load; it CANNOT mutate the claimed Run without
// explicit execution authority.
func (runtime *Runtime) WithExecution(lease ExecutionLease) (*Runtime, error) {
	if runtime == nil || !validExecutionLease(lease) {
		return nil, ErrInvalidInput
	}
	if _, ok := runtime.store.(ExecutionStore); !ok {
		return nil, ErrExecutionRequired
	}
	bound := *runtime
	bound.execution = &lease
	return &bound, nil
}

// AcquireExecution obtains a fenced execution segment using Store time.
func (runtime *Runtime) AcquireExecution(ctx context.Context, request ExecutionAcquireRequest) (ExecutionLease, error) {
	if runtime == nil || !validExecutionAcquireRequest(request) {
		return ExecutionLease{}, ErrInvalidInput
	}
	store, ok := runtime.store.(ExecutionStore)
	if !ok {
		return ExecutionLease{}, ErrExecutionRequired
	}
	return store.AcquireExecution(ctx, request)
}

// RenewExecution never changes epoch. Expired segments cannot be revived.
func (runtime *Runtime) RenewExecution(ctx context.Context, request ExecutionRenewRequest) (ExecutionLease, error) {
	if runtime == nil || !validExecutionLease(request.Lease) || !validExecutionDuration(request.LeaseDuration) {
		return ExecutionLease{}, ErrInvalidInput
	}
	store, ok := runtime.store.(ExecutionStore)
	if !ok {
		return ExecutionLease{}, ErrExecutionRequired
	}
	return store.RenewExecution(ctx, request)
}

// ReleaseExecution relinquishes the segment; a new claim increments epoch.
func (runtime *Runtime) ReleaseExecution(ctx context.Context, lease ExecutionLease) error {
	if runtime == nil || !validExecutionLease(lease) {
		return ErrInvalidInput
	}
	store, ok := runtime.store.(ExecutionStore)
	if !ok {
		return ErrExecutionRequired
	}
	return store.ReleaseExecution(ctx, lease)
}

const maxExecutionLeaseDuration = 24 * time.Hour

func validExecutionDuration(ttl time.Duration) bool {
	return ttl >= time.Millisecond && ttl <= maxExecutionLeaseDuration
}
func validExecutionAcquireRequest(r ExecutionAcquireRequest) bool {
	return strings.TrimSpace(r.RunID) != "" && strings.TrimSpace(r.OwnerID) != "" && validExecutionDuration(r.LeaseDuration)
}
func validExecutionLease(l ExecutionLease) bool {
	return strings.TrimSpace(l.RunID) != "" && strings.TrimSpace(l.OwnerID) != "" &&
		strings.TrimSpace(l.LeaseID) != "" && l.Epoch > 0 && !l.ExpiresAt.IsZero()
}

// The lease cannot be forged by simply replacing RunID on a bound Runtime:
// Apply enforces the expected Run ID and exact Store lease in one transaction.
func (runtime *Runtime) validBoundRun(runID string) bool {
	return runtime.execution == nil || (validExecutionLease(*runtime.execution) && runtime.execution.RunID == runID)
}
