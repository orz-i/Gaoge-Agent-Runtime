package harness

import (
	"encoding/json"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

// RootRetryTransitionParentItemID is reserved to the owning Store's Retry CAS.
// It is an immutable fact in the existing Item ledger, not another retry saga
// or dispatch grant. Public AppendItem must never mint these records.
const RootRetryTransitionParentItemID = "harness.root.retry.transition"

type rootRetryTransition struct {
	Version   int                 `json:"version"`
	Previous  childAttemptBinding `json:"previous"`
	Next      childAttemptBinding `json:"next"`
	InputHash string              `json:"inputHash"`
}

// RootRetryTransitionFact constructs data only. Stores persist it exclusively
// under the same Turn lock/transaction as the original Attempt rotation.
func RootRetryTransitionFact(previous Invocation, nextRunID string, now time.Time) (Item, error) {
	if previous.ParentItemID != "" || previous.ExecutionClass != ExecutionAgent ||
		!validChildAttempt(invocationAttempt(previous)) || !retryableInvocationStatus(previous.Status) ||
		nextRunID == "" || nextRunID == previous.ExecutionRefID || now.IsZero() || previous.Attempt+1 <= previous.Attempt {
		return Item{}, ErrConflict
	}
	payload, err := json.Marshal(rootRetryTransition{Version: 1, Previous: invocationAttempt(previous),
		Next: childAttemptBinding{InvocationID: previous.ID, ExecutionRefID: nextRunID, Attempt: previous.Attempt + 1}, InputHash: previous.InputHash})
	if err != nil {
		return Item{}, err
	}
	return Item{ID: stableID("hrootretry", previous.ID, attemptString(previous.Attempt+1)), TurnID: previous.TurnID,
		Kind: ItemDiagnostic, Status: ItemCompleted, RunID: nextRunID, InvocationID: previous.ID,
		ParentItemID: RootRetryTransitionParentItemID, Payload: payload, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}, nil
}

func historicalRetryRootBound(current Invocation, frozen childRootBinding, ledger *budget.Ledger, facts ...[]Item) bool {
	if frozen.InvocationID != current.ID || frozen.Attempt < 1 || frozen.Attempt >= current.Attempt || frozen.ExecutionRefID == current.ExecutionRefID {
		return false
	}
	// Budget grants from the original atomic Store CAS are also immutable
	// ancestry authority. They remain mandatory whenever accounting exists.
	if ledger != nil {
		if ledger.ID != current.TurnID || ledger.Revision == 0 {
			return false
		}
		runID := current.ExecutionRefID
		for attempt := current.Attempt; attempt > frozen.Attempt; attempt-- {
			grant, ok := ledger.RootRetries[runID]
			if !ok || grant.InvocationID != current.ID || grant.Attempt != attempt || grant.OriginalRunID == "" || grant.OriginalRunID == runID ||
				grant.Active != (attempt == current.Attempt) || !ledger.CancelledRuns[grant.OriginalRunID] {
				return false
			}
			runID = grant.OriginalRunID
		}
		if runID != frozen.ExecutionRefID {
			return false
		}
	}
	var items []Item
	if len(facts) != 0 {
		items = facts[0]
	}
	bound := invocationAttempt(current)
	for bound.Attempt > frozen.Attempt {
		id := stableID("hrootretry", current.ID, attemptString(bound.Attempt))
		matches := 0
		var source rootRetryTransition
		for _, fact := range items {
			if fact.ID != id {
				continue
			}
			matches++
			if fact.Seq == 0 || fact.TurnID != current.TurnID || fact.InvocationID != current.ID || fact.RunID != bound.ExecutionRefID ||
				fact.ParentItemID != RootRetryTransitionParentItemID || fact.Kind != ItemDiagnostic || fact.Status != ItemCompleted ||
				!decodeTypedAuthorityJSON(fact.Payload, &source) || source.Version != 1 || source.Next != bound ||
				source.Previous.InvocationID != current.ID || !validChildAttempt(source.Previous) || source.Previous.Attempt != bound.Attempt-1 ||
				source.Previous.ExecutionRefID == bound.ExecutionRefID || source.InputHash != current.InputHash {
				return false
			}
		}
		if matches == 0 {
			if ledger == nil {
				return false
			}
			// Pre-existing atomic budget grants remain original authority.
			// Upgrading must not require backfilling historical Item facts;
			// continue validating every other present/missing link as well.
			grant := ledger.RootRetries[bound.ExecutionRefID]
			bound = childAttemptBinding{InvocationID: current.ID, ExecutionRefID: grant.OriginalRunID, Attempt: bound.Attempt - 1}
			continue
		}
		if matches != 1 {
			return false
		}
		bound = source.Previous
	}
	return bound == frozen.attempt()
}

// historicalHandoffRoot is shared by terminal projection, no-Run cancellation
// and descendant cleanup. Current and prior Attempts use the same frozen source;
// only an exact Store-owned transition chain may cross an Attempt boundary.
func historicalHandoffRoot(current, child Invocation, items []Item, ledger *budget.Ledger) (Invocation, childRootBinding, bool) {
	binding, err := frozenChildRoot(child)
	if err != nil {
		return Invocation{}, childRootBinding{}, false
	}
	original := current
	if binding.attempt() != invocationAttempt(current) {
		if !historicalRetryRootBound(current, binding, ledger, items) {
			return Invocation{}, childRootBinding{}, false
		}
		original.Attempt, original.ExecutionRefID = binding.Attempt, binding.ExecutionRefID
	}
	_, verified, ok := verifiedTerminalHandoffFrozenSource(original, child)
	return original, verified, ok
}

func historicalHandoffSettled(original, child Invocation, items []Item) bool {
	switch child.Status {
	case InvocationAccepted, InvocationCompleted, InvocationFailed, InvocationCancelled:
	case InvocationRunning, InvocationWaitingInput, InvocationPausedBudget:
		return false
	default:
		return false
	}
	return !acceptedChildPhysicalEffectPossible(items, child) || VerifiedTerminalHandoffChild(original, child, items)
}
