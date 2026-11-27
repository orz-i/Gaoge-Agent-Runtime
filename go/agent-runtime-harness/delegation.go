package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type HandoffStarter interface {
	StartOrLoad(context.Context, kernel.Snapshot, handoff.Delegation) (handoff.Delegation, error)
}

// DelegateRequest names a Host-authorized ChildRunner route. The model-visible
// fields select only a member and a task. Agent carries a trusted Host-selected
// Agent execution request, not a second role catalog or model-writable authority.
// JSON Tool input cannot grant its own Model, Tool, Skills or physical identity.
type DelegateRequest struct {
	MemberID       string             `json:"memberID"`
	Goal           string             `json:"goal"`
	MemberRevision string             `json:"-"`
	Remote         bool               `json:"-"`
	Agent          agent.StartRequest `json:"-"`
	callID         string
	// Preserve the first model Call identity even after Host policy prepares
	// the final execution scope; recovery uses the original persisted receipt.
	callHash string
	// Set only by the Runtime after the Host policy accepts this exact
	// request or a verified durable original Tool intent is recovered.
	hostAuthorized bool
}

type DelegationResult struct {
	Delegation handoff.Delegation `json:"delegation"`
	Snapshot   Snapshot           `json:"snapshot"`
}

type delegationItemPayload struct {
	DelegationID     string              `json:"delegationID"`
	MemberID         string              `json:"memberID"`
	MemberRevision   string              `json:"memberRevision,omitempty"`
	ChildRunID       string              `json:"childRunID"`
	Goal             string              `json:"goal"`
	Status           handoff.Status      `json:"status"`
	Result           json.RawMessage     `json:"result,omitempty"`
	ParentRunID      string              `json:"parentRunID,omitempty"`
	Execution        *handoff.Delegation `json:"execution,omitempty"`
	OriginalCallHash string              `json:"originalCallHash,omitempty"`
}

func normalizeDelegateRequest(request DelegateRequest) (DelegateRequest, error) {
	request.MemberID = strings.TrimSpace(request.MemberID)
	request.MemberRevision = strings.TrimSpace(request.MemberRevision)
	request.Goal = strings.TrimSpace(request.Goal)
	if request.MemberID == "" || len(request.MemberID) > 160 || len(request.MemberRevision) > 256 ||
		request.Goal == "" || len(request.Goal) > 200_000 {
		return DelegateRequest{}, ErrInvalidRequest
	}
	// The Host may only fill execution options. Physical identity, parent
	// lineage, Actor/Thread, Goal, and continuation grants are Runtime-owned.
	a := &request.Agent
	if a.ID != "" || a.RequestID != "" || a.ParentRunID != "" || a.LogicalParentRunID != "" || a.OriginFingerprint != "" ||
		a.Actor != (kernel.ActorRef{}) || a.Thread != (kernel.ThreadRef{}) || a.Goal != "" ||
		a.DeadlineAt != nil || a.CallAllowance != nil || a.AutoAllowance != nil ||
		len(a.RequiredToolKeys) != 0 || model.ValidateOutputContract(a.OutputContract) != nil || a.MaxModelOutputTokens < 0 || !enforceableHarnessBudgetLimits(a.Limits) {
		return DelegateRequest{}, ErrInvalidRequest
	}
	// Advanced Host-selected child routes and the standard NamedAgent roster
	// have exactly the same capability validation and native mapping. Only
	// physical identity, authorization and allowance fields are not AgentSpec.
	spec, err := normalizeAgentSpec(AgentSpec{
		Model: a.Model, ModelOptions: a.ModelOptions, Instructions: a.Instructions,
		OutputContract: a.OutputContract, MaxModelOutputTokens: a.MaxModelOutputTokens,
		ToolKeys: a.ToolKeys, InitialToolKeys: a.InitialToolKeys,
		HostedToolGrants: a.HostedToolGrants, Limits: a.Limits,
	})
	if err != nil {
		return DelegateRequest{}, err
	}
	request.Agent = spec.startRequest()
	if request.Remote && !agent.SupportsRemoteGoalOnly(request.Agent) {
		return DelegateRequest{}, ErrInvalidRequest
	}
	return request, nil
}

