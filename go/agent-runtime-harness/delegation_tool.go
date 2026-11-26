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

func (handler *DelegationToolHandler) Execute(
	ctx context.Context,
	request tools.ExecutionRequest,
) (tools.ExecutionResult, error) {
	if handler == nil || strings.TrimSpace(request.RunID) == "" || strings.TrimSpace(request.Call.ID) == "" ||
		request.Call.ToolKey != DelegationToolKey || !json.Valid(request.Call.Arguments) {
		return tools.ExecutionResult{}, tools.ErrInvalidCall
	}
	handler.mu.RLock()
	runner := handler.runner
	handler.mu.RUnlock()
	if runner == nil {
		return tools.ExecutionResult{}, ErrDelegationToolUnbound
	}
	var input DelegateRequest
	if err := json.Unmarshal(request.Call.Arguments, &input); err != nil {
		return tools.RecoverableNoEffectResult(request.Call.ID, tools.NewRecoverableCallError(
			"delegation.invalid_input", "invalid delegation input", err))
	}
	input, err := handler.prepareRequest(ctx, runner, request, input)
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
	modelResult, err := runner.delegationModelResult(ctx, result.Delegation)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	content, err := json.Marshal(struct {
		DelegationID string          `json:"delegationID"`
		ChildRunID   string          `json:"childRunID"`
		Status       string          `json:"status"`
		Result       json.RawMessage `json:"result,omitempty"`
		ErrorCode    string          `json:"errorCode,omitempty"`
	}{
		DelegationID: result.Delegation.ID,
		ChildRunID:   result.Delegation.ChildRunID,
		Status:       string(result.Delegation.Status),
		Result:       modelResult,
		ErrorCode:    delegationReceiptErrorCode(result.Delegation.Status),
	})
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	disposition := "committed"
	if result.Delegation.Status == handoff.StatusQueued || result.Delegation.Status == handoff.StatusRunning {
		disposition = tools.ReceiptDispositionPending
	}
	var pending *tools.PendingExecution
	if disposition == tools.ReceiptDispositionPending {
		pending, err = runner.localDelegationWait(ctx, result.Delegation)
		if err != nil {
			return tools.ExecutionResult{}, err
		}
	}
	return tools.ExecutionResult{
		Content: content,
		Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: disposition},
		Pending: pending,
	}, nil
}

// VerifyCommittedTool is a trusted, READ-ONLY reconciliation port for an
// already dispatched delegation. It consumes the exact Harness Delegation Item
// owned by the frozen parent Call and the current child Kernel Run; it never
// invokes Delegate/StartOrLoad, Host policy, or another Tool Handler.
func (handler *DelegationToolHandler) VerifyCommittedTool(
	ctx context.Context, request tools.ExecutionRequest,
) (tools.ExecutionResult, error) {
	if handler == nil || strings.TrimSpace(request.RunID) == "" ||
		strings.TrimSpace(request.Call.ID) == "" || request.Call.ToolKey != DelegationToolKey ||
		!json.Valid(request.Call.Arguments) {
		return tools.ExecutionResult{}, tools.ErrInvalidCall
	}
	handler.mu.RLock()
	runner := handler.runner
	handler.mu.RUnlock()
	if runner == nil {
		return tools.ExecutionResult{}, ErrDelegationToolUnbound
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
	child, frozen, err := runner.delegationObservation(ctx, stored.ChildRunID)
	if err != nil {
		return tools.ExecutionResult{}, errors.Join(agent.ErrToolOutcomeUnknown, err)
	}
	owner, err := runner.store.GetInvocationByExecutionRefID(ctx, stored.ChildRunID)
	if err != nil || owner.ParentItemID != startedID || frozen.ID != delegationID {
		return tools.ExecutionResult{}, errors.Join(agent.ErrToolOutcomeUnknown, err)
	}
	// The original fixed route and frozen task, not a same-ID local Run,
	// authorize this read-only terminal receipt. Never consult a dynamic or
	// remote route's coincidentally present local Kernel twin.
	if !terminalRuntimeStatus(child.Run.Status) {
		return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
	}
	status := handoff.Status(child.Run.Status)
	delegation := frozen
	delegation.Status, delegation.ErrorCode = status, child.Run.ErrorCode
	if child.Result != nil {
		delegation.Result = append(json.RawMessage(nil), child.Result.Content...)
	}
	modelResult, err := runner.delegationModelResult(ctx, delegation)
	if err != nil {
		return tools.ExecutionResult{}, err
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
		Status:       string(status), Result: modelResult,
		ErrorCode: delegationReceiptErrorCode(status),
	})
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	return tools.ExecutionResult{
		Content: content,
		Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"},
	}, nil
}

