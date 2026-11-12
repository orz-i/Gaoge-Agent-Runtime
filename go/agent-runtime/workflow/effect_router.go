package workflow

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

var (
	ErrInvalidEffectRouter = errors.New("invalid workflow effect router")
	ErrEffectUnavailable   = errors.New("workflow effect class is unavailable")
	ErrEffectForbidden     = errors.New("workflow effect is forbidden")
)

// EffectAuthorization is the declarative, redacted input to a host policy gate.
type EffectAuthorization struct {
	RunID               string
	Actor               string
	Tenant              string
	DefinitionID        string
	DefinitionHash      string
	NodeID              string
	Class               EffectClass
	Kind                string
	Revision            string
	RequiredPermissions []string
	CostClass           CostClass
	MaxCostUnits        int64
	SideEffectClass     SideEffectClass
	Compensation        bool
}

// EffectAuthorizer runs before a statically selected executor receives an intent.
type EffectAuthorizer interface {
	AuthorizeEffect(context.Context, EffectAuthorization) error
}

// EffectRoute binds one closed effect class to one executor at composition time.
type EffectRoute struct {
	Class    EffectClass
	Executor EffectExecutor
}

// LocalAgentTaskOwner is an opt-in proof from the original, statically
// composed AgentTask executor. A bare EffectExecutor or a matching Run ID
// must never be promoted into local execution/approval authority.
// The host must return the actual fixed Agent and original relation recorder.
type LocalAgentTaskOwner interface {
	LocalAgentTaskRunner() *agent.Runner
	LocalAgentTaskRecorder() runrelation.Recorder
	// VerifyAgentTaskOrigin binds the frozen Host-specific input/route to
	// the concrete child created by this executor. It must be pure, without
	// new calls, authorization grants or process-local recovery state.
	VerifyAgentTaskOrigin(Effect, kernel.Snapshot) bool
}

// LocalAgentTaskApprovalCancellationOwner is a strictly scoped Host
// attestation: every physical approval for this AgentTask must be admitted
// through Harness's original-Store per-child dispatch grant and compete
// atomically with Harness's cancellation intent. It does NOT certify arbitrary
// running Model/Tool/middleware effects and cannot replace Kernel CAS.
// Routes without this opt-in remain fail closed for Host-prepared Agents.
type LocalAgentTaskApprovalCancellationOwner interface {
	FencesAgentTaskApprovalThroughHarness() bool
}

// StaticEffectRouter contains an immutable, duplicate-free class route table.
type StaticEffectRouter struct {
	routes     map[EffectClass]EffectExecutor
	authorizer EffectAuthorizer
	// Capture once at composition; mutable executor fields cannot replace
	// either original authority after a child has been dispatched.
	agentTaskOriginal       *agent.Runner
	agentTaskRecorder       runrelation.Recorder
	agentTaskApprovalFenced bool
}

// NewStaticEffectRouter constructs a closed effect composition root.
func NewStaticEffectRouter(authorizer EffectAuthorizer, routes ...EffectRoute) (*StaticEffectRouter, error) {
	if len(routes) == 0 {
		return nil, ErrInvalidEffectRouter
	}
	router := &StaticEffectRouter{
		routes: make(map[EffectClass]EffectExecutor, len(routes)), authorizer: authorizer,
	}
	for _, route := range routes {
		route.Class = EffectClass(strings.TrimSpace(string(route.Class)))
		if !validEffectClass(route.Class) || route.Executor == nil {
			return nil, ErrInvalidEffectRouter
		}
		if _, duplicate := router.routes[route.Class]; duplicate {
			return nil, ErrInvalidEffectRouter
		}
		router.routes[route.Class] = route.Executor
		if route.Class == EffectClassAgent {
			if owner, ok := route.Executor.(LocalAgentTaskOwner); ok {
				router.agentTaskOriginal = owner.LocalAgentTaskRunner()
				router.agentTaskRecorder = owner.LocalAgentTaskRecorder()
				if fence, ok := route.Executor.(LocalAgentTaskApprovalCancellationOwner); ok {
					router.agentTaskApprovalFenced = fence.FencesAgentTaskApprovalThroughHarness()
				}
			}
		}
	}
	return router, nil
}

// localAgentTaskOwner accepts only the original immutable class route,
// whose executor explicitly attests to the same relation recorder. A custom
// effect router, dynamically resolved Agent or foreign recorder fails closed.
func (router *StaticEffectRouter) localAgentTaskOwner(recorder runrelation.Recorder) LocalAgentTaskOwner {
	if router == nil || recorder == nil || router.agentTaskOriginal == nil ||
		!runrelation.SameRecorder(router.agentTaskRecorder, recorder) {
		return nil
	}
	owner, ok := router.routes[EffectClassAgent].(LocalAgentTaskOwner)
	if !ok || owner.LocalAgentTaskRunner() != router.agentTaskOriginal ||
		!runrelation.SameRecorder(owner.LocalAgentTaskRecorder(), router.agentTaskRecorder) {
		return nil
	}
	return owner
}

// localAgentTaskWaitingCancellationFenced rechecks the original captured
// control authority. A later mutable Host field cannot grant this opt-in.
func (router *StaticEffectRouter) localAgentTaskWaitingCancellationFenced(recorder runrelation.Recorder) bool {
	owner := router.localAgentTaskOwner(recorder)
	if owner == nil || !router.agentTaskApprovalFenced {
		return false
	}
	fence, ok := owner.(LocalAgentTaskApprovalCancellationOwner)
	return ok && fence.FencesAgentTaskApprovalThroughHarness()
}

// Execute authorizes and dispatches without dynamic service discovery.
func (router *StaticEffectRouter) Execute(
	ctx context.Context,
	request EffectRequest,
) (EffectResult, error) {
	if router == nil {
		return EffectResult{}, ErrEffectUnavailable
	}
	request.Class = EffectClass(strings.TrimSpace(string(request.Class)))
	executor, exists := router.routes[request.Class]
	if !exists {
		return EffectResult{}, ErrEffectUnavailable
	}
	if router.authorizer != nil {
		authorization := EffectAuthorization{
			RunID: request.RunID, Actor: request.Actor.ActorID, Tenant: request.Actor.TenantID,
			DefinitionID: request.DefinitionID, DefinitionHash: request.DefinitionHash,
			NodeID: request.NodeID, Class: request.Class, Kind: request.Kind, Revision: request.Revision,
			RequiredPermissions: append([]string(nil), request.Policy.RequiredPermissions...),
			CostClass:           request.Policy.CostClass, MaxCostUnits: request.MaxCostUnits,
			SideEffectClass: request.Policy.SideEffectClass, Compensation: request.Compensation,
		}
		if err := router.authorizer.AuthorizeEffect(ctx, authorization); err != nil {
			return EffectResult{}, errors.Join(ErrEffectForbidden, err)
		}
	}
	return executor.Execute(ctx, cloneEffectRequest(request))
}

func validEffectClass(class EffectClass) bool {
	switch class {
	case EffectClassGeneric, EffectClassAgent, EffectClassApplication, EffectClassMedia,
		EffectClassSubworkflow, EffectClassCompensation:
		return true
	default:
		return false
	}
}

func cloneEffectRequest(request EffectRequest) EffectRequest {
	request.Definition = cloneDefinitionReference(request.Definition)
	request.Input = cloneJSON(request.Input)
	request.Policy = cloneDefinitionPolicy(request.Policy)
	return request
}
