package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
)

// ControlKind enumerates only the current trusted Feature-owned gates.
// This is NOT a generic command bus, user credential or execution lease.
type ControlKind string

const (
	ControlApproval  ControlKind = "approval"
	ControlAllowance ControlKind = "allowance"
	ControlCancel    ControlKind = "cancel"
	ControlDeadline  ControlKind = "deadline"
	// ControlExternalSignal atomically records Feature-validated readiness
	// for an already-persisted wait; it is NOT an inbox or Tool receipt.
	ControlExternalSignal ControlKind = "external_signal"
)

// ControlGuard identifies the exact persisted gate whose validated control
// transition is being committed. A host MUST authenticate the caller and
// Feature MUST validate the Tool approval / allowance before invoking it.
type ControlGuard struct {
	Kind           ControlKind
	CheckpointID   string
	BlockedCode    string
	WaitID         string     // Stable Feature wait identity including generation.
	WaitDeadlineAt *time.Time // Store-time limit from the persisted wait.
	// Unexported and minted only by first-party Feature validators; custom SDK
	// consumers and independent Store adapters cannot forge a guarded resume.
	verified *featurecontrol.Proof
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
	// The Store clock controls whether a suspended Run is still eligible
	// to return to running. A client clock cannot extend the deadline.
	if (guard.Kind == ControlApproval || guard.Kind == ControlAllowance || guard.Kind == ControlExternalSignal) &&
		current.Run.DeadlineAt != nil && !current.Run.DeadlineAt.After(storeNow) {
		return ErrDeadline
	}
	if guard.Kind == ControlApproval || guard.Kind == ControlAllowance || guard.Kind == ControlExternalSignal {
		source := guard.CheckpointID
		if guard.Kind == ControlAllowance {
			source = guard.BlockedCode
		}
		if guard.Kind == ControlExternalSignal {
			source = guard.WaitID
		}
		// Trust boundary: arbitrary SDK calls cannot change Feature State
		// without a proof issued by a first-party Feature after validation.
		if !guard.verified.Matches(current.Run.ID, current.Run.Revision,
			string(guard.Kind), source, next.State, next.Checkpoint) {
			return ErrInvalidInput
		}
	}
	switch guard.Kind {
	case ControlApproval:
		if guard.WaitID != "" || guard.WaitDeadlineAt != nil {
			return ErrInvalidInput
		}
		if strings.TrimSpace(guard.CheckpointID) == "" || guard.BlockedCode != "" ||
			current.Run.Status != RunStatusWaitingInput || current.Checkpoint == nil ||
			current.Checkpoint.ID != guard.CheckpointID || current.Checkpoint.Status != CheckpointPending ||
			next.Run.Status != RunStatusRunning {
			return ErrInvalidInput
		}
		// Nil checkpoint previously cleared a gate without a persisted
		// decision. Anchor the resolved decision to the exact payload.
		if next.Checkpoint == nil ||
			next.Checkpoint.ID != current.Checkpoint.ID ||
			next.Checkpoint.Kind != current.Checkpoint.Kind ||
			next.Checkpoint.Status != CheckpointResolved ||
			!bytes.Equal(next.Checkpoint.Payload, current.Checkpoint.Payload) {
			return ErrInvalidInput
		}
		var answer struct {
			Decision string `json:"decision"`
		}
		if json.Unmarshal(next.Checkpoint.Response, &answer) != nil ||
			(answer.Decision != "approve" && answer.Decision != "reject") {
			return ErrInvalidInput
		}
	case ControlAllowance:
		if guard.WaitID != "" || guard.WaitDeadlineAt != nil {
			return ErrInvalidInput
		}
		if guard.CheckpointID != "" || strings.TrimSpace(guard.BlockedCode) == "" ||
			current.Run.Status != RunStatusPausedBudget || current.Run.ErrorCode != guard.BlockedCode ||
			next.Run.Status != RunStatusRunning || bytes.Equal(next.State, current.State) {
			// A no-op status reset is never proof of a changed allowance.
			// The Agent validator remains responsible for hard ceilings.
			return ErrInvalidInput
		}
	case ControlExternalSignal:
		// The Feature sealed an exact wait and signal after checking the
		// persisted ToolWait generation. The Store checks time and revision
		// and revokes old owners under the SAME CAS as the wakeup outbox.
		if guard.CheckpointID != "" || guard.BlockedCode != "" ||
			strings.TrimSpace(guard.WaitID) == "" ||
			guard.WaitDeadlineAt == nil || storeNow.IsZero() ||
			!guard.WaitDeadlineAt.After(storeNow) ||
			current.Run.Status != RunStatusRunning || next.Run.Status != RunStatusRunning ||
			bytes.Equal(next.State, current.State) ||
			!reflect.DeepEqual(next.Checkpoint, current.Checkpoint) ||
			next.Run.ErrorCode != current.Run.ErrorCode ||
			next.Run.ErrorDetail != current.Run.ErrorDetail {
			if guard.WaitDeadlineAt != nil && !guard.WaitDeadlineAt.After(storeNow) {
				return ErrDeadline
			}
			return ErrInvalidInput
		}
	case ControlCancel:
		if guard.WaitID != "" || guard.WaitDeadlineAt != nil {
			return ErrInvalidInput
		}
		if guard.CheckpointID != "" || guard.BlockedCode != "" || next.Run.Status != RunStatusCancelled ||
			next.Run.ErrorCode != "run.cancelled" || !bytes.Equal(next.State, current.State) {
			return ErrInvalidInput
		}
	case ControlDeadline:
		if guard.WaitID != "" || guard.WaitDeadlineAt != nil {
			return ErrInvalidInput
		}
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
	guard ControlGuard, mutation Mutation, permit *featurecontrol.Proof) (Snapshot, bool, error) {
	if runtime == nil || runtime.execution != nil || hasReservedApplyEvidence(mutation.Events) {
		return Snapshot{}, false, controlNoEffectBeforeStore(ErrInvalidInput)
	}
	current, err := runtime.Load(ctx, runID)
	if err != nil {
		return Snapshot{}, false, controlNoEffectBeforeStore(err)
	}
	if current.Run.Revision != expectedRevision {
		return Snapshot{}, false, controlNoEffectBeforeStore(ErrConflict)
	}
	if err = validateMutation(current, mutation); err != nil {
		return Snapshot{}, false, controlNoEffectBeforeStore(err)
	}
	next := runtime.makeStoreMutation(current, mutation)
	// The Store sees the exact same unforgeable, mutation-bound proof and
	// checks it again under the authoritative transaction/row lock.
	guard.verified = permit
	if err = ValidateControl(current, guard, next, runtime.Now()); err != nil {
		return Snapshot{}, false, controlNoEffectBeforeStore(err)
	}
	// Write-capable Store code begins here. Never certify a Store-returned
	// error as no-effect: even a failed CAS response can be outcome-unknown.
	if store, ok := runtime.store.(ControlStore); ok {
		snapshot, revoked, writeErr := store.ApplyControl(ctx, runID, expectedRevision, guard, next)
		return snapshot, revoked, controlStoreOutcome(writeErr)
	}
	// Older custom stores implement no execution epochs and retain the existing
	// six-state single-executor behavior; never fall back on a fenced Store.
	if _, fenced := runtime.store.(ExecutionStore); fenced {
		return Snapshot{}, false, controlNoEffectBeforeStore(ErrExecutionRequired)
	}
	result, writeErr := runtime.store.Apply(ctx, runID, expectedRevision, next)
	return result, false, controlStoreOutcome(writeErr)
}