func (runner *Runner) Delegate(ctx context.Context, turnID string, request DelegateRequest) (DelegationResult, error) {
	turn, err := loadVerifiedTurn(ctx, runner.store, strings.TrimSpace(turnID))
	if err != nil {
		return DelegationResult{}, err
	}
	invocation, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if err != nil {
		return DelegationResult{}, err
	}
	if !runner.canDelegate(invocation) {
		return DelegationResult{}, ErrInvalidRequest
	}
	return runner.delegate(ctx, turn, invocation, request)
}

// authorizeDelegation is the one Host-owned authorization boundary shared by
// direct Go/HTTP and model Tool entry points. A missing Host policy denies
// new members; only an already frozen Tool intent skips its second evaluation.
func (runner *Runner) authorizeDelegation(ctx context.Context, execution tools.ExecutionRequest, request DelegateRequest) (DelegateRequest, error) {
	if runner == nil || runner.delegationPolicy == nil {
		return DelegateRequest{}, ErrInvalidRequest
	}
	var err error
	request, err = normalizeDelegateRequest(request)
	if err != nil {
		return DelegateRequest{}, err
	}
	request, err = runner.delegationPolicy.PrepareDelegation(ctx, execution, request)
	if err != nil {
		return DelegateRequest{}, err
	}
	request, err = normalizeDelegateRequest(request)
	if err != nil {
		return DelegateRequest{}, err
	}
	request.hostAuthorized = true
	return request, nil
}

// DelegateByExecutionRefID is the Tool-facing form used by an Agent
// without exposing Harness Turn or Invocation IDs to the model.
func (runner *Runner) DelegateByExecutionRefID(ctx context.Context, executionRefID string, request DelegateRequest) (DelegationResult, error) {
	invocation, err := runner.store.GetInvocationByExecutionRefID(ctx, strings.TrimSpace(executionRefID))
	if err != nil {
		return DelegationResult{}, err
	}
	turn, err := loadVerifiedTurn(ctx, runner.store, invocation.TurnID)
	if err != nil {
		return DelegationResult{}, err
	}
	return runner.delegate(ctx, turn, invocation, request)
}

func (runner *Runner) delegate(ctx context.Context, turn Turn, invocation Invocation, request DelegateRequest) (DelegationResult, error) {
	request, err := normalizeDelegateRequest(request)
	if err != nil || !runner.canDelegate(invocation) {
		return DelegationResult{}, errors.Join(ErrInvalidRequest, err)
	}
	if !request.hostAuthorized {
		// Direct Go/HTTP callers cannot bypass the policy used by the Tool.
		// This is a trusted Host invocation, never a model-writable grant.
		var arguments json.RawMessage
		arguments, err = json.Marshal(request)
		if err != nil {
			return DelegationResult{}, err
		}
		request, err = runner.authorizeDelegation(ctx, tools.ExecutionRequest{
			RunID: invocation.ExecutionRefID, Call: tools.Call{
				ID:      stableID("manual", turn.ID, invocation.ID, request.MemberID, request.Goal),
				ToolKey: DelegationToolKey, Arguments: arguments,
			},
		}, request)
		if err != nil {
			return DelegationResult{}, err
		}
	}
	if err = runner.requireFrozenAdmission(ctx, turn); err != nil {
		return DelegationResult{}, err
	}
	delegation, parent, err := runner.prepareDelegation(ctx, turn, invocation, request)
	if err != nil {
		return DelegationResult{}, err
	}
	startedItemID, admitted, err := runner.admitOriginalDelegation(ctx, turn, invocation, delegation)
	if errors.Is(err, budget.ErrWaiting) {
		if _, e := runner.recordDelegationItem(ctx, turn, invocation, delegation, ItemWaiting, "", request.callHash); e != nil {
			return DelegationResult{}, e
		}
		snapshot, e := runner.Load(ctx, turn.ID)
		return DelegationResult{Delegation: delegation, Snapshot: snapshot}, e
	}
	if err != nil {
		return DelegationResult{}, err
	}
	if !admitted {
		return runner.verifiedTerminalDelegationReplay(ctx, turn, delegation, startedItemID)
	}

	return runner.executeDelegation(ctx, turn, invocation, parent, delegation, startedItemID, request.callHash)
}

