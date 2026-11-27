package harness

import (
	"context"
	"errors"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

// SettleUncreatedRoot atomically commits both terminal records under
// the same original Turn mutex that admits physical Dispatch grants.
// It cannot turn an observed Kernel Run (checked by the original Runner)
// into a fabricated no-Run result, and an unrelated Cancellation intent
// cannot supply the exact owner proof.
func (store *MemoryStore) SettleUncreatedRoot(ctx context.Context, original Invocation, turn Turn) (Invocation, Turn, error) {
	if store == nil || !VerifiedUncreatedRootWitness(ctx, original, turn) {
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
		!sameTurnIdentity(storedTurn, turn) || !undispatchedRootTurn(current, storedTurn) {
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
	children := make([]Invocation, 0)
	for _, candidate := range store.invocations {
		if candidate.TurnID == turn.ID && candidate.ID != original.ID {
			children = append(children, candidate)
		}
	}
	current, storedTurn, valid := ProjectUncreatedRoot(ctx, current, storedTurn, items, store.activeCancellationFenceLocked(turn.ID), ledger, children)
	if !valid || current.Status == InvocationFailed && !store.validTerminalProjectionLocked(ctx, current.TurnID, "") {
		return Invocation{}, Turn{}, ErrConflict
	}
	now := time.Now().UTC()
	current.Revision++
	current.UpdatedAt = now
	storedTurn.Revision++
	storedTurn.UpdatedAt = now
	store.invocations[current.ID] = cloneInvocation(current)
	store.turns[turn.ID] = storedTurn
	return cloneInvocation(current), storedTurn, nil
}
