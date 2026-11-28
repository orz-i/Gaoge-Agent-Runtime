package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func (runner *Runner) standardRemoteCoordinator() *handoff.Coordinator {
	c, ok := runner.handoffs.(*handoff.Coordinator)
	native, local := runner.cancellation.(*kernel.Runtime)
	if !ok || !local || native != runner.runtime || !c.OwnsStandardRemoteRoutes(runner.runtime) {
		return nil
	}
	return c
}

// standardDelegationReceiptOwner selects the same original native Agent
// for local eager children and standard remote children. This is not a
// physical grant: the Agent still requires the persisted exact callback
// return and the original child/ledger receipt before any consumption.
func (runner *Runner) standardDelegationReceiptOwner() *agent.Runner {
	original, ok := runner.agent.(*agent.Runner)
	coordinator, bound := runner.handoffs.(*handoff.Coordinator)
	native, local := runner.cancellation.(*kernel.Runtime)
	if !ok || !bound || !local || native != runner.runtime || !original.ObservesKernelRuntime(runner.runtime) {
		return nil
	}
	if coordinator.UsesLocalAgentRunner(runner.runtime, original) || runner.standardRemoteCoordinator() != nil {
		return original
	}
	return nil
}

// observeOriginalRemote is read-only with respect to peer effects: it can
// reconcile an existing Task/session, but cannot issue a new Start or Cancel.
func (runner *Runner) observeOriginalRemote(ctx context.Context, owner Invocation, current kernel.Snapshot) (kernel.Snapshot, error) {
	c := runner.standardRemoteCoordinator()
	if c == nil || !isHandoffDelegationInvocation(owner) || current.Run.Kind != handoff.RemoteAgentRunKind {
		return kernel.Snapshot{}, ErrHandoffCancellationUnverified
	}
	d, parent, err := runner.localHandoffCancellationAuthority(ctx, owner, current)
	if err != nil || !c.ObservesRemoteShadow(ctx, runner.runtime, d, current) {
		return kernel.Snapshot{}, ErrHandoffCancellationUnverified
	}
	if terminalRuntimeStatus(current.Run.Status) {
		return current, nil
	}
	_, err = c.Refresh(ctx, kernel.Snapshot{Run: parent}, d)
	if err != nil && !errors.Is(err, handoff.ErrChildPending) && !errors.Is(err, handoff.ErrChildFailed) {
		return current, err
	}
	observed, err := runner.runtime.Load(ctx, current.Run.ID)
	if err != nil || !handoff.VerifiedChildRun(d, parent, observed) ||
		!c.ObservesRemoteShadow(ctx, runner.runtime, d, observed) {
		return kernel.Snapshot{}, ErrHandoffCancellationUnverified
	}
	return observed, nil
}

// Standard Session observation uses the original immutable Child Invocation,
// never a resolver-selected new goal or replay of its Tool/Start.
func (runner *Runner) refreshStandardRemoteChildren(ctx context.Context, turn Turn) error {
	if runner.standardRemoteCoordinator() == nil {
		return nil
	}
	items, err := runner.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if err != nil {
		return err
	}
	if ActiveCancellationFence(items) {
		return ErrConflict
	}
	children, err := runner.store.ListInvocations(ctx, turn.ID)
	if err != nil {
		return err
	}
	proven := make(map[string]kernel.Snapshot)
	for _, child := range children {
		if !isHandoffDelegationInvocation(child) || terminalInvocationStatus(child.Status) {
			continue
		}
		d, decodeErr := decodeFrozenHandoffDelegation(child.Input)
		if decodeErr != nil {
			return decodeErr
		}
		if !d.Remote || PendingExecutionDispatch(items, child.ID) {
			continue
		}
		current, loadErr := runner.runtime.Load(ctx, child.ExecutionRefID)
		if errors.Is(loadErr, kernel.ErrNotFound) {
			continue
		}
		if loadErr != nil {
			return loadErr
		}
		observed, observeErr := runner.observeOriginalRemote(ctx, child, current)
		if observeErr != nil {
			return observeErr
		}
		if terminalRuntimeStatus(observed.Run.Status) {
			proven[observed.Run.ID] = observed
		}
	}
	if len(proven) != 0 {
		return runner.syncTerminalDelegations(ctx, turn, proven)
	}
	return nil
}

func (runner *Runner) observeStandardRemoteCancellation(ctx context.Context, effect CancellationEffect) (kernel.Snapshot, error) {
	if runner.standardRemoteCoordinator() == nil {
		return kernel.Snapshot{}, ErrCancellationOutcomeUnknown
	}
	owner, err := runner.store.GetInvocationByExecutionRefID(ctx, effect.RunID)
	if err != nil || owner.TurnID != effect.TurnID {
		return kernel.Snapshot{}, ErrCancellationOutcomeUnknown
	}
	current, err := runner.runtime.Load(ctx, effect.RunID)
	if err != nil || !sameCancellationRunOrigin(effect.Before, current.Run) {
		return kernel.Snapshot{}, ErrCancellationOutcomeUnknown
	}
	result, err := runner.observeOriginalRemote(ctx, owner, current)
	if err != nil || !terminalRuntimeStatus(result.Run.Status) {
		return kernel.Snapshot{}, ErrCancellationOutcomeUnknown
	}
	return result, nil
}
