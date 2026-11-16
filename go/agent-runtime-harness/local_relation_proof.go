package harness

import (
	"context"
	"errors"
	"fmt"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// Matching a local Run ID is never sufficient execution ownership.
// Native Agent and delegated ChildRunner receipts must identify the original
// frozen Invocation, Attempt and physical executor before approval/control.
// Approval is physical Agent continuation, not merely a same-Kernel read.
// Native Invocation owners use their configured Agent; delegated/raw Feature
// children must retain the original fixed Agent object and all its hooks.
func (runner *Runner) verifyLocalApprovalRun(ctx context.Context, candidate kernel.Snapshot) error {
	if err := runner.verifyLocalRelationRun(ctx, candidate, false); err != nil {
		return err
	}
	if err := runner.requireLiveApprovalAncestors(ctx, candidate.Run.ID); err != nil {
		return err
	}
	owner, err := runner.store.GetInvocationByExecutionRefID(ctx, candidate.Run.ID)
	if err != nil {
		return err
	}
	if !isHandoffDelegationInvocation(owner) {
		return nil
	}
	original, ok := runner.agent.(*agent.Runner)
	coordinator, bound := runner.handoffs.(*handoff.Coordinator)
	frozen, _, proofErr := runner.localHandoffCancellationAuthority(ctx, owner, candidate)
	if !ok || !bound || proofErr != nil ||
		(!coordinator.ObservesLocalKernelRunner(runner.runtime) && candidate.Run.Kind != agent.RunKind) ||
		!coordinator.UsesLocalAgentForDelegation(runner.runtime, original, frozen) {
		return ErrRuntimeCancellationUnverified
	}
	return nil
}

// requireLiveApprovalAncestors separates read-only descendant ownership from
// permission to create a new physical approval/Tool dispatch. A parent
// Agent may already be cancelled or failed in Kernel while
// its Harness Turn projection is stale. No terminal parent can mint a fresh
// child approval, but cancellation cleanup can still traverse that ancestry.
func (runner *Runner) requireLiveApprovalAncestors(ctx context.Context, childRunID string) error {
	if runner == nil || runner.runtime == nil || childRunID == "" {
		return ErrRuntimeCancellationUnverified
	}
	if runner.relationReader == nil {
		// Without an indexed relation, the original native Invocation proof
		// is the only authorization route. The child cannot invent an edge.
		return nil
	}
	seen := map[string]struct{}{childRunID: {}}
	for current := childRunID; ; {
		relation, err := runner.relationReader.GetByChild(ctx, current)
		if errors.Is(err, runrelation.ErrNotFound) {
			// Absence of an index is only a root boundary. A physical
			// descendant, or an independently routed Handoff child whose
			// parent is frozen in its Invocation, must not silently become
			// an approval root if its indexed relation is missing.
			physical, loadErr := runner.runtime.Load(ctx, current)
			if loadErr != nil || physical.Run.ID != current {
				return errors.Join(ErrRuntimeCancellationUnverified, loadErr)
			}
			if physical.Run.ParentRunID != "" {
				return ErrRuntimeCancellationUnverified
			}
			if runner.store != nil {
				owner, ownerErr := runner.store.GetInvocationByExecutionRefID(ctx, current)
				if ownerErr == nil && owner.ParentItemID != "" {
					return ErrRuntimeCancellationUnverified
				}
				if ownerErr != nil && !errors.Is(ownerErr, ErrNotFound) {
					return errors.Join(ErrRuntimeCancellationUnverified, ownerErr)
				}
			}
			return nil
		}
		if err != nil || relation.ChildRunID != current || relation.ParentRunID == "" {
			return errors.Join(ErrRuntimeCancellationUnverified, err)
		}
		// Only retained delegation/capability edges can extend authority.
		// An obsolete or unknown composite relation must not authorize work.
		if relation.Kind != runrelation.KindCapability && relation.Kind != runrelation.KindDelegation {
			return ErrRuntimeCancellationUnverified
		}
		if _, cycle := seen[relation.ParentRunID]; cycle {
			return ErrRuntimeCancellationUnverified
		}
		seen[relation.ParentRunID] = struct{}{}
		child, loadErr := runner.runtime.Load(ctx, current)
		if loadErr != nil || child.Run.ID != current ||
			(child.Run.ParentRunID != "" && child.Run.ParentRunID != relation.ParentRunID) {
			return errors.Join(ErrRuntimeCancellationUnverified, loadErr)
		}
		parent, loadErr := runner.runtime.Load(ctx, relation.ParentRunID)
		if loadErr != nil || parent.Run.ID != relation.ParentRunID {
			return errors.Join(ErrRuntimeCancellationUnverified, loadErr)
		}
		switch parent.Run.Status {
		case kernel.RunStatusRunning, kernel.RunStatusWaitingInput, kernel.RunStatusPausedBudget:
			// The next ancestor may have been terminated independently. Keep
			// walking; a live immediate parent alone is never sufficient.
		case kernel.RunStatusCompleted, kernel.RunStatusFailed, kernel.RunStatusCancelled:
			return ErrRuntimeCancellationUnverified
		default:
			return ErrRuntimeCancellationUnverified
		}
		current = relation.ParentRunID
	}
}

// verifyCapabilityRelationOwner binds an immutable KindCapability edge to
// the first-party native child Invocation that originally minted it. A valid
// Run from a different Turn (including its valid root Invocation) cannot
// borrow this edge to expose Subtask results or receive physical control.
// Physical Run identity is separately verified by verifyLocalRelationChain.
func (runner *Runner) verifyCapabilityRelationOwner(
	ctx context.Context, relation runrelation.Relation, child Invocation,
) error {
	if runner == nil || runner.store == nil || relation.Kind != runrelation.KindCapability ||
		child.ParentItemID == "" || child.Attempt <= 0 ||
		!validExecutionClass(child.ExecutionClass) || isHandoffDelegationInvocation(child) ||
		child.ExecutionRefID == "" || relation.ChildRunID != child.ExecutionRefID ||
		relation.OwnerNodeID != invocationRelationOwnerID(child) {
		return ErrConflict
	}
	binding, err := frozenChildRoot(child)
	if err != nil || relation.ParentRunID == "" || relation.ParentRunID != binding.ExecutionRefID {
		return ErrConflict
	}
	root, err := runner.store.GetInvocation(ctx, binding.InvocationID)
	if err != nil || root.ID != binding.InvocationID || root.TurnID != child.TurnID ||
		root.ParentItemID != "" || !validExecutionClass(root.ExecutionClass) ||
		invocationAttempt(root) != binding.attempt() {
		return ErrConflict
	}
	return nil
}

func (runner *Runner) verifyLocalRelationRun(ctx context.Context, candidate kernel.Snapshot, control bool) error {
	return runner.verifyLocalRelationChain(ctx, candidate, control, make(map[string]bool))
}

func (runner *Runner) verifyLocalRelationChain(ctx context.Context, candidate kernel.Snapshot, control bool, seen map[string]bool) error {
	unverified := func() error {
		return fmt.Errorf("Run %q has no verified local execution owner: %w", candidate.Run.ID, ErrRuntimeCancellationUnverified)
	}
	if candidate.Run.ID == "" || seen[candidate.Run.ID] {
		return unverified()
	}
	seen[candidate.Run.ID] = true
	var relation runrelation.Relation
	relationErr := runrelation.ErrNotFound
	if runner.relationReader != nil {
		relation, relationErr = runner.relationReader.GetByChild(ctx, candidate.Run.ID)
	}
	if relationErr != nil && !errors.Is(relationErr, runrelation.ErrNotFound) {
		return errors.Join(unverified(), relationErr)
	}
	owner, err := runner.store.GetInvocationByExecutionRefID(ctx, candidate.Run.ID)
	if err != nil {
		return errors.Join(unverified(), err)
	}
	// A relation is only an index. It cannot grant a foreign parent control
	// over an otherwise legitimate Run merely by naming its ID. Only the
	// original frozen child Invocation and Handoff/Capability receipt can
	// authenticate an incoming edge.
	if relationErr == nil {
		switch relation.Kind {
		case runrelation.KindCapability:
			if proofErr := runner.verifyCapabilityRelationOwner(ctx, relation, owner); proofErr != nil {
				return errors.Join(unverified(), proofErr)
			}
		case runrelation.KindDelegation:
			if !isHandoffDelegationInvocation(owner) {
				return unverified()
			}
		default:
			return unverified()
		}
	}
	if isHandoffDelegationInvocation(owner) {
		coordinator, ok := runner.handoffs.(*handoff.Coordinator)
		if !ok {
			return unverified()
		}
		frozen, parent, proofErr := runner.localHandoffCancellationAuthority(ctx, owner, candidate)
		if proofErr != nil {
			return unverified()
		}
		switch candidate.Run.Kind {
		case handoff.RemoteAgentRunKind:
			if !coordinator.ObservesRemoteShadow(ctx, runner.runtime, frozen, candidate) ||
				(control && !runner.remoteHandoffCancellerOwnsDelegation(ctx, frozen, candidate)) {
				return unverified()
			}
		case agent.RunKind:
			if !coordinator.ObservesLocalDelegation(runner.runtime, frozen) ||
				(control && (!coordinator.OwnsLocalDelegation(runner.runtime, frozen) ||
					!runner.localRelationCancellerOwnsKernel(agent.RunKind))) {
				return unverified()
			}
		default:
			return unverified()
		}
		if relationErr == nil && (relation.Kind != runrelation.KindDelegation ||
			relation.ChildRunID != candidate.Run.ID || relation.ParentRunID != parent.ID ||
			relation.OwnerNodeID != frozen.ID) {
			return unverified()
		}
	} else {
		var proofErr error
		if owner.ParentItemID == "" {
			proofErr = validateRootRuntimeOwnership(owner, candidate)
		} else {
			proofErr = runner.validateChildRuntimeOwnership(ctx, owner, candidate)
		}
		if proofErr != nil {
			return errors.Join(unverified(), proofErr)
		}
	}
	return nil
}
