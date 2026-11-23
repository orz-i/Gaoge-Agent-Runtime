package harness

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func nonAssignableRuntimeTool(key string) bool {
	return strings.HasPrefix(key, "harness.") || key == agent.ToolDiscoveryKey ||
		strings.HasPrefix(key, "runtime.")
}

// normalizeDelegatedLocalToolGrants seals only non-root, locally dispatched
// tools with an existing immutable definition version. Old configs encode no grants.
func normalizeDelegatedLocalToolGrants(config ConfigSnapshot) ([]DelegatedLocalToolGrant, error) {
	if len(config.DelegationPolicy.LocalToolGrants) == 0 {
		return nil, nil
	}
	grants := slices.Clone(config.DelegationPolicy.LocalToolGrants)
	policies := make(map[string]ToolPolicySnapshot, len(config.ToolPolicies))
	for _, policy := range config.ToolPolicies {
		policies[policy.Key] = policy
	}
	for i, grant := range grants {
		grant.MemberID = strings.TrimSpace(grant.MemberID)
		grant.MemberRevision = strings.TrimSpace(grant.MemberRevision)
		grant.ToolKey = strings.TrimSpace(grant.ToolKey)
		policy, ok := policies[grant.ToolKey]
		if !ok || policy.ExecutionMode != "local_dispatch" || policy.DefinitionVersion == "" ||
			grant.MemberID == "" || grant.MemberRevision == "" || nonAssignableRuntimeTool(grant.ToolKey) ||
			slices.Contains(config.ToolKeys, grant.ToolKey) {
			return nil, ErrInvalidRequest
		}
		grants[i] = grant
	}
	slices.SortFunc(grants, func(a, b DelegatedLocalToolGrant) int {
		if a.MemberID != b.MemberID {
			return strings.Compare(a.MemberID, b.MemberID)
		}
		if a.MemberRevision != b.MemberRevision {
			return strings.Compare(a.MemberRevision, b.MemberRevision)
		}
		return strings.Compare(a.ToolKey, b.ToolKey)
	})
	if len(slices.Compact(grants)) != len(grants) {
		return nil, ErrInvalidRequest
	}
	return grants, nil
}

// The frozen root may assign a delegation-only tool to its exact member
// revision. A nested request must also inherit it from the immediate parent.
// Ordinary callable tools retain the parent's original narrowing rule.
func authorizedDelegatedLocalTools(config ConfigSnapshot, parent agent.View, root bool, request DelegateRequest) ([]string, error) {
	policies := make(map[string]ToolPolicySnapshot, len(config.ToolPolicies))
	for _, policy := range config.ToolPolicies {
		policies[policy.Key] = policy
	}
	for _, key := range request.Agent.ToolKeys {
		policy, ok := policies[key]
		if !ok || nonAssignableRuntimeTool(key) || policy.ExecutionMode != "local_dispatch" || policy.DefinitionVersion == "" {
			return nil, ErrInvalidRequest
		}
		inherited := slices.Contains(parent.ToolKeys, key)
		scoped := false
		selected := false
		for _, grant := range config.DelegationPolicy.LocalToolGrants {
			if grant.ToolKey != key {
				continue
			}
			scoped = true
			if grant.MemberID == request.MemberID && grant.MemberRevision == request.MemberRevision {
				selected = true
			}
		}
		canAssign := inherited || root && selected
		if !canAssign || scoped && !selected {
			return nil, ErrInvalidRequest
		}
	}
	return normalizeStrings(request.Agent.ToolKeys), nil
}

func frozenDelegationExecution(value handoff.Delegation) *handoff.Delegation {
	value.Status, value.Result, value.ErrorCode, value.Error = handoff.StatusQueued, nil, "", ""
	return &value
}

func (runner *Runner) frozenDelegation(ctx context.Context, turnID, id string) (handoff.Delegation, bool, error) {
	items, err := listAllItems(ctx, runner.store, turnID)
	if err != nil {
		return handoff.Delegation{}, false, err
	}
	for _, item := range items {
		if item.Kind != ItemDelegation || (item.Status != ItemStarted && item.Status != ItemWaiting) {
			continue
		}
		var payload delegationItemPayload
		if !decodeTypedAuthorityJSON(item.Payload, &payload) {
			// An obsolete Role-bearing Item is not a new member authority.
			return handoff.Delegation{}, false, ErrConflict
		}
		if payload.DelegationID == id && payload.Execution != nil {
			return *payload.Execution, true, nil
		}
	}
	return handoff.Delegation{}, false, nil
}

