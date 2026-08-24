package workflow

import (
	"context"
	"errors"
	"strings"
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

// StaticEffectRouter contains an immutable, duplicate-free class route table.
type StaticEffectRouter struct {
	routes     map[EffectClass]EffectExecutor
	authorizer EffectAuthorizer
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
	}
	return router, nil
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
