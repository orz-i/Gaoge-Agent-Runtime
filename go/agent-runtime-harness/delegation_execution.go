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

func assignableParentToolPolicies(config ConfigSnapshot, parent agent.View) []ToolPolicySnapshot {
	policies := make([]ToolPolicySnapshot, 0, len(config.ToolPolicies))
	for _, policy := range config.ToolPolicies {
		if !slices.Contains(parent.ToolKeys, policy.Key) || nonAssignableRuntimeTool(policy.Key) {
			continue
		}
		if policy.ExecutionMode != "local_dispatch" && policy.ExecutionMode != "provider_hosted" {
			continue
		}
		if policy.DefinitionVersion == "" {
			continue
		}
		policies = append(policies, policy)
	}
	return policies
}

// authorizedDelegatedLocalTools verifies the Host-selected Child local Tool
// set against the original immutable parent Run and sealed Tool policies.
// No model Tool argument or dynamic member name can expand local authority.
// Provider-hosted capabilities use explicit Host grants rather than being
// silently copied from the parent's model and credential binding.
func authorizedDelegatedLocalTools(config ConfigSnapshot, parent agent.View, keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	policies := assignableParentToolPolicies(config, parent)
	for _, key := range keys {
		permitted := false
		for _, policy := range policies {
			if policy.Key == key && policy.ExecutionMode == "local_dispatch" {
				permitted = true
				break
			}
		}
		if !permitted {
			return nil, ErrInvalidRequest
		}
	}
	return normalizeStrings(keys), nil
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
		Limits:      delegation.Limits,
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
	id, err := InvocationID(turn.ID, parentItemID, CapabilityAgent, delegation.ID)
	if err != nil {
		return err
	}
	invocation, _, err := runner.createVerifiedInvocation(ctx, Invocation{
		ID: id, TurnID: turn.ID, ParentItemID: parentItemID, CapabilityKey: CapabilityAgent, DefinitionVersion: RuntimeCapabilityVersion,
		ExecutionClass: ExecutionAgent, Input: input, InputHash: inputHash, ExecutionRefID: delegation.ChildRunID,
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
	if invocation.ParentItemID == "" || invocation.ExecutionClass != ExecutionAgent ||
		invocation.CapabilityKey != CapabilityAgent || len(invocation.Input) == 0 ||
		hashInvocationBytes(invocation.Input) != invocation.InputHash {
		return false
	}
	delegation, err := decodeFrozenHandoffDelegation(invocation.Input)
	if err != nil || strings.TrimSpace(delegation.ID) == "" || strings.TrimSpace(delegation.MemberID) == "" ||
		strings.TrimSpace(delegation.ChildRunID) == "" || strings.TrimSpace(delegation.Goal) == "" {
		return false
	}
	id, err := InvocationID(invocation.TurnID, invocation.ParentItemID, CapabilityAgent, delegation.ID)
	return err == nil && id == invocation.ID
}
