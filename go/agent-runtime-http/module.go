package http

import "github.com/gin-gonic/gin"

// RouteModule is the minimal capability-owned HTTP mounting surface.
type RouteModule interface {
	RegisterRoutes(*gin.RouterGroup)
}

type Module struct {
	Handler  *Handler
	features []RouteModule
}

func NewModule(handler *Handler, features ...RouteModule) *Module {
	return &Module{Handler: handler, features: append([]RouteModule(nil), features...)}
}
