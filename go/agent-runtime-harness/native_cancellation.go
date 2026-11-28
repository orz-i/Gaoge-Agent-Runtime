package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Native cancellation uses the original Runtime's control CAS and receipts.
// A Host election is required only where a separate effect owner is involved.
// Missing or opaque ownership evidence never permits bypassing that election.
func (r *Runner) needsHostCancellation(ctx context.Context, turn Turn, runID string) (bool, error) {
	native, ok := r.agent.(*agent.Runner)
	if !ok || !native.ObservesKernelRuntime(r.runtime) || r.cancellation != r.runtime || r.interactions != nil {
		return true, nil
	}
	// Application/opaque owners still use the independent effect contract.
	// They may not have a physical RunRelation or a frozen native child Item.
	existing, err := r.store.ListInvocations(ctx, turn.ID)
	if err != nil {
		return true, err
	}
	for _, owner := range existing {
		if owner.ParentItemID != "" && !isHandoffDelegationInvocation(owner) {
			return true, nil
		}
	}
	owners, targets, affected, err := r.collectCancellationScope(ctx, turn, runID)
	if err != nil {
		return true, err
	}
	items, err := r.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if err != nil {
		return true, err
	}
	if ActiveCancellationFence(items) || PendingExecutionDispatch(items, "") {
		return true, nil
	}
	known := make(map[string]bool)
	for _, owner := range owners {
		if _, selected := targets[owner.ID]; !selected {
			continue
		}
		known[owner.ExecutionRefID] = true
		if owner.ParentItemID != "" {
			d, e := decodeFrozenHandoffDelegation(owner.Input)
			if e != nil || !d.NativeLocal || d.Remote {
				return true, nil
			}
		}
	}
	for _, item := range items {
		if item.Kind != ItemDelegation {
			continue
		}
		var d delegationItemPayload
		if !decodeTypedAuthorityJSON(item.Payload, &d) {
			return true, ErrConflict
		}
		if _, selected := affected[d.ChildRunID]; !selected {
			if _, parent := targets[item.InvocationID]; !parent {
				continue
			}
		}
		if d.Execution == nil || !d.Execution.NativeLocal || d.Execution.Remote {
			return true, nil
		}
		known[d.ChildRunID] = true
		affected[d.ChildRunID] = struct{}{}
	}
	for id := range affected {
		if !known[id] {
			return true, nil
		}
		snapshot, e := r.runtime.Load(ctx, id)
		if errors.Is(e, kernel.ErrNotFound) {
			continue
		}
		if e != nil {
			return true, e
		}
		if snapshot.Run.Kind != agent.RunKind {
			return true, nil
		}
		// A terminal delegated Run with foreign/opaque callback custody is
		// not a proven native return. Retain the independent effect fence.
		if id != runID || snapshot.Run.LogicalParentRunID != "" {
			if terminalRuntimeStatus(snapshot.Run.Status) && !native.VerifiedDeferredRootControl(ctx, r.runtime, snapshot) {
				return true, nil
			}
		}
		_, waiting, e := agent.FrozenExternalToolCall(snapshot)
		if e != nil {
			return true, e
		}
		if waiting {
			return true, nil
		}
	}
	return false, nil
}