// Both an ordinary Start and an original capacity wakeup admit the same
// immutable child. A known prior Start is read before budget mutation;
// only the original native rejection can re-enter its existing identity.
func (runner *Runner) admitOriginalDelegation(ctx context.Context, turn Turn, owner Invocation, d handoff.Delegation) (string, bool, error) {
	id := stableID("hid", turn.ID, d.ID, string(ItemStarted))
	started, err := runner.store.GetItem(ctx, id)
	retry := err == nil
	if retry {
		p, valid := frozenDelegationAdmissionPayload(owner, started)
		if !valid || started.Status != ItemStarted || started.ID != id || started.InvocationID != owner.ID ||
			started.RunID != d.ChildRunID || p.ParentRunID != owner.ExecutionRefID || !sameHandoffFrozenExecution(d, *p.Execution) {
			return id, false, ErrConflict
		}
		ready, e := runner.retryNativeDelegation(ctx, turn, d, id)
		if e != nil || !ready {
			return id, false, e
		}
	} else if !errors.Is(err, ErrNotFound) {
		return id, false, err
	}
	if err = runner.admitDelegationChild(ctx, turn, owner, d); err != nil {
		return id, false, err
	}
	if retry {
		return id, true, nil
	}
	return runner.recordDelegationItemWithAdmission(ctx, turn, owner, d, ItemStarted, "")
}

// verifiedTerminalDelegationReplay is read-only. It cannot run the original
// Tool/Model, issue a ChildRunner Start, or turn a matching Run ID into a
// terminal receipt. Both the original physical-grant return and original
// immutable ancestor/child proof must agree with the live physical owner.
func (runner *Runner) verifiedTerminalDelegationReplay(
	ctx context.Context, turn Turn, frozen handoff.Delegation, startedItemID string,
) (DelegationResult, error) {
	unknown := func() (DelegationResult, error) {
		return DelegationResult{}, ErrExecutionOutcomeUnknown
	}
	childOwner, err := runner.store.GetInvocationByExecutionRefID(ctx, frozen.ChildRunID)
	if err != nil || !isHandoffDelegationInvocation(childOwner) ||
		childOwner.ParentItemID != startedItemID || childOwner.TurnID != turn.ID {
		return unknown()
	}
	root, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if err != nil {
		return unknown()
	}
	items, err := runner.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if err != nil {
		return unknown()
	}
	status, valid := verifiedTerminalHandoffProjection(root, childOwner, items)
	if !valid {
		return unknown()
	}
	result, _, err := runner.terminalDelegationOutcome(ctx, childOwner, frozen)
	if err != nil || status != delegationItemStatus(result.Status) {
		return unknown()
	}
	snapshot, err := runner.Load(ctx, turn.ID)
	if err != nil {
		return DelegationResult{}, err
	}
	return DelegationResult{Delegation: result, Snapshot: snapshot}, nil
}

func (runner *Runner) canDelegate(invocation Invocation) bool {
	return runner != nil && runner.handoffs != nil && runner.relations != nil &&
		strings.TrimSpace(invocation.ExecutionRefID) != ""
}

