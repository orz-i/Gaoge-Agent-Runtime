package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// ResumeWaitingDelegation is a narrow, Feature-owned continuation of an
// already admitted and frozen Waiting delegation Item. It does not run the
// original Tool handler, preparation policies, model, or a new logical Call.
//
// The exact prior ToolDispatchID, immutable Invocation, Waiting Item and
// frozen child identity must all agree. Native local capacity is only
// consumed by Create; the original dispatch admission fences actual work.
// An ambiguous Started/Grant outcome is never restarted.
func (runner *Runner) ResumeWaitingDelegation(
	ctx context.Context, parentRunID string, expectedRevision uint64,
) (bool, error) {
	if runner == nil || runner.store == nil || runner.runtime == nil ||
		runner.handoffs == nil || runner.relations == nil || runner.budgets == nil {
		return false, ErrInvalidRequest
	}
	parent, err := runner.runtime.Load(ctx, parentRunID)
	if err != nil {
		return false, err
	}
	if parent.Run.Revision != expectedRevision {
		return false, kernel.ErrConflict
	}
	call, pending, err := agent.FrozenPendingDispatch(parent)
	var wait tools.PendingExecution
	if err == nil && !pending {
		call, wait, pending, err = agent.FrozenChildWaitCall(parent)
	}
	if err != nil || !pending || call.ToolKey != DelegationToolKey {
		return false, err
	}
	owner, err := runner.store.GetInvocationByExecutionRefID(ctx, parentRunID)
	if err != nil {
		return false, err
	}
	if !runner.canDelegate(owner) || owner.ExecutionRefID != parentRunID {
		return false, ErrConflict
	}
	turn, err := loadVerifiedTurn(ctx, runner.store, owner.TurnID)
	if err != nil {
		return false, err
	}
	if err := runner.requireFrozenAdmission(ctx, turn); err != nil {
		return false, err
	}
	if owner.ParentItemID == "" {
		err = validateRootRuntimeOwnership(owner, parent)
	} else {
		// A nested parent is still the original immutable local Handoff
		// child, never a remote same-ID shadow.
		_, _, err = runner.localDelegationObservation(ctx, parentRunID)
	}
	if err != nil {
		return false, err
	}
	delegationID := delegationToolID(owner, call.ID)
	waitingItemID := stableID("hid", turn.ID, delegationID, string(ItemWaiting))
	waiting, err := runner.store.GetItem(ctx, waitingItemID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	frozen, err := verifiedFrozenWaitingDelegation(waiting, turn, owner, parentRunID, call)
	if err != nil {
		return true, err
	}
	if wait.Kind != "" && (wait.OperationID != frozen.ID || wait.ChildRunID != frozen.ChildRunID) {
		return true, ErrConflict
	}
	startedItemID, admitted, err := runner.admitOriginalDelegation(ctx, turn, owner, frozen)
	if err != nil {
		return true, err
	}
	if !admitted {
		if frozen.NativeLocal || runner.nativeLocalDelegation(frozen) != nil {
			if _, e := runner.runtime.Load(ctx, frozen.ChildRunID); e == nil {
				return false, nil
			} else if !errors.Is(e, kernel.ErrNotFound) {
				return true, e
			}
		}
		return true, ErrExecutionOutcomeUnknown
	}
	result, err := runner.executeDelegation(ctx, turn, owner, parent, frozen, startedItemID, hashInvocationBytes(call.Arguments))
	if err == nil && result.Delegation.Status == handoff.StatusQueued {
		return true, budget.ErrWaiting
	}
	return true, err
}

// verifiedFrozenWaitingDelegation is a read-only original-source check. A
// Waiting Item never authorizes a new Tool or role just because its ID looks
// deterministic. The queued role, goal and explicitly selected tools must
// still come from the caller's original committed Agent Tool dispatch.
func verifiedFrozenWaitingDelegation(
	waiting Item, turn Turn, owner Invocation, parentRunID string, call tools.Call,
) (handoff.Delegation, error) {
	if turn.ID == "" || owner.ID == "" || parentRunID == "" ||
		owner.TurnID != turn.ID || owner.ExecutionRefID != parentRunID ||
		call.ID == "" || call.ToolKey != DelegationToolKey {
		return handoff.Delegation{}, ErrConflict
	}
	id := delegationToolID(owner, call.ID)
	payload, valid := frozenDelegationAdmissionPayload(owner, waiting)
	if !valid || waiting.Status != ItemWaiting || waiting.InvocationID != owner.ID ||
		payload.DelegationID != id || waiting.RunID != stableID("hchild", parentRunID, id) ||
		payload.ParentRunID != parentRunID || payload.OriginalCallHash != hashInvocationBytes(call.Arguments) {
		return handoff.Delegation{}, ErrConflict
	}
	return *payload.Execution, nil
}
