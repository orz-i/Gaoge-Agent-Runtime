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
	eager    bool // standard Session preserves native eager execution
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
		if r.eager {
			return r.original, nil
		}
		return deferredAgentChild{original: r.original}, nil
	}
	if r.remote == nil {
		return nil, ErrChildUnavailable
	}
	child, err := r.remote.ResolveChild(ctx, cloneDelegation(d))
	if err != nil {
		return nil, err
	}
	if r.eager {
		remote, ok := child.(RemoteShadowObserver)
		if !ok || !r.original.ObservesKernelRuntime(remote.RemoteRuntime()) {
			return nil, ErrChildRunUnverified
		}
	}
	return child, nil
}

// NewAgentWithRemote is the standard AgentSession route: native children
// use the original eager Agent loop, and remote children use the same frozen
// executor for Start, Observe and Cancel. It requires no continuation worker.
func NewAgentWithRemote(original *agent.Runner, remote ChildRunnerResolver) (*Coordinator, error) {
	c, err := NewDeferredAgentWithRemote(original, remote)
	if err != nil {
		return nil, err
	}
	c.resolver.(*deferredAgentRoutes).eager = true
	return c, nil
}

// OwnsStandardRemoteRoutes distinguishes the standard, same-Kernel
// lifecycle from advanced Host-routed/deferred compositions.
func (c *Coordinator) OwnsStandardRemoteRoutes(rt *kernel.Runtime) bool {
	if c == nil || rt == nil {
		return false
	}
	r, ok := c.resolver.(*deferredAgentRoutes)
	return ok && r.eager && r.original.ObservesKernelRuntime(rt)
}

// NewDeferredAgentWithRemote routes the Host-frozen Remote bit through the
// same prepared local Agent and remote ChildRunner. No additional mutable
// product selector can override an admitted member's physical ownership.
func NewDeferredAgentWithRemote(original *agent.Runner, remote ChildRunnerResolver) (*Coordinator, error) {
	if remote == nil {
		return nil, ErrInvalidDelegation
	}
	return NewDeferredAgentWithRouting(original, func(delegation Delegation) (DelegationRoute, error) {
		if delegation.Remote {
			return DelegationRouteRemote, nil
		}
		return DelegationRouteLocal, nil
	}, remote)
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

// ObservesRemoteShadow validates a previously admitted A2A shadow without
// issuing a remote GET or mutating Kernel state. The original frozen route
// must resolve to an explicit first-party observer; untyped/custom routes
// cannot borrow this authority even when their Run metadata matches.
func (c *Coordinator) ObservesRemoteShadow(
	ctx context.Context, rt *kernel.Runtime, d Delegation, child kernel.Snapshot,
) bool {
	if c == nil || rt == nil || !c.ObservesRemoteDelegation(d) {
		return false
	}
	r, ok := c.resolver.(*deferredAgentRoutes)
	if !ok || r.remote == nil {
		return false
	}
	resolved, err := r.ResolveChild(ctx, cloneDelegation(d))
	if err != nil {
		return false
	}
	observer, ok := resolved.(RemoteShadowObserver)
	return ok && observer.RemoteRuntime() == rt && observer.MatchesRemoteDelegation(d) &&
		observer.ObservesFrozenRemoteShadow(rt, cloneDelegation(d), child)
}

// CancelRemoteDelegation controls only the exact standard route and committed
// original shadow. The same executor that owns Start and Observe owns Cancel.
func (c *Coordinator) CancelRemoteDelegation(ctx context.Context, rt *kernel.Runtime,
	d Delegation, parent kernel.Run, current kernel.Snapshot) (kernel.Snapshot, error) {
	if !c.OwnsStandardRemoteRoutes(rt) || !VerifiedChildRun(d, parent, current) {
		return kernel.Snapshot{}, ErrChildRunUnverified
	}
	child, err := c.resolveChild(ctx, d)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	remote, ok := child.(RemoteShadowObserver)
	if !ok || remote.RemoteRuntime() != rt || !remote.ObservesFrozenRemoteShadow(rt, d, current) {
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

// ObservesRemoteDelegation attests only that this statically composed deferred
// route selects its original remote ChildRunner for the frozen Delegation.
// It neither grants execution nor certifies a terminal result. Callers must
// independently verify the parent, child Run and original Harness invocation.
func (c *Coordinator) ObservesRemoteDelegation(d Delegation) bool {
	if c == nil || !validDelegation(d) {
		return false
	}
	r, ok := c.resolver.(*deferredAgentRoutes)
	if !ok || r.remote == nil {
		return false
	}
	route, err := r.route(d)
	return err == nil && route == DelegationRouteRemote
}
