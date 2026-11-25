package agent

import (
	"context"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Account for each approval invocation with its exact committed Tool call or
// the persisted checkpoint binding that the next execution must consume.
type approvedToolCalls struct {
	mu       sync.Mutex
	pending  map[string]int
	allowed  map[string]string
	returns  map[string]approvalReturn
	recorded bool
}

type delegatedToolReceipt struct {
	Call           tools.Call `json:"call"`
	ResultDigest   string     `json:"resultDigest"`
	ApprovalDigest string     `json:"approvalDigest,omitempty"`
}

// One approval source, recorded by the actual executing frame. The exact
// committed owner result is optional until a held call is reconciled.
type approvalReturn struct {
	ApprovalDigest    string
	OwnerResultDigest string
	PendingKind       string
}

func approvalCallKey(call tools.Call) string {
	seal, err := jsoncontract.SealOpaque("agent-tool-approval-call-v1", call)
	if err != nil {
		return ""
	}
	return seal.Digest
}

func approvalSourceDigest(q tools.ExecutionRequest, checkpoint *kernel.Checkpoint) string {
	source := "not-required"
	if checkpoint != nil {
		source = "approved-checkpoint"
	}
	seal, err := jsoncontract.SealOpaque("agent-tool-approval-source-v1", struct {
		Source     string
		Request    tools.ExecutionRequest
		Checkpoint *kernel.Checkpoint
	}{source, q, checkpoint})
	if err != nil {
		return ""
	}
	return seal.Digest
}

func qualifiedOwnerToolReceipt(call tools.Call, result tools.ExecutionResult, approval string) (delegatedToolReceipt, bool) {
	if !validBindingFingerprint(approval) || result.Receipt.ExecutionID != call.ID ||
		result.Receipt.Disposition != "committed" || !validBindingFingerprint(result.Receipt.OwnerReceiptDigest) ||
		result.Pending != nil || tools.ValidateExecutionResult(result) != nil {
		return delegatedToolReceipt{}, false
	}
	raw, err := encodeToolOutcome(newToolOutcomeReceipt(result, nil))
	if err != nil {
		return delegatedToolReceipt{}, false
	}
	return delegatedToolReceipt{Call: tools.CloneCall(call), ResultDigest: approvalHash(raw), ApprovalDigest: approval}, true
}

func (a *approvedToolCalls) enter(call tools.Call) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending == nil {
		a.pending = make(map[string]int)
	}
	a.pending[approvalCallKey(call)]++
}

func (a *approvedToolCalls) bind(call tools.Call) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := approvalCallKey(call)
	if a.pending[key] != 1 {
		return false
	}
	delete(a.pending, key)
	delete(a.allowed, key)
	a.recorded = true
	return true
}

// allowed is populated only after the actual policy set has returned a
// successful not-required decision for this invocation.
func (a *approvedToolCalls) authorize(q tools.ExecutionRequest) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := approvalCallKey(q.Call)
	if key == "" || a.pending[key] != 1 {
		return
	}
	if a.allowed == nil {
		a.allowed = make(map[string]string)
	}
	a.allowed[key] = approvalSourceDigest(q, nil)
}

