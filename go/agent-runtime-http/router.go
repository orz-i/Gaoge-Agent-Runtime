package http

import "github.com/gin-gonic/gin"

func (module *Module) RegisterRoutes(routes *gin.RouterGroup) {
	if module == nil || module.Handler == nil || routes == nil {
		return
	}
	routes.GET("/runs/:run_id", module.Handler.GetRun)
	routes.GET("/runs/:run_id/feed", module.Handler.StreamRunFeed)
	routes.POST("/runs/:run_id/cancel", module.Handler.CancelRun)
	routes.GET("/runs/:run_id/workbench", module.Handler.GetWorkbench)
	for _, feature := range module.features {
		if feature != nil {
			feature.RegisterRoutes(routes)
		}
	}
}
