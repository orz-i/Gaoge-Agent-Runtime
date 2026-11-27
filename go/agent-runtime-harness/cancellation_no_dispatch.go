package harness

import (
	"context"
	"errors"
	"slices"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// missingRunCancellation gives missing-Run targets ONE meaning across
// initial control and recovery. Absence alone is never proof: the exact frozen
// Root/child/Waiting source and complete dispatch ledger must agree while the
// original elected cancellation fences future physical admission. A nil reason
// observes recovery eligibility only; a supplied reason settles the election.
func (runner *Runner) missingRunCancellation(ctx context.Context, turn Turn, runID string, settleReason *string) (bool, error) {
	if _, err := runner.runtime.Load(ctx, runID); !errors.Is(err, kernel.ErrNotFound) {
		return false, err
	}
	owner, err := runner.store.GetInvocationByExecutionRefID(ctx, runID)
	if err == nil {
		if owner.TurnID != turn.ID {
			return false, ErrConflict
		}
		if owner.ParentItemID == "" {
			return runner.undispatchedRootNoEffect(ctx, turn, owner, false)
		}
		if !isHandoffDelegationInvocation(owner) {
			// A completed Host/non-Handoff owner with no physical grant or
			// RunRelation has no remaining Kernel control to dispatch. Preserve
			// its original terminal projection, never reopen or synthesize it.
			if terminalInvocationStatus(owner.Status) {
				items, readErr := runner.loadGlobalPhysicalEvidence(ctx, turn.ID)
				if readErr != nil {
					return false, readErr
				}
				if !acceptedChildPhysicalEffectPossible(items, owner) {
					if runner.relationReader == nil {
						return true, nil
					}
					_, edgeErr := runner.relationReader.GetByChild(ctx, runID)
					if errors.Is(edgeErr, runrelation.ErrNotFound) {
						return true, nil
					}
					return false, edgeErr
				}
			}
			return false, nil
		}
		if _, err = runner.bindCancellationAttempt(ctx, turn, runID, owner); err != nil {
			return false, err
		}
		possible, proofErr := runner.handoffPhysicalEffectPossible(ctx, owner)
		return !possible && proofErr == nil, proofErr
	}
	if !errors.Is(err, ErrNotFound) {
		return false, err
	}
	items, err := runner.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if err != nil {
		return false, err
	}
	root, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if err != nil {
		return false, err
	}
	owners, err := runner.store.ListInvocations(ctx, turn.ID)
	if err != nil {
		return false, err
	}
	byID, byRun := map[string]Invocation{root.ID: root}, map[string]Invocation{}
	for _, owner := range owners {
		byID[owner.ID], byRun[owner.ExecutionRefID] = owner, owner
	}
	ledger, err := runner.loadTurnBudget(ctx, turn)
	if err != nil {
		return false, err
	}
	prepared, source, valid := delegationNoInvocationSource(root, runID, byID, byRun, items, ledger)
	if !valid {
		return false, nil
	}
	if runner.relationReader != nil {
		relation, edgeErr := runner.relationReader.GetByChild(ctx, runID)
		if !errors.Is(edgeErr, runrelation.ErrNotFound) {
			if edgeErr != nil {
				return false, edgeErr
			}
			if source.Status != ItemStarted || relation.Kind != runrelation.KindDelegation || relation.ParentRunID != prepared.ParentRunID || relation.OwnerNodeID != prepared.DelegationID {
				return false, nil
			}
		}
	}
	if settleReason == nil {
		return true, nil
	}
	// Absence plus the sealed admission proves eligibility, not the negative
	// native decision. Derive the exact future child input without creating
	// an Invocation, and let native Create/DecideBeforeCreate arbitrate.
	d := *prepared.Execution
	parent := byID[source.InvocationID]
	original, valid := delegationOriginalParentRoot(root, parent.ID, prepared.ParentRunID, byID, items, ledger)
	if !valid {
		return false, ErrConflict
	}
	if invocationAttempt(original) != invocationAttempt(root) {
		return true, nil // validated history; never mutate a prior Attempt
	}
	binding := childRootBinding{InvocationID: root.ID, ExecutionRefID: root.ExecutionRefID, Attempt: root.Attempt}
	if parent.ID != root.ID {
		frozen, e := frozenChildRoot(parent)
		if e != nil {
			return false, e
		}
		binding.Parents = append([]childAttemptBinding{invocationAttempt(parent)}, frozen.Parents...)
	}
	intent, bound := ctx.Value(cancellationElectionKey{}).(Item)
	if !bound || intent.TurnID != turn.ID || intent.Seq <= source.Seq || !ValidCancellationFenceIntentAdmission(intent) ||
		(intent.RunID != runID && intent.RunID != root.ExecutionRefID && !slices.ContainsFunc(binding.Parents, func(p childAttemptBinding) bool { return p.ExecutionRefID == intent.RunID })) {
		return false, ErrConflict
	}
	if d.NativeLocal {
		if runner.nativeHandoff(d) == nil {
			return false, ErrConflict
		}
		input, hash, e := marshalInvocationValue(d)
		if e != nil {
			return false, e
		}
		input, _, e = sealChildRootBinding(input, hash, binding)
		if e != nil {
			return false, e
		}
		q, e := runner.nativeChildCreationSelection(ctx, turn, d, parent.ExecutionRefID, input, kernel.RunStatusCancelled, *settleReason)
		if e != nil {
			return false, e
		}
		decision, e := runner.decideNativeCreation(ctx, q)
		if e != nil || decision.Status != kernel.RunStatusCancelled {
			return false, errors.Join(ErrExecutionOutcomeUnknown, e)
		}
		if e = runner.projectCreationDecisionBudget(ctx, decision); e != nil {
			return false, e
		}
	}
	d.Status = "cancelled"
	_, err = runner.recordDelegationItem(ctx, turn, parent, d, ItemCancelled, source.ID)
	return err == nil, err
}