func (runner *Runner) admitDelegationChild(ctx context.Context, turn Turn, parent Invocation, delegation handoff.Delegation) error {
	config, err := loadSealedConfig(ctx, runner.store, turn.ConfigSnapshotID)
	if err != nil || config.SharedBudget == nil {
		return err
	}
	// Historical frozen Waiting envelopes cannot bypass the current meter
	// contract. Reject unsupported hard limits before any ledger mutation.
	childLimits := delegation.Limits
	if delegation.SubtreeLimits != nil {
		childLimits = *delegation.SubtreeLimits
	}
	if !enforceableHarnessBudgetLimits(childLimits) {
		return ErrInvalidRequest
	}
	if runner.budgets == nil {
		return ErrInvalidRequest
	}
	coordinator := runner.budgets.coordinator()
	ledger, err := runner.budgets.bindRun(ctx, parent.ExecutionRefID)
	if err != nil {
		return err
	}
	// Recover terminal releases missed between the Runtime commit and the budget
	// commit. Waiting coordinators and merely cancelled requests still occupy
	// capacity until their Run has actually ended.
	var finished []string
	for id, binding := range ledger.Runs {
		if ledger.FinishedRuns[id] {
			continue
		}
		snapshot, loadErr := runner.runtime.Load(ctx, id)
		if errors.Is(loadErr, kernel.ErrNotFound) {
			// A registered child that was never created must not consume
			// descendant capacity forever after its owner has terminated.
			// Do not reclaim under a live/unknown parent: it may still be
			// preparing the child in another process.
			if binding.ParentRunID != "" {
				owner, ownerErr := runner.runtime.Load(ctx, binding.ParentRunID)
				if ownerErr == nil && terminalRuntimeStatus(owner.Run.Status) {
					// Cancellation prevents a late StartRun from spending budget;
					// reconciliation frees only lifetime capacity, not usage.
					if _, cancelErr := coordinator.CancelRun(ctx, turn.ID, id); cancelErr != nil {
						return cancelErr
					}
					finished = append(finished, id)
				} else if ownerErr != nil && !errors.Is(ownerErr, kernel.ErrNotFound) {
					return ownerErr
				}
			}
			continue
		}
		if loadErr != nil {
			return loadErr
		}
		if terminalRuntimeStatus(snapshot.Run.Status) {
			finished = append(finished, id)
		}
	}
	if len(finished) > 0 {
		if _, err = coordinator.FinishRuns(ctx, turn.ID, finished...); err != nil {
			return err
		}
	}
	// Admission happens before child topology facts are appended so a hard
	// descendant ceiling cannot leave a phantom relation or child Invocation.
	ledger, err = coordinator.RegisterRun(ctx, turn.ID, delegation.ChildRunID, budget.RunBudget{
		ParentRunID: parent.ExecutionRefID,
		Limits:      childLimits,
	})
	if err != nil {
		return err
	}
	return projectBudgetItem(ctx, runner.store, runner.turnFeed, runner.clock, ledger)
}

// prepareHandoffChild persists one exact Host-selected Child Attempt before
// the original ChildRunner is ever allowed to issue an external effect.
func (runner *Runner) prepareHandoffChild(ctx context.Context, turn Turn, parent Invocation, delegation handoff.Delegation, parentItemID string) error {
	input, inputHash, err := marshalInvocationValue(delegation)
	if err != nil {
		return err
	}
	root := parent
	if strings.TrimSpace(parent.ParentItemID) != "" {
		root, err = runner.loadFrozenChildRoot(ctx, parent)
		if err != nil {
			return err
		}
	}
	// Delegate Items may stop at the immediate Invocation owner, so inherit
	// the already-frozen intermediate owners as well as the root Attempt.
	parentAttempts, err := runner.freezeChildParentAttempts(ctx, turn.ID, parentItemID, root)
	if err != nil {
		return err
	}
	input, inputHash, err = sealChildRootInput(input, inputHash, root, parentAttempts...)
	if err != nil {
		return err
	}
	now := runner.clock.Now().UTC()
	id, err := AgentInvocationID(turn.ID, parentItemID, delegation.ID)
	if err != nil {
		return err
	}
	invocation, _, err := runner.createVerifiedInvocation(ctx, Invocation{
		ID: id, TurnID: turn.ID, ParentItemID: parentItemID,
		Input: input, InputHash: inputHash, ExecutionRefID: delegation.ChildRunID,
		Status: InvocationAccepted, Attempt: 1, Revision: 1, OutputRefs: []HostRef{}, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return err
	}
	return runner.recordInvocationItem(ctx, invocation)
}

// decodeFrozenHandoffDelegation admits only the current MemberID execution
// envelope. Historical RoleID/RoleRevision/RoleName fields must never be
// silently ignored and reinterpreted as a fresh Host-selected member grant.
// Additional frozen ancestry evidence is deliberately preserved in the input.
func decodeFrozenHandoffDelegation(raw json.RawMessage) (handoff.Delegation, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return handoff.Delegation{}, ErrConflict
	}
	for key := range fields {
		if strings.EqualFold(key, "roleID") || strings.EqualFold(key, "roleRevision") || strings.EqualFold(key, "roleName") {
			return handoff.Delegation{}, ErrConflict
		}
	}
	var delegation handoff.Delegation
	if json.Unmarshal(raw, &delegation) != nil {
		return handoff.Delegation{}, ErrConflict
	}
	return delegation, nil
}

// isHandoffDelegationInvocation identifies the original durable Handoff
// admission by its frozen child identity. Every member delegation belongs to
// its originally routed ChildRunner, never the Harness native Agent starter.
func isHandoffDelegationInvocation(invocation Invocation) bool {
	if invocation.ParentItemID == "" || !validInvocationIdentity(invocation) || len(invocation.Input) == 0 ||
		hashInvocationBytes(invocation.Input) != invocation.InputHash {
		return false
	}
	delegation, err := decodeFrozenHandoffDelegation(invocation.Input)
	if err != nil || strings.TrimSpace(delegation.ID) == "" || strings.TrimSpace(delegation.MemberID) == "" ||
		strings.TrimSpace(delegation.ChildRunID) == "" || strings.TrimSpace(delegation.Goal) == "" {
		return false
	}
	id, err := AgentInvocationID(invocation.TurnID, invocation.ParentItemID, delegation.ID)
	return err == nil && id == invocation.ID
}