// localDelegationObservation is read-only route authority shared by Tool
// recovery, autonomous waits and native-result projection. A persisted child
// ID (even with matching public metadata) does not establish Kernel residence.
func (runner *Runner) localDelegationObservation(ctx context.Context, runID string) (kernel.Snapshot, handoff.Delegation, error) {
	coordinator, ok := runner.handoffs.(*handoff.Coordinator)
	if !ok || !coordinator.SupportsLocalDelegationObservation(runner.runtime) {
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
	if !runner.observesLocalChild(ctx, coordinator, frozen, child) {
		return kernel.Snapshot{}, handoff.Delegation{}, ErrHandoffCancellationUnverified
	}
	return child, frozen, nil
}

// The caller has authenticated the original frozen Handoff and child. A
// completed result needs the executing callbacks' acknowledged custody even
// when the reader has Host approval hooks and cannot take physical control.
func (runner *Runner) observesLocalChild(ctx context.Context, coordinator *handoff.Coordinator, frozen handoff.Delegation, child kernel.Snapshot) bool {
	rt := runner.runtime
	if (!coordinator.ObservesLocalKernelRunner(rt) && child.Run.Kind != agent.RunKind) || !coordinator.ObservesLocalDelegation(rt, frozen) {
		return false
	}
	if !terminalRuntimeStatus(child.Run.Status) || !coordinator.IsDeferredLocalDelegation(rt, frozen) {
		return true
	}
	if child.Run.Status == kernel.RunStatusCompleted {
		return agent.TerminalPhysicalEffectsSettled(child) && (rt.VerifyOwnedRootSettlement(ctx, child) == nil ||
			coordinator.VerifiedLocalToolResult(ctx, rt, frozen, child))
	}
	return coordinator.VerifiedLocalDelegationControl(ctx, rt, frozen, child)
}

// remoteDelegationObservation checks the frozen Harness authority, exact
// first-party A2A route and already committed local shadow. The provider
// outcome verifier itself does not refresh the remote task or mutate Kernel.
func (runner *Runner) remoteDelegationObservation(ctx context.Context, runID string) (kernel.Snapshot, handoff.Delegation, error) {
	coordinator, ok := runner.handoffs.(*handoff.Coordinator)
	if !ok {
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
	if child.Run.Kind != handoff.RemoteAgentRunKind {
		return kernel.Snapshot{}, handoff.Delegation{}, ErrHandoffCancellationUnverified
	}
	frozen, _, err := runner.localHandoffCancellationAuthority(ctx, owner, child)
	if err != nil || !coordinator.ObservesRemoteShadow(ctx, runner.runtime, frozen, child) {
		return kernel.Snapshot{}, handoff.Delegation{}, errors.Join(ErrHandoffCancellationUnverified, err)
	}
	return child, frozen, nil
}

// delegationObservation requires a physical, immutable, exact-route proof for
// either the locally prepared Agent or the first-party A2A shadow. A foreign
// same-ID Run may not impersonate either.
func (runner *Runner) delegationObservation(ctx context.Context, runID string) (kernel.Snapshot, handoff.Delegation, error) {
	child, frozen, err := runner.localDelegationObservation(ctx, runID)
	if !errors.Is(err, ErrHandoffCancellationUnverified) {
		return child, frozen, err
	}
	return runner.remoteDelegationObservation(ctx, runID)
}

// A local Agent's terminal output includes full native search receipts for
// auditing. Forward the actual answer, citations and artifact references to
// its supervisor, plus receipt identities, rather than copying whole web pages
// into the parent context. The exact receipts remain on the child Run.
func (runner *Runner) delegationModelResult(ctx context.Context, delegation handoff.Delegation) (json.RawMessage, error) {
	raw := append(json.RawMessage(nil), delegation.Result...)
	if delegation.Status != handoff.StatusCompleted || len(raw) == 0 || raw[0] != '{' {
		return raw, nil
	}
	child, _, err := runner.localDelegationObservation(ctx, delegation.ChildRunID)
	if errors.Is(err, ErrHandoffCancellationUnverified) || errors.Is(err, kernel.ErrNotFound) {
		return raw, nil // Remote output must not borrow a local twin's type.
	}
	if err != nil {
		return nil, err
	}
	if child.Run.Kind != agent.RunKind {
		return raw, nil
	}
	var result agent.Result
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	for index := range result.HostedToolCalls {
		call := &result.HostedToolCalls[index]
		call.Input, call.Output, call.Error = nil, nil, nil
	}
	return json.Marshal(result)
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

// Only the local SDK Agent has an autonomous committed-transition contract.
// Custom/remote runners retain their existing scheduled observation behavior.
func (runner *Runner) localDelegationWait(ctx context.Context, delegation handoff.Delegation) (*tools.PendingExecution, error) {
	if delegation.Status == handoff.StatusQueued && runner.nativeLocalDelegation(delegation) != nil {
		// The actual original handler just committed its frozen Waiting
		// selection. The child Run may not yet exist; this wait does not
		// grant dispatch and keeps the parent's original Tool identity.
		return &tools.PendingExecution{Kind: tools.PendingChildTerminal, OperationID: delegation.ID, ChildRunID: delegation.ChildRunID}, nil
	}
	child, frozen, err := runner.delegationObservation(ctx, delegation.ChildRunID)
	if errors.Is(err, kernel.ErrNotFound) || errors.Is(err, ErrHandoffCancellationUnverified) {
		return nil, nil // No local autonomous wait for an unproven route.
	}
	if err != nil {
		return nil, err
	}
	if (child.Run.Kind != agent.RunKind && child.Run.Kind != handoff.RemoteAgentRunKind) || !sameHandoffFrozenExecution(frozen, delegation) {
		return nil, nil
	}
	return &tools.PendingExecution{
		Kind: tools.PendingChildTerminal, OperationID: delegation.ID, ChildRunID: delegation.ChildRunID,
	}, nil
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
	delegationID := delegationToolID(invocation, request.Call.ID)
	// There are at most two original frozen candidates for this Tool call:
	// Waiting (before physical dispatch) and Started (after admission).
	// The old full-Turn scan chose the earlier Item by Seq. Preserve that
	// ordering when both are present; all unrelated business history can be
	// ignored without granting any new physical execution authority.
	var first Item
	found := false
	for _, status := range []ItemStatus{ItemStarted, ItemWaiting} {
		id := stableID("hid", invocation.TurnID, delegationID, string(status))
		item, itemErr := runner.store.GetItem(ctx, id)
		if errors.Is(itemErr, ErrNotFound) {
			continue
		}
		if itemErr != nil {
			return DelegateRequest{}, false, itemErr
		}
		if item.ID != id || item.TurnID != invocation.TurnID || item.Seq == 0 ||
			item.Kind != ItemDelegation || item.Status != status ||
			item.InvocationID != invocation.ID {
			return DelegateRequest{}, false, ErrConflict
		}
		if found && item.Seq == first.Seq {
			return DelegateRequest{}, false, ErrConflict
		}
		if !found || item.Seq < first.Seq {
			first, found = item, true
		}
	}
	if !found {
		return DelegateRequest{}, false, nil
	}
	var payload delegationItemPayload
	if !decodeTypedAuthorityJSON(first.Payload, &payload) {
		return DelegateRequest{}, false, ErrConflict
	}
	return DelegateRequest{MemberID: payload.MemberID, MemberRevision: payload.MemberRevision, Goal: payload.Goal, callID: request.Call.ID}, true, nil
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
