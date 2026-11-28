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
		return !r.eager && r.original.ObservesKernelRuntime(rt)
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
		return !c.OwnsStandardRemoteRoutes(rt) && bound.ObservesKernelRuntime(rt)
	}
	return false
}

// VerifiedLocalToolResult consults only the original fixed local route's
// receipt reader. It cannot start, cancel or retire the child.
func (c *Coordinator) VerifiedLocalToolResult(ctx context.Context, rt *kernel.Runtime, d Delegation, child kernel.Snapshot) bool {
	if c == nil || !validDelegation(d) || child.Run.ID != d.ChildRunID {
		return false
	}
	original := c.deferredAgent
	if original == nil {
		original = c.localDeferredAgent(d)
	}
	return original != nil && original.VerifyDeferredToolResults(ctx, rt, child)
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
	// Static route identity cannot establish that a physical callback has
	// retired. Every Deferred Root consumes the same owned Root settlement
	// witness, including middleware-free/native Agent compositions. Preserve
	// the legacy eager ChildRunner route's separate strict Kernel ownership.
	if routed := c.localDeferredAgent(original); routed != nil {
		if c.OwnsStandardRemoteRoutes(rt) && routed.OwnsKernelRuntime(rt) {
			return true
		}
		return routed.VerifiedDeferredRootControl(ctx, rt, child)
	}
	if c.deferredAgent != nil {
		return c.deferredAgent.VerifiedDeferredRootControl(ctx, rt, child)
	}
	if c.OwnsLocalDelegation(rt, original) {
		return true
	}
	// An eager first-party Agent is not globally callback-free. It can
	// nevertheless prove the exact already-returned native execution in
	// the same way as a deferred Agent. Never extend this to custom routes.
	fixed, ok := c.resolver.(staticChildRunnerResolver)
	if !ok {
		return false
	}
	native, ok := fixed.children.(*agent.Runner)
	return ok && native.VerifiedDeferredRootControl(ctx, rt, child)
}
