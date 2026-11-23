package harness

import (
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
	if a.ID != "" || a.RequestID != "" || a.ParentRunID != "" || a.OriginFingerprint != "" ||
		a.Actor != (kernel.ActorRef{}) || a.Thread != (kernel.ThreadRef{}) || a.Goal != "" ||
		a.DeadlineAt != nil || a.CallAllowance != nil || a.AutoAllowance != nil ||
		len(a.RequiredToolKeys) != 0 || a.OutputContract != nil || a.MaxModelOutputTokens < 0 || !enforceableHarnessBudgetLimits(a.Limits) {
		return DelegateRequest{}, ErrInvalidRequest
	}
	a.Instructions = strings.TrimSpace(a.Instructions)
	a.Model = strings.TrimSpace(a.Model)
	var err error
	a.ModelOptions, err = canonicalJSON(a.ModelOptions)
	if err != nil {
		return DelegateRequest{}, err
	}
	a.ToolKeys = normalizeStrings(a.ToolKeys)
	a.InitialToolKeys = normalizeStrings(a.InitialToolKeys)
	for _, key := range a.InitialToolKeys {
		if !slices.Contains(a.ToolKeys, key) {
			return DelegateRequest{}, ErrInvalidRequest
		}
	}
	a.HostedToolGrants, err = agent.NormalizeHostedToolGrantsForRole(a.HostedToolGrants)
	if err != nil {
		return DelegateRequest{}, ErrInvalidRequest
	}
	for _, grant := range a.HostedToolGrants {
		if slices.Contains(a.ToolKeys, grant.Key) {
			return DelegateRequest{}, ErrInvalidRequest
		}
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
	if err = runner.admitDelegationChild(ctx, turn, invocation, delegation); err != nil {
		if errors.Is(err, budget.ErrWaiting) {
			// Freeze the requested delegation, but create no child Invocation,
			// relation or Run until lifetime capacity is available. The same
			// logical Tool call is observed again by the scheduled continuation.
			if _, itemErr := runner.recordDelegationItem(ctx, turn, invocation, delegation, ItemWaiting, "", request.callHash); itemErr != nil {
				return DelegationResult{}, itemErr
			}
			snapshot, loadErr := runner.Load(ctx, turn.ID)
			return DelegationResult{Delegation: delegation, Snapshot: snapshot}, loadErr
		}
		return DelegationResult{}, err
	}
	// Ordinary Tool/Manual entry and autonomous Waiting recovery share the
	// same Store-serialized first-writer boundary. A settled physical Grant
	// is not permission to call StartOrLoad again; even a returned Running
	// child could have performed non-idempotent remote work. Reconciliation
	// must use the original read-only receipt path instead.
	startedItemID, admitted, err := runner.recordDelegationItemWithAdmission(ctx, turn, invocation, delegation, ItemStarted, "")
	if err != nil {
		return DelegationResult{}, err
	}
	if !admitted {
		// Do not issue another physical Handoff, but allow a duplicate
		// Manual/API caller to read a fully authenticated *original*
		// terminal result. A merely Running child, absent grant receipt,
		// foreign route, or inconsistent terminal projection is unknown.
		return runner.verifiedTerminalDelegationReplay(ctx, turn, delegation, startedItemID)
	}
	return runner.executeDelegation(ctx, turn, invocation, parent, delegation, startedItemID)
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
	items, err := listAllItems(ctx, runner.store, turn.ID)
	if err != nil || !VerifiedTerminalHandoffChild(root, childOwner, items) {
		return unknown()
	}
	child, physical, err := runner.delegationObservation(ctx, frozen.ChildRunID)
	if err != nil || !sameHandoffFrozenExecution(frozen, physical) ||
		!terminalRuntimeStatus(child.Run.Status) {
		return unknown()
	}
	result := frozen
	result.Status = handoff.Status(child.Run.Status)
	result.ErrorCode = child.Run.ErrorCode
	if child.Result != nil {
		result.Result = append(json.RawMessage(nil), child.Result.Content...)
	}
	// A physical terminal Run and a forged terminal Item must never
	// disagree. The ordinary original-return Item is authoritative only
	// when VerifiedTerminalHandoffChild also verified its unique grant.
	matched := false
	for _, item := range items {
		if item.Kind == ItemDelegation && item.ParentItemID == startedItemID {
			if item.Status != delegationItemStatus(result.Status) {
				return unknown()
			}
			matched = true
		}
	}
	if !matched {
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
	if frozen, found, loadErr := runner.frozenDelegation(ctx, turn.ID, delegationID); found || loadErr != nil {
		return frozen, parent, loadErr
	}
	// The Host chose the named Agent before entering Runtime. Freeze only the
	// resulting execution inputs; Runtime may tighten authority, never resolve
	// names into an independent Role/Tool/Skill policy of its own.
	a := request.Agent
	delegation.MemberRevision = request.MemberRevision
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
	if len(config.DelegationPolicy.NamedAgents) != 0 {
		named, found := namedAgentForMember(config, request.MemberID, request.MemberRevision)
		if !found || a.Model != named.Model || !slices.Equal(normalizeStrings(a.ToolKeys), named.ToolKeys) ||
			!slices.Equal(normalizeStrings(a.InitialToolKeys), named.InitialToolKeys) ||
			!slices.Equal(a.HostedToolGrants, named.HostedToolGrants) || a.Instructions != named.Instructions ||
			a.MaxModelOutputTokens != named.MaxModelOutputTokens || a.Limits != named.Limits ||
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
	return delegation, parent, nil
}

func (runner *Runner) executeDelegation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	parent kernel.Snapshot,
	delegation handoff.Delegation,
	startedItemID string,
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
	delegation, delegateErr := runner.dispatchHandoff(ctx, turn, parent, delegation, startedItemID)
	status := delegationItemStatus(delegation.Status)
	if status != ItemStarted {
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

// reconcileLocalTerminalHandoffEvidence repairs a durable terminal
// ChildRunner observation before the original root's terminal Store CAS.
// It never starts/resumes/cancels the child: only a statically composed
// local ChildRunner with proven Kernel ownership may supply this result.
// Remote or ambiguous routes remain fenced until their own first-party
// terminal Handoff fact is committed.
func (runner *Runner) reconcileLocalTerminalHandoffEvidence(
	ctx context.Context, turn Turn,
) error {
	coordinator, ok := runner.handoffs.(*handoff.Coordinator)
	if !ok || !coordinator.SupportsLocalDelegationObservation(runner.runtime) {
		return nil
	}
	items, err := listAllItems(ctx, runner.store, turn.ID)
	if err != nil {
		return err
	}
	proven := make(map[string]kernel.Snapshot)
	for _, item := range items {
		if item.Kind != ItemDelegation || item.Status != ItemStarted {
			continue
		}
		// A second terminal projection is not permitted to mint an
		// alternate result or mutate the existing immutable receipt.
		hasTerminal := false
		for _, next := range items {
			if next.Kind == ItemDelegation && next.ParentItemID == item.ID &&
				(next.Status == ItemCompleted || next.Status == ItemFailed ||
					next.Status == ItemCancelled) {
				hasTerminal = true
				break
			}
		}
		if hasTerminal {
			continue
		}
		owner, loadErr := runner.store.GetInvocationByExecutionRefID(ctx, item.RunID)
		if errors.Is(loadErr, ErrNotFound) {
			// A frozen Started admission can precede child Invocation creation.
			// This same no-dispatch source is used by Cancel, Retry and Store
			// terminal CAS; no child observation/terminal receipt is invented.
			_, _, valid, proofErr := runner.readNoInvocationDelegationSource(ctx, turn, item.RunID, items)
			if proofErr != nil {
				return proofErr
			}
			if valid {
				continue
			}
		}
		if loadErr != nil || !isHandoffDelegationInvocation(owner) ||
			owner.TurnID != turn.ID || owner.ParentItemID != item.ID {
			return ErrConflict
		}
		child, _, observationErr := runner.delegationObservation(ctx, item.RunID)
		if errors.Is(observationErr, kernel.ErrNotFound) || (!coordinator.ObservesLocalKernelRunner(runner.runtime) && errors.Is(observationErr, ErrHandoffCancellationUnverified)) {
			continue
		}
		if observationErr != nil {
			return observationErr
		}
		if terminalRuntimeStatus(child.Run.Status) {
			proven[item.RunID] = child
		}
	}
	if len(proven) == 0 {
		return nil
	}
	return runner.syncTerminalDelegations(ctx, turn, proven)
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
	// Unlike a native Feature Start, Handoff may route to an independent
	// child Runner. Its own observed outcome, not a fabricated local Kernel
	// snapshot, is the only evidence that its StartOrLoad finished.
	grant, err := runner.beginPhysicalInvocationDispatch(ctx, child, "handoff-start", delegation.ChildRunID)
	if err != nil {
		return handoff.Delegation{}, err
	}
	started, startErr := runner.handoffs.StartOrLoad(withoutContextCheckpoint(ctx), parent, delegation)
	if !sameHandoffFrozenExecution(frozen, started) ||
		(started.Status != handoff.StatusRunning && started.Status != handoff.StatusCompleted &&
			started.Status != handoff.StatusFailed && started.Status != handoff.StatusCancelled) {
		// The call may already have reached an external ChildRunner. Keep
		// the open grant/Attempt for explicit, read-only reconciliation.
		return handoff.Delegation{}, errors.Join(ErrExecutionOutcomeUnknown, startErr)
	}
	if receiptErr := runner.finishPhysicalDispatchGrant(ctx, grant, "handoff_start_returned"); receiptErr != nil {
		return started, errors.Join(ErrExecutionOutcomeUnknown, startErr, receiptErr)
	}
	return started, startErr
}

// A Handoff reply may project only outcome fields. Compare the full frozen
// execution envelope, not a selected identity subset: any future Delegation
// field is immutable by default until explicitly admitted as an outcome.
// It preserves the exact persisted JSON representation and ordered slice
// identity. encoding/json compacts and escapes RawMessage when persisting
// Invocation.Input, so compare its canonical wire representation rather than
// rejecting the original valid request's whitespace or escaping. Empty slices
// and nil are equivalent: Handoff's cloneDelegation uses append(nil, ...)
// and legitimately projects a previously allocated empty ToolKeys slice as nil.
func sameHandoffFrozenExecution(expected, returned handoff.Delegation) bool {
	returned.Status, returned.Result = expected.Status, expected.Result
	returned.ErrorCode, returned.Error = expected.ErrorCode, expected.Error
	if !normalizeHandoffForComparison(&expected) || !normalizeHandoffForComparison(&returned) {
		return false
	}
	return reflect.DeepEqual(expected, returned)
}

func normalizeHandoffForComparison(value *handoff.Delegation) bool {
	if len(value.ModelOptions) == 0 {
		value.ModelOptions = nil
	} else {
		// json.Marshal(Delegation) applied this exact RawMessage
		// normalization at admission. Invalid returned JSON fails closed.
		canonical, err := json.Marshal(value.ModelOptions)
		if err != nil {
			return false
		}
		value.ModelOptions = canonical
	}
	if len(value.ToolKeys) == 0 {
		value.ToolKeys = nil
	}
	if len(value.InitialToolKeys) == 0 {
		value.InitialToolKeys = nil
	}
	if len(value.HostedToolGrants) == 0 {
		value.HostedToolGrants = nil
	}
	return true
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
