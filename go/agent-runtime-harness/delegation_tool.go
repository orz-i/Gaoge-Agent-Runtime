package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const DelegationToolKey = "harness.delegate_agent"

var ErrDelegationToolUnbound = errors.New("harness delegation tool is not bound")

// DelegationToolPolicy applies product-owned, run-scoped delegation
// normalization and constraints before Harness starts or loads a child Agent
// Run. Products may attach immutable host-owned evidence here instead of
// requiring the model to restate it in Tool arguments.
type DelegationToolPolicy interface {
	PrepareDelegation(context.Context, tools.ExecutionRequest, DelegateRequest) (DelegateRequest, error)
}

// DelegationToolHandler is explicitly bound once by the composition root after Runner construction.
// The Tool Registry remains immutable; this is not a dynamic capability registry.
type DelegationToolHandler struct {
	mu     sync.RWMutex
	runner *Runner
}

func NewDelegationToolHandler() *DelegationToolHandler {
	return &DelegationToolHandler{}
}

func (handler *DelegationToolHandler) Bind(runner *Runner) error {
	if handler == nil || runner == nil {
		return ErrInvalidRequest
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.runner != nil && handler.runner != runner {
		return ErrConflict
	}
	handler.runner = runner
	return nil
}

func (handler *DelegationToolHandler) originalRequestOwner(request tools.ExecutionRequest) (*Runner, error) {
	if handler == nil || strings.TrimSpace(request.RunID) == "" || strings.TrimSpace(request.Call.ID) == "" ||
		request.Call.ToolKey != DelegationToolKey || !json.Valid(request.Call.Arguments) {
		return nil, tools.ErrInvalidCall
	}
	handler.mu.RLock()
	defer handler.mu.RUnlock()
	if handler.runner == nil {
		return nil, ErrDelegationToolUnbound
	}
	return handler.runner, nil
}

func (handler *DelegationToolHandler) Execute(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
	runner, err := handler.originalRequestOwner(request)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	var input DelegateRequest
	if err := json.Unmarshal(request.Call.Arguments, &input); err != nil {
		return tools.RecoverableNoEffectResult(request.Call.ID, tools.NewRecoverableCallError(
			"delegation.invalid_input", "invalid delegation input", err))
	}
	input, err = handler.prepareRequest(ctx, runner, request, input)
	if err != nil {
		// The Host policy rejected the delegation *before* a child Run
		// was created or invoked. Only this pre-effect phase may certify
		// a model-correctable, exact-ID no-effect receipt.
		return tools.RecoverableNoEffectResult(request.Call.ID, err)
	}
	result, err := runner.DelegateByExecutionRefID(ctx, request.RunID, input)
	// An independently delegated specialist is an observation, not a mandatory
	// join prerequisite. Its terminal failure is a valid durable Tool receipt:
	// the parent Agent must see the status and decide how to proceed. Preserve
	// hard failures for authorization, invalid state, deadlines and budgets.
	if errors.Is(err, handoff.ErrChildFailed) && result.Snapshot.Turn.ID != "" &&
		(result.Delegation.Status == handoff.StatusFailed || result.Delegation.Status == handoff.StatusCancelled) {
		err = nil
	}
	if err != nil {
		if errors.Is(err, ErrExecutionOutcomeUnknown) {
			return tools.ExecutionResult{}, err
		}
		if code, _, correctable := tools.RecoverableCallErrorInfo(err); correctable &&
			code == "delegation.tool_selection_denied" {
			// This exact authorization denial originates in prepareDelegation,
			// before child admission, relation writes, or physical effect.
			return tools.RecoverableNoEffectResult(request.Call.ID, err)
		}
		if dimension, ok := budget.DeniedDimension(err); ok && dimension == budget.DimensionChildRuns {
			// The child-run coordinator denied admission before another
			// physical child Run was allocated. Preserve a bounded
			// model-correctable refusal with exact CallID no-effect proof.
			return tools.RecoverableNoEffectResult(request.Call.ID, tools.NewRecoverableCallErrorWithBlockedTools(
				"delegation.child_runs_exhausted",
				"No more child runs are available for this task. Continue with the information already collected and finish without further delegation.",
				err,
				DelegationToolKey,
			))
		}
		return tools.ExecutionResult{}, err
	}
	if result.Delegation.NativeLocal && terminalDelegationStatus(result.Delegation.Status) {
		return handler.VerifyCommittedTool(ctx, request)
	}
	return runner.delegationToolReceipt(ctx, request, result.Delegation, "")
}

func terminalDelegationStatus(status handoff.Status) bool {
	return status == handoff.StatusCompleted || status == handoff.StatusFailed || status == handoff.StatusCancelled
}

func (runner *Runner) delegationToolReceipt(ctx context.Context, request tools.ExecutionRequest, delegation handoff.Delegation, digest string) (tools.ExecutionResult, error) {
	result := tools.ExecutionResult{Receipt: tools.Receipt{
		ExecutionID: request.Call.ID, Disposition: "committed", OwnerReceiptDigest: digest,
	}}
	modelResult := append(json.RawMessage(nil), delegation.Result...)
	pending := !terminalDelegationStatus(delegation.Status)
	if pending {
		result.Receipt.Disposition = tools.ReceiptDispositionPending
	}
	// One authenticated observation serves both the neutral wait contract and
	// local result filtering. Opaque results never borrow a local twin's type.
	queued := delegation.Status == handoff.StatusQueued && (delegation.NativeLocal || runner.nativeLocalDelegation(delegation) != nil)
	filter := delegation.Status == handoff.StatusCompleted && len(modelResult) > 0 && modelResult[0] == '{' && !delegation.Remote
	if queued {
		result.Pending = &tools.PendingExecution{Kind: tools.PendingChildTerminal, OperationID: delegation.ID, ChildRunID: delegation.ChildRunID}
	} else if pending || filter {
		child, frozen, err := runner.delegationObservation(ctx, delegation.ChildRunID)
		if err != nil && !errors.Is(err, kernel.ErrNotFound) && !errors.Is(err, ErrHandoffCancellationUnverified) {
			return tools.ExecutionResult{}, err
		}
		if err == nil {
			if pending && sameHandoffFrozenExecution(frozen, delegation) {
				result.Pending = &tools.PendingExecution{Kind: tools.PendingChildTerminal, OperationID: delegation.ID, ChildRunID: delegation.ChildRunID}
			}
			if filter && child.Run.Kind == agent.RunKind {
				var answer agent.Result
				if err := json.Unmarshal(modelResult, &answer); err != nil {
					return tools.ExecutionResult{}, err
				}
				for i := range answer.HostedToolCalls {
					call := &answer.HostedToolCalls[i]
					call.Input, call.Output, call.Error = nil, nil, nil
				}
				modelResult, err = json.Marshal(answer)
				if err != nil {
					return tools.ExecutionResult{}, err
				}
			}
		}
	}
	content, err := json.Marshal(struct {
		DelegationID string          `json:"delegationID"`
		ChildRunID   string          `json:"childRunID"`
		Status       string          `json:"status"`
		Result       json.RawMessage `json:"result,omitempty"`
		ErrorCode    string          `json:"errorCode,omitempty"`
	}{
		DelegationID: delegation.ID,
		ChildRunID:   delegation.ChildRunID,
		Status:       string(delegation.Status),
		Result:       modelResult,
		ErrorCode:    delegationReceiptErrorCode(delegation.Status),
	})
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	result.Content = content
	return result, nil
}

// VerifyCommittedTool is a trusted, READ-ONLY reconciliation port for an
// already dispatched delegation. It consumes the exact Harness Delegation Item
// owned by the frozen parent Call and the current child Kernel Run; it never
// invokes Delegate/StartOrLoad, Host policy, or another Tool Handler.
func (handler *DelegationToolHandler) VerifyCommittedTool(
	ctx context.Context, request tools.ExecutionRequest,
) (tools.ExecutionResult, error) {
	runner, err := handler.originalRequestOwner(request)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	invocation, err := runner.store.GetInvocationByExecutionRefID(ctx, request.RunID)
	if err != nil || invocation.ExecutionRefID != request.RunID {
		return tools.ExecutionResult{}, errors.Join(agent.ErrToolOutcomeUnknown, err)
	}
	delegationID := delegationToolID(invocation, request.Call.ID)
	startedID := stableID("hid", invocation.TurnID, delegationID, string(ItemStarted))
	// This original Tool call has one deterministic Started Item ID. Reading
	// every unrelated Turn Item cannot strengthen its ownership proof.
	item, err := runner.store.GetItem(ctx, startedID)
	if errors.Is(err, ErrNotFound) {
		return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
	}
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	if item.ID != startedID || item.TurnID != invocation.TurnID ||
		item.Seq == 0 || item.Kind != ItemDelegation || item.Status != ItemStarted ||
		item.InvocationID != invocation.ID || item.RunID == "" {
		return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
	}
	var stored delegationItemPayload
	if err = json.Unmarshal(item.Payload, &stored); err != nil ||
		stored.DelegationID != delegationID || stored.ParentRunID != request.RunID ||
		stored.ChildRunID != item.RunID || stored.ChildRunID != stableID("hchild", request.RunID, delegationID) {
		return tools.ExecutionResult{}, errors.Join(agent.ErrToolOutcomeUnknown, err)
	}
	owner, err := verifiedRunInvocation(ctx, runner.store, stored.ChildRunID)
	if err != nil || owner.ParentItemID != startedID || owner.TurnID != invocation.TurnID {
		return tools.ExecutionResult{}, errors.Join(agent.ErrToolOutcomeUnknown, err)
	}
	frozen, err := decodeFrozenHandoffDelegation(owner.Input)
	if err != nil || !matchesDelegationPayload(stored, frozen, request.RunID) {
		return tools.ExecutionResult{}, errors.Join(agent.ErrToolOutcomeUnknown, err)
	}
	delegation, digest, err := runner.terminalDelegationOutcome(ctx, owner, frozen)
	if err != nil {
		return tools.ExecutionResult{}, errors.Join(agent.ErrToolOutcomeUnknown, err)
	}
	return runner.delegationToolReceipt(ctx, request, delegation, digest)
}

// A Tool receipt reads the original native identity or the actual ChildRunner
// result. A missing Run never substitutes a speculative failure/cancellation.
func (r *Runner) terminalDelegationOutcome(ctx context.Context, owner Invocation, d handoff.Delegation) (handoff.Delegation, string, error) {
	child, frozen, e := r.delegationObservation(ctx, owner.ExecutionRefID)
	if errors.Is(e, kernel.ErrNotFound) && d.NativeLocal {
		turn, e := loadVerifiedTurnIdentity(ctx, r.store, owner.TurnID)
		if e != nil {
			return handoff.Delegation{}, "", e
		}
		decision, e := r.nativeChildDecision(ctx, turn, owner)
		if e != nil {
			return handoff.Delegation{}, "", e
		}
		d.Status, d.Error = handoff.Status(decision.Status), decision.Reason
		return d, kernel.ManagedDigest(decision), nil
	}
	if e != nil || !sameHandoffFrozenExecution(d, frozen) || !terminalRuntimeStatus(child.Run.Status) {
		return handoff.Delegation{}, "", errors.Join(ErrExecutionOutcomeUnknown, e)
	}
	d = handoff.ProjectChild(d, child)
	digest := ""
	if d.NativeLocal {
		digest = kernel.ManagedDigest(child)
	}
	return d, digest, nil
}

// localDelegationObservation is read-only route authority shared by Tool
// recovery, autonomous waits and native-result projection. A persisted child
// ID (even with matching public metadata) does not establish Kernel residence.
func (runner *Runner) localDelegationObservation(ctx context.Context, runID string) (kernel.Snapshot, handoff.Delegation, error) {
	return runner.observeDelegation(ctx, runID, false)
}

func (runner *Runner) delegationObservation(ctx context.Context, runID string) (kernel.Snapshot, handoff.Delegation, error) {
	return runner.observeDelegation(ctx, runID, true)
}

// Read the original owner once and select the observer from the frozen route,
// never from a coincidentally matching local or remote RunID.
func (runner *Runner) observeDelegation(ctx context.Context, runID string, allowRemote bool) (kernel.Snapshot, handoff.Delegation, error) {
	coordinator, ok := runner.handoffs.(*handoff.Coordinator)
	if !ok || (!allowRemote && !coordinator.ObservesLocalKernelRunner(runner.runtime)) {
		return kernel.Snapshot{}, handoff.Delegation{}, ErrHandoffCancellationUnverified
	}
	owner, err := runner.store.GetInvocationByExecutionRefID(ctx, runID)
	if err != nil || !isHandoffDelegationInvocation(owner) || owner.ExecutionRefID != runID {
		return kernel.Snapshot{}, handoff.Delegation{}, errors.Join(ErrHandoffCancellationUnverified, err)
	}
	child, err := runner.runtime.Load(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, handoff.Delegation{}, err
	}
	frozen, _, err := runner.localHandoffCancellationAuthority(ctx, owner, child)
	if err != nil {
		return kernel.Snapshot{}, handoff.Delegation{}, err
	}
	if frozen.Remote {
		if !allowRemote || !coordinator.ObservesRemoteShadow(ctx, runner.runtime, frozen, child) {
			return kernel.Snapshot{}, handoff.Delegation{}, ErrHandoffCancellationUnverified
		}
	} else if !runner.observesLocalChild(ctx, owner.TurnID, coordinator, frozen, child) {
		return kernel.Snapshot{}, handoff.Delegation{}, ErrHandoffCancellationUnverified
	}
	return child, frozen, nil
}

// The caller has authenticated the original frozen Handoff and child. A
// completed result needs the executing callbacks' acknowledged custody even
// when the reader has Host approval hooks and cannot take physical control.
func (runner *Runner) observesLocalChild(ctx context.Context, turnID string, coordinator *handoff.Coordinator, frozen handoff.Delegation, child kernel.Snapshot) bool {
	rt := runner.runtime
	if !coordinator.ObservesLocalDelegation(rt, frozen) {
		return false
	}
	if frozen.NativeLocal && terminalRuntimeStatus(child.Run.Status) {
		native, err := runner.nativeHandoff(ctx, turnID, frozen)
		if err != nil || native == nil || !agent.TerminalPhysicalEffectsSettled(child) {
			return false
		}
		if _, e := rt.LookupChildExecution(ctx, child.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
			return false
		}
		receipt, e := rt.LookupOwnedRetirementForRun(ctx, child.Run.ID)
		if e != nil && !errors.Is(e, kernel.ErrNotFound) {
			return false
		}
		if e == nil && receipt.Evidence.Completion.HostCompleted && kernel.ManagedDigest(receipt.Evidence.Snapshot) == kernel.ManagedDigest(child) {
			return true
		}
		return rt.VerifyExecutionCustody(ctx, child, kernel.CustodyRetired) == nil ||
			child.Run.Status == kernel.RunStatusCancelled && (native.VerifiedDeferredRootControl(ctx, rt, child) || errors.Is(e, kernel.ErrNotFound) && child.Run.Revision == 2)
	}
	if !terminalRuntimeStatus(child.Run.Status) || !coordinator.PreparesLocal() {
		return true
	}
	if child.Run.Status == kernel.RunStatusCompleted {
		return agent.TerminalPhysicalEffectsSettled(child) && (rt.VerifyExecutionCustody(ctx, child, kernel.CustodyRetired) == nil ||
			coordinator.VerifiedLocalToolResult(ctx, rt, frozen, child))
	}
	return coordinator.VerifiedLocalDelegationControl(ctx, rt, frozen, child)
}

// Error details from a Child Run are untrusted provider text. Return a
// bounded, stable code to the supervising model instead.
func delegationReceiptErrorCode(status handoff.Status) string {
	switch status {
	case handoff.StatusFailed:
		return "delegation.child_failed"
	case handoff.StatusCancelled:
		return "delegation.child_cancelled"
	case handoff.StatusQueued, handoff.StatusRunning, handoff.StatusCompleted:
		return ""
	default:
		return ""
	}
}

func (handler *DelegationToolHandler) prepareRequest(
	ctx context.Context,
	runner *Runner,
	request tools.ExecutionRequest,
	input DelegateRequest,
) (DelegateRequest, error) {
	prepared, found, err := runner.preparedDelegationToolRequest(ctx, request)
	if err != nil {
		return DelegateRequest{}, err
	}
	if found {
		// This frozen Tool intent was already authorized by the Host on its
		// first dispatch. Recovery must not re-run Host policy.
		prepared.hostAuthorized = true
		return prepared, nil
	}
	input, err = runner.authorizeDelegation(ctx, tools.CloneExecutionRequest(request), input)
	if err != nil {
		return DelegateRequest{}, err
	}
	input.callID = request.Call.ID
	input.callHash = hashInvocationBytes(request.Call.Arguments)
	return input, nil
}

// A pending Tool call reuses its durable, policy-prepared goal after a restart.
// Polling must not consume product delegation budgets or rebuild frozen evidence.
func (runner *Runner) preparedDelegationToolRequest(
	ctx context.Context,
	request tools.ExecutionRequest,
) (DelegateRequest, bool, error) {
	invocation, err := runner.store.GetInvocationByExecutionRefID(ctx, request.RunID)
	if err != nil {
		return DelegateRequest{}, false, err
	}
	payload, found, err := runner.originalDelegationPayload(ctx, invocation.TurnID, delegationToolID(invocation, request.Call.ID), invocation.ID)
	return DelegateRequest{MemberID: payload.MemberID, MemberRevision: payload.MemberRevision, Goal: payload.Goal, callID: request.Call.ID}, found, err
}

// The earliest indexed Waiting/Started selection is the one original
// preparation for both Tool and direct Start. No full Turn scan.
func (runner *Runner) originalDelegationPayload(ctx context.Context, turnID, delegationID, ownerID string) (delegationItemPayload, bool, error) {
	// There are at most two original frozen candidates for this Tool call:
	// Waiting (before physical dispatch) and Started (after admission).
	// The old full-Turn scan chose the earlier Item by Seq. Preserve that
	// ordering when both are present; all unrelated business history can be
	// ignored without granting any new physical execution authority.
	var first Item
	found := false
	for _, status := range []ItemStatus{ItemStarted, ItemWaiting} {
		id := stableID("hid", turnID, delegationID, string(status))
		item, itemErr := runner.store.GetItem(ctx, id)
		if errors.Is(itemErr, ErrNotFound) {
			continue
		}
		if itemErr != nil {
			return delegationItemPayload{}, false, itemErr
		}
		if item.ID != id || item.TurnID != turnID || item.Seq == 0 ||
			item.Kind != ItemDelegation || item.Status != status ||
			(ownerID != "" && item.InvocationID != ownerID) {
			return delegationItemPayload{}, false, ErrConflict
		}
		if found && item.Seq == first.Seq {
			return delegationItemPayload{}, false, ErrConflict
		}
		if !found || item.Seq < first.Seq {
			first, found = item, true
		}
	}
	if !found {
		return delegationItemPayload{}, false, nil
	}
	var payload delegationItemPayload
	if !decodeTypedAuthorityJSON(first.Payload, &payload) {
		return delegationItemPayload{}, false, ErrConflict
	}
	return payload, true, nil
}

func delegationToolID(invocation Invocation, callID string) string {
	if invocation.Attempt > 1 {
		return stableID("hd", invocation.TurnID, invocation.ID, invocation.ExecutionRefID, callID)
	}
	return stableID("hd", invocation.TurnID, invocation.ID, callID)
}

func DelegationToolRegistration(handler *DelegationToolHandler) tools.Registration {
	return tools.Registration{
		Definition: tools.Definition{
			Key:         DelegationToolKey,
			Name:        "delegate_to_specialist_agent",
			Description: "Delegate one focused subtask to an authorized specialist. A failed or cancelled child returns a status receipt; use successful evidence, retry selectively, or explain the limitation instead of assuming the parent task failed.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["memberID","goal"],"properties":{"memberID":{"type":"string","minLength":1,"maxLength":160},"goal":{"type":"string","minLength":1,"maxLength":200000}}}`),
		},
		Handler: handler,
	}
}

func DelegationToolPolicySnapshot() ToolPolicySnapshot {
	return ToolPolicySnapshot{
		Key: DelegationToolKey, DefinitionVersion: harnessToolDefinitionVersion,
		ApprovalCapability: approvalCapabilityPerCall, ApprovalMode: approvalModeNever,
		RiskLevel: toolRiskLevelLow, SideEffectLevel: "compute", IdempotencyMode: toolIdempotencyRequestKey,
	}
}
