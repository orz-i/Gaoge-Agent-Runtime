package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// cancellationNoDispatchReady gives missing-Run targets ONE meaning across
// initial control and recovery. Absence alone is never proof: the exact frozen
// Root/child/Waiting source and complete dispatch ledger must agree while the
// original elected cancellation fences future physical admission.
func (runner *Runner) cancellationNoDispatchReady(ctx context.Context, turn Turn, runID string) (bool, error) {
	if _, err := runner.runtime.Load(ctx, runID); !errors.Is(err, kernel.ErrNotFound) {
		return false, err
	}
	owner, err := runner.store.GetInvocationByExecutionRefID(ctx, runID)
	if err == nil {
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
	prepared, started, valid, err := runner.readNoInvocationDelegationSource(ctx, turn, runID, items)
	if err != nil {
		return false, err
	}
	if !valid {
		return false, nil
	}
	if runner.relationReader != nil {
		relation, edgeErr := runner.relationReader.GetByChild(ctx, runID)
		if !errors.Is(edgeErr, runrelation.ErrNotFound) {
			if edgeErr != nil {
				return false, edgeErr
			}
			if !started || relation.Kind != runrelation.KindDelegation || relation.ParentRunID != prepared.ParentRunID || relation.OwnerNodeID != prepared.DelegationID {
				return false, nil
			}
		}
	}
	return true, nil
}

func (runner *Runner) readNoInvocationDelegationSource(ctx context.Context, turn Turn, runID string, items []Item) (delegationItemPayload, bool, bool, error) {
	root, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if err != nil {
		return delegationItemPayload{}, false, false, err
	}
	owners, err := runner.store.ListInvocations(ctx, turn.ID)
	if err != nil {
		return delegationItemPayload{}, false, false, err
	}
	byID, byRun := map[string]Invocation{root.ID: root}, map[string]Invocation{}
	for _, owner := range owners {
		byID[owner.ID], byRun[owner.ExecutionRefID] = owner, owner
	}
	ledger, err := runner.loadTurnBudget(ctx, turn)
	if err != nil {
		return delegationItemPayload{}, false, false, err
	}
	p, started, valid := delegationNoInvocationSource(root, runID, byID, byRun, items, ledger)
	return p, started, valid, nil
}