func (runner *Runner) prepareDelegation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	request DelegateRequest,
) (handoff.Delegation, kernel.Snapshot, error) {
	config, err := loadSealedConfig(ctx, runner.store, turn.ConfigSnapshotID)
	if err != nil {
		return handoff.Delegation{}, kernel.Snapshot{}, err
	}
	if err = runner.enforceDelegationDepth(ctx, config, invocation); err != nil {
		return handoff.Delegation{}, kernel.Snapshot{}, err
	}
	parent, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	if err != nil || terminalRuntimeStatus(parent.Run.Status) {
		return handoff.Delegation{}, kernel.Snapshot{}, errors.Join(ErrConflict, err)
	}
	delegationID := stableID("hd", turn.ID, request.MemberID, request.Goal)
	if invocation.Attempt > 1 {
		delegationID = stableID("hd", turn.ID, invocation.ExecutionRefID, request.MemberID, request.Goal)
	}
	if request.callID != "" {
		delegationID = delegationToolID(invocation, request.callID)
	}
	childRunID := stableID("hchild", invocation.ExecutionRefID, delegationID)
	delegation := handoff.Delegation{
		ID: delegationID, MemberID: request.MemberID, ChildRunID: childRunID,
		Goal: request.Goal, Status: handoff.StatusQueued,
	}
	if frozen, found, loadErr := runner.frozenDelegation(ctx, turn.ID, delegationID, invocation.ID); found || loadErr != nil {
		return frozen, parent, loadErr
	}
	// The Host chose the named Agent before entering Runtime. Freeze only the
	// resulting execution inputs; Runtime may tighten authority, never resolve
	// names into an independent Role/Tool/Skill policy of its own.
	a := request.Agent
	delegation.MemberRevision = request.MemberRevision
	delegation.Remote = request.Remote
	if delegation.Remote {
		// The selected remote member has its own identity/revision but none
		// of the parent's native Model, Tool, sampling or budget capabilities.
		named, selected := namedAgentForMember(config, request.MemberID, request.MemberRevision)
		if !selected || !named.Remote || !agent.SupportsRemoteGoalOnly(a) ||
			config.SharedBudget != nil {
			return handoff.Delegation{}, parent, ErrInvalidRequest
		}
		return delegation, parent, nil
	}
	// In a nested handoff the immediate parent may run a different Model from
	// the Turn root. ModelOptions and local Tool visibility belong to that
	// parent, not a second resolution of the root Config catalog.
	parentModel, parentOptions, parentLimits := config.Model, config.ModelOptions, config.Limits
	parentOutputCeiling := config.MaxModelOutputTokens
	parentView, viewErr := agent.ViewState(parent)
	if viewErr == nil {
		parentModel, parentOptions, parentLimits = parentView.Model, parentView.ModelOptions, parentView.Budget.Limits
		parentOutputCeiling = parentView.MaxModelOutputTokens
	} else if invocation.ParentItemID != "" {
		return handoff.Delegation{}, parent, ErrConflict
	}
	delegation.Model = firstNonEmpty(a.Model, parentModel)
	delegation.MaxModelOutputTokens = parentOutputCeiling
	if a.MaxModelOutputTokens > 0 {
		if parentOutputCeiling > 0 && a.MaxModelOutputTokens > parentOutputCeiling {
			return handoff.Delegation{}, parent, ErrInvalidRequest
		}
		delegation.MaxModelOutputTokens = a.MaxModelOutputTokens
	}
	if a.Model == "" || a.Model == parentModel {
		delegation.ModelOptions = append(json.RawMessage(nil), parentOptions...)
	} else {
		delegation.ModelOptions = nil
	}
	if len(a.ModelOptions) != 0 {
		delegation.ModelOptions = append(json.RawMessage(nil), a.ModelOptions...)
	}
	delegation.Instructions = a.Instructions
	delegation.OutputContract = model.CloneOutputContract(a.OutputContract)
	if len(config.DelegationPolicy.NamedAgents) != 0 {
		named, found := namedAgentForMember(config, request.MemberID, request.MemberRevision)
		if !found || a.Model != named.Model || !slices.Equal(normalizeStrings(a.ToolKeys), named.ToolKeys) ||
			!slices.Equal(normalizeStrings(a.InitialToolKeys), named.InitialToolKeys) ||
			!slices.Equal(a.HostedToolGrants, named.HostedToolGrants) || a.Instructions != named.Instructions ||
			a.MaxModelOutputTokens != named.MaxModelOutputTokens || a.Limits != named.Limits ||
			!reflect.DeepEqual(a.OutputContract, named.OutputContract) ||
			!reflect.DeepEqual(a.ModelOptions, named.ModelOptions) {
			return handoff.Delegation{}, parent, ErrInvalidRequest
		}
		if manifest := selectedSkillManifest(named.Skills); manifest != "" {
			delegation.Instructions = strings.TrimSpace(delegation.Instructions + "\n\n" + manifest)
		}
	}
	var local []string
	if len(a.ToolKeys) != 0 {
		if viewErr != nil {
			return handoff.Delegation{}, parent, viewErr
		}
		root := invocation.ParentItemID == ""
		if len(config.DelegationPolicy.LocalToolGrants) != 0 {
			if root {
				err = validateRootRuntimeOwnership(invocation, parent)
			} else {
				_, _, err = runner.localDelegationObservation(ctx, invocation.ExecutionRefID)
			}
			if err != nil {
				return handoff.Delegation{}, parent, err
			}
		}
		local, err = authorizedDelegatedLocalTools(config, parentView, root, request)
	}
	if err != nil {
		return handoff.Delegation{}, parent, tools.NewRecoverableCallError(
			"delegation.tool_selection_denied", "The requested child tools are not authorized by the original parent Run.", err)
	}
	delegation.ToolKeys = normalizeStrings(append(delegation.ToolKeys, local...))
	if named, found := namedAgentForMember(config, request.MemberID, request.MemberRevision); found && len(named.Skills) != 0 {
		// This reserved Tool is not an ordinary local grant and never belongs
		// to the parent merely because its child is authorized to read Skills.
		delegation.ToolKeys = append(delegation.ToolKeys, SkillReadToolKey)
	}
	delegation.HostedToolGrants = append([]agent.HostedToolGrant(nil), a.HostedToolGrants...)
	if len(delegation.HostedToolGrants) > 0 && delegation.Model == "" {
		return handoff.Delegation{}, parent, ErrInvalidRequest
	}
	for _, grant := range delegation.HostedToolGrants {
		delegation.ToolKeys = append(delegation.ToolKeys, grant.Key)
	}
	delegation.ToolKeys = normalizeStrings(delegation.ToolKeys)
	delegation.InitialToolKeys = normalizeStrings(append(delegation.InitialToolKeys, a.InitialToolKeys...))
	for _, key := range delegation.InitialToolKeys {
		if !slices.Contains(delegation.ToolKeys, key) {
			return handoff.Delegation{}, parent, ErrInvalidRequest
		}
	}
	// Shared descendant ceilings and local Agent call/token limits have
	// distinct enforcers. Freeze only limits with actual accounting signals
	// for the ledger; cost and state-byte hard ceilings are rejected earlier.
	// The original parent subtree remains enforced by the ledger ancestry.
	resolved, limitErr := budget.TightenLimits(parentLimits, a.Limits)
	if limitErr != nil {
		return handoff.Delegation{}, parent, ErrInvalidRequest
	}
	if config.SharedBudget != nil {
		// Keep an independent frozen copy: the Agent-only projection below
		// intentionally clears dimensions this runner cannot enforce.
		subtreeLimits := resolved
		delegation.SubtreeLimits = &subtreeLimits
	} else if a.Limits.MaxChildRuns > 0 || a.Limits.MaxConcurrentRuns > 0 ||
		a.Limits.MaxConcurrentDescendants > 0 {
		// Without a shared ledger, Agent-local budget cannot enforce these.
		return handoff.Delegation{}, parent, ErrInvalidRequest
	}
	resolved.MaxChildRuns, resolved.MaxConcurrentRuns, resolved.MaxCostUnits, resolved.MaxStateBytes = 0, 0, 0, 0
	resolved.MaxConcurrentDescendants = 0
	delegation.Limits = resolved
	delegation.NativeLocal = runner.nativeHandoff(delegation) != nil
	return delegation, parent, nil
}

