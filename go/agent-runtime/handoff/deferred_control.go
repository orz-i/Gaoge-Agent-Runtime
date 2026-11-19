package handoff

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// SupportsDeferredAgent describes composition, not execution or cancellation
// authority. It is used only to distinguish a statically bound deferred Agent
// from a legacy, potentially effectful eager ChildRunner before observing the
// frozen Delegation record.
func (c *Coordinator) SupportsDeferredAgent(rt *kernel.Runtime) bool {
	if c == nil || rt == nil {
		return false
	}
	if c.deferredAgent != nil {
		return c.deferredAgent.ObservesKernelRuntime(rt)
	}
	if r, ok := c.resolver.(*deferredAgentRoutes); ok {
		return r.original.ObservesKernelRuntime(rt)
	}
	return false
}

// IsDeferredLocalDelegation is purely a route classification for an already
// authenticated original frozen Delegation. Only that exact static first-party
// route may demand the settled deferred callback before terminal projection.
func (c *Coordinator) IsDeferredLocalDelegation(rt *kernel.Runtime, d Delegation) bool {
	if c == nil || rt == nil || !validDelegation(d) {
		return false
	}
	if c.deferredAgent != nil {
		return c.deferredAgent.ObservesKernelRuntime(rt)
	}
	if bound := c.localDeferredAgent(d); bound != nil {
		return bound.ObservesKernelRuntime(rt)
	}
	return false
}

// VerifiedLocalDelegationControl is a per-original-Delegation physical custody
// proof, separate from global Kernel ownership and read-only observation.
// The caller must first authenticate the original frozen Harness Delegation,
// RunRelation and physical snapshot. Arbitrary NewRouted/custom/remote owners
// never borrow this first-party native Root retirement capability.
func (c *Coordinator) VerifiedLocalDelegationControl(
	ctx context.Context, rt *kernel.Runtime, original Delegation, child kernel.Snapshot,
) bool {
	if c == nil || rt == nil || !c.ObservesLocalDelegation(rt, original) ||
		child.Run.Kind != agent.RunKind {
		return false
	}
	if c.OwnsLocalDelegation(rt, original) {
		return true // Existing native static ownership without Host middleware.
	}
	if routed := c.localDeferredAgent(original); routed != nil {
		return routed.VerifiedDeferredRootControl(ctx, rt, child)
	}
	return c.deferredAgent != nil &&
		c.deferredAgent.VerifiedDeferredRootControl(ctx, rt, child)
}
