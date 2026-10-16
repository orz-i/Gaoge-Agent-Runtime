package kernel

import (
	"bytes"
	"context"
	"strings"
	"time"
)

// ControlKind enumerates only the current trusted Feature-owned gates.
// This is NOT a generic command bus, user credential or execution lease.
type ControlKind string

const (
	ControlApproval  ControlKind = "approval"
	ControlAllowance ControlKind = "allowance"
	ControlCancel    ControlKind = "cancel"
	ControlDeadline  ControlKind = "deadline"
)

// ControlGuard identifies the exact persisted gate whose validated control
// transition is being committed. A host MUST authenticate the caller and
// Feature MUST validate the Tool approval / allowance before invoking it.
type ControlGuard struct {
	Kind         ControlKind
	CheckpointID string // Required for approval; binds the pending Checkpoint.
	BlockedCode  string // Required for allowance; binds the blocked dimension.
}

// ControlStore is one optional, guarded-CAS Store capability. It does not
// authorize arbitrary execution: the source gate and target must match.
// The returned bool is true only if the committed control revoked a prior
// execution epoch. The Feature must not automatically dispatch a Model/Tool
// after such a control until an executor has acquired new Store authority.
// Unfenced legacy Stores still use their existing Store.Apply semantics.
type ControlStore interface {
	ApplyControl(context.Context, string, uint64, ControlGuard, StoreMutation) (Snapshot, bool, error)
}

// ValidateControl is pure, Feature-neutral gate validation. Store adapters
// MUST perform it on an authoritative snapshot under the SAME write
// transaction / row lock as their revision CAS and owner revocation.
func ValidateControl(current Snapshot, guard ControlGuard, mutation StoreMutation, storeNow time.Time) error {
	next := mutation.Record
	if current.Run.ID == "" || next.Run.ID != current.Run.ID || next.Run.Revision != current.Run.Revision+1 {
		return ErrConflict
	}
	// Control may not rewrite immutable Run identity or change its deadline.
	if next.Run.Kind != current.Run.Kind || next.Run.Actor != current.Run.Actor ||
		next.Run.Thread != current.Run.Thread || next.Run.Goal != current.Run.Goal ||
		next.Run.RequestID != current.Run.RequestID ||
		!next.Run.CreatedAt.Equal(current.Run.CreatedAt) ||
		(next.Run.DeadlineAt == nil) != (current.Run.DeadlineAt == nil) {
		return ErrInvalidInput
	}
	if next.Run.DeadlineAt != nil && !next.Run.DeadlineAt.Equal(*current.Run.DeadlineAt) {
		return ErrInvalidInput
	}

	if terminalStatus(current.Run.Status) {
		return ErrTerminal
	}
	if !validTransition(current.Run.Status, next.Run.Status) || !validState(next.State) {
		return ErrInvalidInput
	}
	if next.Result != nil {
		return ErrInvalidInput
	}
	switch guard.Kind {
	case ControlApproval:
		if strings.TrimSpace(guard.CheckpointID) == "" || guard.BlockedCode != "" ||
			current.Run.Status != RunStatusWaitingInput || current.Checkpoint == nil ||
			current.Checkpoint.ID != guard.CheckpointID || current.Checkpoint.Status != CheckpointPending ||
			next.Run.Status != RunStatusRunning {
			return ErrInvalidInput
		}
		if next.Checkpoint != nil {
			// Approval-resolution decisions and exact pending Tool bindings are
			// verified by the owning Feature; Kernel verifies checkpoint identity.
			if next.Checkpoint.ID != current.Checkpoint.ID ||
				next.Checkpoint.Kind != current.Checkpoint.Kind ||
				next.Checkpoint.Status != CheckpointResolved ||
				!bytes.Equal(next.Checkpoint.Payload, current.Checkpoint.Payload) {
				return ErrInvalidInput
			}
		}
	case ControlAllowance:
		if guard.CheckpointID != "" || strings.TrimSpace(guard.BlockedCode) == "" ||
			current.Run.Status != RunStatusPausedBudget || current.Run.ErrorCode != guard.BlockedCode ||
			next.Run.Status != RunStatusRunning {
			return ErrInvalidInput
		}
	case ControlCancel:
		if guard.CheckpointID != "" || guard.BlockedCode != "" || next.Run.Status != RunStatusCancelled ||
			next.Run.ErrorCode != "run.cancelled" || !bytes.Equal(next.State, current.State) {
			return ErrInvalidInput
		}
	case ControlDeadline:
		if guard.CheckpointID != "" || guard.BlockedCode != "" || next.Run.Status != RunStatusFailed ||
			next.Run.ErrorCode != "run.deadline_exceeded" || !bytes.Equal(next.State, current.State) ||
			current.Run.DeadlineAt == nil || storeNow.IsZero() || current.Run.DeadlineAt.After(storeNow) {
			return ErrDeadline
		}
	default:
		return ErrInvalidInput
	}
	return nil
}

// ApplyControl is a trusted Feature-validated gate/termination CAS. It never
// accepts an execution-owned Runtime token or bypasses a Store guard. The
// Store revalidates conditions and atomically revokes old authority with the
// state, journal and ReadyWork. A successful revoked=true result requires
// a new execution claim before the Feature dispatches external effects.
func (runtime *Runtime) ApplyControl(ctx context.Context, runID string, expectedRevision uint64,
	guard ControlGuard, mutation Mutation) (Snapshot, bool, error) {
	if runtime == nil || runtime.execution != nil {
		return Snapshot{}, false, ErrInvalidInput
	}
	current, err := runtime.Load(ctx, runID)
	if err != nil {
		return Snapshot{}, false, err
	}
	if current.Run.Revision != expectedRevision {
		return Snapshot{}, false, ErrConflict
	}
	if err = validateMutation(current, mutation); err != nil {
		return Snapshot{}, false, err
	}
	next := runtime.makeStoreMutation(current, mutation)
	if err = ValidateControl(current, guard, next, runtime.Now()); err != nil {
		return Snapshot{}, false, err
	}
	if store, ok := runtime.store.(ControlStore); ok {
		return store.ApplyControl(ctx, runID, expectedRevision, guard, next)
	}
	// Older custom stores implement no execution epochs and retain the existing
	// six-state single-executor behavior; never fall back on a fenced Store.
	if _, fenced := runtime.store.(ExecutionStore); fenced {
		return Snapshot{}, false, ErrExecutionRequired
	}
	result, err := runtime.store.Apply(ctx, runID, expectedRevision, next)
	return result, false, err
}