// Retry only the immutable same child after an actual native no-effect
// rejection. The Store's original dispatch fence still elects the next
// caller, including a concurrent cancellation or an already returned Start.
func (runner *Runner) retryNativeDelegation(ctx context.Context, turn Turn, d handoff.Delegation, startedID string) (bool, error) {
	if !d.NativeLocal && runner.nativeLocalDelegation(d) == nil {
		return false, nil
	}
	owner, e := verifiedRunInvocation(ctx, runner.store, d.ChildRunID)
	if errors.Is(e, ErrNotFound) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if owner.TurnID != turn.ID || owner.ParentItemID != startedID {
		return false, ErrConflict
	}
	frozen, e := decodeFrozenHandoffDelegation(owner.Input)
	if e != nil || !sameHandoffFrozenExecution(frozen, d) {
		return false, ErrConflict
	}
	items, e := runner.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if e != nil {
		return false, e
	}
	if d.NativeLocal {
		// Only a recorded original Waiting admission can attempt the same
		// identity after a budget-capacity rejection. Create still elects.
		if runner.nativeHandoff(d) == nil || owner.Status != InvocationAccepted || ActiveCancellationFence(items) || PendingExecutionDispatch(items, owner.ID) {
			return false, nil
		}
		waiting, e := runner.store.GetItem(ctx, stableID("hid", turn.ID, d.ID, string(ItemWaiting)))
		if errors.Is(e, ErrNotFound) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		root, e := loadTopLevelInvocation(ctx, runner.store, turn.ID)
		if e != nil {
			return false, e
		}
		payload, valid := frozenDelegationAdmissionPayload(root, waiting)
		if !valid || !sameHandoffFrozenExecution(*payload.Execution, d) {
			return false, ErrConflict
		}
		if _, e = runner.runtime.LoadCreationDecision(ctx, d.ChildRunID); !errors.Is(e, kernel.ErrNotFound) {
			if e == nil || errors.Is(e, kernel.ErrAlreadyExists) {
				return false, nil
			}
			return false, e
		}
	} else if !verifiedRejectedHandoffStarts(items, owner) {
		return false, nil
	}
	_, e = runner.runtime.Load(ctx, d.ChildRunID)
	if errors.Is(e, kernel.ErrNotFound) {
		return true, nil
	}
	return false, e
}

