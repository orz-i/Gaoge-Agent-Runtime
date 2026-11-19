package harness

import (
	"context"
	"errors"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

// CancelUnstartedRootRetry atomically commits both terminal records under
// the same original Turn mutex that admits physical Dispatch grants.
// It cannot turn an observed Kernel Run (checked by the original Runner)
// into a fabricated no-Run result, and an unrelated Cancellation intent
// cannot supply the exact owner proof.
func (store *MemoryStore) CancelUnstartedRootRetry(ctx context.Context, original Invocation, turn Turn) (Invocation, Turn, error) {
	if store == nil || !VerifiedUnstartedRetryCancellation(ctx, original, turn) {
		return Invocation{}, Turn{}, ErrConflict
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, ok := store.invocations[original.ID]
	storedTurn, turnFound := store.turns[turn.ID]
	if !ok || !turnFound || current.Revision != original.Revision ||
		current.ExecutionRefID != original.ExecutionRefID ||
		!sameInvocationIdentity(current, original) ||
		current.Status != InvocationAccepted || storedTurn.Revision != turn.Revision ||
		!sameTurnIdentity(storedTurn, turn) || storedTurn.Status != TurnRunning {
		return Invocation{}, Turn{}, ErrConflict
	}
	items, err := store.globalPhysicalEvidenceLocked(turn.ID)
	if err != nil {
		return Invocation{}, Turn{}, err
	}
	var ledger *budget.Ledger
	if store.MemoryLedgerStore != nil {
		loaded, loadErr := store.LoadBudget(ctx, turn.ID)
		if loadErr != nil && !errors.Is(loadErr, budget.ErrLedgerNotFound) {
			return Invocation{}, Turn{}, loadErr
		}
		if loadErr == nil {
			ledger = &loaded
		}
	}
	descendants := 0
	for _, candidate := range store.invocations {
		if candidate.TurnID == turn.ID && candidate.ID != original.ID {
			descendants++
		}
	}
	if !ValidUnstartedRetryCancellationEvidence(current, storedTurn, items, store.activeCancellationFenceLocked(turn.ID), ledger, descendants) {
		return Invocation{}, Turn{}, ErrConflict
	}
	now := time.Now().UTC()
	current.Status = InvocationCancelled
	current.ErrorCode = "run.cancelled"
	current.ErrorDetail = "replacement Agent Run cancelled before physical admission"
	current.Revision++
	current.UpdatedAt = now
	storedTurn.Status = TurnCancelled
	storedTurn.ErrorCode = current.ErrorCode
	storedTurn.ErrorDetail = current.ErrorDetail
	storedTurn.Revision++
	storedTurn.UpdatedAt = now
	store.invocations[current.ID] = cloneInvocation(current)
	store.turns[turn.ID] = storedTurn
	return cloneInvocation(current), storedTurn, nil
}
