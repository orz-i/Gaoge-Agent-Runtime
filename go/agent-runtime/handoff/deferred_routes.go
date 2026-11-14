package handoff

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// DelegationRoute is a fixed composition choice, not a persisted execution fact.
type DelegationRoute uint8

const (
	DelegationRouteLocal DelegationRoute = iota + 1
	DelegationRouteRemote
)

// DelegationRouteSelector must be pure and stable for the frozen Delegation,
// including after reconstruction. It must not admit budgets, resolve mutable
// directories, or perform effects. The configuring Host is trusted to uphold
// this contract. The input is an isolated copy, never the caller's record.
type DelegationRouteSelector func(Delegation) (DelegationRoute, error)

type deferredAgentChild struct{ original *agent.Runner }

func (c deferredAgentChild) StartRun(ctx context.Context, r agent.StartRequest) (kernel.Snapshot, error) {
	return c.original.PrepareRun(ctx, r)
}
func (c deferredAgentChild) LoadRun(ctx context.Context, id string) (kernel.Snapshot, error) {
	return c.original.LoadRun(ctx, id)
}

type deferredAgentRoutes struct {
	original *agent.Runner
	classify DelegationRouteSelector
	remote   ChildRunnerResolver
}

func (r *deferredAgentRoutes) route(d Delegation) (DelegationRoute, error) {
	kind, err := r.classify(cloneDelegation(d))
	if err != nil {
		return 0, err
	}
	if kind != DelegationRouteLocal && kind != DelegationRouteRemote {
		return 0, ErrChildUnavailable
	}
	return kind, nil
}
func (r *deferredAgentRoutes) ResolveChild(ctx context.Context, d Delegation) (ChildRunner, error) {
	kind, err := r.route(d)
	if err != nil {
		return nil, err
	}
	if kind == DelegationRouteLocal {
		return deferredAgentChild{original: r.original}, nil
	}
	if r.remote == nil {
		return nil, ErrChildUnavailable
	}
	return r.remote.ResolveChild(ctx, cloneDelegation(d))
}

// NewDeferredAgentWithRouting retains one concrete local Agent and an explicit
// remote resolver. All three global ownership queries remain false. Only the
// per-Delegation queries may prove a local route; the remote resolver is never
// consulted by those queries, even if it returns a same-Kernel runner.
func NewDeferredAgentWithRouting(original *agent.Runner, classify DelegationRouteSelector, remote ChildRunnerResolver) (*Coordinator, error) {
	if original == nil || classify == nil {
		return nil, ErrInvalidDelegation
	}
	return &Coordinator{resolver: &deferredAgentRoutes{original: original, classify: classify, remote: remote}}, nil
}
func (c *Coordinator) localDeferredAgent(d Delegation) *agent.Runner {
	if c == nil || !validDelegation(d) {
		return nil
	}
	r, ok := c.resolver.(*deferredAgentRoutes)
	if !ok {
		return nil
	}
	kind, err := r.route(d)
	if err != nil || kind != DelegationRouteLocal {
		return nil
	}
	return r.original
}
func (c *Coordinator) ObservesLocalDelegation(rt *kernel.Runtime, d Delegation) bool {
	if original := c.localDeferredAgent(d); original != nil {
		return original.ObservesKernelRuntime(rt)
	}
	return c.ObservesLocalKernelRunner(rt)
}
func (c *Coordinator) UsesLocalAgentForDelegation(rt *kernel.Runtime, original *agent.Runner, d Delegation) bool {
	if bound := c.localDeferredAgent(d); bound != nil {
		return bound == original && bound.ObservesKernelRuntime(rt)
	}
	return c.UsesLocalAgentRunner(rt, original)
}
func (c *Coordinator) OwnsLocalDelegation(rt *kernel.Runtime, d Delegation) bool {
	if original := c.localDeferredAgent(d); original != nil {
		return original.OwnsKernelRuntime(rt)
	}
	return c.OwnsLocalKernelRunner(rt)
}

// SupportsLocalDelegationObservation only says this composition can attempt a
// per-Delegation local proof. It authorizes no Run, projection, continuation or
// cancellation. Callers must still validate the persisted frozen source and
// use the corresponding per-Delegation proof. Legacy NewRouted stays false.
func (c *Coordinator) SupportsLocalDelegationObservation(rt *kernel.Runtime) bool {
	if c == nil || rt == nil {
		return false
	}
	if r, ok := c.resolver.(*deferredAgentRoutes); ok {
		return r.original.ObservesKernelRuntime(rt)
	}
	return c.ObservesLocalKernelRunner(rt)
}
