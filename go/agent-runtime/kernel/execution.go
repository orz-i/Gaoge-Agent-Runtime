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
// ExpiresAt is informational. Store-owned time and persisted expiry determine
// whether the stable (RunID, Epoch, OwnerID, LeaseID) token remains valid;
// renewing it does not require rebinding a Runtime/Agent loop.
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

// ExecutionRenewRequest extends the Store expiry of the existing stable
// execution identity without rotating the owner's token.
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
	// CheckExecution is a Store-time, pre-dispatch authorization check.
	// nil token allows only a Run never claimed by execution fencing.
	// It does not promise physical exactly-once effects after a check.
	CheckExecution(context.Context, string, *ExecutionLease) error
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

// SupportsExecution reports whether the current Store can fence a direct
// Run. A Runner opting into automatic execution must check this BEFORE
// creating a Run to avoid leaving an unrecoverable orphan on a legacy Store.
func (runtime *Runtime) SupportsExecution() bool {
	if runtime == nil {
		return false
	}
	_, ok := runtime.store.(ExecutionStore)
	return ok
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

// RenewExecution keeps the bearer token stable. Expired segments cannot be revived.
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

// RequireExecution validates current Store authority BEFORE a Feature
// dispatches a Model or Tool. Legacy Stores stay compatible; a Store
// advertising ExecutionStore must fail closed for fenced, unbound Runs.
// Store CAS fencing remains independently enforced at commit.
func (runtime *Runtime) RequireExecution(ctx context.Context, runID string) error {
	if runtime == nil || ctx == nil || strings.TrimSpace(runID) == "" {
		return ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !runtime.validBoundRun(runID) {
		return ErrExecutionConflict
	}
	store, ok := runtime.store.(ExecutionStore)
	if !ok {
		if runtime.execution != nil {
			return ErrExecutionRequired
		}
		return nil
	}
	return store.CheckExecution(ctx, runID, runtime.execution)
}
