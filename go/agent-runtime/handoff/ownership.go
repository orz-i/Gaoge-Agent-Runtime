package handoff

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// ObservesLocalDelegation requires the Host-frozen route and fixed local
// executor. A resolver returning a native Agent cannot lend it this authority.
func (c *Coordinator) ObservesLocalDelegation(rt *kernel.Runtime, d Delegation) bool {
	return validDelegation(d) && !d.Remote && c.ObservesLocalKernelRunner(rt)
}

// VerifiedLocalToolResult reads only the original local Agent's outcome proof.
func (c *Coordinator) VerifiedLocalToolResult(ctx context.Context, rt *kernel.Runtime, d Delegation, child kernel.Snapshot) bool {
	original := c.LocalAgent(rt)
	return original != nil && c.ObservesLocalDelegation(rt, d) && child.Run.ID == d.ChildRunID &&
		original.VerifyDeferredToolResults(ctx, rt, child)
}

// VerifiedLocalDelegationControl proves physical custody separately from a
// read-only binding. Preparing a Run does not retire an outstanding callback.
func (c *Coordinator) VerifiedLocalDelegationControl(ctx context.Context, rt *kernel.Runtime, d Delegation, child kernel.Snapshot) bool {
	if !c.ObservesLocalDelegation(rt, d) || child.Run.Kind != agent.RunKind {
		return false
	}
	if native := c.LocalAgent(rt); native != nil {
		return !c.PreparesLocal() && native.OwnsKernelRuntime(rt) || native.VerifiedDeferredRootControl(ctx, rt, child)
	}
	owner, ok := c.config.Local.(LocalKernelChildRunner)
	return ok && owner.OwnsKernelRuntime(rt)
}

// OwnsRemoteRoutes requires the original same-Kernel local/remote composition;
// remote receipt and protocol control are still checked per frozen Delegation.
func (c *Coordinator) OwnsRemoteRoutes(rt *kernel.Runtime) bool {
	return c.LocalAgent(rt) != nil && c.config.Remote != nil
}

// ObservesRemoteShadow authenticates a committed original shadow without
// issuing a protocol GET. This is not permission to start or cancel it.
func (c *Coordinator) ObservesRemoteShadow(ctx context.Context, rt *kernel.Runtime, d Delegation, child kernel.Snapshot) bool {
	if c == nil || rt == nil || !validDelegation(d) || !d.Remote {
		return false
	}
	resolved, err := c.resolveChild(ctx, d)
	if err != nil {
		return false
	}
	observer := resolved.(RemoteShadowObserver) // resolveChild authenticated the remote contract.
	return observer.RemoteRuntime() == rt && observer.ObservesFrozenRemoteShadow(rt, cloneDelegation(d), child)
}

// CancelRemoteDelegation retains the protocol owner's cancellation and exact
// receipt validation; Kernel cancellation cannot settle an external effect.
func (c *Coordinator) CancelRemoteDelegation(ctx context.Context, rt *kernel.Runtime, d Delegation, parent kernel.Run, current kernel.Snapshot) (kernel.Snapshot, error) {
	if !c.OwnsRemoteRoutes(rt) || !VerifiedChildRun(d, parent, current) || !d.Remote {
		return kernel.Snapshot{}, ErrChildRunUnverified
	}
	child, err := c.resolveChild(ctx, d)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	remote := child.(RemoteShadowObserver)
	if remote.RemoteRuntime() != rt || !remote.ObservesFrozenRemoteShadow(rt, d, current) {
		return kernel.Snapshot{}, ErrChildRunUnverified
	}
	next, err := remote.CancelRemote(ctx, current.Run.ID, current.Run.Revision)
	if err != nil {
		return next, err
	}
	if !verifiedRoutedChild(remote, d, parent, next) {
		return kernel.Snapshot{}, ErrChildRunUnverified
	}
	return next, nil
}
