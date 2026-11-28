package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Select an independent Host election only for an actual scoped independent
// effect. Native creation (including a negative decision) and control own
// their identity in the Runtime; absence is never a terminal receipt.
func (r *Runner) needsHostCancellation(ctx context.Context, turn Turn, runID string) (bool, error) {
	native, ok := r.agent.(*agent.Runner)
	if !ok || !native.ObservesKernelRuntime(r.runtime) || r.cancellation != r.runtime || r.interactions != nil {
		return true, nil
	}
	owners, targets, affected, err := r.collectCancellationScope(ctx, turn, runID)
	if err != nil {
		return true, err
	}
	items, err := r.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if err != nil {
		return true, err
	}
	if ActiveCancellationFence(items) || PendingExecutionDispatch(items, "") {
		return true, nil
	}
	root, ok := topLevelInvocation(owners)
	if !ok {
		return true, ErrConflict
	}
	byID, byRun := make(map[string]Invocation), make(map[string]Invocation)
	for _, owner := range owners {
		byID[owner.ID], byRun[owner.ExecutionRefID] = owner, owner
	}
	ledger, err := r.loadTurnBudget(ctx, turn)
	if err != nil {
		return true, err
	}
	if root.ExecutionRefID == runID {
		_, historical, e := r.currentCancellationChildren(ctx, turn, root.ID, owners)
		if e != nil {
			return true, e
		}
		for id := range historical {
			delete(affected, id)
			delete(targets, byRun[id].ID)
		}
	}
	known := make(map[string]bool)
	for _, owner := range owners {
		if _, selected := targets[owner.ID]; !selected {
			continue
		}
		known[owner.ExecutionRefID] = true
		if owner.ParentItemID == "" {
			continue
		}
		if !isHandoffDelegationInvocation(owner) {
			// A completed product-only child has no independent physical control.
			proven, e := r.missingRunCancellation(ctx, turn, owner.ExecutionRefID, nil)
			if e != nil {
				return true, e
			}
			if !proven {
				return true, nil
			}
			delete(affected, owner.ExecutionRefID)
			delete(targets, owner.ID)
			continue
		}
		d, e := decodeFrozenHandoffDelegation(owner.Input)
		if e != nil || !d.NativeLocal || d.Remote {
			return true, nil
		}
		if route, e := r.nativeHandoff(ctx, turn.ID, d); e != nil || route != native {
			return true, errors.Join(ErrHandoffCancellationUnverified, e)
		}
	}
	for _, item := range items {
		if item.Kind != ItemDelegation {
			continue
		}
		var d delegationItemPayload
		if !decodeTypedAuthorityJSON(item.Payload, &d) {
			return true, ErrConflict
		}
		if _, selected := affected[d.ChildRunID]; !selected {
			if _, parent := targets[item.InvocationID]; !parent {
				continue
			}
		}
		original, valid := delegationOriginalParentRoot(root, item.InvocationID, d.ParentRunID, byID, items, ledger)
		if !valid {
			return true, ErrConflict
		}
		if original.ExecutionRefID != root.ExecutionRefID {
			// A historical admission must still have valid original history.
			if byRun[d.ChildRunID].ID == "" {
				if _, _, valid = delegationNoInvocationSource(root, d.ChildRunID, byID, byRun, items, ledger); !valid {
					return true, ErrConflict
				}
			}
			delete(affected, d.ChildRunID)
			continue
		}
		if d.Execution == nil || !d.Execution.NativeLocal || d.Execution.Remote {
			return true, nil
		}
		if route, e := r.nativeHandoff(ctx, turn.ID, *d.Execution); e != nil || route != native {
			return true, errors.Join(ErrHandoffCancellationUnverified, e)
		}
		known[d.ChildRunID] = true
		affected[d.ChildRunID] = struct{}{}
	}
	for id := range affected {
		if !known[id] {
			return true, nil
		}
		snapshot, e := r.runtime.Load(ctx, id)
		if errors.Is(e, kernel.ErrNotFound) {
			if id == root.ExecutionRefID {
				// Cancel's atomic DecideBeforeCreate competes with the original root.
				continue
			}
			if owner := byRun[id]; owner.ID != "" {
				// Validate the exact original selection and any committed decision.
				// ErrNotFound after that validation is eligibility to DecideBeforeCreate,
				// not proof of cancellation. The control path must win that transaction.
				if _, e = r.nativeChildDecision(ctx, turn, owner); e != nil && !errors.Is(e, kernel.ErrNotFound) {
					return true, e
				}
			} else {
				proven, e := r.missingRunCancellation(ctx, turn, id, nil)
				if e != nil || !proven {
					return true, errors.Join(ErrRuntimeCancellationUnverified, e)
				}
			}
			continue
		}
		if e != nil {
			return true, e
		}
		if snapshot.Run.Kind != agent.RunKind {
			return true, nil
		}
		if id != runID || snapshot.Run.LogicalParentRunID != "" {
			if terminalRuntimeStatus(snapshot.Run.Status) && !native.VerifiedDeferredRootControl(ctx, r.runtime, snapshot) {
				return true, nil
			}
		}
		_, waiting, e := agent.FrozenExternalToolCall(snapshot)
		if e != nil {
			return true, e
		}
		if waiting {
			return true, nil
		}
	}
	return false, nil
}
