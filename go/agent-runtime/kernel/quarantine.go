package kernel

import (
	"context"
	"errors"
	"strings"
)

// ErrExecutionQuarantined means the Run retains an authoritative execution
// block until an exact trusted repair or terminal control. A Queue DLQ is not
// this block; an unrelated Run revision cannot silently release it.
var ErrExecutionQuarantined = errors.New("kernel current work is quarantined")

// ExecutionQuarantine binds an operator/Feature-verified poison diagnosis to
// one current Run revision and its feature-owned wakeup generation. Revision
// is the existing RunID:Revision work identity (not a new progress plan).
// The Store cannot interpret opaque Feature state, so only a trusted host
// that independently validates Generation/Reason may call this capability.
type ExecutionQuarantine struct {
	RunID      string
	Revision   uint64
	Generation uint64
	Reason     string
}

// ExecutionQuarantineStore is an optional Stage 2 authoritative Store guard.
// The initial block must atomically revoke active execution authority;
// Check/Acquire/ApplyExecution must all reject the current blocked revision.
// Repair requires the EXACT stored block identity and fresh Run revision;
// callers MUST authenticate the operator and verify ambiguous effects first.
// Neither method is exposed to unauthenticated HTTP/queue producers.
type ExecutionQuarantineStore interface {
	QuarantineExecution(context.Context, ExecutionQuarantine) error
	RepairExecutionQuarantine(context.Context, ExecutionQuarantine) error
}

func validExecutionQuarantine(value ExecutionQuarantine) bool {
	return value.RunID != "" && strings.TrimSpace(value.RunID) == value.RunID &&
		len(value.RunID) <= 128 && value.Revision > 0 &&
		value.Reason != "" && len(value.Reason) <= 64 &&
		strings.TrimSpace(value.Reason) == value.Reason
}

// SupportsExecutionQuarantine distinguishes a Store that actually persists
// this guard from a legacy Store wrapper that cannot provide it.
func (runtime *Runtime) SupportsExecutionQuarantine() bool {
	if runtime == nil {
		return false
	}
	_, ok := runtime.store.(ExecutionQuarantineStore)
	return ok
}

// QuarantineExecution is a trusted-internal operation, NOT a user command or
// a repair authorization grant. Store revision CAS makes stale work harmless.
func (runtime *Runtime) QuarantineExecution(ctx context.Context, value ExecutionQuarantine) error {
	if runtime == nil || ctx == nil || !validExecutionQuarantine(value) {
		return ErrInvalidInput
	}
	if !runtime.validBoundRun(value.RunID) {
		return ErrExecutionConflict
	}
	store, ok := runtime.store.(ExecutionQuarantineStore)
	if !ok {
		return ErrExecutionRequired
	}
	return store.QuarantineExecution(ctx, value)
}

// RepairExecutionQuarantine must be called only by a host that has validated
// the exact work identity and any unresolved external Provider disposition.
func (runtime *Runtime) RepairExecutionQuarantine(ctx context.Context, value ExecutionQuarantine) error {
	if runtime == nil || ctx == nil || !validExecutionQuarantine(value) {
		return ErrInvalidInput
	}
	if !runtime.validBoundRun(value.RunID) {
		return ErrExecutionConflict
	}
	store, ok := runtime.store.(ExecutionQuarantineStore)
	if !ok {
		return ErrExecutionRequired
	}
	return store.RepairExecutionQuarantine(ctx, value)
}
