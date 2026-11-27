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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
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
		if !ok || nonAssignableRuntimeTool(key) || (policy.ExecutionMode != "" && policy.ExecutionMode != "local_dispatch") || policy.DefinitionVersion == "" {
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

func (runner *Runner) frozenDelegation(ctx context.Context, turnID, id string, ownerID ...string) (handoff.Delegation, bool, error) {
	owner := ""
	if len(ownerID) == 1 {
		owner = ownerID[0]
	}
	payload, found, err := runner.originalDelegationPayload(ctx, turnID, id, owner)
	if err != nil || !found {
		return handoff.Delegation{}, false, err
	}
	if payload.Execution == nil || payload.DelegationID != id {
		return handoff.Delegation{}, false, ErrConflict
	}
	return *payload.Execution, true, nil
}

// The standard local child uses the exact same native budget adapter and
// Runtime as its root. Route identity is a composition fact, not inferred
// from a same-ID local Run.
func (runner *Runner) nativeLocalDelegation(d handoff.Delegation) *agent.Runner {
	if !runner.nativeBudget() {
		return nil
	}
	native, ok := runner.agent.(*agent.Runner)
	c, bound := runner.handoffs.(*handoff.Coordinator)
	if !ok || !bound || !c.UsesEagerLocalAgentForDelegation(runner.runtime, native, d) {
		return nil
	}
	return native
}

// Only a fixed, scoped local Agent in this exact Runtime may replace
// Host Dispatch. The selected route is frozen with the original Delegation.
func (runner *Runner) nativeHandoff(d handoff.Delegation) *agent.Runner {
	native := runner.nativeRootStart()
	c, ok := runner.handoffs.(*handoff.Coordinator)
	if native == nil || !ok || d.Remote || !c.UsesLocalAgentForDelegation(runner.runtime, native, d) {
		return nil
	}
	return native
}

func delegationBudgetLimits(d handoff.Delegation) budget.Limits {
	if d.SubtreeLimits != nil {
		return *d.SubtreeLimits
	}
	return d.Limits
}

func (runner *Runner) admitDelegationChild(ctx context.Context, turn Turn, parent Invocation, delegation handoff.Delegation) error {
	config, err := loadSealedConfig(ctx, runner.store, turn.ConfigSnapshotID)
	if err != nil || config.SharedBudget == nil {
		return err
	}
	// Historical frozen Waiting envelopes cannot bypass the current meter
	// contract. Reject unsupported hard limits before any ledger mutation.
	childLimits := delegationBudgetLimits(delegation)
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
	binding := budget.RunBudget{ParentRunID: parent.ExecutionRefID, Limits: childLimits}
	if delegation.NativeLocal || runner.nativeLocalDelegation(delegation) != nil {
		// Capacity preview only. Do not consume a child count, lifetime
		// slot, or project a phantom registration before native Create.
		if ledger.ID != turn.ID {
			return budget.ErrLedgerConflict
		}
		draft := budget.LedgerMap{ledger.ID: ledger}
		_, err = (budget.Coordinator{Store: draft}).RegisterRun(ctx, ledger.ID, delegation.ChildRunID, binding)
		return err
	}
	// Reconcile legacy/remote lifetime releases and uncreated reservations.
	// A native local terminal Run must already have settled its own ledger.
	// Waiting or merely cancelled requests retain lifetime capacity.
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
			// Native terminal commits already settle the lifetime in the
			// same transaction. Missing accounting is not a repairable
			// Host projection, nor permission to admit another descendant.
			if runner.budgets.dependencies.Runtime != nil && snapshot.Run.Kind == "agent" {
				return ErrExecutionOutcomeUnknown
			}
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
	ledger, err = coordinator.RegisterRun(ctx, turn.ID, delegation.ChildRunID, binding)
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
	parentAttempts, err := runner.freezeChildParentAttempts(ctx, turn.ID, parentItemID, root, "")
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

// nativeChildSelection uses the same original sealed parent and child identity
// for Create, definitive rejection and cancellation. No Host grant is needed.
func (r *Runner) nativeChildSelection(ctx context.Context, turn Turn, owner Invocation, status kernel.RunStatus, reason string) (handoff.Delegation, Invocation, kernel.RunCreationDecision, error) {
	d, parent, e := r.originalHandoffParent(ctx, owner)
	if e != nil || !d.NativeLocal || owner.TurnID != turn.ID || owner.Attempt != 1 || r.nativeHandoff(d) == nil {
		return d, parent, kernel.RunCreationDecision{}, errors.Join(ErrConflict, e)
	}
	q, e := r.nativeChildCreationSelection(ctx, turn, d, parent.ExecutionRefID, owner.Input, status, reason)
	return d, parent, q, e
}

func (r *Runner) nativeChildCreationSelection(ctx context.Context, turn Turn, d handoff.Delegation, parentRunID string, input json.RawMessage, status kernel.RunStatus, reason string) (kernel.RunCreationDecision, error) {
	q := kernel.RunCreationDecision{RunID: d.ChildRunID, LogicalParentRunID: parentRunID, OriginFingerprint: agent.FrozenOriginFingerprint(input), Status: status, Reason: strings.TrimSpace(reason)}
	config, err := loadSealedConfig(ctx, r.store, turn.ConfigSnapshotID)
	if err != nil {
		return q, err
	}
	if config.SharedBudget != nil {
		if !r.nativeBudget() {
			return q, ErrConflict
		}
		q.Budget = budget.RunEnrollment{ScopeID: turn.ID, Limits: *config.SharedBudget, Binding: budget.RunBudget{ParentRunID: parentRunID, Limits: delegationBudgetLimits(d)}}
	}
	return q, nil
}

func (r *Runner) nativeChildDecision(ctx context.Context, turn Turn, owner Invocation) (kernel.RunCreationDecision, error) {
	_, _, q, e := r.nativeChildSelection(ctx, turn, owner, kernel.RunStatusCancelled, "")
	if e != nil {
		return q, e
	}
	return r.readNativeCreationDecision(ctx, q)
}

// Project only the immutable native negative decision; no synthetic Kernel
// snapshot, lease, ledger member or ChildRunner receipt is created.
func (r *Runner) projectUncreatedNativeChild(ctx context.Context, turn Turn, owner Invocation, d kernel.RunCreationDecision) (handoff.Delegation, error) {
	frozen, parent, q, e := r.nativeChildSelection(ctx, turn, owner, d.Status, d.Reason)
	if e != nil || !d.MatchesSelection(q) {
		return handoff.Delegation{}, errors.Join(ErrConflict, e)
	}
	if e = r.projectCreationDecisionBudget(ctx, d); e != nil {
		return handoff.Delegation{}, e
	}
	current, e := r.store.GetInvocation(ctx, owner.ID)
	if e != nil || !sameFrozenInvocationAuthority(owner, current) {
		return handoff.Delegation{}, errors.Join(ErrConflict, e)
	}
	code := agentStartFailedCode
	if d.Status == kernel.RunStatusCancelled {
		code = "run.cancelled"
	}
	current, e = r.syncInvocationStatus(withTerminalWitness(ctx, current, TurnStatus(d.Status)), current, invocationStatusFromTurn(TurnStatus(d.Status)), code, d.Reason)
	if e != nil {
		return handoff.Delegation{}, e
	}
	frozen.Status, frozen.ErrorCode, frozen.Error = handoff.Status(d.Status), current.ErrorCode, current.ErrorDetail
	_, e = r.recordDelegationItem(ctx, turn, parent, frozen, delegationItemStatus(frozen.Status), owner.ParentItemID)
	return frozen, e
}

func (r *Runner) dispatchNativeHandoff(ctx context.Context, turn Turn, parent kernel.Snapshot, child Invocation, d handoff.Delegation) (handoff.Delegation, error) {
	frozen, owner, q, e := r.nativeChildSelection(ctx, turn, child, kernel.RunStatusCancelled, "")
	native := r.nativeHandoff(d)
	if e != nil || native == nil || !sameHandoffFrozenExecution(frozen, d) || owner.ExecutionRefID != parent.Run.ID {
		return handoff.Delegation{}, errors.Join(ErrConflict, e)
	}
	if ready, e := r.originalNativeCommand(ctx, child); e != nil || !ready {
		return handoff.Delegation{}, errors.Join(ErrConflict, e)
	}
	decision, e := r.readNativeCreationDecision(ctx, q)
	var startErr error
	if errors.Is(e, kernel.ErrNotFound) {
		c := r.handoffs.(*handoff.Coordinator)
		var snap kernel.Snapshot
		var outcome agent.PhysicalCallOutcome
		snap, outcome, startErr = r.observeNativeCommand(withoutContextCheckpoint(ctx), native, child, "start", nil, func(cctx context.Context) (kernel.Snapshot, error) {
			return c.StartNative(cctx, r.runtime, native, parent, d, child.Input)
		})
		if snap.Run.ID != "" {
			current, original, err := r.localDelegationObservation(ctx, child.ExecutionRefID)
			if err != nil || kernel.ManagedDigest(current) != kernel.ManagedDigest(snap) {
				return handoff.Delegation{}, errors.Join(ErrExecutionOutcomeUnknown, startErr, err)
			}
			// An original retained grant qualifies only a pending wait, not
			// a returned effect. No second election or terminal projection.
			if outcome.Kind() == agent.PhysicalCallUnknown && snap.Run.Status == kernel.RunStatusRunning && errors.Is(startErr, agent.ErrRootOutcomeUnknown) {
				if g, err := r.runtime.LookupChildExecution(ctx, child.ExecutionRefID); err == nil && g.IsOwned() && g.AdmissionOperation == "start" {
					return handoff.ProjectChild(original, current), handoff.ErrChildPending
				}
			}
			if !outcome.Matches(child.ExecutionRefID, plugin.RunStart) || outcome.Kind() == agent.PhysicalCallCreated || outcome.Kind() == agent.PhysicalCallNotEntered {
				return handoff.Delegation{}, errors.Join(ErrExecutionOutcomeUnknown, startErr)
			}
			if _, err = r.syncChildInvocationSnapshot(ctx, turn, child, current); err != nil {
				return handoff.Delegation{}, errors.Join(ErrExecutionOutcomeUnknown, err)
			}
			result := handoff.ProjectChild(original, current)
			if result.Status == handoff.StatusFailed || result.Status == handoff.StatusCancelled {
				startErr = errors.Join(startErr, handoff.ErrChildFailed)
			}
			return result, startErr
		}
		if agent.IsNativeStartNoEffect(startErr) && errors.Is(startErr, budget.ErrWaiting) {
			return handoff.Delegation{}, dispatchProvenNoEffect{cause: startErr}
		}
		decision, e = r.resolveNativeStartDecision(ctx, q, startErr)
	}
	if e != nil {
		return handoff.Delegation{}, errors.Join(ErrExecutionOutcomeUnknown, startErr, e)
	}
	result, e := r.projectUncreatedNativeChild(ctx, turn, child, decision)
	return result, errors.Join(handoff.ErrChildFailed, startErr, e)
}
