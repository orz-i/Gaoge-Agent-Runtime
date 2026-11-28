package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// VerifyExternalToolControlScope reads the already elected native cancellation
// and the original frozen delegation chain. targetRunID is its actual target.
// The external owner must separately bind its own ControlID and operation receipt.
func (r *Runner) VerifyExternalToolControlScope(ctx context.Context, turnID, targetRunID string, c agent.ExternalToolControl) error {
	if r == nil || ctx == nil || r.runtime == nil || r.store == nil || targetRunID == "" || c.ControlID == "" {
		return ErrInvalidRequest
	}
	native, ok := r.cancellation.(*kernel.Runtime)
	original, firstParty := r.agent.(*agent.Runner)
	if !ok || native != r.runtime || !firstParty || !original.ObservesKernelRuntime(r.runtime) {
		return ErrRuntimeCancellationUnverified
	}
	turn, err := r.store.GetTurn(ctx, turnID)
	if err != nil {
		return err
	}
	if err := r.verifyExternalCancellationElection(ctx, turn, targetRunID); err != nil {
		return err
	}
	root, err := loadTopLevelInvocation(ctx, r.store, turn.ID)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for id := c.RunID; id != ""; {
		if seen[id] {
			return ErrConflict
		}
		seen[id] = true
		owner, err := r.store.GetInvocationByExecutionRefID(ctx, id)
		if err != nil || owner.TurnID != turn.ID || owner.ExecutionRefID != id {
			return errors.Join(ErrConflict, err)
		}
		s, err := r.runtime.Load(ctx, id)
		if err != nil || s.Run.Actor != c.Actor || s.Run.Kind != agent.RunKind || s.Run.ParentRunID != "" {
			return errors.Join(ErrConflict, err)
		}
		if owner.ID == root.ID {
			if validateRootRuntimeOwnership(root, s) != nil || !seen[targetRunID] || id == c.RunID && c.ParentRunID != "" {
				return ErrConflict
			}
			return nil
		}
		parentID, err := r.externalToolDelegationParent(ctx, owner, s)
		if err != nil || id == c.RunID && parentID != c.ParentRunID {
			return errors.Join(ErrConflict, err)
		}
		id = parentID
	}
	return ErrConflict
}

func (r *Runner) externalToolDelegationParent(ctx context.Context, owner Invocation, s kernel.Snapshot) (string, error) {
	coordinator, ok := r.handoffs.(*handoff.Coordinator)
	original, local := r.agent.(*agent.Runner)
	if !ok || !local || r.relationReader == nil || !isHandoffDelegationInvocation(owner) {
		return "", ErrRuntimeCancellationUnverified
	}
	d, parent, err := r.localHandoffCancellationAuthority(ctx, owner, s)
	if err != nil || d.Remote || original == nil || coordinator.LocalAgent(r.runtime) != original {
		return "", errors.Join(ErrRuntimeCancellationUnverified, err)
	}
	relation, err := r.relationReader.GetByChild(ctx, s.Run.ID)
	if err != nil || relation.Kind != runrelation.KindDelegation || relation.ParentRunID != parent.ID || relation.ChildRunID != s.Run.ID || relation.OwnerNodeID != d.ID {
		return "", errors.Join(ErrConflict, err)
	}
	return parent.ID, nil
}

func (r *Runner) verifyExternalCancellationElection(ctx context.Context, turn Turn, target string) error {
	items, err := r.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if err != nil {
		return err
	}
	receipts := cancellationFenceReceipts(items)
	count := 0
	var elected Item
	for _, item := range items {
		if item.ParentItemID != CancellationFenceParentItemID || VerifiedCancellationFenceReceipt(item, receipts[item.ID]) {
			continue
		}
		source, ok := cancellationProtocol(item)
		if len(receipts[item.ID]) != 0 || !CancellationEffectsSettled(item, items) || !ok || source != cancellationSource(r.cancellation) || item.RunID != target || item.TurnID != turn.ID || item.Seq == 0 || !ValidCancellationFenceIntentAdmission(item) {
			return ErrConflict
		}
		count++
		elected = item
	}
	if count != 1 {
		return ErrConflict
	}
	ledger, err := r.loadTurnBudget(ctx, turn)
	if err != nil || ledger != nil && !ledger.RunCancelled(target) && !r.nativeBudget() {
		return errors.Join(ErrConflict, err)
	}
	if r.nativeBudget() && (ledger == nil || !ledger.RunCancelled(target)) {
		for _, item := range items {
			if validDeferredChildGrantCancellation(item, elected) {
				return nil
			}
		}
		return ErrConflict
	}
	return nil
}
