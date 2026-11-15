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
// frozen child identity must all agree. RegisterRun and AppendItem remain
// CAS/idempotent, while beginPhysicalInvocationDispatch fences the actual
// ChildRunner effect. An ambiguous Started/Grant outcome is never restarted.
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
	startedItemID := stableID("hid", turn.ID, frozen.ID, string(ItemStarted))
	if started, loadErr := runner.store.GetItem(ctx, startedItemID); loadErr == nil {
		// A competing worker or crashed predecessor has already committed
		// admission. Never dispatch the same child twice: there may be an
		// unknown physical Start behind this Started Item. Keep the original
		// continuation retryable, rather than falsely ACKing the wait.
		var original delegationItemPayload
		if started.Seq == 0 || started.ID != startedItemID ||
			started.TurnID != turn.ID || started.InvocationID != owner.ID ||
			started.RunID != frozen.ChildRunID || started.Kind != ItemDelegation ||
			started.Status != ItemStarted ||
			!decodeTypedAuthorityJSON(started.Payload, &original) ||
			original.Execution == nil ||
			!sameHandoffFrozenExecution(frozen, *original.Execution) ||
			original.ParentRunID != parentRunID || original.DelegationID != frozen.ID ||
			original.ChildRunID != frozen.ChildRunID ||
			original.MemberID != frozen.MemberID ||
			original.MemberRevision != frozen.MemberRevision ||
			original.RoleID != frozen.RoleID ||
			original.RoleRevision != frozen.RoleRevision ||
			original.RoleName != frozen.RoleName || original.Goal != frozen.Goal ||
			original.Status != handoff.StatusQueued || len(original.Result) != 0 ||
			original.Execution.Status != handoff.StatusQueued ||
			len(original.Execution.Result) != 0 ||
			original.Execution.Error != "" || original.Execution.ErrorCode != "" {
			return true, ErrConflict
		}
		return true, ErrExecutionOutcomeUnknown
	} else if !errors.Is(loadErr, ErrNotFound) {
		return true, loadErr
	}
	if err := runner.admitRoleChild(ctx, turn, owner, frozen); err != nil {
		if errors.Is(err, budget.ErrWaiting) {
			return true, budget.ErrWaiting
		}
		return true, err
	}
	// The same Store enforces cancellation-intent and frozen-original grant
	// exclusion on this stable Started Item. No independent worker identity.
	_, admitted, err := runner.recordDelegationItemWithAdmission(ctx, turn, owner, frozen, ItemStarted, "")
	if err != nil {
		return true, err
	}
	if !admitted {
		// A concurrent worker won the Store CAS. It alone can start the
		// child; without its physical return receipt, our outcome is unknown.
		return true, ErrExecutionOutcomeUnknown
	}
	// This is the first child physical dispatch, not a replay of the parent
	// Agent Tool call. An already committed/uncertain child Grant remains
	// fenced by executeDelegation.
	_, err = runner.executeDelegation(ctx, turn, owner, parent, frozen, startedItemID)
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
	if waiting.Kind != ItemDelegation || waiting.Status != ItemWaiting ||
		waiting.Seq == 0 || waiting.TurnID != turn.ID ||
		waiting.InvocationID != owner.ID ||
		waiting.ID != stableID("hid", turn.ID, id, string(ItemWaiting)) ||
		waiting.ParentItemID != "" ||
		waiting.RunID != stableID("hchild", parentRunID, id) {
		return handoff.Delegation{}, ErrConflict
	}
	// Host policy may change the model-proposed Goal, role and allowed
	// tools once on first dispatch. Its already-frozen Waiting Item is
	// the authority; its original Tool arguments hash binds that fact to
	// this exact committed model call without rerunning Host policy.
	var payload delegationItemPayload
	if !decodeTypedAuthorityJSON(waiting.Payload, &payload) {
		return handoff.Delegation{}, ErrConflict
	}
	frozen := payload.Execution
	if frozen == nil || frozen.ID != id || frozen.ChildRunID != waiting.RunID ||
		frozen.Status != handoff.StatusQueued || len(frozen.Result) != 0 ||
		frozen.Error != "" || frozen.ErrorCode != "" ||
		payload.DelegationID != id || payload.ChildRunID != waiting.RunID ||
		payload.ParentRunID != parentRunID ||
		len(payload.OriginalCallHash) != 64 ||
		payload.OriginalCallHash != hashInvocationBytes(call.Arguments) ||
		payload.Status != handoff.StatusQueued || len(payload.Result) != 0 ||
		frozen.MemberID != payload.MemberID ||
		frozen.MemberRevision != payload.MemberRevision ||
		frozen.RoleID != payload.RoleID ||
		frozen.RoleRevision != payload.RoleRevision ||
		frozen.RoleName != payload.RoleName ||
		frozen.Goal != payload.Goal {
		return handoff.Delegation{}, ErrConflict
	}
	return *frozen, nil
}
