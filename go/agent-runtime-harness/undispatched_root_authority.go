package harness

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

type unstartedRetryCancellationWitness struct {
	turnID, invocationID, runID      string
	attempt                          int
	invocationRevision, turnRevision uint64
}
type unstartedRetryCancellationKey struct{}

func withUndispatchedRootCancellationWitness(ctx context.Context, inv Invocation, turn Turn) context.Context {
	return context.WithValue(ctx, unstartedRetryCancellationKey{}, unstartedRetryCancellationWitness{
		turnID: turn.ID, invocationID: inv.ID, runID: inv.ExecutionRefID,
		attempt: inv.Attempt, invocationRevision: inv.Revision, turnRevision: turn.Revision,
	})
}

// VerifiedUndispatchedRootCancellation is a read-only opaque witness consumed
// exclusively inside the same Store transaction as both terminal revisions.
func VerifiedUndispatchedRootCancellation(ctx context.Context, inv Invocation, turn Turn) bool {
	proof, ok := ctx.Value(unstartedRetryCancellationKey{}).(unstartedRetryCancellationWitness)
	return ok && proof.turnID == turn.ID && proof.turnID == inv.TurnID && proof.turnID != "" &&
		proof.invocationID == inv.ID && proof.invocationID != "" &&
		proof.runID == inv.ExecutionRefID && proof.runID != "" && proof.attempt == inv.Attempt &&
		inv.Attempt > 0 && inv.ParentItemID == "" && proof.invocationRevision == inv.Revision && proof.turnRevision == turn.Revision &&
		inv.Status == InvocationAccepted && undispatchedRootTurn(inv, turn)
}

func undispatchedRootTurn(inv Invocation, turn Turn) bool {
	return inv.Attempt == 1 && turn.Status == TurnAccepted || inv.Attempt > 1 && turn.Status == TurnRunning
}

// ValidUndispatchedRootCancellationEvidence is shared by SQL and Memory within
// their original Turn lock. A physical grant on the new immutable execution
// reference (even if a Host later claims it returned) forbids synthetic
// no-effect cancellation. A prior-Run grant is historical and does not count.
func ValidUndispatchedRootCancellationEvidence(inv Invocation, turn Turn, items []Item, hasActiveCancellation bool, ledger *budget.Ledger, children []Invocation) bool {
	if !hasActiveCancellation ||
		inv.Status != InvocationAccepted || !undispatchedRootTurn(inv, turn) ||
		inv.Attempt < 1 || inv.ID == "" || inv.ExecutionRefID == "" ||
		inv.TurnID != turn.ID || inv.ParentItemID != "" {
		return false
	}
	if ledger != nil {
		if inv.Attempt == 1 {
			if !ledger.Cancelled || len(ledger.RootRetries) != 0 {
				return false
			}
		} else {
			grant, ok := ledger.RootRetries[inv.ExecutionRefID]
			if !ok || !grant.Active || grant.InvocationID != inv.ID || grant.Attempt != inv.Attempt ||
				!ledger.CancelledRuns[inv.ExecutionRefID] {
				return false
			}
		}
	}
	// Retry and Cancel must use the same historical child proof. A child
	// terminated under a prior immutable Attempt cannot block a new Root
	// that never obtained physical execution; an active or ambiguous child
	// still fences both operations.
	if RetryChildEffectFenceWithBudget(children, items, inv, ledger) != nil {
		return false
	}
	if !validUnstartedRetryHistoricalChildren(inv, children, items, ledger) {
		return false
	}
	originalIntent := false
	fenceReceipts := cancellationFenceReceipts(items)
	for _, item := range items {
		if item.ParentItemID == CancellationFenceParentItemID && item.RunID == inv.ExecutionRefID &&
			ValidCancellationFenceIntentAdmission(item) && !VerifiedCancellationFenceReceipt(item, fenceReceipts[item.ID]) {
			if !CancellationEffectsSettled(item, items) {
				return false
			}
			originalIntent = true
		}
		if item.ParentItemID == ExecutionDispatchParentItemID &&
			(item.RunID == inv.ExecutionRefID ||
				(item.InvocationID == inv.ID && item.RunID == "")) {
			return false
		}
	}
	return originalIntent && !PendingExecutionDispatch(items, "")
}
