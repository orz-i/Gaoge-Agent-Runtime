package harness

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

// UnstartedRootRetryStore atomically terminalizes the SAME original Attempt
// and Turn after the elected cancellation has fenced physical admission.
// Missing Kernel Run is not itself an execution or cancellation proof: the
// Runner first checks frozen identity, native ownership, the complete
// dispatch ledger and budget closure. Stores recheck them under Turn lock.
type UnstartedRootRetryStore interface {
	CancelUnstartedRootRetry(context.Context, Invocation, Turn) (Invocation, Turn, error)
}

type unstartedRetryCancellationWitness struct {
	turnID, invocationID, runID      string
	attempt                          int
	invocationRevision, turnRevision uint64
}
type unstartedRetryCancellationKey struct{}

func withUnstartedRetryCancellationWitness(ctx context.Context, inv Invocation, turn Turn) context.Context {
	return context.WithValue(ctx, unstartedRetryCancellationKey{}, unstartedRetryCancellationWitness{
		turnID: turn.ID, invocationID: inv.ID, runID: inv.ExecutionRefID,
		attempt: inv.Attempt, invocationRevision: inv.Revision, turnRevision: turn.Revision,
	})
}

// VerifiedUnstartedRetryCancellation is a read-only opaque witness consumed
// exclusively inside the same Store transaction as both terminal revisions.
func VerifiedUnstartedRetryCancellation(ctx context.Context, inv Invocation, turn Turn) bool {
	proof, ok := ctx.Value(unstartedRetryCancellationKey{}).(unstartedRetryCancellationWitness)
	return ok && proof.turnID == turn.ID && proof.turnID == inv.TurnID && proof.turnID != "" &&
		proof.invocationID == inv.ID && proof.invocationID != "" &&
		proof.runID == inv.ExecutionRefID && proof.runID != "" && proof.attempt == inv.Attempt &&
		inv.Attempt > 1 && inv.ParentItemID == "" && inv.ExecutionClass == ExecutionAgent &&
		proof.invocationRevision == inv.Revision && proof.turnRevision == turn.Revision &&
		inv.Status == InvocationAccepted && turn.Status == TurnRunning
}

// ValidUnstartedRetryCancellationEvidence is shared by SQL and Memory within
// their original Turn lock. A physical grant on the new immutable execution
// reference (even if a Host later claims it returned) forbids synthetic
// no-effect cancellation. A prior-Run grant is historical and does not count.
func ValidUnstartedRetryCancellationEvidence(inv Invocation, turn Turn, items []Item, hasActiveCancellation bool, ledger *budget.Ledger, children []Invocation) bool {
	if !hasActiveCancellation ||
		inv.Status != InvocationAccepted || turn.Status != TurnRunning ||
		inv.Attempt < 2 || inv.ID == "" || inv.ExecutionRefID == "" ||
		inv.TurnID != turn.ID || inv.ParentItemID != "" || inv.ExecutionClass != ExecutionAgent {
		return false
	}
	if ledger != nil {
		grant, ok := ledger.RootRetries[inv.ExecutionRefID]
		if !ok || !grant.Active || grant.InvocationID != inv.ID || grant.Attempt != inv.Attempt ||
			!ledger.CancelledRuns[inv.ExecutionRefID] {
			return false
		}
	}
	// Retry and Cancel must use the same historical child proof. A child
	// terminated under a prior immutable Attempt cannot block a new Root
	// that never obtained physical execution; an active or ambiguous child
	// still fences both operations.
	if RetryChildEffectFence(children, items, inv) != nil {
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