func (runner *Runner) executeDelegation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	parent kernel.Snapshot,
	delegation handoff.Delegation,
	startedItemID, callHash string,
) (DelegationResult, error) {
	if err := runner.ensureDelegationRelation(ctx, invocation.ExecutionRefID, delegation); err != nil {
		return DelegationResult{}, err
	}
	// Every delegated child selected by its frozen MemberID owns
	// one frozen Invocation and the same cancellation-fenced physical grant.
	// A member-only Handoff must not bypass this boundary.
	if err := runner.prepareHandoffChild(ctx, turn, invocation, delegation, startedItemID); err != nil {
		return DelegationResult{}, err
	}
	returned, delegateErr := runner.dispatchHandoff(ctx, turn, parent, delegation, startedItemID)
	if isDispatchProvenNoEffect(delegateErr) {
		if errors.Is(delegateErr, budget.ErrWaiting) {
			if _, e := runner.recordDelegationItem(ctx, turn, invocation, delegation, ItemWaiting, "", callHash); e != nil {
				return DelegationResult{}, errors.Join(ErrExecutionOutcomeUnknown, e)
			}
			snapshot, e := runner.Load(ctx, turn.ID)
			return DelegationResult{Delegation: delegation, Snapshot: snapshot}, e
		}
		// Project the original rejected Host selection, not a synthetic
		// terminal Kernel Run. Never let it block a parent's known no-effect
		// Tool receipt or grant another Attempt.
		child, e := verifiedRunInvocation(ctx, runner.store, delegation.ChildRunID)
		if e != nil || child.ParentItemID != startedItemID {
			return DelegationResult{}, errors.Join(ErrExecutionOutcomeUnknown, e)
		}
		witness, e := withOriginalNoEffectTerminalWitness(ctx, child, delegateErr)
		if e != nil {
			return DelegationResult{}, e
		}
		_, e = runner.syncInvocationStatus(witness, child, InvocationFailed, agentStartFailedCode, delegateErr.Error())
		if e != nil {
			return DelegationResult{}, errors.Join(ErrExecutionOutcomeUnknown, e)
		}
		return DelegationResult{}, delegateErr
	}
	if returned.ID == "" {
		return DelegationResult{}, delegateErr
	}
	delegation = returned
	status := delegationItemStatus(delegation.Status)
	if status != ItemStarted && !delegation.NativeLocal {
		if _, itemErr := runner.recordDelegationItem(ctx, turn, invocation, delegation, status, startedItemID); itemErr != nil {
			return DelegationResult{}, errors.Join(delegateErr, itemErr)
		}
	}
	snapshot, loadErr := runner.Load(ctx, turn.ID)
	if errors.Is(delegateErr, handoff.ErrChildPending) && !errors.Is(delegateErr, ErrExecutionOutcomeUnknown) {
		delegateErr = nil
	}
	return DelegationResult{Delegation: delegation, Snapshot: snapshot}, errors.Join(delegateErr, loadErr)
}

