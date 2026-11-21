package agent

import (
	"context"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Account for each approval invocation with its exact committed Tool call or
// the persisted checkpoint binding that the next execution must consume.
type approvedToolCalls struct {
	mu       sync.Mutex
	pending  map[string]int
	recorded bool
}

type delegatedToolReceipt struct {
	Call         tools.Call `json:"call"`
	ResultDigest string     `json:"resultDigest"`
}

func (a *approvedToolCalls) enter(call tools.Call) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending == nil {
		a.pending = make(map[string]int)
	}
	a.pending[kernel.ManagedDigest(call)]++
}

func (a *approvedToolCalls) bind(call tools.Call) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := kernel.ManagedDigest(call)
	if a.pending[key] != 1 {
		return false
	}
	delete(a.pending, key)
	a.recorded = true
	return true
}

func (a *approvedToolCalls) committed(call tools.Call, result tools.ExecutionResult, approvedCheckpoint bool) (delegatedToolReceipt, bool) {
	if result.Receipt.ExecutionID != call.ID || result.Receipt.Disposition != "committed" || len(result.Receipt.OwnerReceiptDigest) != 64 ||
		result.Pending != nil || tools.ValidateExecutionResult(result) != nil || !a.bind(call) && !approvedCheckpoint {
		return delegatedToolReceipt{}, false
	}
	return delegatedToolReceipt{Call: tools.CloneCall(call), ResultDigest: kernel.ManagedDigest(result)}, true
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
	return err == nil && len(state.DelegatedToolReceipts) > 0 && r.verifyToolHistory(ctx, child.Run.ID, state)
}

func (r *Runner) verifyToolHistory(ctx context.Context, runID string, state runState) bool {
	if r.delegatedToolResults == nil || len(state.DelegatedToolReceipts) != state.Budget.Usage.ToolCalls {
		return false
	}
	seen := make(map[string]bool, len(state.DelegatedToolReceipts))
	for _, receipt := range state.DelegatedToolReceipts {
		if receipt.Call.ID == "" || seen[receipt.Call.ID] || len(receipt.ResultDigest) != 64 {
			return false
		}
		seen[receipt.Call.ID] = true
		result, err := r.delegatedToolResults.VerifyCommittedTool(ctx, tools.ExecutionRequest{RunID: runID, Call: tools.CloneCall(receipt.Call)})
		if err != nil || result.Receipt.ExecutionID != receipt.Call.ID || result.Receipt.Disposition != "committed" ||
			result.Pending != nil || tools.ValidateExecutionResult(result) != nil || kernel.ManagedDigest(result) != receipt.ResultDigest {
			return false
		}
	}
	return true
}