func (a *approvedToolCalls) source(s kernel.Snapshot, state runState, call tools.Call) string {
	if approvedPendingCall(s, state) {
		pending, ok := nextPendingCall(state)
		if ok && approvalCallKey(pending) == approvalCallKey(call) {
			return approvalSourceDigest(tools.ExecutionRequest{RunID: s.Run.ID, Call: call}, s.Checkpoint)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.allowed[approvalCallKey(call)]
}

func (a *approvedToolCalls) recordReturn(call tools.Call, proof approvalReturn, approvedCheckpoint bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := approvalCallKey(call)
	if key == "" || !validBindingFingerprint(proof.ApprovalDigest) {
		return false
	}
	if a.pending[key] != 1 && !approvedCheckpoint {
		return false
	}
	if !approvedCheckpoint && a.allowed[key] != proof.ApprovalDigest {
		return false
	}
	if a.returns == nil {
		a.returns = make(map[string]approvalReturn)
	}
	if old, ok := a.returns[key]; ok && old != proof {
		return false
	}
	a.returns[key] = proof
	delete(a.pending, key)
	delete(a.allowed, key)
	a.recorded = true
	return true
}

func (a *approvedToolCalls) committed(s kernel.Snapshot, state runState, call tools.Call, result tools.ExecutionResult) (delegatedToolReceipt, bool) {
	receipt, ok := qualifiedOwnerToolReceipt(call, result, a.source(s, state, call))
	if !ok || !a.recordReturn(call, approvalReturn{ApprovalDigest: receipt.ApprovalDigest, OwnerResultDigest: receipt.ResultDigest}, approvedPendingCall(s, state)) {
		return delegatedToolReceipt{}, false
	}
	return receipt, true
}

// pendingReturn captures the actual qualified return before its state CAS.
// The descriptor selects only the wait transport, never approval custody.
func (a *approvedToolCalls) pendingReturn(s kernel.Snapshot, state runState, call tools.Call, result tools.ExecutionResult) {
	kind := qualifiedPendingKind(result, nil, call.ID)
	if kind == "" {
		return
	}
	proof := approvalReturn{ApprovalDigest: a.source(s, state, call), PendingKind: kind}
	a.recordReturn(call, proof, approvedPendingCall(s, state))
}

func (a *approvedToolCalls) returned(call tools.Call) (approvalReturn, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.returns[approvalCallKey(call)]
	return v, ok
}

func (a *approvedToolCalls) allBound() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.recorded && len(a.pending) == 0
}

// VerifyDeferredToolResults is read-only. The original callbacks' native
// returns must be acknowledged and every consumed Tool must still match its
// independent owner. Neither this evidence nor an observable result authorizes
// cancellation, retry, retirement or another dispatch.
func (r *Runner) VerifyDeferredToolResults(ctx context.Context, rt *kernel.Runtime, child kernel.Snapshot) bool {
	if r == nil || rt == nil || r.runtime != rt || r.delegatedToolResults == nil ||
		child.Run.Status != kernel.RunStatusCompleted || !TerminalPhysicalEffectsSettled(child) ||
		rt.VerifyCompletedToolReturn(ctx, child) != nil {
		return false
	}
	state, err := decodeState(child.State)
	return err == nil && len(state.RecoveredTools) == 0 && len(state.DelegatedToolReceipts) > 0 && r.verifyToolHistory(ctx, child.Run.ID, state)
}

func (r *Runner) verifyToolHistory(ctx context.Context, runID string, state runState) bool {
	if r.delegatedToolResults == nil || len(state.DelegatedToolReceipts) != state.Budget.Usage.ToolCalls {
		return false
	}
	seen := make(map[string]bool, len(state.DelegatedToolReceipts)+len(state.RecoveredTools))
	verify := func(receipt delegatedToolReceipt) bool {
		if receipt.Call.ID == "" || seen[receipt.Call.ID] || !validBindingFingerprint(receipt.ResultDigest) {
			return false
		}
		seen[receipt.Call.ID] = true
		result, err := r.delegatedToolResults.VerifyCommittedTool(ctx, tools.ExecutionRequest{RunID: runID, Call: tools.CloneCall(receipt.Call)})
		if err != nil || result.Receipt.ExecutionID != receipt.Call.ID || result.Receipt.Disposition != "committed" ||
			result.Pending != nil || tools.ValidateExecutionResult(result) != nil {
			return false
		}
		if receipt.ApprovalDigest != "" {
			qualified, ok := qualifiedOwnerToolReceipt(receipt.Call, result, receipt.ApprovalDigest)
			return ok && qualified.ResultDigest == receipt.ResultDigest
		}
		return kernel.ManagedDigest(result) == receipt.ResultDigest
	}
	for _, receipt := range state.DelegatedToolReceipts {
		if !verify(receipt) {
			return false
		}
	}
	// A receipt-only recovery may already have retired its physical grant,
	// while the business loop has not incremented local usage. These staged
	// physical facts must independently qualify too; a zero usage count is
	// not proof that there are no original Tool obligations.
	for _, staged := range state.RecoveredTools {
		receipt, ok := qualifiedOwnerToolReceipt(staged.Call, staged.Result, staged.ApprovalDigest)
		if staged.Rejection != nil || !ok || !verify(receipt) {
			return false
		}
	}
	return true
}