// dispatchHandoff uses the native Feature dispatch grant for every delegated
// child, owned by its frozen child Invocation rather than its parent Agent.
// Only a matching Handoff-observed Run outcome can settle that grant.
func (runner *Runner) dispatchHandoff(
	ctx context.Context, turn Turn, parent kernel.Snapshot,
	delegation handoff.Delegation, parentItemID string,
) (handoff.Delegation, error) {
	child, err := runner.store.GetInvocationByExecutionRefID(ctx, delegation.ChildRunID)
	if err != nil || child.TurnID != turn.ID || child.ParentItemID != parentItemID ||
		child.ExecutionRefID != delegation.ChildRunID ||
		!validInvocationExecutionAuthority(child) || !isHandoffDelegationInvocation(child) {
		return handoff.Delegation{}, errors.Join(ErrConflict, err)
	}
	// The hashed child Input, not the caller's slice-backed Delegation, is
	// the immutable execution authority. A ChildRunner can mutate a passed
	// ToolKeys/HostedToolGrants backing array in place; comparing its reply
	// with the original Go value would silently compare the same mutation.
	frozen, decodeErr := decodeFrozenHandoffDelegation(child.Input)
	if decodeErr != nil || !sameHandoffFrozenExecution(frozen, delegation) {
		return handoff.Delegation{}, ErrConflict
	}
	if frozen.NativeLocal {
		return runner.dispatchNativeHandoff(ctx, turn, parent, child, frozen)
	}
	// Unlike a native Feature Start, Handoff may route to an independent
	// child Runner. Its own observed outcome, not a fabricated local Kernel
	// snapshot, is the only evidence that its StartOrLoad finished.
	grant, err := runner.beginPhysicalInvocationDispatch(ctx, child, "handoff-start", delegation.ChildRunID)
	if err != nil {
		return handoff.Delegation{}, err
	}
	var started handoff.Delegation
	var startErr error
	if native := runner.nativeLocalDelegation(frozen); native != nil {
		_, outcome, e := native.ObservePhysicalDispatch(withoutContextCheckpoint(ctx), child.ExecutionRefID, plugin.RunStart, grant.ID, func(c context.Context) (kernel.Snapshot, error) {
			started, startErr = runner.handoffs.StartOrLoad(c, parent, delegation)
			return kernel.Snapshot{}, startErr
		})
		startErr = e
		if started.ID == "" && agent.IsNativeStartNoEffect(startErr) {
			if e = runner.finishPhysicalDispatchGrant(ctx, grant, "native_agent_start_no_effect"); e != nil {
				return handoff.Delegation{}, errors.Join(ErrExecutionOutcomeUnknown, startErr, e)
			}
			return handoff.Delegation{}, dispatchProvenNoEffect{cause: startErr}
		}
		if !outcome.Matches(child.ExecutionRefID, plugin.RunStart) || outcome.Kind() == agent.PhysicalCallCreated {
			return handoff.Delegation{}, errors.Join(ErrExecutionOutcomeUnknown, startErr)
		}
	} else {
		started, startErr = runner.handoffs.StartOrLoad(withoutContextCheckpoint(ctx), parent, delegation)
	}
	if !sameHandoffFrozenExecution(frozen, started) ||
		(started.Status != handoff.StatusRunning && !terminalDelegationStatus(started.Status)) {
		// The call may already have reached an external ChildRunner. Keep
		// the open grant/Attempt for explicit, read-only reconciliation.
		return handoff.Delegation{}, errors.Join(ErrExecutionOutcomeUnknown, startErr)
	}
	if receiptErr := runner.finishPhysicalDispatchGrant(ctx, grant, "handoff_start_returned"); receiptErr != nil {
		return started, errors.Join(ErrExecutionOutcomeUnknown, startErr, receiptErr)
	}
	return started, startErr
}

