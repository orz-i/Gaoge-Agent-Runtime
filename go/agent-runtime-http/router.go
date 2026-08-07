package http

import "github.com/gin-gonic/gin"

func (module *Module) RegisterRoutes(routes *gin.RouterGroup) {
	if module == nil || module.Handler == nil || routes == nil {
		return
	}
	routes.POST("/agent-runs", module.Handler.StartAgentRun)
	routes.POST("/plan-runs", module.Handler.StartPlanRun)
	routes.POST("/plan-runs/:run_id/approval", module.Handler.ResolvePlanApproval)
	routes.POST("/workflow-runs", module.Handler.StartWorkflowRun)
	routes.POST("/workflow-runs/:run_id/wait", module.Handler.ResolveWorkflowWait)
	routes.POST("/team-runs", module.Handler.StartTeamRun)
	routes.GET("/runs/:run_id", module.Handler.GetRun)
	routes.GET("/runs/:run_id/feed", module.Handler.StreamRunFeed)
	routes.POST("/runs/:run_id/cancel", module.Handler.CancelRun)
	routes.GET("/runs/:run_id/workbench", module.Handler.GetWorkbench)
}
