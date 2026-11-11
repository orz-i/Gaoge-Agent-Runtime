package harness

import (
	"context"
	"errors"
)

// ErrApplicationParentContinuationUnverified means a child Application's
// business result is committed, but there is no complete, original-owner-bound
// receipt that its parent Agent entered a subsequent physical Resume.
// Refresh/ReconcileInteraction are observation/recovery operations; they may
// not silently claim that an external continuation ran, nor dispatch one
// automatically without the caller explicitly authorizing ResumeTurn.
var ErrApplicationParentContinuationUnverified = errors.New("harness application child completed but parent Agent continuation is unverified")

// requireObservedApplicationParentContinuation is a read-only, original-source
// witness used by both business-response replay and operator reconciliation.
// It deliberately does NOT call Agent.Resume. The parent's original Kernel
// Resume is a separate physical effect, serialized through the existing
// dispatch grant/receipt ledger and explicit ResumeTurn operation.
func (runner *Runner) requireObservedApplicationParentContinuation(
	ctx context.Context, interaction Interaction, child Invocation,
) error {
	if child.ExecutionClass != ExecutionApplication || child.ParentItemID == "" ||
		child.Status != InvocationCompleted {
		return nil
	}
	if interaction.TurnID == "" || child.TurnID != interaction.TurnID ||
		child.ID != interaction.InvocationID {
		return ErrConflict
	}
	items, err := listAllItems(ctx, runner.store, child.TurnID)
	if err != nil {
		return err
	}
	// A terminal Invocation Status by itself is never evidence that the
	// original Host callback returned successfully or was applied to this
	// business response. The complete typed receipt must still be valid.
	if !verifiedCompletedApplicationResponse(items, interaction, child) {
		return ErrConflict
	}
	root, err := loadTopLevelInvocation(ctx, runner.store, child.TurnID)
	if err != nil {
		return err
	}
	if root.ID == child.ID || root.TurnID != child.TurnID ||
		root.ParentItemID != "" || root.ExecutionClass != ExecutionAgent ||
		root.ExecutionRefID == "" {
		return ErrConflict
	}
	// The terminal lifecycle Item is emitted only AFTER the committed
	// Invocation CAS. A Resume grant predating that Item may belong to an
	// unrelated earlier Agent segment and cannot settle this child result.
	completedID := invocationLifecycleItemID(child, InvocationCompleted, child.Revision)
	completedPayload, payloadErr := invocationItemPayload(child)
	if payloadErr != nil {
		return payloadErr
	}
	var childCompletedSeq uint64
	for _, item := range items {
		if item.ID != completedID {
			continue
		}
		if childCompletedSeq != 0 || item.TurnID != child.TurnID ||
			item.InvocationID != child.ID || item.RunID != child.ExecutionRefID ||
			item.Kind != ItemInvocation || item.Status != ItemCompleted ||
			item.Seq == 0 || !sameUniqueAuthorityJSON(item.Payload, completedPayload) {
			return ErrConflict
		}
		childCompletedSeq = item.Seq
	}
	if childCompletedSeq == 0 {
		return ErrApplicationParentContinuationUnverified
	}
	receipts := executionDispatchReceiptIndex(items)
	observed := false
	for _, grant := range items {
		if grant.ParentItemID != ExecutionDispatchParentItemID ||
			grant.InvocationID != root.ID || grant.RunID != root.ExecutionRefID ||
			grant.TurnID != child.TurnID || grant.Seq <= childCompletedSeq {
			continue
		}
		phase, valid := VerifiedExecutionDispatchGrant(grant)
		if !valid {
			return ErrConflict
		}
		if phase != "resume" {
			continue
		}
		boundary, settled := VerifiedExecutionDispatchReceipt(grant, receipts[grant.ID])
		if !settled {
			return ErrApplicationParentContinuationUnverified
		}
		if boundary == "physical_dispatch_returned" {
			observed = true
		} else if boundary != "physical_dispatch_rejected" {
			return ErrConflict
		}
	}
	if !observed {
		return ErrApplicationParentContinuationUnverified
	}
	return nil
}