// Compare the exact persisted execution envelope, excluding only product
// outcome fields. JSON serialization is the same canonicalization used at
// admission (including ordered slices, omitempty and RawMessage normalization).
// New fields are immutable by default; invalid returned JSON fails closed.
func sameHandoffFrozenExecution(expected, returned handoff.Delegation) bool {
	a, err := json.Marshal(frozenDelegationExecution(expected))
	b, otherErr := json.Marshal(frozenDelegationExecution(returned))
	return err == nil && otherErr == nil && bytes.Equal(a, b)
}

func (runner *Runner) projectDelegationRelations(ctx context.Context, turn Turn) error {
	invocation, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if err != nil {
		return err
	}
	items, err := listAllItems(ctx, runner.store, turn.ID)
	if err != nil {
		return err
	}
	projected := make(map[string]struct{})
	for _, item := range items {
		if item.Kind != ItemDelegation {
			continue
		}
		if item.Status == ItemWaiting {
			continue
		}
		var payload delegationItemPayload
		if !decodeTypedAuthorityJSON(item.Payload, &payload) {
			return ErrConflict
		}
		if _, exists := projected[payload.DelegationID]; exists {
			continue
		}
		if err = runner.ensureDelegationRelation(ctx, firstNonEmpty(payload.ParentRunID, invocation.ExecutionRefID), handoff.Delegation{
			ID: payload.DelegationID, ChildRunID: payload.ChildRunID,
		}); err != nil {
			return err
		}
		projected[payload.DelegationID] = struct{}{}
	}
	return nil
}

func (runner *Runner) ensureDelegationRelation(ctx context.Context, parentRunID string, delegation handoff.Delegation) error {
	_, err := runner.relations.Ensure(ctx, runrelation.Draft{
		ParentRunID: parentRunID, ChildRunID: delegation.ChildRunID,
		Kind: runrelation.KindDelegation, OwnerNodeID: delegation.ID,
	})
	return err
}

func (runner *Runner) recordDelegationItem(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	delegation handoff.Delegation,
	status ItemStatus,
	parentItemID string,
	originalCallHash ...string,
) (string, error) {
	id, _, err := runner.recordDelegationItemWithAdmission(ctx, turn, invocation, delegation, status, parentItemID, originalCallHash...)
	return id, err
}

// A trusted waiting-child continuation may dispatch only the first Store
// writer of its original stable Started Item; duplicate admission is not
// a physical execution grant.
func (runner *Runner) recordDelegationItemWithAdmission(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	delegation handoff.Delegation,
	status ItemStatus,
	parentItemID string,
	originalCallHash ...string,
) (string, bool, error) {
	// Only the original Tool Handler writes a model-arguments hash to a
	// Waiting Item. Older or synthetic records lack this admission proof
	// and cannot be resumed by the new autonomous continuation.
	callHash := ""
	if status == ItemWaiting && len(originalCallHash) == 1 {
		callHash = originalCallHash[0]
	}
	payload, err := json.Marshal(delegationItemPayload{
		DelegationID: delegation.ID, MemberID: delegation.MemberID, MemberRevision: delegation.MemberRevision, ChildRunID: delegation.ChildRunID,
		Goal: delegation.Goal, Status: delegation.Status, Result: append(json.RawMessage(nil), delegation.Result...),
		ParentRunID: invocation.ExecutionRefID,
		Execution:   frozenDelegationExecution(delegation), OriginalCallHash: callHash,
	})
	if err != nil {
		return "", false, err
	}
	itemID := stableID("hid", turn.ID, delegation.ID, string(status))
	now := runner.clock.Now().UTC()
	_, created, err := appendItemFactWithAdmission(ctx, runner.store, runner.turnFeed, Item{
		ID: itemID, TurnID: turn.ID, Kind: ItemDelegation, Status: status,
		RunID: delegation.ChildRunID, InvocationID: invocation.ID, ParentItemID: parentItemID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return itemID, created, err
}

func delegationItemStatus(status handoff.Status) ItemStatus {
	switch status {
	case handoff.StatusCompleted:
		return ItemCompleted
	case handoff.StatusFailed:
		return ItemFailed
	case handoff.StatusCancelled:
		return ItemCancelled
	case handoff.StatusQueued, handoff.StatusRunning:
		return ItemStarted
	default:
		return ItemStarted
	}
}
