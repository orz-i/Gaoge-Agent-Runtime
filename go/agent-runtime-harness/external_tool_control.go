package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

type externalToolCancellationControlKey struct{}

// ContinueExternalToolCancellation resumes only the original bound external
// owner control. The Host must first consume each original no-effect/committed
// receipt. Ordinary Cancel/CancelSubtask cannot finish a different bound control.
// The target is the original elected Root or scoped Subtask Run, never a sibling.
func (r *Runner) ContinueExternalToolCancellation(ctx context.Context, turnID, targetRunID, controlID, reason string) (Snapshot, error) {
	if ctx == nil || controlID == "" {
		return Snapshot{}, ErrInvalidRequest
	}
	turn, err := loadVerifiedTurnIdentity(ctx, r.store, turnID)
	if err != nil {
		return Snapshot{}, err
	}
	if _, err = r.verifyExternalCancellationElection(ctx, turn, targetRunID); err != nil {
		return Snapshot{}, err
	}
	root, err := loadTopLevelInvocation(ctx, r.store, turn.ID)
	if err != nil {
		return Snapshot{}, err
	}
	ctx = context.WithValue(ctx, externalToolCancellationControlKey{}, controlID)
	if targetRunID == root.ExecutionRefID {
		return r.Cancel(ctx, turn.ID, reason)
	}
	items, err := listAllItems(ctx, r.store, turn.ID)
	if err != nil {
		return Snapshot{}, err
	}
	tasks, err := r.loadSubtasks(ctx, turn, items, nil)
	if err != nil {
		return Snapshot{}, err
	}
	for _, task := range tasks {
		if task.RunID == targetRunID {
			return r.CancelSubtask(ctx, turn.ID, task.ID, reason)
		}
	}
	return Snapshot{}, ErrNotFound
}

// VerifyExternalToolControlScope verifies the original cancellation and frozen
// delegation chain. For native accounting it also durably binds the original
// owner ControlID once, without calling the owner or changing quota. Retries
// must retain that same control. targetRunID is the elected subtree target.
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
	election, err := r.verifyExternalCancellationElection(ctx, turn, targetRunID)
	if err != nil {
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
			if r.nativeBudget() {
				current, err := r.runtime.Load(ctx, c.RunID)
				if err != nil {
					return err
				}
				retained, err := original.ElectExternalToolCancellation(ctx, c.RunID, current.Run.Revision, election.ID, targetRunID)
				if err != nil {
					return err
				}
				return original.BindExternalToolCancellationControl(ctx, retained.Run.Revision, election.ID, targetRunID, c)
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
	if err != nil || !coordinator.UsesLocalAgentForDelegation(r.runtime, original, d) {
		return "", errors.Join(ErrRuntimeCancellationUnverified, err)
	}
	relation, err := r.relationReader.GetByChild(ctx, s.Run.ID)
	if err != nil || relation.Kind != runrelation.KindDelegation || relation.ParentRunID != parent.ID || relation.ChildRunID != s.Run.ID || relation.OwnerNodeID != d.ID {
		return "", errors.Join(ErrConflict, err)
	}
	return parent.ID, nil
}

func (r *Runner) verifyExternalCancellationElection(ctx context.Context, turn Turn, target string) (Item, error) {
	items, err := r.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if err != nil {
		return Item{}, err
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
			return Item{}, ErrConflict
		}
		elected = item
		count++
	}
	if count != 1 {
		return Item{}, ErrConflict
	}
	ledger, err := r.loadTurnBudget(ctx, turn)
	if err != nil || ledger != nil && !ledger.RunCancelled(target) && !r.nativeBudget() {
		return Item{}, errors.Join(ErrConflict, err)
	}
	return elected, nil
}

// This is a read-only native return proof, not an external owner operation or
// a budget mutation. The physical Grant and exact pending call must still be
// present. A live/opaque callback cannot preserve a receipt-recovery election.
func (r *Runner) nativeExternalCancellationWait(ctx context.Context, runID, electionID string) error {
	native, ok := r.agent.(*agent.Runner)
	if !ok || !native.ObservesKernelRuntime(r.runtime) {
		return ErrRuntimeCancellationUnverified
	}
	if present, err := native.VerifyExternalToolCancellationElection(ctx, runID, electionID, runID); present || err != nil {
		return err
	}
	s, err := r.runtime.Load(ctx, runID)
	if err != nil {
		return err
	}
	call, waiting, err := agent.FrozenExternalToolCall(s)
	if err != nil || !waiting {
		return errors.Join(ErrConflict, err)
	}
	wait, err := native.QueryPendingTool(ctx, runID, call.ID)
	if err != nil {
		return err
	}
	return native.VerifyExternalToolControlWait(ctx, s.Run.Revision, agent.ExternalToolControl{
		Actor: s.Run.Actor, RunID: runID, CallID: call.ID,
		OperationID: wait.OperationID, InputDigest: wait.InputDigest, ControlID: electionID,
	})
}
