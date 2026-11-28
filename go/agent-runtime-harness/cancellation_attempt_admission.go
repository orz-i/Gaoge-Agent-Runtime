package harness

import (
	"context"
	"encoding/json"
	"errors"
)

type cancellationAttemptAdmission struct {
	root, target Invocation
	runID        string
}
type cancellationAttemptAdmissionKey struct{}

// bindCancellationAttempt freezes the operation's exact original owner BEFORE
// intent admission. Stores consume it under the same Turn lock as Retry. A
// read-only owner check alone cannot protect the gap before AppendItem.
func (runner *Runner) bindCancellationAttempt(ctx context.Context, turn Turn, runID string, original ...Invocation) (context.Context, error) {
	root, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if err != nil {
		return ctx, err
	}
	var target Invocation
	if len(original) != 0 {
		target = original[0]
	} else {
		target, err = runner.store.GetInvocationByExecutionRefID(ctx, runID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return ctx, err
		}
	}
	if target.ID != "" {
		if target.ExecutionRefID != runID || target.TurnID != turn.ID {
			return ctx, ErrConflict
		}
		if target.ParentItemID == "" {
			if !sameFrozenInvocationAuthority(root, target) {
				return ctx, ErrConflict
			}
		} else if _, err = verifyFrozenChildAttempts(ctx, runner.store, target, root); err != nil {
			return ctx, errors.Join(ErrHandoffCancellationUnverified, err)
		}
	} else {
		// Waiting-only delegations have no child Invocation by design. Their
		// immutable Item still binds the original parent execution. Unknown raw
		// relations may retain a protective intent, but an identified obsolete
		// target must never elect a new fence over its successor.
		items, readErr := listAllItems(ctx, runner.store, turn.ID)
		if readErr != nil {
			return ctx, readErr
		}
		for _, item := range items {
			if item.RunID != runID {
				continue
			}
			if item.Kind == ItemAgentInvocation && item.InvocationID == root.ID && runID != root.ExecutionRefID {
				return ctx, ErrConflict
			}
			if item.Kind == ItemDelegation {
				var payload delegationItemPayload
				parent, loadErr := runner.store.GetInvocation(ctx, item.InvocationID)
				if json.Unmarshal(item.Payload, &payload) != nil || loadErr != nil ||
					parent.TurnID != turn.ID || parent.ExecutionRefID != payload.ParentRunID {
					return ctx, errors.Join(ErrHandoffCancellationUnverified, ErrConflict, loadErr)
				}
				// A pre-Invocation target inherits its parent's frozen Root
				// Attempt, not merely the parent's unchanged physical Run ID.
				// This recursive call always takes the existing-target branch.
				if _, parentErr := runner.bindCancellationAttempt(ctx, turn, parent.ExecutionRefID, parent); parentErr != nil {
					return ctx, parentErr
				}
			}
		}
	}
	return context.WithValue(ctx, cancellationAttemptAdmissionKey{}, cancellationAttemptAdmission{
		root: cloneInvocation(root), target: cloneInvocation(target), runID: runID,
	}), nil
}

// CancellationAttemptForStore exposes only a read-only original binding. A
// foreign Store caller cannot mint this private Runner admission capability.
func CancellationAttemptForStore(ctx context.Context) (Invocation, Invocation, bool) {
	proof, ok := ctx.Value(cancellationAttemptAdmissionKey{}).(cancellationAttemptAdmission)
	return cloneInvocation(proof.root), cloneInvocation(proof.target), ok
}

// ValidCancellationAttemptAdmission must be checked with authoritative records
// under the same Store Turn lock as creating the intent. Status/revision may
// progress within an Attempt; immutable physical ownership may not rotate.
func ValidCancellationAttemptAdmission(ctx context.Context, intent Item, root, target Invocation) bool {
	proof, ok := ctx.Value(cancellationAttemptAdmissionKey{}).(cancellationAttemptAdmission)
	if !ok {
		source, protocol := cancellationProtocol(intent)
		return !protocol || source != "native"
	}
	if proof.runID != intent.RunID || root.TurnID != intent.TurnID || root.ParentItemID != "" ||
		!sameFrozenInvocationAuthority(proof.root, root) {
		return false
	}
	if proof.target.ID == "" {
		return target.ID == ""
	}
	return target.TurnID == intent.TurnID && target.ExecutionRefID == intent.RunID &&
		sameFrozenInvocationAuthority(proof.target, target)
}
